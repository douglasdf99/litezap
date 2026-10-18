# LiteZap — Arquitetura de informação da documentação

Data: 2026-10-17
Status: proposta (insumo para a peça #6, site e documentação)
Relacionado: `2026-10-17-zapi-parity.md` (matriz de recursos e marcos), branch `feat/site-docs` (site Astro + Starlight já iniciado)

## 1. Objetivo

Definir a estrutura do site de documentação para que o LiteZap pareça (e seja) tão completo quanto a Z-API na referência de endpoints e tão bem organizado quanto o Supabase na separação entre começar, guias, referência, self-host e recursos. A redação é nossa; das referências copiamos só padrões de organização.

## 2. O que aprender com cada referência

### 2.1 Z-API (verificado no índice público `developer.z-api.io/llms.txt`, 2026-10-17)

- **Uma página por endpoint**, agrupadas por recurso (Instância, Mensagens, Privacidade, Contatos, Chats, Chamadas, Grupos, Comunidades, Canais, Listas, Status, Fila, Business, Webhooks, Parceiros). Cada grupo abre com uma página "Introdução" que explica o conceito antes dos endpoints. São cerca de 250 páginas de referência.
- **Anatomia da página de endpoint**: frase de resumo; visão geral com dicas de uso; captura de como a mensagem aparece no WhatsApp; atributos divididos em obrigatórios e opcionais (caminho e corpo, com tipo); corpo de exemplo; resposta 200 com cada campo descrito; erros comuns (método errado, `Content-Type` faltando); link para o payload do webhook que aquele envio gera.
- **Webhooks documentados como páginas próprias**, com uma página de exemplos de payload por tipo de mensagem recebida.
- **Seção de dicas operacionais** que reduz suporte: bloqueios e banimentos, boas práticas, LID, número fixo, emuladores, comparação com a API oficial, estado dos botões, prazo de expiração de arquivos, coleção Postman.
- **Segurança como seção de topo** (credenciais, IP, 2FA, token da conta).
- Hospedado em Mintlify, com `llms.txt` (índice para IA), pt-BR e inglês, coleção Postman pública.
- Fraquezas que não devemos repetir: exemplos de código só em JSON (sem curl/SDKs), páginas de introdução rasas, informação de confiabilidade datada e contraditória (página dos botões), sem changelog nem página de status na docs, sem tabela única de erros.

### 2.2 Supabase (de conhecimento geral do site público; não reverificado nesta pesquisa — incerto em detalhes de rótulos)

- **Navegação de topo em poucas áreas**: começar, guias por produto, referência (API e uma referência por SDK/linguagem), CLI, self-hosting, recursos (changelog, status, suporte).
- **Quickstarts por stack** (Next.js, React, Python…), cada um terminando com algo funcionando.
- **Referência de SDK por linguagem**, uma página por método, com seletor de linguagem persistente e exemplos lado a lado.
- **Self-hosting como cidadão de primeira classe** (Docker, configuração, upgrades, segurança), coerente com o modelo open source + nuvem que o LiteZap adota.
- **Guias de solução de problemas** e páginas de limites/cotas.
- **Changelog público** e **status page** linkados no rodapé e no topo.
- "Editar no GitHub" em toda página, busca global (Ctrl+K), modo escuro, recursos para IA (índice para LLMs, prompts).

### 2.3 Síntese para o LiteZap

Estrutura do Supabase (áreas claras, self-host forte, SDKs, changelog/status) + granularidade da Z-API (uma página por endpoint, introdução por recurso, payload de webhook por evento, dicas operacionais) + o que nenhum dos dois tem bem: exemplos em 5 linguagens em toda página de endpoint, tabela única de erros com `retryable`, e garantias de confiabilidade explícitas (fila, ritmo, rampa, at-least-once).

## 3. Mapa do site (árvore da barra lateral)

Legenda: `[existe]` = página já escrita na branch `feat/site-docs` (caminho entre parênteses); `[gerar]` = gerada do `openapi.yaml`; `[escrever]` = a escrever; `(Mx)` = depende do marco que entrega o recurso. Slugs em pt-BR na raiz; inglês em `/en/` com os mesmos caminhos.

### 3.1 Começar

- Introdução — o que é, três formas de rodar, o que não é `[existe]` (`docs/index.mdx`)
- Quickstart — do zero à primeira mensagem `[existe]` (`docs/quickstart.mdx`)
- Escolha como rodar: app local × self-host × nuvem `[escrever]`
- App local `[existe]` (`docs/app-local.md`)
- Primeiro envio: roteiro com `?wait=true`, carência e janela `[escrever]` (conteúdo existe no README, seção "Como testar com seu WhatsApp")
- Migrando da Z-API — tabela endpoint a endpoint, diferenças de autenticação e de webhooks `[escrever]`
- Glossário (JID, LID, instância, worker, outbox) `[escrever]`

### 3.2 Conceitos

- Instâncias e estados `[existe]` (`docs/conceitos/instancias.md`)
- Autenticação e tokens `[existe]` (`docs/conceitos/autenticacao.md`)
- Fila e ritmo `[existe]` (`docs/conceitos/fila-e-ritmo.md`)
- Rampa de números novos `[existe]` (`docs/conceitos/rampa.md`)
- Janela de envio `[existe]` (`docs/conceitos/janela-de-envio.md`)
- Verificação de números `[existe]` (`docs/conceitos/verificacao-de-numeros.md`)
- Proxy por instância `[existe]` (`docs/conceitos/proxy.md`)
- Mídia (envio, recebimento, URLs assinadas, retenção) `[existe]` (`docs/conceitos/midia.md`)
- Identificadores: telefone, JID, LID e o 9º dígito `[escrever]`
- Idempotência e `client_message_id` `[escrever]`
- Eventos: envelope, versionamento, garantias de entrega `[escrever]` (conteúdo no README, seção "Eventos")
- Erros e retentativas `[escrever]` (ponte para a referência de erros)
- Limites e cotas (tamanhos de mídia, check-numbers, ritmo mínimo) `[escrever]`

### 3.3 Guias

- Boas práticas e como evitar banimento `[existe]` (`docs/boas-praticas.md`)
- Receber mensagens com webhooks (passo a passo, verificar assinatura) `[escrever]`
- Tempo real com SSE `[escrever]`
- Enviar mídia (URL, base64, voz) `[escrever]`
- Atendimento com IA via hook de agente `[escrever]` (M5)
- Usar o LiteZap pelo Claude/ChatGPT (MCP) `[escrever]` (M4)
- Integrar com n8n / Make / Zapier `[escrever]`
- Construir um CRM simples (chats, histórico, marcar como lido) `[escrever]`
- Grupos: criar, administrar, moderar `[escrever]` (M8)
- Mensagens interativas (botões e listas): estado real e riscos `[escrever]` (Exp)
- Multi-instância para SaaS (parceiros) `[escrever]` (M11)
- Solução de problemas: QR não aparece, desconexões, mensagens presas na fila, número banido `[escrever]`

### 3.4 Self-hosted

- Visão geral `[existe]` (`docs/self-hosted/index.md`)
- Configuração (`litezap.yaml` e `LITEZAP_*`, referência completa) `[existe]` (`docs/self-hosted/configuracao.md`)
- Docker e Compose `[existe]` (`docs/self-hosted/docker.md`)
- systemd `[existe]` (`docs/self-hosted/systemd.md`)
- Proxy reverso e TLS `[existe]` (`docs/self-hosted/proxy-reverso.md`)
- Segurança `[existe]` (`docs/self-hosted/seguranca.md`)
- Backup, restauração e upgrade `[escrever]`
- Observabilidade: logs, `/metrics`, `/healthz`, `/readyz` `[escrever]` (`/metrics` no M2b)
- Dimensionamento (memória por worker, instâncias por máquina) `[escrever]`

### 3.5 Referência da API

Hoje existe uma página por **tag** gerada do OpenAPI (`site/src/pages/docs/api/[tag].astro`) mais Visão geral (`docs/api/index.md`) e Erros (`docs/api/erros.md`). Proposta: manter a página por tag como **introdução do recurso** e gerar **uma página por operação** (`/docs/api/<tag>/<operationId>/`), com a anatomia da seção 4.

- Visão geral: URL base, autenticação, paginação por cursor, formatos de data e JID, versionamento `[existe]`
- Erros: tabela única com código, HTTP, `retryable` e o que fazer `[existe]` (ampliar com cada código novo)
- Instâncias `[existe como tag]`
  - Criar instância `POST /v1/instances` `[gerar]`
  - Listar instâncias `GET /v1/instances` `[gerar]`
  - Obter instância `GET /v1/instances/{id}` `[gerar]`
  - Apagar instância `DELETE /v1/instances/{id}` `[gerar]`
  - Iniciar / parar / reiniciar `POST .../start|stop|restart` `[gerar]` (3 páginas)
  - Logout `POST .../logout` `[gerar]`
  - Rotacionar token `POST .../token/rotate` `[gerar]`
  - Renomear, apagar em lote `[gerar]` (M11)
- Pareamento `[existe como tag]`
  - Obter QR `GET .../qr` `[gerar]`
  - Código por telefone `POST .../pair-phone` `[gerar]`
  - Responder passkey `[gerar]` (M9)
- Configurações `[existe como tag]`
  - Obter / atualizar configurações `GET|PUT .../settings` `[gerar]` (2 páginas)
- Mensagens `[existe como tag]`
  - Enviar mensagem `POST .../messages` — uma página-mãe com seletor por `type` `[gerar]`
  - Subpáginas por tipo (guia de campos + exemplo): texto, imagem, vídeo, áudio/voz, documento, reação `[escrever]`; figurinha, GIF, PTV, localização, contato(s), enquete, link com prévia, resposta citada, menções, editar, apagar, fixar, encaminhar `[escrever]` (M6); evento `[escrever]` (M10); botões/listas `[escrever]` (Exp)
  - Obter mensagem `GET .../messages/{msg_id}` `[gerar]`
  - Buscar mensagens `GET .../messages/search` `[gerar]` (M2b)
- Fila `[escrever]` (M6)
  - Listar fila, cancelar item, esvaziar fila `[gerar]` (3 páginas)
- Chats `[existe como tag]`
  - Listar chats `GET .../chats` `[gerar]`
  - Mensagens do chat `GET .../chats/{jid}/messages` `[gerar]`
  - Marcar como lido `POST .../chats/{jid}/read` `[gerar]`
  - Obter chat, arquivar, fixar, silenciar, não lido, limpar, apagar, mensagens temporárias `[gerar]` (M9)
- Contatos `[existe como tag]`
  - Listar contatos `GET .../contacts` `[gerar]`
  - Obter contato `GET .../contacts/{jid}` `[gerar]`
  - Foto de perfil, bloquear/desbloquear, lista de bloqueados `[gerar]` (M9)
- Números `[existe como tag]`
  - Verificar números `POST .../check-numbers` `[gerar]`
- Mídia `[existe como tag]`
  - Baixar mídia recebida `GET .../media/{media_id}` `[gerar]`
  - Mídia por URL assinada `GET /media/{id}/{media_id}` `[gerar]`
- Grupos `[escrever]` (M8) — listar, criar, obter, atualizar (nome/descrição/foto/configurações), participantes (adicionar/remover/promover/rebaixar), pedidos de entrada, link de convite, entrar, sair, info por convite `[gerar]` (~14 páginas)
- Comunidades `[escrever]` (M8) — criar, listar, obter, vincular/desvincular grupos, participantes, configurações `[gerar]` (~8 páginas)
- Perfil e privacidade `[escrever]` (M9) — nome, recado, foto, ler/alterar privacidade, mensagens temporárias padrão `[gerar]` (~6 páginas)
- Status `[escrever]` (M10) — publicar texto/imagem/vídeo, responder `[gerar]` (~4 páginas)
- Canais `[escrever]` (M10) — criar, listar, obter, seguir/deixar, silenciar, publicar, reagir `[gerar]` (~8 páginas)
- Business `[escrever]` (M10) — etiquetas (CRUD, atribuir), perfil comercial `[gerar]` (~7 páginas)
- Chamadas `[escrever]` (M7) — configurar rejeição automática (via settings) + página conceitual dizendo o que não é suportado
- Webhooks `[escrever como tag]` (rotas já existem no código, faltam no OpenAPI da branch de site)
  - Criar, listar, obter, atualizar, apagar webhook `[gerar]` (5 páginas)
  - Listar entregas, reenviar entrega `[gerar]` (2 páginas)
- Eventos em tempo real (SSE) `[escrever como tag]`
  - Stream da instância `GET .../events`, stream global `GET /v1/events` `[gerar]` (2 páginas)
- Operação `[existe como tag]`
  - `GET /healthz`, `GET /readyz` `[gerar]`; `GET /metrics` `[gerar]` (M2b)
- `openapi.yaml` para download `[existe]`

### 3.6 Eventos (catálogo de payloads)

Uma página por tipo de evento, gerada de um catálogo em código (fonte única para README, OpenAPI `webhooks:` e docs):

- Envelope e cabeçalhos (`X-LiteZap-Signature`, `-Event-Id`, `-Event-Type`), verificação em Go/Node/Python/PHP `[escrever]` (conteúdo existe no README)
- Entrega: retry, backoff, dead-letter, idempotência no receptor `[escrever]`
- `instance.status`, `instance.qr`, `instance.pair_code` `[escrever]` (3 páginas)
- `message.received` — com exemplos por tipo (texto, imagem, áudio, documento, figurinha, localização, contato, reação) `[escrever]`
- `message.sent`, `message.status`, `message.reaction` `[escrever]` (3 páginas)
- `history.synced` `[escrever]`
- `message.revoked`, `message.edited`, `message.button_reply`, `message.poll_vote`, `chat.presence`, `call.received`, `call.ended` `[escrever]` (M6/M7)
- `group.updated` `[escrever]` (M8); `chat.updated` `[escrever]` (M9); `status.received`, `label.updated` `[escrever]` (M10); `instance.proxy_failed` `[escrever]` (M11)
- `agent.failed`, `agent.handoff` `[escrever]` (M5)

### 3.7 MCP e IA (M4/M5)

- Visão geral: MCP HTTP × `litezap mcp` (stdio) `[escrever]`
- Conectar no Claude Desktop, Claude Code, Cursor, ChatGPT `[escrever]` (uma página por cliente)
- Referência de ferramentas MCP (uma página por ferramenta, gerada da definição em código) `[escrever]`
- Hook de agente: contrato HTTP, ações, pausa por humano, exemplo com Claude `[escrever]` (M5)
- `llms.txt` e `llms-full.txt` do próprio site `[escrever]` (gerados no build)

### 3.8 SDKs e ferramentas

- Visão geral dos SDKs `[escrever]`
- Node/TypeScript, Python, Go, PHP: instalação, autenticação, um exemplo por recurso, verificação de webhook `[escrever]` (4 páginas por SDK no início; referência por método gerada quando os SDKs existirem — incerto: os SDKs ainda não existem; até lá, exemplos com HTTP puro)
- CLI `litezap` (`serve`, `worker`, `mcp`, flags) `[escrever]`
- Coleção Postman / Insomnia e arquivo Bruno gerados do OpenAPI `[escrever]`

### 3.9 Projeto e recursos

- Roadmap `[existe]` (`docs/roadmap.md`) — atualizar com M6–M11
- Paridade com a Z-API (tabela resumida da matriz, sem os detalhes internos) `[escrever]`
- Changelog (por versão, gerado de releases/commits convencionais) `[escrever]`
- Status da nuvem (página externa, linkada no topo) `[escrever]` (peça #3)
- Segurança: política de divulgação responsável `[escrever]`
- Contribuir (setup, testes, worker falso, E2E) `[escrever]`
- Licença e termos de uso aceitável (o que o LiteZap não faz: aquecimento, massa) `[escrever]`
- FAQ `[escrever]`

### 3.10 Contagem

| Área | Existem | A escrever / gerar |
|---|---|---|
| Começar | 3 | 4 |
| Conceitos | 8 | 5 |
| Guias | 1 | 11 |
| Self-hosted | 6 | 3 |
| Referência (visão geral, erros, introduções por tag) | 2 + 9 tags | ~12 novas introduções de recurso |
| Referência por operação (hoje) | 0 (agrupadas por tag) | 25 operações no OpenAPI atual + 9 rotas já no código mas fora do OpenAPI (7 de webhooks, 2 streams SSE) |
| Referência por operação (M6–M11) | — | ~65 |
| Eventos | 0 (só no README) | ~25 |
| MCP e IA | 0 | ~10 + uma por ferramenta |
| SDKs e ferramentas | 0 | ~8 |
| Projeto | 1 | 8 |

Os números da referência futura são estimativas a partir da matriz de paridade.

## 4. Anatomia da página de endpoint

Toda página de operação segue a mesma ordem (gerada do OpenAPI, com trechos manuais opcionais por `x-litezap-*`):

1. **Título e resumo** em uma frase; selos: método + caminho, escopo (`admin` / `instância`), marco em que entrou, `experimental` quando for o caso.
2. **Quando usar**: dois a quatro parágrafos curtos, com links para o conceito (ex.: enviar → fila e ritmo, janela, rampa).
3. **Autenticação**: qual token, e o que acontece com o outro (`403`/`404`).
4. **Parâmetros**: tabelas separadas para caminho, query e corpo; colunas nome, tipo, obrigatório, padrão, limites, descrição. Objetos aninhados expansíveis.
5. **Exemplos de requisição** em abas com escolha persistente: `curl`, Node (fetch), Python (`requests`/`httpx`), Go (`net/http`), PHP (Guzzle/cURL). Gerados de um único exemplo do OpenAPI para não divergir.
6. **Resposta de sucesso**: status, tabela de campos e exemplo JSON.
7. **Erros**: só os códigos possíveis nesta operação, cada um com HTTP, `retryable` e a ação recomendada; link para a tabela geral.
8. **Eventos gerados**: quais eventos a chamada dispara (ex.: enviar → `message.sent`, `message.status`), com link para a página do payload.
9. **Notas de confiabilidade e risco**: limites do WhatsApp, risco de banimento, comportamento offline (ex.: fica na fila até conectar).
10. **Equivalente na Z-API** (linha discreta): nome do endpoint correspondente, para quem está migrando.
11. **Experimente**: console com a URL base configurável e token guardado só no navegador (opcional; nunca enviar o token para outro domínio).

Página de evento: resumo, quando é emitido, tabela de campos de `data`, exemplos (um por variação relevante), eventos relacionados, ordem e garantias (pode chegar fora de ordem? duplicado?), versão.

Página de introdução de recurso: conceito, diagrama curto quando ajuda, lista de operações, eventos do recurso, limites, perguntas frequentes.

## 5. Padrões transversais

- **Fonte única**: `openapi.yaml` (gerado ou validado contra as rotas do `chi` em teste) para a referência; catálogo de eventos em Go para payloads; definição das ferramentas MCP para a referência MCP. Teste no CI falha se uma rota existir sem estar no OpenAPI.
- **Idiomas**: pt-BR na raiz e inglês em `/en/`, mesmos slugs; a referência gerada sai nos dois a partir de descrições bilíngues no OpenAPI (`x-i18n`) ou de arquivos de tradução.
- **Busca** (Pagefind do Starlight), modo escuro, "editar no GitHub" (já configurado), última atualização na página.
- **Versão**: seletor quando houver `v2`; referência sempre da versão estável; changelog marca mudanças que quebram compatibilidade.
- **Para IA**: `llms.txt` (índice) e `llms-full.txt` (conteúdo completo) gerados no build; cada página disponível também em `.md`.
- **Tom**: direto, segunda pessoa, sem promessas de "nunca bane"; riscos sempre explícitos.
- **Estabilidade honesta**: páginas de recursos instáveis (botões) têm data da última verificação e o resultado do teste E2E, em vez de tabelas que envelhecem.

## 6. Ordem de execução sugerida

1. Gerar páginas por operação a partir do OpenAPI atual e incluir webhooks e SSE no `openapi.yaml` (fecha a lacuna das rotas do M3).
2. Catálogo de eventos gerado + páginas de verificação de assinatura (conteúdo já pronto no README).
3. Exemplos em 5 linguagens no gerador.
4. "Migrando da Z-API" + paridade resumida (alto valor comercial, pouco código).
5. Guias de webhooks, SSE e solução de problemas.
6. A cada marco M4–M11: referência, eventos e guia do recurso no mesmo PR do código.
