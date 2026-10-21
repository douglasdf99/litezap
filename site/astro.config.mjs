// @ts-check
import { readFileSync } from 'node:fs';
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import { parse } from 'yaml';
import { navFromSpec } from './src/lib/slug.mjs';

const GITHUB = 'https://github.com/douglasdf99/litezap';
const SITE = process.env.SITE_URL ?? 'http://localhost:4321';
// Public Sans (identidade LiteZap) para texto; JetBrains Mono para código.
const FONTS =
	'https://fonts.googleapis.com/css2?family=Public+Sans:ital,wght@0,300..800;1,400&family=JetBrains+Mono:wght@400;500;600&display=swap';

// A referência da API e o catálogo de eventos são gerados de public/openapi.yaml.
const nav = navFromSpec(parse(readFileSync(new URL('./public/openapi.yaml', import.meta.url), 'utf8')));

const soon = (milestone) => ({ text: milestone ? `Em breve · ${milestone}` : 'Em breve', variant: 'note' });
const exp = { text: 'Experimental', variant: 'caution' };
/** @param {string} slug @param {string} [milestone] */
const soonPage = (slug, milestone) => ({ slug, badge: soon(milestone) });

const apiGroups = nav.tags.map((t) => ({
	label: t.name,
	collapsed: true,
	items: [
		{ label: 'Introdução', link: `/docs/api/${t.slug}/` },
		...(t.slug === 'mensagens' ? [{ label: 'Tipos de mensagem', link: '/docs/api/mensagens/tipos/' }] : []),
		...t.ops.map((o) => ({ label: o.summary, link: `/docs/api/${t.slug}/${o.slug}/` })),
	],
}));

export default defineConfig({
	site: SITE,
	integrations: [
		starlight({
			title: 'LiteZap',
			description: 'API de WhatsApp open source que roda no seu PC, no seu servidor ou na nuvem.',
			logo: {
				light: './src/assets/brand/logo-full.png',
				dark: './src/assets/brand/logo-full-white.png',
				alt: 'LiteZap',
				replacesTitle: true,
			},
			favicon: '/favicon.png',
			head: [
				{ tag: 'link', attrs: { rel: 'preconnect', href: 'https://fonts.googleapis.com' } },
				{ tag: 'link', attrs: { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' } },
				{ tag: 'link', attrs: { rel: 'stylesheet', href: FONTS } },
				{ tag: 'link', attrs: { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' } },
				{ tag: 'meta', attrs: { property: 'og:image', content: `${SITE}/og.png` } },
				{ tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
				{ tag: 'meta', attrs: { name: 'theme-color', content: '#377AFB' } },
			],
			expressiveCode: {
				themes: ['github-dark-default', 'github-light-default'],
				styleOverrides: {
					borderRadius: '0.625rem',
					borderColor: 'var(--lz-line)',
					codeFontFamily: 'var(--sl-font-mono)',
					codeFontSize: '0.8125rem',
					uiFontFamily: 'var(--sl-font)',
					frames: {
						shadowColor: 'transparent',
						editorActiveTabIndicatorTopColor: '#377AFB',
						editorActiveTabIndicatorBottomColor: 'transparent',
					},
				},
			},
			// pt-BR na raiz. Para inglês: adicione `en: { label: 'English', lang: 'en' }` em locales
			// e crie src/content/docs/en/docs/... com os mesmos caminhos.
			defaultLocale: 'root',
			locales: { root: { label: 'Português', lang: 'pt-BR' } },
			social: [{ icon: 'github', label: 'GitHub', href: GITHUB }],
			editLink: { baseUrl: `${GITHUB}/edit/main/site/` },
			customCss: ['./src/styles/theme.css'],
			sidebar: [
				{
					label: 'Começar',
					items: [
						{ label: 'Introdução', link: '/docs/' },
						'docs/quickstart',
						'docs/escolha-como-rodar',
						{ slug: 'docs/baixar', badge: { text: 'Novo', variant: 'success' } },
						'docs/app-local',
						'docs/primeiro-envio',
						'docs/migrar-da-z-api',
						'docs/glossario',
					],
				},
				{
					label: 'Conceitos',
					collapsed: true,
					items: [
						'docs/conceitos/instancias',
						'docs/conceitos/autenticacao',
						'docs/conceitos/identificadores',
						'docs/conceitos/fila-e-ritmo',
						'docs/conceitos/rampa',
						'docs/conceitos/janela-de-envio',
						'docs/conceitos/verificacao-de-numeros',
						'docs/conceitos/idempotencia',
						'docs/conceitos/proxy',
						'docs/conceitos/midia',
						'docs/conceitos/eventos',
						'docs/conceitos/erros-e-retentativas',
						'docs/conceitos/limites',
					],
				},
				{
					label: 'Guias',
					collapsed: true,
					items: [
						'docs/boas-praticas',
						'docs/guias/webhooks',
						'docs/guias/sse',
						'docs/guias/enviar-midia',
						'docs/guias/crm',
						'docs/guias/automacao',
						'docs/guias/solucao-de-problemas',
						'docs/guias/multi-instancia',
						{ slug: 'docs/guias/ia-gratis', badge: { text: 'Novo', variant: 'success' } },
						{ slug: 'docs/guias/atendimento-ia', badge: { text: 'Novo', variant: 'success' } },
						{ slug: 'docs/guias/agentes', badge: { text: 'Novo', variant: 'success' } },
						'docs/guias/grupos',
						{ slug: 'docs/guias/mensagens-interativas', badge: exp },
					],
				},
				{
					label: 'Self-hosted',
					collapsed: true,
					items: [
						'docs/self-hosted',
						'docs/self-hosted/configuracao',
						'docs/self-hosted/docker',
						'docs/self-hosted/systemd',
						'docs/self-hosted/proxy-reverso',
						'docs/self-hosted/seguranca',
						'docs/self-hosted/backup-e-upgrade',
						'docs/self-hosted/observabilidade',
						'docs/self-hosted/dimensionamento',
					],
				},
				{
					label: 'Referência da API',
					items: [
						{ label: 'Visão geral', link: '/docs/api/' },
						{ label: 'Erros', link: '/docs/api/erros/' },
						...apiGroups,
						{ label: 'openapi.yaml', link: '/openapi.yaml', attrs: { target: '_blank' } },
					],
				},
				{
					label: 'Eventos',
					collapsed: true,
					items: [
						{ label: 'Visão geral', link: '/docs/eventos/' },
						'docs/eventos/assinatura',
						'docs/eventos/entrega',
						...nav.events.map((e) => ({ label: e.type, link: `/docs/eventos/${e.slug}/` })),
					],
				},
				{
					label: 'MCP e IA',
					collapsed: true,
					items: [
						'docs/mcp',
						'docs/mcp/ferramentas',
						{ slug: 'docs/mcp/claude-code-plugin', badge: { text: 'Novo', variant: 'success' } },
						'docs/mcp/hook-de-agente',
					],
				},
				{
					label: 'SDKs e ferramentas',
					collapsed: true,
					items: [soonPage('docs/sdks'), 'docs/ferramentas/cli', 'docs/ferramentas/postman'],
				},
				{
					label: 'Projeto',
					collapsed: true,
					items: [
						'docs/roadmap',
						'docs/changelog',
						'docs/faq',
						'docs/contribuir',
						'docs/politica-de-seguranca',
						'docs/uso-aceitavel',
					],
				},
			],
		}),
	],
});
