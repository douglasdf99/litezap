---
title: IA grátis
description: Use o Atendente IA e os Agentes do LiteZap sem pagar nada e sem cartão de crédito — no seu computador com o Ollama ou na nuvem com o plano grátis do Google Gemini ou da Groq.
---

O [Atendente IA](/docs/guias/atendimento-ia/) e os [Agentes](/docs/guias/agentes/) funcionam **de graça**. Você não
precisa de cartão de crédito nem de conta paga. Há três caminhos:

| Opção | Custo | Precisa de | Qualidade | Velocidade | Privacidade |
|---|---|---|---|---|---|
| **No meu computador (Ollama)**, recomendado | Grátis, sem limite | Um computador com 8 GB de RAM (4 GB com o modelo pequeno) e uns 5 GB livres em disco | Boa para atendimento simples; erra mais que os pagos em tarefas longas | De 2 a 20 s por resposta, conforme o computador | As conversas **não saem** do seu computador |
| **Google Gemini grátis** | Grátis, com limite por minuto e por dia | Uma conta Google | Muito boa, perto dos pagos | Rápida (1 a 5 s) | No plano grátis o **Google pode usar as conversas** para melhorar os modelos |
| **Groq grátis** | Grátis, com limite por minuto | Uma conta grátis na Groq | Boa (modelo Llama de 70B) | Muito rápida (menos de 2 s) | As conversas passam pelos servidores da Groq (EUA) |
| Pago (Anthropic Claude) | Por uso, com cartão | Chave de API da Anthropic | A melhor | Rápida | Sem uso para treino |

No painel, aba **Atendente IA** (ou a aba **Agentes**, enquanto não houver IA configurada), escolha uma opção em
**Qual IA usar?**. Só o token admin altera o provedor.

## No meu computador (Ollama)

