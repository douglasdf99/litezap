---
title: Política de segurança
description: Como relatar vulnerabilidades no LiteZap.
---

Levamos a sério a segurança de quem roda o LiteZap: ele guarda sessões de WhatsApp, e uma falha pode expor números
inteiros.

## Como relatar

- **Não** abra uma issue pública para vulnerabilidades.
- Use o relato privado de vulnerabilidades do GitHub no repositório
  ([Security > Report a vulnerability](https://github.com/douglasdf99/litezap/security/advisories/new)).
- Inclua: versão ou commit, passos para reproduzir, impacto esperado e, se possível, uma sugestão de correção.

## O que esperar

- Confirmação de recebimento e uma avaliação inicial.
- Correção em sigilo, com crédito a você no aviso de segurança (se quiser).
- Divulgação coordenada depois que a correção estiver disponível.

## Escopo

Dentro: o motor (`litezap serve` e `worker`), a API, webhooks, SSE, URLs assinadas, o Dockerfile e a configuração
padrão. Fora: vulnerabilidades no próprio WhatsApp ou na biblioteca whatsmeow (relate a elas), e instalações com
configuração insegura contrária à documentação (ex.: API exposta sem TLS).

Para endurecer sua instalação, veja [segurança e backups](/docs/self-hosted/seguranca/).
