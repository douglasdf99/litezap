---
title: Perguntas frequentes
description: Respostas curtas para as dúvidas mais comuns sobre o LiteZap.
---

## O LiteZap é a API oficial do WhatsApp?

Não. É uma API não oficial, que conecta o número como um aparelho vinculado (como o WhatsApp Web), usando a biblioteca
open source whatsmeow. Não tem afiliação com o WhatsApp ou a Meta.

## Meu número pode ser banido?

Pode. Qualquer automação não oficial tem esse risco. O LiteZap reduz o risco com ritmo, rampa, janela de envio e
verificação de número ligados por padrão, mas o que mais pesa é o seu uso: para quem você envia, quanto e com que
conteúdo. Leia as [boas práticas](/docs/boas-praticas/).

## Preciso deixar o celular ligado?

Não o tempo todo: o número funciona como aparelho vinculado. Mas o WhatsApp desconecta aparelhos vinculados se o
celular principal ficar muito tempo sem conexão, então mantenha-o ligado e conectado de vez em quando.

## Quanto custa?

O motor é grátis e open source (Apache-2.0), para rodar no seu PC ou servidor. A nuvem LiteZap (gerenciada) terá
cobrança mensal por instância e ainda não está disponível.

## Posso usar vários números?

Sim. Cada número é uma instância com token e processo próprios. Para muitos números, use um proxy por instância.

## Funciona com grupos?

Recebe mensagens de grupos e envia para grupos de que o número participa. A gestão de grupos (criar, participantes,
admins) chega no [M8](/docs/guias/grupos/).

## Tem botões e listas?

Não, e se vierem será como recurso experimental. Veja [mensagens interativas](/docs/guias/mensagens-interativas/).

## Faz chamadas de voz?

Não. A biblioteca de protocolo não suporta mídia de chamada. A rejeição automática de chamadas chega no M7.

## Posso mandar mensagens em massa?

O LiteZap foi feito para conversas que o destinatário espera: atendimento, avisos para clientes, contatos com
opt-in. Ele não tem listas de transmissão e aplica limites de propósito. Veja o [uso aceitável](/docs/uso-aceitavel/).

## Onde ficam meus dados?

Na pasta `data/` da máquina onde o LiteZap roda: banco SQLite, sessões e mídia. No modo local e self-hosted, nada
passa por servidores do LiteZap.

## Como migro da Z-API?

Veja [Migrar da Z-API](/docs/migrar-da-z-api/): mapeamento recurso a recurso e um roteiro de migração.

## Tem SDK?

Ainda não; toda página da referência tem exemplos em cURL, Node.js, Python, Go e PHP, e o
[openapi.yaml](/openapi.yaml) gera clientes. Veja [SDKs](/docs/sdks/).