O [Ollama](https://ollama.com) é um programa grátis e de código aberto que roda modelos de IA no seu computador. O
LiteZap conversa com ele só pelo endereço local `http://127.0.0.1:11434`, nunca por outro endereço.

1. **Instale o Ollama** pela página oficial: [ollama.com/download](https://ollama.com/download) (Mac, Windows e Linux).
   O LiteZap não instala nada sozinho.
2. **Abra o Ollama.** No Mac e no Windows ele fica perto do relógio; no Linux roda como serviço.
3. No painel, escolha **Grátis no meu computador** e clique em **Verificar de novo**. O LiteZap mostra a versão, a
   memória do computador e o modelo recomendado.
4. Clique em **Baixar modelo** e acompanhe a barra de progresso (o download é feito pelo próprio Ollama e continua de
   onde parou se cair).
5. Clique em **Usar**. Pronto: o atendente e os agentes passam a usar o modelo local, com custo **Grátis** no uso.

### Qual modelo para qual computador

| Computador | Modelo | Download | Observação |
|---|---|---|---|
| 8 GB de RAM ou mais (Mac M1 ou mais novo, PC com placa de vídeo) | `qwen2.5:7b` (recomendado) | 4,7 GB | Bom em português e no uso de ferramentas |
| 4 a 8 GB de RAM | `llama3.2:3b` | 2,0 GB | Mais rápido, porém erra mais; prefira o modo Aprovação nos agentes |

Os dois suportam *tool calling* (necessário para os Agentes). Também funciona baixar pelo terminal:
`ollama pull qwen2.5:7b`. Feche programas pesados enquanto o modelo responde em computadores com pouca memória.

:::tip[Modelos pequenos erram mais]
O LiteZap ajuda os modelos locais: usa instruções mais curtas e em português, com a lista de ferramentas escrita,
corrige chamadas de ferramenta mal formatadas (JSON quebrado, chamada escrita como texto) e, se o modelo errar,
explica o erro e dá **uma** nova chance antes de encerrar a execução com segurança (nada é executado a partir de uma
chamada inválida). Mesmo assim, comece os agentes no modo **Aprovação** e teste no simulador.
:::

O computador precisa ficar ligado com o Ollama aberto para o atendente responder. Para um LiteZap num servidor (VPS),
instale o Ollama no mesmo servidor; o LiteZap só fala com o Ollama local.

## Google Gemini grátis

1. Abra [aistudio.google.com/apikey](https://aistudio.google.com/apikey) e entre com sua conta Google.
2. Clique em **Create API key** (Criar chave de API). Não é preciso cadastrar cartão.
3. No painel, escolha **Grátis na nuvem (Google Gemini)**, cole a chave e clique em **Usar**.

O modelo padrão é o `gemini-2.5-flash` (o `gemini-2.5-flash-lite` é mais rápido e mais simples). O LiteZap usa o
endpoint OpenAI-compatível do Google (`https://generativelanguage.googleapis.com/v1beta/openai`).

:::caution[Seus dados no plano grátis]
No plano grátis da API do Gemini, o Google pode usar o que você envia e recebe para melhorar os produtos dele, e
pessoas podem revisar esse conteúdo. Não use para dados sensíveis dos seus clientes. Os limites (pedidos por minuto e
por dia) mudam com o tempo: confira no AI Studio. Ao estourar, o atendente falha naquela mensagem e você pode assumir.
:::

## Groq grátis

1. Crie uma conta grátis e abra [console.groq.com/keys](https://console.groq.com/keys).
2. Clique em **Create API Key**. Não é preciso cadastrar cartão.
3. No painel, escolha **Groq grátis**, cole a chave e clique em **Usar**.

O modelo padrão é o `llama-3.3-70b-versatile`, que usa ferramentas bem; o `llama-3.1-8b-instant` é mais rápido. O plano
grátis limita pedidos e tokens por minuto, o que pode atrasar agentes que fazem muitas chamadas seguidas.

## Pela API

Os presets são atalhos sobre o provedor `openai` (qualquer servidor OpenAI-compatível): `preset` preenche
`base_url` e `model`.

```bash
# Ollama no mesmo computador (sem chave)
curl -s -X PUT $LZ/v1/instances/$ID/attendant -H "Authorization: Bearer $ADMIN" -d '{"preset":"ollama"}'

# Gemini grátis
curl -s -X PUT $LZ/v1/instances/$ID/attendant -H "Authorization: Bearer $ADMIN" \
  -d '{"preset":"gemini_free","api_key":"AIza..."}'

# Groq grátis, com outro modelo
curl -s -X PUT $LZ/v1/instances/$ID/attendant -H "Authorization: Bearer $ADMIN" \
  -d '{"preset":"groq_free","model":"llama-3.1-8b-instant","api_key":"gsk_..."}'
```

Com o token admin, `GET /v1/ai/presets` lista os presets; `GET /v1/ai/local` verifica o Ollama (versão, modelos
instalados, memória e modelo recomendado); `POST /v1/ai/local/pull` com `{"model":"qwen2.5:7b"}` começa o download e
`GET /v1/ai/local/pull` mostra o progresso. Com um preset grátis ou um servidor local, `GET /attendant` traz
`free: true` e o custo estimado é zero.

## Prós e contras

- **Ollama**: grátis de verdade e privado, mas depende do seu computador (ligado, com memória livre) e a qualidade é
  menor. Ideal para responder dúvidas simples, horários e catálogo.
- **Gemini grátis**: a melhor qualidade sem pagar, mas com limites que mudam e uso dos dados pelo Google.
- **Groq grátis**: a mais rápida, com limites por minuto que podem travar agentes com muitas ferramentas.
- **Claude (pago)**: a melhor qualidade e o uso mais confiável de ferramentas, cobrado por uso.

Você pode trocar a qualquer momento, sem perder os agentes: o provedor é um só por instância, e os limites diários
(`daily_call_cap`) continuam valendo em todas as opções.
