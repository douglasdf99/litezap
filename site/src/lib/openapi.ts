// Leitura leve de public/openapi.yaml para gerar a referência da API e o catálogo de eventos.
import { parse } from 'yaml';
import raw from '../../public/openapi.yaml?raw';
import { slugify } from './slug.mjs';

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

const refName = (node: any): string | undefined =>
	typeof node?.$ref === 'string' ? node.$ref.split('/').pop() : undefined;

export interface Response {
	code: string;
	ref?: string;
	description: string;
	content: Obj;
}

export interface Operation {
	id: string;
	slug: string;
	method: string;
	path: string;
	summary: string;
	description?: string;
	scope: string;
	milestone?: string;
	zapi?: string;
	events: string[];
	notes?: string;
	sample: Obj;
	parameters: Obj[];
	requestBody?: Obj;
	responses: Response[];
	public: boolean;
	tag: { name: string; slug: string };
}

export interface Tag {
	name: string;
	slug: string;
	description?: string;
	intro?: string;
	concept?: string;
	operations: Operation[];
}

export function tags(): Tag[] {
	const list: Tag[] = (spec.tags ?? []).map((t: Obj) => ({
		name: t.name,
		slug: t['x-slug'],
		description: t.description,
		intro: t['x-intro'],
		concept: t['x-concept'],
		operations: [],
	}));
	for (const [path, item] of Object.entries<Obj>(spec.paths ?? {})) {
		const shared = (item.parameters ?? []).map(deref);
		for (const method of METHODS) {
			const op = item[method];
			if (!op) continue;
			const tag = list.find((t) => t.name === op.tags?.[0]);
			if (!tag) continue;
			tag.operations.push({
				id: op.operationId,
				slug: slugify(op.summary),
				method,
				path,
				summary: op.summary,
				description: op.description,
				scope: op['x-litezap-scope'] ?? 'instancia',
				milestone: op['x-litezap-milestone'],
				zapi: op['x-zapi'],
				events: op['x-litezap-events'] ?? [],
				notes: op['x-litezap-notes'],
				sample: op['x-litezap-sample'] ?? {},
				parameters: [...shared, ...(op.parameters ?? []).map(deref)],
				requestBody: op.requestBody ? deref(op.requestBody) : undefined,
				responses: Object.entries<Obj>(op.responses ?? {}).map(([code, r]) => {
					const d = deref(r);
					return { code, ref: refName(r), description: d.description ?? '', content: d.content ?? {} };
				}),
				public: Array.isArray(op.security) && op.security.length === 0,
				tag: { name: tag.name, slug: tag.slug },
			});
		}
	}
	return list;
}

export function operations(): Operation[] {
	return tags().flatMap((t) => t.operations);
}

export function opHref(op: Pick<Operation, 'slug'> & { tag: { slug: string } }): string {
	return `/docs/api/${op.tag.slug}/${op.slug}/`;
}

// ---------- Eventos (seção `webhooks` do OpenAPI) ----------

export interface EventDef {
	type: string;
	slug: string;
	summary: string;
	description: string;
	milestone?: string;
	zapi?: string;
	related: string[];
	fields: [string, string, string][];
	examples: { name?: string; value: unknown }[];
}

export function events(): EventDef[] {
	return Object.entries<Obj>(spec.webhooks ?? {}).map(([type, item]) => {
		const p = item.post;
		return {
			type,
			slug: p['x-slug'],
			summary: p.summary,
			description: p.description,
			milestone: p['x-litezap-milestone'],
			zapi: p['x-zapi'],
			related: p['x-related'] ?? [],
			fields: p['x-fields'] ?? [],
			examples: examples(p.requestBody?.content?.['application/json']),
		};
	});
}

export const eventHref = (type: string) => {
	const e = events().find((x) => x.type === type);
	return e ? `/docs/eventos/${e.slug}/` : '/docs/eventos/';
};

// ---------- Escopos e erros ----------

export const scopeLabel: Record<string, string> = {
	admin: 'token admin',
	instancia: 'token da instância ou admin',
	assinatura: 'sem token (URL assinada)',
	operacao: 'público em loopback; admin fora dele',
};

export const scopeText: Record<string, string> = {
	admin:
		'Exige o **token admin**. Com um token de instância a resposta é `403 forbidden`.',
	instancia:
		'Aceita o **token da instância** (só a própria instância) ou o **token admin**. Um token de outra instância recebe `403 forbidden` (ou `404 not_found` em webhooks de outra instância).',
	assinatura:
		'Não usa token: a URL assinada (`exp` e `sig`) é a credencial e vale 1 hora. Assinatura inválida ou expirada responde `403 forbidden`.',
	operacao:
		'Pública quando o servidor escuta em loopback (`127.0.0.1`/`localhost`). Em qualquer outro `listen_addr` exige o **token admin**.',
};

