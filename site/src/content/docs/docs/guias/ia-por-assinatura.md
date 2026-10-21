---
title: IA por assinatura
description: Rodar agentes do WhatsApp com uma assinatura que você já paga (Claude ou ChatGPT) em vez de pagar a API por chamada — o que é permitido, como configurar e os limites reais.
---

Os [Agentes](/docs/guias/agentes/) e o [Atendente IA](/docs/guias/atendimento-ia/) normalmente usam um **provedor de
IA com chave de API**: cada chamada é cobrada. Se você já paga uma assinatura (Claude Pro/Max ou ChatGPT
Plus/Pro/Business), há dois caminhos para usar esse custo fixo. Eles são bem diferentes, porque as regras da Anthropic
e da OpenAI são diferentes.

| | Assinatura Claude | Assinatura ChatGPT |
|---|---|---|
| Como | **Plantão** numa sessão sua do Claude Code, com o plugin LiteZap | Agente do app com o runtime **Codex CLI** |
| Quem roda o modelo | Você, no Claude Code | O LiteZap, chamando o `codex` instalado e logado por você |
| Precisa de sessão aberta | Sim | Não (só o app LiteZap rodando e o computador acordado) |
| Permissões e limites do agente | As regras do comando `/litezap:whatsapp-plantao` e as permissões do Claude Code | As mesmas dos agentes do app, aplicadas pelo LiteZap em cada chamada |
| Status das regras | Permitido | **Não está claro** — opt-in com aviso |

## Assinatura Claude: plantão no Claude Code

### Por que não é um "provedor" do app

A Anthropic **não permite** que produtos de terceiros ofereçam o login do claude.ai nem usem os limites de uma
assinatura, inclusive agentes feitos com o Agent SDK. Uma ferramenta de terceiros também não pode encaminhar pedidos
pelas credenciais Free, Pro ou Max dos seus usuários. Por isso o LiteZap **nunca** chama a sua assinatura Claude
sozinho (não há um `claude -p` rodando por trás do app).

