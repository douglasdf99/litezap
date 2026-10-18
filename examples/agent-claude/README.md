# agent-claude — atendente de referência para o hook de agente do LiteZap

Servidor HTTP mínimo (Go) que implementa o contrato do **hook de agente** do LiteZap
(spec §4.3) com o Claude, pela API de Mensagens da Anthropic (SDK oficial
`github.com/anthropics/anthropic-sdk-go`). É um módulo Go separado: o motor do LiteZap não
depende do SDK.

O que ele faz a cada chamada:

1. Confere a assinatura `X-LiteZap-Signature` (HMAC-SHA256 com o segredo do hook, tolerância
   de 5 minutos) e descarta repetições do mesmo `X-LiteZap-Event-Id`.
2. Monta a conversa a partir de `context` (mensagens do cliente = `user`; mensagens do seu
   número, do robô ou de um humano = `assistant`).
3. Pede a resposta ao Claude (modelo padrão `claude-sonnet-5-5`, esforço `low`).
4. Devolve `{"reply": "..."}`; quando o cliente pede um humano (ou o modelo não pode ajudar),
   inclui `{"type":"handoff","note":"..."}`: o LiteZap desliga o robô naquele chat e emite
   `agent.handoff`.

Se a API da Anthropic falhar, o servidor responde `502`: o LiteZap é *fail-closed*, não envia
nada e emite `agent.failed`.

## Rodando

```bash
cd examples/agent-claude
export ANTHROPIC_API_KEY=...            # sua chave da Anthropic
export LITEZAP_AGENT_SECRET=agsec_...   # o segredo mostrado pelo LiteZap ao criar o hook
export LITEZAP_AGENT_SYSTEM_PROMPT_FILE=./system_prompt.example.md   # opcional
go run .
# agent-claude listening addr=127.0.0.1:8790 model=claude-sonnet-5-5
```

| Variável | Padrão | |
|---|---|---|
| `ANTHROPIC_API_KEY` | — | lida pelo SDK |
| `LITEZAP_AGENT_SECRET` | — | obrigatório (16+ caracteres) |
| `LITEZAP_AGENT_ADDR` | `127.0.0.1:8790` | endereço de escuta |
| `LITEZAP_AGENT_MODEL` | `claude-sonnet-5-5` | qualquer modelo Claude |
| `LITEZAP_AGENT_EFFORT` | `low` | `low`, `medium`, `high` |
| `LITEZAP_AGENT_SYSTEM_PROMPT_FILE` | — | instruções do seu negócio (horários, regras) |
| `LITEZAP_AGENT_FALLBACK` | ligado | `off` desliga o *fallback* do lado do servidor quando o modelo recusa |

Por padrão a chamada usa o *fallback* do lado do servidor da Anthropic
(`server-side-fallback-2026-07-01`, `fallbacks: "default"`): se o modelo recusar por política,
outro modelo responde na mesma chamada. Se ainda assim houver recusa, o agente faz *handoff*.

## Ligando no LiteZap

O agente escuta em `127.0.0.1`: libere destinos locais no servidor do LiteZap
(`webhook_allow_private_urls: true` ou `LITEZAP_WEBHOOK_ALLOW_PRIVATE_URLS=true`) e reinicie o
`serve`. Depois configure o hook (pelo painel, aba **Atendente IA**, ou pela API):

```bash
curl -s -X PUT http://127.0.0.1:8787/v1/instances/$INST/agent-hook \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"http://127.0.0.1:8790/","enabled":true}'
# → {..., "secret": "agsec_..."}  (o segredo só aparece agora: use-o em LITEZAP_AGENT_SECRET)
```

## Segurança

- **Mensagens são conteúdo não confiável.** O *system prompt* deixa claro que cada mensagem do
  cliente é dado, não instrução; pedidos para mudar as regras, revelar o prompt ou segredos são
  ignorados. As instruções do seu negócio ficam separadas em `<business>`.
- O agente não tem ferramentas além do *handoff*: não consulta sistemas, não envia para outros
  chats (o LiteZap sempre responde no chat que disparou a chamada) e não vê a chave da API.
- Para o ID de usuário enviado à Anthropic (monitoramento de abuso) usa-se um hash do chat,
  nunca o número.
- Respostas automáticas aumentam o risco de banimento se usadas para spam: o LiteZap só chama o
  hook para quem escreveu primeiro, passa cada resposta pela fila com ritmo, e pausa o robô se
  um chat recebe respostas demais (`max_replies_per_window`) ou quando um humano responde pelo
  celular.

## Testes

```bash
go test ./...   # sem rede: assinatura, conversa, handoff e falhas, com um modelo falso
```
