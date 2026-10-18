// Exemplos de requisição em 5 linguagens, gerados de um único exemplo do OpenAPI.
import type { Operation } from './openapi';

const BASE = 'http://localhost:8787';

/** Valores de exemplo para os parâmetros de caminho. */
const PATH_VALUES: Record<string, { env: string; value: string }> = {
	id: { env: 'ID', value: 'inst_7f3a9c2e5b1d4086' },
	msg_id: { env: 'MSG_ID', value: 'msg_a41c9e07d2b35f68' },
	jid: { env: 'JID', value: '5511999999999@s.whatsapp.net' },
	media_id: { env: 'MEDIA_ID', value: 'med_5b8e1f3c9d2a7046' },
	wid: { env: 'WEBHOOK_ID', value: 'wh_3c7e1a9f5b2d4806' },
	delivery_id: { env: 'DELIVERY_ID', value: 'dlv_8b1f4e2a6c9d3075' },
};

export interface Sample {
	label: string;
	lang: string;
	code: string;
}

const tokenEnv = (op: Operation) => (op.scope === 'admin' || op.scope === 'operacao' ? 'LITEZAP_ADMIN_TOKEN' : 'LITEZAP_TOKEN');

function queryString(q: Record<string, string>): string {
	const s = new URLSearchParams(q).toString();
	return s ? `?${s}` : '';
}

const ind = (n: number) => '    '.repeat(n);

function toPython(v: unknown, d = 1): string {
	if (v === null) return 'None';
	if (v === true) return 'True';
	if (v === false) return 'False';
	if (typeof v === 'number') return String(v);
	if (typeof v === 'string') return JSON.stringify(v);
	if (Array.isArray(v)) {
		if (v.every((x) => typeof x !== 'object' || x === null)) return `[${v.map((x) => toPython(x, d)).join(', ')}]`;
		return `[\n${v.map((x) => ind(d + 1) + toPython(x, d + 1)).join(',\n')},\n${ind(d)}]`;
	}
	const e = Object.entries(v as object);
	if (!e.length) return '{}';
	return `{\n${e.map(([k, x]) => `${ind(d + 1)}${JSON.stringify(k)}: ${toPython(x, d + 1)}`).join(',\n')},\n${ind(d)}}`;
}

