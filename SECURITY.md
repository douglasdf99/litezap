# Security Policy

## Reporting a vulnerability

Não abra issue pública para falhas de segurança.

Reporte em privado pelos **GitHub Security Advisories**: aba **Security** do repositório > **Report a vulnerability** ([link direto](https://github.com/douglasdf99/litezap/security/advisories/new)).

Inclua, se puder: versão (`litezap version`), como reproduzir, impacto esperado e, se tiver, uma sugestão de correção.

## O que esperar

- Confirmação do recebimento em até 7 dias.
- Avaliação e plano de correção combinados com você no próprio advisory.
- Crédito no advisory publicado, se quiser.

## Supported versions

Só a última release recebe correções de segurança enquanto o projeto estiver em `0.x`.

## Escopo

Entram: o binário `litezap` (API, painel, MCP, workers), o app de bandeja e a imagem Docker publicada. Ficam fora: falhas do próprio WhatsApp ou do [whatsmeow](https://github.com/tulir/whatsmeow) (reporte a eles), banimento de contas pelo WhatsApp e configurações inseguras feitas pelo operador (ex.: expor a porta sem TLS ou sem token).
