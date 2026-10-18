---
title: App local
description: Rodar o LiteZap no seu próprio computador, com o seu IP e sem nuvem.
---

O "modo local" do LiteZap é, hoje, **rodar o mesmo binário no seu computador**. A API fica só em `localhost`, as
sessões ficam numa pasta sua e o WhatsApp vê a conexão da sua própria internet, como um WhatsApp Web comum.

Bom para: integrar um sistema que roda na mesma máquina (ERP, planilha, automação local), testar antes de ir para um
servidor, ou para quem não quer depender de nuvem.

## Instalar e rodar

Siga o [quickstart](/docs/quickstart/): compile com Go e rode `./litezap serve`. Por padrão:

- A API escuta em `127.0.0.1:8787`: só programas da sua máquina conseguem acessá-la.
- `/healthz` e `/readyz` ficam abertas (só para a sua máquina).
- URLs assinadas de mídia usam `http://127.0.0.1:8787`, o que serve para programas locais.
- Para enviar mídia de um servidor da sua rede local por `media.url`, ligue `media_allow_private_urls: true`.

### macOS

- O binário compilado localmente roda direto. Se você baixar um binário pronto, o Gatekeeper pode bloquear; libere em
  Ajustes do Sistema > Privacidade e Segurança.
- `open qr.png` abre o QR no Preview.
- Para subir no login, use um LaunchAgent (`~/Library/LaunchAgents`) com `litezap serve --config ...`.

### Linux

- Use `xdg-open qr.png` para abrir o QR.
- Para manter rodando, uma unit de usuário (`systemctl --user`) baseada na [unit do servidor](/docs/self-hosted/systemd/)
  funciona bem.

### Windows

- O LiteZap compila para Windows (`GOOS=windows`), mas **ainda não foi testado** nessa plataforma. Os workers usam
  sockets Unix, suportados no Windows 10 e mais novos.
- Prefira o WSL2 enquanto o suporte nativo não for validado.

## Cuidados

- **Computador ligado.** Se a máquina dorme ou desliga, o número fica offline; mensagens na fila saem quando o
  LiteZap voltar (respeitando `expires_after`).
- **Não exponha a porta.** Se precisar acessar de outro lugar, use um túnel com autenticação ou vá para o
  [self-hosted](/docs/self-hosted/) com TLS. Mudar `listen_addr` para `0.0.0.0` numa rede pública expõe a sua API.
- **Backup da pasta `data/`.** Ela guarda as sessões; veja [backups](/docs/self-hosted/seguranca/#backups).

## App desktop (em breve)

Está nos planos um aplicativo desktop que embrulha o mesmo motor, para quem não quer terminal:

- ícone na bandeja do sistema, iniciando junto com o computador;
- tela de QR code e lista de números conectados;
- atualização automática;
- túnel seguro opcional para receber chamadas de sistemas na nuvem.

Acompanhe no [roadmap](/docs/roadmap/).
