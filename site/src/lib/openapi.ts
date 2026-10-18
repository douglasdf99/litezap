// Leitura leve de public/openapi.yaml para gerar as páginas da referência da API.
import { parse } from 'yaml';
import raw from '../../public/openapi.yaml?raw';

type Obj = Record<string, any>;

export const spec: Obj = parse(raw);

const METHODS = ['get', 'post', 'put', 'patch', 'delete'] as const;

/** Resolve um $ref local ("#/components/..."); objetos sem $ref voltam como estão. */
export function deref<T = Obj>(node: any): T {
	let cur = node;
	const seen = new Set<string>();
	while (cur && typeof cur === 'object' && typeof cur.$ref === 'string') {
		if (seen.has(cur.$ref)) break;
		seen.add(cur.$ref);
		cur = cur.$ref
			.replace(/^#\//, '')
			.split('/')
			.reduce((acc: any, k: string) => acc?.[k], spec);
	}
	return cur as T;
}

export interface Operation {
	id: string;
	method: string;
	path: string;
	summary: string;
	description?: string;
	scope: string;
	parameters: Obj[];
	requestBody?: Obj;
	responses: [string, Obj][];
	public: boolean;
}

export interface Tag {
	name: string;
	slug: string;
	description?: string;
	operations: Operation[];
}

export function tags(): Tag[] {
	const list: Tag[] = (spec.tags ?? []).map((t: Obj) => ({
		name: t.name,
		slug: t['x-slug'],
		description: t.description,
		operations: [],
	}));
	for (const [path, item] of Object.entries<Obj>(spec.paths ?? {})) {
		const shared = (item.parameters ?? []).map(deref);
		for (const method of METHODS) {
			const op = item[method];
			if (!op) continue;
			const own = (op.parameters ?? []).map(deref);
			const tag = list.find((t) => t.name === op.tags?.[0]);
			if (!tag) continue;
			tag.operations.push({
				id: op.operationId,
				method,
				path,
				summary: op.summary,
				description: op.description,
				scope: op['x-litezap-scope'] ?? 'instancia',
				parameters: [...shared, ...own],
				requestBody: op.requestBody ? deref(op.requestBody) : undefined,
				responses: Object.entries<Obj>(op.responses ?? {}).map(([code, r]) => [code, deref(r)]),
				public: Array.isArray(op.security) && op.security.length === 0,
			});
		}
	}
	return list;
}

export const scopeLabel: Record<string, string> = {
	admin: 'token admin',
	instancia: 'token da instância ou admin',
	assinatura: 'sem token (URL assinada)',
	operacao: 'público em loopback; admin fora dele',
};

const esc = (s: string) =>
	s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

/** Markdown mínimo das descrições: parágrafos, `código` e **negrito**. */
export function md(text?: string): string {
	if (!text) return '';
	return text
		.trim()
		.split(/\n\s*\n/)
		.map((p) => {
			const html = esc(p.replace(/\s*\n\s*/g, ' '))
				.replace(/`([^`]+)`/g, '<code>$1</code>')
				.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
			return `<p>${html}</p>`;
		})
		.join('');
}

/** Exemplos de um media type: `example` ou `examples` (nome → valor). */
export function examples(media?: Obj): { name?: string; value: unknown }[] {
	if (!media) return [];
	if (media.example !== undefined) return [{ value: media.example }];
	return Object.entries<Obj>(media.examples ?? {}).map(([name, ex]) => ({
		name: ex.summary ?? name,
		value: ex.value,
	}));
}

export function schemaType(s?: Obj): string {
	const sc = deref(s);
	if (!sc) return '';
	if (s?.$ref) return s.$ref.split('/').pop();
	if (sc.type === 'array') return `${schemaType(sc.items)}[]`;
	if (sc.enum) return sc.enum.map((v: unknown) => JSON.stringify(v)).join(' | ');
	return sc.type ?? 'object';
}

/** Campos de primeiro nível (e um nível aninhado) de um schema de corpo. */
export function fields(schema?: Obj, prefix = ''): { name: string; type: string; required: boolean; description?: string }[] {
	const sc = deref(schema);
	if (!sc?.properties) return [];
	const req: string[] = sc.required ?? [];
	const out: ReturnType<typeof fields> = [];
	for (const [name, prop] of Object.entries<Obj>(sc.properties)) {
		const p = deref(prop);
		out.push({ name: prefix + name, type: schemaType(prop), required: req.includes(name), description: p?.description });
		if (!prefix && p?.type === 'object' && p.properties) out.push(...fields(p, `${name}.`));
	}
	return out;
}

/** Exemplo de curl para a operação. */
export function curl(op: Operation, body?: unknown): string {
	const path = op.path.replace('{id}', '$ID').replace(/\{(\w+)\}/g, (_, n) => `$${n.toUpperCase()}`);
	const query = op.id === 'getQR' ? '?format=png' : '';
	const lines = [`curl -s${op.method === 'get' ? '' : ` -X ${op.method.toUpperCase()}`} "$LITEZAP_URL${path}${query}"`];
	if (!op.public) lines.push(`  -H "Authorization: Bearer ${op.scope === 'admin' ? '$ADMIN' : '$TOK'}"`);
	if (op.scope === 'operacao') lines.push('  # fora de loopback: -H "Authorization: Bearer $ADMIN"');
	if (body !== undefined) lines.push(`  -d '${JSON.stringify(body)}'`);
	if (op.id === 'getQR') lines.push('  -o qr.png');
	return lines.join(' \\\n').replace(' \\\n  #', '\n  #');
}