O que é permitido é **você** usar o Claude Code oficial, logado com a sua assinatura, de forma comum e individual. O
plugin LiteZap só entrega as ferramentas do WhatsApp para essa sessão. Fontes:
[Legal and compliance do Claude Code](https://code.claude.com/docs/en/legal-and-compliance) e
[Agent SDK overview](https://code.claude.com/docs/en/agent-sdk/overview).

### Como usar

1. Instale o plugin LiteZap no Claude Code (painel do LiteZap > **Conectar a IA** > Claude Code, ou
   [Plugin do Claude Code](/docs/mcp/claude-code-plugin/)).
2. (Opcional, recomendado) Escreva o perfil do plantão em `.claude/whatsapp-plantao.md` no projeto em que você abre o
   Claude Code. Exemplo:

   ```markdown
   Você atende a Padaria Pão Quente. Responda curto e cordial.
   - Horário: seg a sáb, 6h às 20h; domingo, 7h às 12h.
   - Endereço: Rua das Flores, 120.
   - Encomendas de bolo: só com 48h de antecedência; quem confirma é a Ana.
   Passe para um humano: reclamação, desconto, encomenda, qualquer coisa fora desta lista.
   ```

   Também serve um subagente (`perfil: atendente-whatsapp`) ou o agente ativo do Atendente LiteZap, que o plantão lê
   sem alterar.
3. Rode uma rodada para conferir:

   ```
   /litezap:whatsapp-plantao
   ```

   Por padrão é **modo rascunho**: o Claude mostra as respostas propostas e só envia as que você aprovar.
4. Para ficar de plantão, combine com o `/loop` do Claude Code e deixe a sessão aberta:

   ```
   /loop 3m /litezap:whatsapp-plantao
   ```

   Para enviar sem perguntar, diga explicitamente: `/loop 3m /litezap:whatsapp-plantao autônomo`.

### Limites honestos

- Só funciona **enquanto a sessão do Claude Code estiver aberta**, com o computador acordado e o LiteZap rodando.
- Cada rodada consome o **limite de uso da sua assinatura**. Os limites dos planos pressupõem uso individual comum; um
  plantão muito frequente pode esgotar o limite e parar até ele renovar.
- No modo rascunho, cada rodada com mensagens espera você responder: não é atendimento sem supervisão.
- O plantão não usa os limites e permissões configurados nos Agentes do app: as regras são as do comando (só responde
  quem escreveu, no chat dele, até 10 envios por rodada) e as permissões do Claude Code (`send_message` pede
  autorização na primeira vez).
- Desligue o Atendente LiteZap e os Agentes do app no mesmo número, ou dois atendentes vão responder o mesmo chat.

## Assinatura ChatGPT: agentes com o Codex CLI

### O que o LiteZap faz

Com o runtime **Minha assinatura ChatGPT (Codex)**, a cada gatilho o LiteZap roda `codex exec` (o modo não interativo
do [Codex CLI](https://developers.openai.com/codex/)) que **você** instalou e logou neste computador. O Codex usa o seu
login salvo; o LiteZap não lê nem guarda credenciais da OpenAI e não repassa chaves de API ao Codex (as variáveis
`OPENAI_API_KEY` e `CODEX_API_KEY` são removidas do ambiente do processo).

Cada execução é isolada:

- **Sem shell, arquivos, navegador nem plugins:** os recursos do Codex que fazem isso são desligados (`--disable`), o
  sandbox é somente leitura, a pasta de trabalho é vazia e temporária, a configuração e as regras do usuário
  (`--ignore-user-config`, `--ignore-rules`) e a busca na web ficam de fora, e nada é salvo em disco (`--ephemeral`).
- **Só as ferramentas do agente:** o Codex recebe as ferramentas do LiteZap por um endpoint MCP que existe só durante
  aquela execução, em `127.0.0.1`, com um token aleatório da execução (nunca o token admin nem o da instância). Esse
  endpoint só oferece as ferramentas das permissões do agente, e **cada chamada passa pelo mesmo motor dos agentes**:
  envio só no chat do gatilho (sem "Enviar para outros chats"), leitura só da conversa do gatilho (sem "Ler outras
  conversas"), limites de envios, destinatários e ações por execução, `mentions: ["all"]` recusado, modos
  aprovação/autônomo/sugerir, pausa quando um humano responde e a fila anti-ban.
- **Limites de tempo e de chamadas:** a execução tem um tempo máximo (150 s) e um teto de chamadas de ferramenta (3 ×
  "passos por execução").
- **Vigiado em tempo real:** o LiteZap lê os eventos do Codex enquanto ele roda. Se aparecer qualquer coisa além do
  texto do modelo, do raciocínio ou de uma chamada às ferramentas do LiteZap (um comando, uma mudança de arquivo, uma
  busca na web, outro servidor MCP ou um tipo novo), o Codex é encerrado na hora e a execução falha com
  `codex_violation`.
- **Casa privada:** cada processo do Codex roda com um `HOME` e um `CODEX_HOME` temporários (pasta `0700`) que só têm
  uma cópia do seu `auth.json` (`0600`), apagados no fim. O Codex não vê o seu `~/.codex`, a pasta de dados do LiteZap
  nem o resto da sua pasta pessoal. Se o Codex renovar o token durante a execução, o `auth.json` novo é gravado de volta
  no seu `~/.codex` (só se o original não mudou nesse meio tempo).
- **Binário conferido:** o `codex` (e o `node`, quando o `codex` é um script) só roda se nem o arquivo nem nenhuma pasta
  acima dele puder ser alterada por outro usuário, conferido de novo a cada execução. O `PATH` do processo tem só essas
  pastas e `/usr/bin` e `/bin`.
- **Falha fechada:** o runtime só fica pronto numa faixa de versões verificada (codex-cli 0.145.x até antes de 0.200) e
  quando o `codex features list` funciona e lista todos os recursos perigosos (shell, exec, apps, plugins, hooks,
  navegador, computer use, sub-agentes), que são desligados. Fora disso, o card mostra o motivo e nada roda.
- **Rede:** o Codex recebe as variáveis de proxy do LiteZap (`HTTPS_PROXY`, `HTTP_PROXY`, `ALL_PROXY`, `NO_PROXY`) e os
  certificados (`SSL_CERT_FILE`, `SSL_CERT_DIR`), para chegar à OpenAI pelo mesmo caminho que o resto do computador.
  Nenhuma outra variável de ambiente é repassada.

O custo mostrado nas execuções é US$ 0 (os tokens aparecem, mas o uso conta no seu plano, não na API).

### Configurar

1. Instale o Codex CLI: `npm install -g @openai/codex` (ou `brew install codex`).
2. No Terminal, rode `codex login` e entre com a sua conta ChatGPT. Se o Codex estiver logado com uma chave de API, o
   uso é cobrado por chamada: rode `codex logout` e `codex login` de novo.
3. No painel: aba **Agentes** > card **IA por assinatura**. Ele mostra se o Codex foi encontrado e se está logado
   (detectar só roda `codex --version`, `codex login status` e `codex features list`, sem chamar o modelo).
4. Edite um agente e, em **Quem pensa**, escolha **Minha assinatura ChatGPT (Codex)** e aceite o aviso. Só o token
   admin pode criar, editar, executar ou simular um agente com esse runtime, porque ele usa o login do computador onde
   o LiteZap roda (as mensagens novas continuam disparando o agente normalmente).
   O login precisa estar no arquivo `~/.codex/auth.json` (o padrão do `codex login`).
5. Teste com o simulador ou **Executar agora** antes de ligar o gatilho.

Pela API: `PUT /v1/instances/{id}/agents/{agent_id}` com `{"runtime": "codex_cli", "accept_runtime_notice": true}`, e
`GET /v1/instances/{id}/agents/runtimes` para o status. Se o app não achar o `codex` (apps abertos pelo Finder não
veem o `PATH` do Terminal), defina `LITEZAP_CODEX_PATH` com o caminho completo.

### Regras da OpenAI: o que se sabe

:::caution[Não está claro se é permitido]
- A OpenAI documenta o `codex exec` para scripts, CI e tarefas agendadas e diz que ele **reutiliza o login salvo** do
  CLI ([Non-interactive mode](https://developers.openai.com/codex/noninteractive)).
- Ao mesmo tempo, recomenda **chave de API** como padrão para automação e trata o login do ChatGPT em automação como
  opção avançada para máquinas confiáveis ([Authentication](https://developers.openai.com/codex/auth)).
- Os [Termos de Uso](https://openai.com/policies/terms-of-use/) proíbem extrair dados ou respostas "de forma automática
  ou programática" e compartilhar a conta com terceiros.
- Nenhuma página diz se um app de terceiros pode acionar o Codex CLI do próprio usuário com o plano ChatGPT.

Por isso o runtime vem **desligado**, é escolhido agente por agente e exige aceitar um aviso. Você decide e responde
por esse uso; a OpenAI pode limitar ou bloquear a conta. Se quiser ficar do lado seguro, use uma chave de API (no
provedor do Atendente IA ou no próprio Codex) ou um modelo local.
:::

### Limites honestos

- O LiteZap e o computador precisam estar ligados; o `codex` precisa continuar logado.
- O uso conta nos **limites do seu plano ChatGPT**. Quando o limite acaba, as execuções falham (`codex_error`) até ele
  renovar.
- Cada gatilho abre um processo do Codex: é mais lento que a API (alguns segundos a mais por resposta).
- O conteúdo das conversas vai para a OpenAI, como em qualquer provedor de nuvem.
- O LiteZap não controla qual modelo o seu plano usa; o padrão é o do Codex.
