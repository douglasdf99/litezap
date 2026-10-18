---
title: Atendente IA
description: O Atendente LiteZap embutido — agentes treinados com o contexto do negócio, modos vendedor, pedido, SDR e suporte, áudio, imagens e ligações.
---

O LiteZap tem um **atendente de IA embutido**: ele roda dentro do motor, então você não precisa hospedar um agente.
Você escolhe o provedor (Claude ou qualquer servidor OpenAI-compatível), cria um ou mais **agentes** com o contexto
do seu negócio e liga o atendimento. Se preferir o seu próprio agente, o [hook de agente](/docs/mcp/hook-de-agente/)
continua funcionando com a sua URL.

Em qualquer dos dois, as respostas saem pela **mesma fila** de qualquer envio (ritmo, janela, rampa, suspensão do
operador), uma conversa por vez, com **pausa automática quando um humano responde pelo celular**, proteção contra
dois robôs conversando e grupos desligados por padrão.

## 1. Configurar o provedor (admin)

No painel, aba **Atendente IA** da instância, bloco "Atendente LiteZap", ou pela API com o token admin:

```bash
# Claude (padrão: claude-sonnet-5-5)
curl -s -X PUT $LZ/v1/instances/$ID/attendant -H "Authorization: Bearer $ADMIN" \
  -d '{"provider":"anthropic","api_key":"sk-ant-...","daily_call_cap":300}'

# ou um servidor OpenAI-compatível (OpenAI, Ollama, LM Studio, vLLM...)
curl -s -X PUT $LZ/v1/instances/$ID/attendant -H "Authorization: Bearer $ADMIN" \
  -d '{"provider":"openai","base_url":"http://127.0.0.1:11434/v1","model":"llama3.1"}'
```

- A chave é **cifrada em repouso** (AES-256-GCM com o arquivo `data/attendant_key`, permissão 0600) e **nunca volta**
  na API (`api_key_set: true`). Guarde o `attendant_key` junto do backup do banco: sem ele, as chaves não abrem — e lembre que um backup com os dois
  arquivos dá acesso às chaves, então proteja-o.
- `daily_call_cap` (padrão 300) limita as chamadas ao provedor por dia (inclui transcrição e voz). Estourou: o
  atendente para até o dia seguinte e sai `agent.failed` com `reason: daily_cap`. `GET /attendant/usage` mostra
  chamadas, tokens e custo estimado por dia. `chat_daily_call_cap` (padrão 40) limita cada conversa, para um único contato não esgotar o limite da instância, e `simulate_daily_cap` (padrão 50) limita o simulador.
