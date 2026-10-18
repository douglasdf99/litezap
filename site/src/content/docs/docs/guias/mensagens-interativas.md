---
title: Mensagens interativas
description: Botões, listas, OTP/PIX e carrossel. Estado real no WhatsApp e por que são experimentais.
---

<span class="lz-badge lz-badge--exp">Experimental · fora da trilha principal</span>

Botões de resposta, listas de opções, botão de copiar código (OTP), PIX e carrossel são tipos de mensagem que o
WhatsApp oficialmente restringe a contas da API oficial. Em clientes não oficiais eles funcionam de forma
**instável**: podem parar de aparecer, aparecer só em alguns aparelhos ou só em chats individuais, e mudar a cada
atualização do WhatsApp.

## Posição do LiteZap

- **Não estão disponíveis hoje.**
- Se entrarem, será depois do marco 10, atrás de uma opção explícita por instância
  (`interactive_messages: experimental`), com aviso claro de que podem quebrar sem aviso.
- A página terá a data da última verificação e o resultado do teste com número real, em vez de uma tabela que
  envelhece.

## O que já dá para fazer

- **Receber respostas** de botões e listas enviados por outras ferramentas: hoje elas chegam registradas como tipo
  desconhecido; o evento dedicado `message.button_reply` está previsto no marco 7.
- **Menus em texto** ("responda 1 para..., 2 para...") funcionam em qualquer aparelho e não dependem de recurso
  instável.
- **Enquetes** (marco 6) são uma alternativa nativa e estável para escolhas simples.
