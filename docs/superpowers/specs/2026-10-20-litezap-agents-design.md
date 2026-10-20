# LiteZap Agentes: agentes Claude que operam o WhatsApp (design)

Status: proposta para aprovação, de 2026-10-20.

## 1. Objetivo

A pessoa conecta o WhatsApp uma vez e cria agentes com o Claude que **gerenciam o WhatsApp dela**, sem reiniciar
nada e sem código. Cada agente pode fazer muito mais do que responder: pode ler conversas, etiquetar, mandar
mensagens e mídia, mexer em grupos, agendar follow-ups e resumir o dia, sempre dentro das permissões que a pessoa
deu.

Existem dois jeitos de usar, com o mesmo motor e as mesmas ferramentas:

| Onde | Para quem | Como liga | Quem paga o Claude |
|---|---|---|---|
| **Claude Code / Claude Desktop** (interativo) | Quem já usa o Claude | Plugin LiteZap (`/plugin install`) ou extensão `.mcpb`, sem reiniciar | A assinatura da pessoa, porque é ela usando o próprio Claude |
| **Agentes no app** (autônomos, 24h) | Quem quer automação rodando sozinha | Aba **Agentes** no app: descrever, escolher gatilhos, ligar | A chave de API da pessoa (Anthropic, ou servidor compatível com OpenAI) |

**Por que os agentes autônomos usam chave de API:** os termos da Anthropic não permitem que um produto de
terceiros use o login ou os limites da assinatura do Claude.ai, incluindo agentes feitos com o Agent SDK. O
LiteZap não chama o `claude` CLI da pessoa por conta própria. Quem quiser usar a assinatura usa o plugin no próprio
Claude Code: a pessoa abre a sessão, roda `/whatsapp-responder`, ou deixa um subagente rodando na sessão dela.

## 2. O que já existe e é reaproveitado

- O motor, as 40 ferramentas MCP, a fila anti-ban, a pausa quando um humano responde, o loop guard e os limites
  por chat e por instância.
- O atendente embutido (spec do motor §4.7): perfis, base de conhecimento, provedores, chaves cifradas, simulador,
  multimodal e log de custo.
- O app desktop (janela nativa e "Conectar a IA") e o painel.

## 3. O que é novo

### 3.1 Agente com ferramentas

O atendente de hoje responde a um chat com um JSON fixo (`reply`, `handoff`, `event`). O agente novo roda um
**loop de tool use**: o modelo chama as ferramentas do LiteZap (as mesmas do MCP, executadas em processo, sem
rede) até concluir a tarefa.

- **Ferramentas por agente:** uma lista de permissões em grupos.

  | Grupo | Exemplos |
  |---|---|
  | Ler | conversas, mensagens, contatos, grupos |
  | Responder no chat do gatilho | — |
  | Enviar para outros chats | — |
  | Etiquetas | — |
  | Grupos (gerenciar) | — |
  | Mídia | — |
  | Agenda e follow-up | — |

  O padrão é "Ler" + "Responder no chat do gatilho". Os grupos de risco ("Enviar para outros chats" e "Grupos")
  pedem confirmação explícita.
- **Modo de ação:**
  - **Autônomo:** envia sozinho.
  - **Aprovação:** cada envio vira um rascunho na aba Agentes e no celular, com notificação no app, e a pessoa
    aprova com um clique.
  - **Só sugerir:** escreve uma nota e não envia nada.
  - O padrão de um agente novo é **Aprovação**.
- **Limites:** chamadas por dia, por chat e passos por execução (padrão 8). Mais o anti-ban da fila, que nenhum
  agente contorna.

### 3.2 Gatilhos

- **Mensagem nova:** filtros por chat, etiqueta, palavra-chave, "só contatos que já falaram comigo" e grupos (que
  ficam desligados por padrão).
- **Horário:** cron simples, por exemplo "todo dia 18h, resumo do dia" ou "segunda 9h, follow-up de quem não
  respondeu".
- **Evento:** ligação perdida, lead novo (`agent.lead`), etiqueta aplicada, pedido de entrada em grupo.
- **Manual:** o botão "Executar agora", com entrada opcional.

### 3.3 Criar agente "com o Claude"

Na aba Agentes, a pessoa escreve em português o que quer, por exemplo "quero que atenda meus clientes da loja,
passe pra mim quando pedirem desconto e todo dia às 18h me mande um resumo".

O Claude devolve um **rascunho de agente**: nome, instruções, gatilhos, ferramentas e modo. A pessoa revisa,
testa no **simulador** (que roda o loop com as ferramentas de escrita em modo "faz de conta") e liga.

Os modelos prontos são Atendente, SDR/agendamento, Follow-up, Resumo diário e Organizador (etiqueta conversas).

### 3.4 Registro e controle

- Cada execução registra o gatilho, os passos (ferramenta, argumentos resumidos, resultado), as mensagens
  enviadas, o custo e a duração.
- A aba Agentes mostra o estado (ligado ou pausado), as últimas execuções, os rascunhos pendentes e o gasto do
  dia.
- Um botão de **pausar todos**.

### 3.5 Claude Code (plugin)

O plugin traz:
- o servidor MCP que acha o app local;
- os comandos `/whatsapp-resumo`, `/whatsapp-responder` e `/whatsapp-agente`;
- os subagentes atendente, SDR e follow-up.

`/whatsapp-agente` também pode **exportar** o agente para o app, via API `POST /agents`, para rodar 24h com a chave
de API.

## 4. Segurança (sem mudanças nas regras atuais)

- O conteúdo das mensagens é dado não confiável e vai em marcadores no prompt, como no atendente.
- As ferramentas são executadas pelo motor com as mesmas validações do REST.
- Um agente só envia para fora do chat do gatilho se tiver a permissão "Enviar para outros chats".
- Os envios passam pela fila (ritmo, rampa, janela e TTL).
- Não há envio em massa: o máximo é N destinatários distintos por execução (padrão 3).
- A pausa quando um humano responde vale para todos os agentes do chat.
- Um agente nunca responde a outro agente: o loop guard já existente.
- As chaves continuam cifradas e só o admin configura o provedor.

## 5. Fatias de entrega

1. **A1:** o loop de tool use e as ferramentas em processo, com um agente, o gatilho "mensagem nova", os modos
   Aprovação e Autônomo, o registro, a aba Agentes básica e o simulador.
2. **A2:** vários agentes com roteamento por chat ou etiqueta, os gatilhos de horário e de evento, e os modelos
   prontos.
3. **A3:** "criar com o Claude" (rascunho a partir de texto), a exportação do plugin para o app e as notificações
   de aprovação.

## 6. Fora do escopo agora

- Ligação de voz com IA.
- Integração com calendário externo: por enquanto o agente emite `agent.appointment_requested` para webhooks e
  n8n.
- Rodar a assinatura do Claude dentro do app (§1).
