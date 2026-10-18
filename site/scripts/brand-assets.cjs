// Gera os assets de marca do site a partir dos logos do repositório legado (uso único).
// node scripts/brand-assets.cjs "$PWD" <litezap-pages>/assets/img/branding   (rodar dentro de site/)
const path = require('path');
const fs = require('fs');
const site = process.argv[2];
const legacy = process.argv[3];
const pnpm = path.join(site, 'node_modules/.pnpm');
const sharp = require(path.join(pnpm, fs.readdirSync(pnpm).find((d) => d.startsWith('sharp@')), 'node_modules/sharp'));

const out = (p) => {
	const f = path.join(site, p);
	fs.mkdirSync(path.dirname(f), { recursive: true });
	return f;
};

(async () => {
	const full = path.join(legacy, 'logo-full.png');
	const white = path.join(legacy, 'logo-full-white.png');
	for (const [src, dst] of [
		[full, 'src/assets/brand/logo-full.png'],
		[white, 'src/assets/brand/logo-full-white.png'],
		[full, 'public/brand/logo-full.png'],
		[white, 'public/brand/logo-full-white.png'],
	])
		fs.copyFileSync(src, out(dst));

	// Checa se o logo branco é todo branco.
	const { data } = await sharp(white).raw().ensureAlpha().toBuffer({ resolveWithObject: true });
	let nonWhite = 0;
	for (let i = 0; i < data.length; i += 4) if (data[i + 3] > 200 && (data[i] < 230 || data[i + 2] < 230)) nonWhite++;
	console.log('white logo non-white opaque pixels:', nonWhite);

	// Marca "Z" recortada (bbox medida: x 0-135, y 14-144), centralizada num quadrado.
	const mark = await sharp(full).extract({ left: 0, top: 14, width: 136, height: 131 }).toBuffer();
	const square = await sharp(mark)
		.extend({ top: 3, bottom: 2, left: 0, right: 0, background: { r: 0, g: 0, b: 0, alpha: 0 } })
		.png()
		.toBuffer();
	await sharp(square).toFile(out('src/assets/brand/logo-mark.png'));
	await sharp(square).toFile(out('public/brand/logo-mark.png'));
	const padded = (size, pad) =>
		sharp(square)
			.resize(size - pad * 2, size - pad * 2, { fit: 'contain', background: { r: 0, g: 0, b: 0, alpha: 0 } })
			.extend({ top: pad, bottom: pad, left: pad, right: pad, background: { r: 0, g: 0, b: 0, alpha: 0 } });
	await padded(32, 1).png().toFile(out('public/favicon-32.png'));
	await padded(64, 2).png().toFile(out('public/favicon.png'));
	// apple-touch: fundo branco
	await sharp({ create: { width: 180, height: 180, channels: 4, background: '#ffffff' } })
		.composite([{ input: await padded(180, 28).png().toBuffer() }])
		.png()
		.toFile(out('public/apple-touch-icon.png'));

	// Imagem OG 1200x630.
	const W = 1200, H = 630;
	const bg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}">
  <defs>
    <radialGradient id="g1" cx="85%" cy="10%" r="70%"><stop offset="0" stop-color="#1F5FE6" stop-opacity=".55"/><stop offset="1" stop-color="#0B1120" stop-opacity="0"/></radialGradient>
    <radialGradient id="g2" cx="5%" cy="100%" r="60%"><stop offset="0" stop-color="#1BBCD8" stop-opacity=".35"/><stop offset="1" stop-color="#0B1120" stop-opacity="0"/></radialGradient>
    <pattern id="grid" width="48" height="48" patternUnits="userSpaceOnUse"><path d="M48 0H0V48" fill="none" stroke="#ffffff" stroke-opacity=".05"/></pattern>
    <linearGradient id="bar" x1="0" x2="1"><stop offset="0" stop-color="#1BBCD8"/><stop offset=".5" stop-color="#1A8CD5"/><stop offset="1" stop-color="#1731D2"/></linearGradient>
  </defs>
  <rect width="100%" height="100%" fill="#0B1120"/>
  <rect width="100%" height="100%" fill="url(#grid)"/>
  <rect width="100%" height="100%" fill="url(#g1)"/>
  <rect width="100%" height="100%" fill="url(#g2)"/>
  <rect x="0" y="${H - 8}" width="${W}" height="8" fill="url(#bar)"/>
  <text x="80" y="330" font-family="Public Sans, Helvetica Neue, Helvetica, Arial, sans-serif" font-size="64" font-weight="700" fill="#F1F5FB">API de WhatsApp open source</text>
  <text x="80" y="410" font-family="Public Sans, Helvetica Neue, Helvetica, Arial, sans-serif" font-size="34" fill="#9FB0C9">No seu PC, no seu servidor ou na nuvem.</text>
  <text x="80" y="520" font-family="Menlo, Consolas, monospace" font-size="26" fill="#5E95FC">POST /v1/instances/{id}/messages</text>
</svg>`;
	const logo = await sharp(white).resize({ width: 420 }).png().toBuffer();
	await sharp(Buffer.from(bg))
		.composite([{ input: logo, left: 80, top: 110 }])
		.png()
		.toFile(out('public/og.png'));
	console.log('done');
})();