/** Códigos de erro: retentável e ação recomendada. */
export const ERRORS: Record<string, { retryable: boolean | 'depende'; action: string }> = {
	validation_failed: { retryable: false, action: 'Corrija o corpo ou os parâmetros; a `message` diz o campo.' },
	invalid_number: { retryable: false, action: 'O número não tem WhatsApp. Não reenvie; confira com check-numbers.' },
	unauthorized: { retryable: false, action: 'Envie `Authorization: Bearer <token>` válido.' },
	forbidden: { retryable: false, action: 'Use o token certo (admin, ou o da própria instância).' },
	instance_not_found: { retryable: false, action: 'Confira o id da instância.' },
	not_found: { retryable: false, action: 'Confira o id do recurso (mensagem, chat, contato, mídia ou webhook).' },
	conflict: { retryable: false, action: 'O recurso está num estado que não permite a operação; veja a `message`.' },
	instance_not_connected: { retryable: 'depende', action: 'Confira o estado da instância; se `logged_out`, pareie de novo.' },
	instance_banned: { retryable: false, action: 'Número banido: apague a instância.' },
	instance_not_pairing: { retryable: 'depende', action: 'Só vale em `qr_pending`; a instância já está pareada ou parada.' },
	media_unavailable: { retryable: false, action: 'A mídia expirou no WhatsApp; peça o arquivo de novo ao contato.' },
	message_expired: { retryable: false, action: 'Não saiu antes de `expires_after`; envie de novo se ainda fizer sentido.' },
	media_too_large: { retryable: false, action: 'Reduza o arquivo (imagem 16MB, vídeo/áudio 64MB, documento 100MB).' },
	media_fetch_failed: { retryable: true, action: 'Confira se a URL é pública e responde; tente de novo.' },
	rate_limited: { retryable: true, action: 'Espere e tente de novo com backoff.' },
	worker_error: { retryable: true, action: 'Tente de novo com backoff; se persistir, veja o log do servidor.' },
	worker_timeout: { retryable: true, action: 'Tente de novo; a instância pode estar sobrecarregada ou reconectando.' },
	shutting_down: { retryable: true, action: 'O servidor está reiniciando; tente de novo em alguns segundos.' },
	internal: { retryable: true, action: 'Erro inesperado; tente de novo e veja o log do servidor.' },
};

const REF_CODES: Record<string, string[]> = {
	ValidationFailed: ['validation_failed'],
	SendBadRequest: ['validation_failed', 'invalid_number'],
	Unauthorized: ['unauthorized'],
	Forbidden: ['forbidden'],
	InstanceNotFound: ['instance_not_found'],
	NotFound: ['not_found', 'instance_not_found'],
	InstanceConflict: ['instance_not_connected', 'instance_banned'],
	NotPairing: ['instance_not_pairing'],
	SendConflict: ['conflict', 'instance_not_connected', 'instance_banned'],
	SignedDisabled: ['conflict'],
	MessageExpired: ['message_expired'],
	MediaUnavailable: ['media_unavailable'],
	MediaTooLarge: ['media_too_large'],
	MediaFetchFailed: ['media_fetch_failed'],
	RateLimited: ['rate_limited'],
	BusyOrShuttingDown: ['rate_limited', 'shutting_down'],
	ShuttingDown: ['shutting_down'],
	WorkerError: ['worker_error'],
	WorkerTimeout: ['worker_timeout'],
	WebhookNotFound: ['not_found'],
	RetryConflict: ['conflict'],
};

export function errorRows(op: Operation) {
	const rows: { code: string; http: string; retryable: string; when: string; action: string }[] = [];
	for (const r of op.responses) {
		if (!/^[45]/.test(r.code)) continue;
		let codes = r.ref ? REF_CODES[r.ref] : undefined;
		if (!codes) {
			const m = r.description.match(/`([a-z_]+)`/);
			codes = m ? [m[1]] : [];
		}
		if (codes.length === 0 && r.code === '503') codes = ['shutting_down'];
		for (const code of codes) {
			const e = ERRORS[code];
			let retry = e ? e.retryable : false;
			if (code === 'instance_not_connected' && r.ref === 'InstanceConflict') retry = 'depende';
			rows.push({
				code,
				http: r.code,
				retryable: retry === 'depende' ? 'depende' : retry ? 'sim' : 'não',
				when: r.description.replace(/^`[^`]+`(?:\s*\([^)]*\))?:?\s*/, ''),
				action: e?.action ?? '',
			});
		}
	}
	return rows;
}

// ---------- Utilidades de renderização ----------

const esc = (s: string) =>
	s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

/** Markdown mínimo: parágrafos, `código`, **negrito** e [links](url). */
export function md(text?: string, inline = false): string {
	if (!text) return '';
	const fmt = (p: string) =>
		esc(p.replace(/\s*\n\s*/g, ' '))
			.replace(/`([^`]+)`/g, '<code>$1</code>')
			.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
			.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, '<a href="$2">$1</a>');
	if (inline) return fmt(text.trim());
	return text
		.trim()
		.split(/\n\s*\n/)
		.map((p) => `<p>${fmt(p)}</p>`)
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

export interface Field {
	name: string;
	type: string;
	required: boolean;
	description?: string;
	extra?: string;
}

/** Campos de primeiro nível (e um nível aninhado) de um schema. */
export function fields(schema?: Obj, prefix = ''): Field[] {
	const sc = deref(schema);
	if (!sc?.properties) return [];
	const req: string[] = sc.required ?? [];
	const out: Field[] = [];
	for (const [name, prop] of Object.entries<Obj>(sc.properties)) {
		const p = deref(prop);
		const extra = [
			p?.default !== undefined ? `padrão ${JSON.stringify(p.default)}` : '',
			p?.maxLength ? `até ${p.maxLength}` : '',
			p?.maxItems ? `até ${p.maxItems} itens` : '',
			p?.minimum !== undefined && p?.maximum !== undefined ? `${p.minimum}–${p.maximum}` : '',
		]
			.filter(Boolean)
			.join(', ');
		out.push({ name: prefix + name, type: schemaType(prop), required: req.includes(name), description: p?.description, extra });
		if (!prefix && p?.type === 'object' && p.properties) out.push(...fields(p, `${name}.`));
		if (!prefix && prop?.$ref && p?.properties && ['MediaInput', 'Ramp', 'SendWindow'].includes(prop.$ref.split('/').pop()))
			out.push(...fields(p, `${name}.`));
	}
	return out;
}