const phpStr = (s: string) => `'${s.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
function toPhp(v: unknown, d = 1): string {
	if (v === null) return 'null';
	if (typeof v === 'boolean') return v ? 'true' : 'false';
	if (typeof v === 'number') return String(v);
	if (typeof v === 'string') return phpStr(v);
	if (Array.isArray(v)) {
		if (v.every((x) => typeof x !== 'object' || x === null)) return `[${v.map((x) => toPhp(x, d)).join(', ')}]`;
		return `[\n${v.map((x) => ind(d + 1) + toPhp(x, d + 1)).join(',\n')},\n${ind(d)}]`;
	}
	const e = Object.entries(v as object);
	if (!e.length) return 'new \\stdClass()';
	return `[\n${e.map(([k, x]) => `${ind(d + 1)}${phpStr(k)} => ${toPhp(x, d + 1)}`).join(',\n')},\n${ind(d)}]`;
}

export function samples(op: Operation, body?: unknown): Sample[] {
	const s = op.sample ?? {};
	const query: Record<string, string> = Object.fromEntries(
		Object.entries(s.query ?? {}).map(([k, v]) => [k, String(v)]),
	);
	const qs = queryString(query);
	const shellPath = op.path.replace(/\{(\w+)\}/g, (_, n) => `$${PATH_VALUES[n]?.env ?? n.toUpperCase()}`);
	const path = op.path.replace(/\{(\w+)\}/g, (_, n) => PATH_VALUES[n]?.value ?? n);
	const url = `${BASE}${path}${qs}`;
	const M = op.method.toUpperCase();
	const auth = !op.public || op.scope === 'operacao';
	const tok = tokenEnv(op);
	const json = body !== undefined ? JSON.stringify(body, null, 2) : undefined;
	const binary: string | undefined = s.binary;
	const sse = !!s.sse;
	const extra: [string, string][] = Object.entries(s.headers ?? {}).map(([k, v]) => [k, String(v)]);

	// curl
	const curl: string[] = [`curl -s${sse ? 'N' : ''}${op.method === 'get' ? '' : ` -X ${M}`} "$LITEZAP_URL${shellPath}${qs}"`];
	if (auth) curl.push(`  -H "Authorization: Bearer $${tok}"`);
	for (const [k, v] of extra) curl.push(`  -H "${k}: ${v}"`);
	if (json) curl.push(`  -d '${JSON.stringify(body)}'`);
	if (binary) curl.push(`  -o ${binary}`);

	// Node
	const nodeOpts: string[] = [];
	if (op.method !== 'get') nodeOpts.push(`  method: "${M}",`);
	const nodeHeaders = [
		auth ? `Authorization: \`Bearer \${process.env.${tok}}\`` : '',
		json ? `"Content-Type": "application/json"` : '',
		...extra.map(([k, v]) => `${JSON.stringify(k)}: ${JSON.stringify(v)}`),
	].filter(Boolean);
	if (nodeHeaders.length) nodeOpts.push(`  headers: { ${nodeHeaders.join(', ')} },`);
	if (json) nodeOpts.push(`  body: JSON.stringify(${json.replace(/\n/g, '\n  ')}),`);
	let node = `const res = await fetch("${url}"${nodeOpts.length ? `, {\n${nodeOpts.join('\n')}\n}` : ''});\n`;
	if (sse) {
		node += `const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
for (;;) {
  const { value, done } = await reader.read();
  if (done) break;
  process.stdout.write(value); // linhas "id:", "event:" e "data:" (envelope JSON)
}`;
	} else if (binary) {
		node = `import { writeFile } from "node:fs/promises";\n\n${node}await writeFile("${binary}", Buffer.from(await res.arrayBuffer()));`;
	} else {
		node += `console.log(res.status, res.status === 204 ? "" : await res.json());`;
	}

	// Python
	const pyArgs = [`    "${BASE}${path}",`];
	if (auth)
		pyArgs.push(
			`    headers={"Authorization": f"Bearer {os.environ['${tok}']}"${extra.map(([k, v]) => `, ${JSON.stringify(k)}: ${JSON.stringify(v)}`).join('')}},`,
		);
	if (Object.keys(query).length) pyArgs.push(`    params=${toPython(query, 1)},`);
	if (body !== undefined) pyArgs.push(`    json=${toPython(body, 1)},`);
	if (sse) pyArgs.push('    stream=True,', '    timeout=None,');
	else pyArgs.push('    timeout=150,');
	let py = `import os\nimport requests\n\nr = requests.${op.method}(\n${pyArgs.join('\n')}\n)\n`;
	if (sse) py += `for line in r.iter_lines(decode_unicode=True):\n    if line.startswith("data: "):\n        print(line[6:])  # envelope JSON`;
	else if (binary) py += `r.raise_for_status()\nopen("${binary}", "wb").write(r.content)`;
	else py += `print(r.status_code, r.json() if r.content else "")`;
	if (!auth) py = py.replace('import os\n', '');

	// Go
	const goImports = ['"fmt"', '"net/http"'];
	if (auth) goImports.push('"os"');
	if (json) goImports.push('"strings"');
	if (sse) goImports.push('"bufio"');
	else if (binary) goImports.push('"io"');
	else goImports.push('"io"');
	goImports.sort();
	const goBody = json ? `strings.NewReader(\`${JSON.stringify(body)}\`)` : 'nil';
	let go = `package main

import (
${goImports.map((i) => `\t${i}`).join('\n')}
)

func main() {
\treq, _ := http.NewRequest("${M}", "${url}", ${goBody})
${auth ? `\treq.Header.Set("Authorization", "Bearer "+os.Getenv("${tok}"))\n` : ''}${json ? '\treq.Header.Set("Content-Type", "application/json")\n' : ''}${extra.map(([k, v]) => `\treq.Header.Set(${JSON.stringify(k)}, ${JSON.stringify(v)})\n`).join('')}\tresp, err := http.DefaultClient.Do(req)
\tif err != nil {
\t\tpanic(err)
\t}
\tdefer resp.Body.Close()
`;
	if (sse) go += `\tsc := bufio.NewScanner(resp.Body)\n\tfor sc.Scan() {\n\t\tfmt.Println(sc.Text()) // "id:", "event:" e "data:" (envelope JSON)\n\t}\n}`;
	else if (binary) go += `\tf, _ := os.Create("${binary}")\n\tdefer f.Close()\n\tio.Copy(f, resp.Body)\n\tfmt.Println(resp.Status)\n}`;
	else go += `\tout, _ := io.ReadAll(resp.Body)\n\tfmt.Println(resp.Status, string(out))\n}`;
	if (binary && !auth) go = go.replace('\t"io"\n\t"net/http"\n', '\t"io"\n\t"net/http"\n\t"os"\n');

	// PHP
	let php: string;
	if (sse) {
		php = `<?php
$ch = curl_init('${url}');
curl_setopt_array($ch, [
    CURLOPT_HTTPHEADER => ['Authorization: Bearer ' . getenv('${tok}')],
    CURLOPT_TIMEOUT => 0,
    CURLOPT_WRITEFUNCTION => function ($ch, $chunk) {
        echo $chunk; // linhas "id:", "event:" e "data:" (envelope JSON)
        return strlen($chunk);
    },
]);
curl_exec($ch);`;
	} else {
		const opts: string[] = [];
		if (auth)
			opts.push(
				`    'headers' => ['Authorization' => 'Bearer ' . getenv('${tok}')${extra.map(([k, v]) => `, ${phpStr(k)} => ${phpStr(v)}`).join('')}],`,
			);
		if (Object.keys(query).length) opts.push(`    'query' => ${toPhp(query, 1)},`);
		if (body !== undefined) opts.push(`    'json' => ${toPhp(body, 1)},`);
		if (binary) opts.push(`    'sink' => '${binary}',`);
		opts.push(`    'http_errors' => false,`);
		php = `<?php
require 'vendor/autoload.php'; // composer require guzzlehttp/guzzle

$client = new GuzzleHttp\\Client(['base_uri' => '${BASE}']);
$res = $client->request('${M}', '${path}', [
${opts.join('\n')}
]);
echo $res->getStatusCode(), ' ', ${binary ? "'salvo em " + binary + "'" : '$res->getBody()'}, PHP_EOL;`;
	}

	return [
		{ label: 'cURL', lang: 'bash', code: curl.join(' \\\n') },
		{ label: 'Node.js', lang: 'js', code: node },
		{ label: 'Python', lang: 'python', code: py },
		{ label: 'Go', lang: 'go', code: go },
		{ label: 'PHP', lang: 'php', code: php },
	];
}

