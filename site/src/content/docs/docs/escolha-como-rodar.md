---
title: Escolha como rodar
description: App local, self-hosted ou nuvem LiteZap. O mesmo motor nas três formas; o que muda é quem cuida da infraestrutura.
---

O LiteZap é um único binário open source. Ele roda igual no seu computador, num servidor seu ou na nuvem
gerenciada; a API, os eventos e as garantias são os mesmos. A escolha é sobre **onde o número fica conectado** e
**quem opera a máquina**.

| | No seu PC | No seu servidor | Nuvem LiteZap |
|---|---|---|---|
| Disponível | <span class="lz-badge lz-badge--ok">Hoje</span> (binário) · app desktop <span class="lz-badge lz-badge--soon">Em breve</span> | <span class="lz-badge lz-badge--ok">Hoje</span> | <span class="lz-badge lz-badge--soon">Em breve</span> |
| Quem opera | Você, na sua máquina | Você, no seu VPS | O LiteZap |
| IP de saída | O da sua internet (residencial) | O do servidor, ou um proxy por número | Isolado por cliente, gerenciado |
| Acesso à API | Só `localhost` | Pela internet, com TLS | Pela internet, com TLS |
| Disponibilidade | Enquanto o PC estiver ligado | 24×7 | 24×7, com monitoramento |
| Backup e upgrade | Você | Você | Incluídos |
| Custo | Grátis | Grátis + o servidor | Mensal por instância |

## Quando escolher cada um

**No seu PC** se o sistema que usa o WhatsApp roda na mesma máquina (ERP, planilha, automação local), se você
quer testar antes de ir para produção, ou se prefere que o número saia pelo IP residencial, como um WhatsApp Web
comum. Veja [App local](/docs/app-local/).

**No seu servidor** se a integração precisa estar no ar o tempo todo e receber webhooks de/para sistemas na
nuvem. Docker ou systemd, atrás de um proxy reverso com TLS. Para vários números, use um
[proxy por instância](/docs/conceitos/proxy/) para não concentrar todos no mesmo IP de datacenter. Veja
[Self-hosted](/docs/self-hosted/).

**Na nuvem LiteZap** (em breve) se você não quer cuidar de servidor, backup, upgrades e IPs. Cada cliente recebe
um projeto pré-instalado e isolado, rodando o mesmo binário open source: dá para começar na nuvem e migrar para o
seu servidor (ou o contrário) copiando a pasta de dados.

## Sem lock-in

- A API é a mesma nas três formas: trocar de uma para outra é mudar a URL base e o token.
- Tudo o que o LiteZap guarda está na pasta `data/` (SQLite e sessões). Mover essa pasta move as instâncias, sem
  novo QR.
- O código é Apache-2.0: você pode ler, auditar, modificar e rodar onde quiser.
