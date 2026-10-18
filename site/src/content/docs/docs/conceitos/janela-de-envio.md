---
title: Janela de envio
description: Horário em que mensagens para contatos frios podem sair. Respostas a conversas ativas saem a qualquer hora.
---

A **janela de envio** define o horário do dia em que o LiteZap pode iniciar conversa com quem não está falando com
você. Por padrão é **08:00–20:00** no fuso da instância.

## Para quem vale

A janela vale **só para chats sem mensagem recebida nas últimas 24 horas** (contatos "frios"):

| Situação | Fora da janela |
|---|---|
| O contato mandou mensagem nas últimas 24h (conversa ativa) | Sai na hora |
| O contato nunca falou com você, ou falou há mais de 24h | Espera a próxima abertura |

Assim, um atendimento que responde às 23h continua funcionando, mas uma campanha disparada às 23h só sai às 8h do dia
seguinte. O início de cada janela tem uma variação aleatória de alguns minutos, para que as mensagens não saiam todas no
mesmo segundo.

## Fuso horário

O horário é interpretado no `timezone` da instância (nome IANA, como `America/Sao_Paulo`). Vazio usa o fuso do
servidor. O binário embute a base de fusos, então funciona mesmo em imagens mínimas e no Windows.

:::caution[Servidor em UTC]
Muitos servidores e containers rodam em UTC. Sem `timezone`, a janela padrão 08:00–20:00 vira 05:00–17:00 no horário
de Brasília. Defina o fuso por instância ou em `instance_defaults.timezone`.
:::

## Configurar

```bash
# janela 09:00–18:00 em São Paulo
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"send_window":{"start":"09:00","end":"18:00"},"timezone":"America/Sao_Paulo"}'

# sem janela (start e end vazios): qualquer horário
curl -s -X PUT localhost:8787/v1/instances/$ID/settings -H "Authorization: Bearer $TOK" \
  -d '{"send_window":{"start":"","end":""}}'
```

Regras: horários no formato `HH:MM`, `end` depois de `start` no mesmo dia (janelas que viram a meia-noite não são
aceitas). Desligar a janela é útil para testes; em produção, mantenha-a.