- **Assinatura do Claude x chave de API**: a assinatura Claude (Pro/Max) só serve para testes locais com um agente
  seu (por exemplo o [`examples/agent-claude`](https://github.com/douglasdf99/litezap/tree/main/examples/agent-claude)
  rodando na sua máquina com as suas credenciais). Para o atendente embutido e para produção, use uma **chave de API**
  da Anthropic (ou do provedor escolhido).

## 2. Criar um agente

Cada agente tem um **modo** pronto e editável — objetivo, roteiro, tom, quando passar para humano e ações
permitidas — mais o **contexto do negócio** (texto livre) e a **base de conhecimento**: perguntas e respostas,
catálogo com preço, horários e políticas.

```bash
curl -s -X POST $LZ/v1/instances/$ID/attendant/agents -H "Authorization: Bearer $TOK" -d '{
  "name": "Bia da Pizzaria",
  "mode": "pedido",
  "business": "Pizzaria do Zé, Rua A 10, Centro. Entrega em até 40 min. Pix ou cartão na entrega.",
  "knowledge": [
    {"kind":"product","title":"Pizza Margherita","price":45,"content":"8 fatias"},
    {"kind":"hours","title":"Seg a Dom","content":"18h às 23h"},
    {"kind":"faq","title":"Tem sem glúten?","content":"Sim, massa sem glúten por +R$ 8."}
  ]}'
```

Os campos de roteiro que você não mandar vêm do modo (`GET /attendant/presets`). O primeiro agente criado é o que
responde; troque com `POST /attendant/agents/{id}/activate`.

### Modos

| Modo | O que faz | Evento |
|---|---|---|
| `vendedor` | Entende a necessidade, indica produtos do catálogo com preço e conduz para a compra, sem inventar preço. | `agent.lead` |
| `pedido` (facilitador de pedido) | Monta o pedido com itens do catálogo, confirma quantidades, endereço/retirada, mostra o resumo com total e pede confirmação. | `agent.order_draft` |
| `sdr` (agendamento) | Qualifica, coleta nome, necessidade e horário de preferência e diz que a equipe confirma. | `agent.lead`, `agent.appointment_requested` |
| `suporte` | Responde só com a base de conhecimento; sem resposta na base, passa para humano. | — |
| `custom` | Começa vazio: você escreve tudo. | o que você permitir |

Todos podem **passar para humano** (`handoff`): o atendente avisa o cliente, desliga naquela conversa e emite
`agent.handoff`. Reative pelo painel ("Por conversa") ou com `PUT /chats/{jid}/agent {"enabled": null}`.

#### Vendedor

1. Coloque o catálogo na base (`kind: product`, com `price`) e as políticas de troca, entrega e pagamento
   (`kind: policy`).
2. Permita `lead` para receber `agent.lead` (nome e necessidade) no seu CRM via webhook.
3. Teste no simulador: "quero um presente até 100 reais" deve trazer 1 a 3 produtos do catálogo com preço.

#### Facilitador de pedido

1. Catálogo completo com preços. O motor **recalcula o total** com os preços do catálogo; itens que a IA citou fora
   do catálogo chegam com `in_catalog: false` e `all_items_in_catalog: false` — **sempre confira esse campo** antes de
   aceitar o pedido.
2. Ligue `order_draft`. Cada resumo novo gera `agent.order_draft` com itens, total, moeda e endereço; repetições
   idênticas na mesma conversa não são reemitidas.
3. A confirmação do cliente ("sim") é a próxima mensagem: trate no seu sistema (n8n, ERP) e responda o cliente.

#### SDR / agendamento

1. Coloque os horários de atendimento (`kind: hours`) e os serviços.
2. Ligue `lead` e `appointment`. Com nome, necessidade e horário, sai `agent.appointment_requested`
   (`preferred_time` como o cliente disse, com data ISO quando clara). **Nenhuma agenda é tocada**: confirme pela sua
   agenda e responda o cliente.

#### Suporte

1. Uma entrada `faq` por dúvida comum; políticas com `kind: policy`.
2. Em "Quando passar para humano", liste o que nunca deve ser resolvido pela IA (reembolso, dados pessoais).

## 3. Testar antes de publicar

O **simulador** roda o agente contra mensagens de exemplo **sem enviar nada** (conta como chamada ao provedor):

```bash
curl -s -X POST $LZ/v1/instances/$ID/attendant/simulate -H "Authorization: Bearer $TOK" -d '{
  "messages": [
    {"from":"automatic","text":"Não atendemos ligações, mande mensagem."},
    {"text":"quero 2 margheritas pra Rua B 5"}
  ]}'
```

`from` pode ser `customer` (padrão), `agent`, `human` ou `automatic`. Mande `agent` com um perfil ainda não salvo
para testar mudanças; no painel, use "Testar sem salvar".

## 4. Ligar o atendimento

```bash
curl -s -X PUT $LZ/v1/instances/$ID/agent-hook -H "Authorization: Bearer $TOK" -d '{"target":"builtin","enabled":true}'
```

Ou, no painel, "Quem responde: Atendente LiteZap" e "Responder automaticamente". Desligue numa conversa com
`PUT /chats/{jid}/agent {"enabled": false}`.

## Áudio, imagens e ligações

- **Mensagem de voz**: com `audio.transcription_model` (ex.: `whisper-1`) num servidor OpenAI-compatível
  (`/audio/transcriptions`), o áudio é transcrito e o agente responde ao conteúdo. Sem transcrição, ele sabe só que
  chegou um áudio.
- **Resposta em voz**: com `audio.speech_model` (ex.: `gpt-4o-mini-tts`, `/audio/speech`) e `voice_reply` no agente
  (`when_audio`: responde áudio com áudio; `always`: toda resposta vira áudio — mais caro e mais lento, use com
  cuidado), a resposta sai como nota de voz (Ogg/Opus). Se a síntese falhar, sai em texto.
- **Imagens e PDFs**: com `vision: true`, a imagem (e o PDF, no Claude) da mensagem vai para o modelo. Arquivos de
  texto pequenos vão como texto.
- **Ligação recebida**: com `calls.auto_reject` ligado e `call_followup` no agente, o atendente escreve para quem
  ligou ("vi que você ligou, como posso ajudar?") pela fila, no máximo uma vez por chamador no intervalo de
  cooldown. Sem `call_followup` no agente, nada muda: sai só o `calls.reject_message` (se houver). Se a IA falhar ou ficar em silêncio, sai o `reject_message` fixo.

## Segurança

- **Prompt injection**: as mensagens do cliente vão para o modelo como dados não confiáveis, dentro de marcadores
  que o cliente não consegue fechar; o modelo é instruído a nunca revelar as instruções nem chaves, e uma resposta que
  contenha o marcador interno do prompt ou uma chave é **bloqueada** (nada é enviado, `agent.failed`).
- **Só a conversa que disparou**: a IA não tem ferramentas de envio; o motor manda a resposta sempre para o chat
  que escreveu, uma vez por mensagem.
- **Dados pessoais**: o telefone do cliente não vai para o modelo (o provedor recebe um pseudônimo estável); o nome
  do WhatsApp vai, a menos que você marque "Não enviar o nome do contato".
- **Contexto rotulado**: o modelo distingue o cliente, as próprias respostas, um humano da equipe, mensagens
  automáticas (como o aviso de ligação recusada) e outros sistemas — ele não repete a fala automática como se fosse
  dele.
- **Custo**: `daily_call_cap`, a proteção contra loop (`max_replies_per_window`) e `max_tokens` limitam o gasto.
- **Banimento**: respostas automáticas só vão para quem escreveu primeiro e respeitam o ritmo; mantenha grupos
  desligados e o limite de respostas baixo.
