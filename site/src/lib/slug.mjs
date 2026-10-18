// Slugs compartilhados pelo astro.config.mjs (barra lateral) e pelas páginas geradas do OpenAPI.

/** "Criar instância" -> "criar-instancia" */
export function slugify(text) {
	return String(text)
		.normalize('NFD')
		.replace(/[̀-ͯ]/g, '')
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, '-')
		.replace(/^-+|-+$/g, '');
}

const METHODS = ['get', 'post', 'put', 'patch', 'delete'];

/** Tags com suas operações (só o necessário para a navegação). */
export function navFromSpec(spec) {
	const tags = (spec.tags ?? []).map((t) => ({ name: t.name, slug: t['x-slug'], ops: [] }));
	for (const [path, item] of Object.entries(spec.paths ?? {})) {
		for (const method of METHODS) {
			const op = item[method];
			if (!op) continue;
			const tag = tags.find((t) => t.name === op.tags?.[0]);
			if (tag) tag.ops.push({ id: op.operationId, summary: op.summary, slug: slugify(op.summary), method, path });
		}
	}
	const events = Object.entries(spec.webhooks ?? {}).map(([type, item]) => ({
		type,
		slug: item.post['x-slug'],
		summary: item.post.summary,
	}));
	return { tags, events };
}
