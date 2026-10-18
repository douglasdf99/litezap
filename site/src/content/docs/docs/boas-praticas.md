---
title: Boas práticas anti-ban
description: Como usar o LiteZap sem queimar números. O que o motor faz por você e o que depende de você.
---

Nenhuma ferramenta não oficial elimina o risco de restrição ou banimento. O que dá para fazer é parecer, e ser, uma
conversa que o destinatário quer ter. O LiteZap foi desenhado para **atendimento, avisos para clientes e contatos que
deram opt-in**. Ele não tem, e não terá, recursos para fabricar perfis, simular conversas de "aquecimento" ou
disparar para listas compradas.

## O que o motor já faz

| Proteção | Padrão | Onde ajustar |
|---|---|---|
| Intervalo mínimo + variação aleatória | 3s + 0–2s | `min_interval_ms`, `jitter_ms` |
| Teto por minuto | 20 | `max_per_minute` (máx. 60 sem `unsafe_pacing`) |
| "Digitando" antes de enviar | ligado | `typing_simulation` |
| Rampa de destinatários novos | 50 → 150/dia em 14 dias | `ramp` |
| Janela para contatos frios | 08:00–20:00 | `send_window`, `timezone` |
| Carência depois de conectar | 15–30s | fixo |
| Verificação do número antes do 1º envio | sempre | fixo |
| Espaçamento de mensagens idênticas para novos | 2× o intervalo | fixo |
| IP de saída estável por número | sempre | `proxy_url` |

Os pisos de ritmo só caem com `unsafe_pacing`, que apenas o admin liga. Pense duas vezes antes.

## O que depende de você

### Antes de enviar

- **Tenha permissão.** Mande mensagem para quem pediu: cliente, lead que preencheu formulário, pessoa que falou com
  você primeiro. Lista comprada é o caminho mais curto para o banimento.
- **Número com histórico.** Um chip recém-ativado que começa enviando para desconhecidos chama atenção. Use o número
  normalmente por um tempo e deixe a rampa ligada.
- **Perfil completo.** Nome, foto e recado coerentes com o seu negócio aumentam a confiança de quem recebe.
- **Higienize a lista.** Números inexistentes geram falhas e sinalizam lista ruim. O LiteZap já verifica antes do
  primeiro envio; para listas, use `check-numbers` com moderação.

### Ao escrever

- **Personalize de verdade.** Nome, pedido, contexto. Centenas de mensagens idênticas são o padrão mais clássico de
  spam, e o LiteZap espaça mais esse caso justamente por isso.
- **Facilite a resposta e a saída.** Mensagens que geram resposta melhoram a reputação do número. Ofereça um jeito
  simples de parar de receber e respeite.
- **Cuidado com links e mídia** na primeira mensagem para alguém. Prefira apresentar-se primeiro e mandar o link
  quando a pessoa responder.

### No volume

- **Comece pequeno e cresça devagar.** A rampa existe por isso; não a desligue num número novo.
- **Respeite o horário.** A janela de envio evita mensagens frias de madrugada. Configure o `timezone` certo.
- **Um número por IP.** Em servidores, use um [proxy](/docs/conceitos/proxy/) por instância. Vários números no mesmo
  IP de datacenter são um sinal forte.
- **Não troque de IP o tempo todo.** Estabilidade é melhor que rotação.

### Observe os sinais

- Muitas mensagens sem resposta, bloqueios e denúncias derrubam o número mais que qualquer configuração. Se a taxa de
  resposta cair, pare e revise o público e o texto.
- `state` indo para `failed` com `state_reason: temp_banned:<duração>` é um aviso: o WhatsApp suspendeu o número
  temporariamente. Pare os envios frios e espere.
- `banned` é definitivo para aquele número.

## Resumo

1. Envie para quem quer receber.
2. Mantenha os padrões do LiteZap: ritmo, rampa e janela.
3. Personalize e provoque respostas.
4. Um número, um IP estável.
5. Ao primeiro sinal de restrição, reduza.