/** Verificação de assinatura de webhook nas 4 linguagens (mesma lógica do motor). */
export const verifySamples: Sample[] = [
	{
		label: 'Go',
		lang: 'go',
		code: `import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"time"
)

// verify confere X-LiteZap-Signature ("t=<unix>,v1=<hex>") com o corpo bruto.
func verify(secret, header string, body []byte) bool {
	var ts string
	var sigs []string
	for _, p := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(p, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-t)) > 300 {
		return false // sem timestamp ou mais de 5 minutos de diferença (anti-replay)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	want := hex.EncodeToString(mac.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return true
		}
	}
	return false
}`,
	},
	{
		label: 'Node.js',
		lang: 'js',
		code: `import crypto from "node:crypto";

// rawBody: o corpo exatamente como chegou (string), antes de JSON.parse.
export function verify(secret, header, rawBody) {
  const parts = Object.fromEntries(header.split(",").map((p) => p.split("=", 2)));
  const t = Number(parts.t);
  if (!parts.v1 || !Number.isFinite(t) || Math.abs(Date.now() / 1000 - t) > 300) return false;
  const want = crypto.createHmac("sha256", secret).update(\`\${parts.t}.\${rawBody}\`).digest("hex");
  const a = Buffer.from(parts.v1, "hex");
  const b = Buffer.from(want, "hex");
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}`,
	},
	{
		label: 'Python',
		lang: 'python',
		code: `import hashlib
import hmac
import time


def verify(secret: str, header: str, raw_body: bytes) -> bool:
    """Confere X-LiteZap-Signature ("t=<unix>,v1=<hex>") com o corpo bruto."""
    parts = {}
    sigs = []
    for p in header.split(","):
        k, _, v = p.partition("=")
        if k == "v1":
            sigs.append(v)
        else:
            parts[k] = v
    try:
        t = int(parts.get("t", ""))
    except ValueError:
        return False
    if abs(time.time() - t) > 300:  # anti-replay: 5 minutos
        return False
    want = hmac.new(secret.encode(), f"{t}.".encode() + raw_body, hashlib.sha256).hexdigest()
    return any(hmac.compare_digest(s, want) for s in sigs)`,
	},
	{
		label: 'PHP',
		lang: 'php',
		code: `<?php
// $rawBody = file_get_contents('php://input');
// $header  = $_SERVER['HTTP_X_LITEZAP_SIGNATURE'] ?? '';
function litezap_verify(string $secret, string $header, string $rawBody): bool
{
    $ts = null;
    $sigs = [];
    foreach (explode(',', $header) as $part) {
        [$k, $v] = array_pad(explode('=', $part, 2), 2, '');
        if ($k === 't') $ts = $v;
        if ($k === 'v1') $sigs[] = $v;
    }
    if ($ts === null || !ctype_digit($ts) || abs(time() - (int) $ts) > 300) {
        return false; // anti-replay: 5 minutos
    }
    $want = hash_hmac('sha256', $ts . '.' . $rawBody, $secret);
    foreach ($sigs as $s) {
        if (hash_equals($want, $s)) return true;
    }
    return false;
}`,
	},
];
