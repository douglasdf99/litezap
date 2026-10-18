// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const GITHUB = 'https://github.com/douglasdf99/litezap';
const SITE = process.env.SITE_URL ?? 'http://localhost:4321';
// Public Sans (identidade LiteZap) para texto; JetBrains Mono para código.
const FONTS =
	'https://fonts.googleapis.com/css2?family=Public+Sans:ital,wght@0,300..800;1,400&family=JetBrains+Mono:wght@400;500;600&display=swap';

// Páginas da referência geradas a partir de public/openapi.yaml (src/pages/docs/api/[tag].astro).
const apiPages = [
	['Instâncias', 'instancias'],
	['Pareamento', 'pareamento'],
	['Configurações', 'configuracoes'],
	['Mensagens', 'mensagens'],
	['Chats', 'chats'],
	['Contatos', 'contatos'],
	['Números', 'numeros'],
	['Mídia', 'midia'],
	['Operação', 'operacao'],
].map(([label, slug]) => ({ label, link: `/docs/api/${slug}/` }));

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
						{ label: 'Quickstart', link: '/docs/quickstart/' },
						{ label: 'App local', link: '/docs/app-local/' },
					],
				},
				{
					label: 'Conceitos',
					items: [
						'docs/conceitos/instancias',
						'docs/conceitos/autenticacao',
						'docs/conceitos/fila-e-ritmo',
						'docs/conceitos/rampa',
						'docs/conceitos/janela-de-envio',
						'docs/conceitos/verificacao-de-numeros',
						'docs/conceitos/proxy',
						'docs/conceitos/midia',
					],
				},
				{
					label: 'Self-hosted',
					items: [
						'docs/self-hosted',
						'docs/self-hosted/configuracao',
						'docs/self-hosted/docker',
						'docs/self-hosted/systemd',
						'docs/self-hosted/proxy-reverso',
						'docs/self-hosted/seguranca',
					],
				},
				{ label: 'Guias', items: ['docs/boas-praticas'] },
				{
					label: 'Referência da API',
					items: [
						{ label: 'Visão geral', link: '/docs/api/' },
						{ label: 'Erros', link: '/docs/api/erros/' },
						...apiPages,
						{ label: 'openapi.yaml', link: '/openapi.yaml', attrs: { target: '_blank' } },
					],
				},
				{ label: 'Projeto', items: ['docs/roadmap'] },
			],
		}),
	],
});
