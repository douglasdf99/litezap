---
title: Segurança e backups
description: Checklist para rodar o LiteZap exposto à internet, e como fazer backup.
---

## Checklist

- **Token admin forte e fora do código.** Defina `LITEZAP_ADMIN_TOKEN` (ex.: `openssl rand -hex 24`) num arquivo de
  ambiente com permissão `0600`, ou deixe o LiteZap gerar `data/admin_token` (já criado com `0600`). Use o token admin
  só no backend; entregue a cada integração apenas o token da instância dela.
- **Rotacione tokens vazados.** `POST /v1/instances/{id}/token/rotate` invalida o token antigo na hora. Para trocar
  o token admin, altere a variável e reinicie.
- **TLS sempre.** Exponha a API só por um [proxy reverso com HTTPS](/docs/self-hosted/proxy-reverso/), com o LiteZap
  escutando em `127.0.0.1`.
- **`public_url` correto.** Sem ele, URLs assinadas usariam o endereço interno; fora de loopback, o LiteZap recusa
  gerá-las (`409 conflict`).
- **Rotas de operação.** `/healthz` e `/readyz` são públicas só quando o `listen_addr` é loopback; em qualquer outro
  endereço exigem o token admin. Atrás de proxy reverso, bloqueie-as no proxy.
- **SSRF.** Mantenha `media_allow_private_urls: false` em servidores: assim `media.url` nunca alcança a sua rede
  interna nem serviços de metadados da nuvem.
- **Firewall.** Libere só 80/443 (proxy reverso) e SSH. A porta 8787 não precisa estar aberta.
- **Usuário dedicado.** Rode como usuário sem privilégios (a imagem Docker já usa `nonroot`; veja a unit
  [systemd](/docs/self-hosted/systemd/)).
- **Pasta de dados protegida.** `data/` contém as sessões do WhatsApp: quem tem esses arquivos controla os números.
  Permissão `0700`, disco criptografado quando possível.

O que o LiteZap já faz por você: guarda só o hash dos tokens de instância, compara tokens em tempo constante, nunca
coloca telefone nem token nas mensagens de erro, marca respostas com segredos como `no-store` e serve mídia recebida
com `nosniff`, CSP `sandbox` e download forçado para tipos que o navegador poderia executar.

## Backups

Tudo está em `data_dir`. O banco e as sessões são SQLite em modo WAL, então **copie com o serviço parado** (ou a
partir de um snapshot do disco), para não pegar arquivos pela metade:

```bash
sudo systemctl stop litezap
sudo tar czf /backup/litezap-data-$(date +%F).tgz -C /var/lib/litezap data
sudo systemctl start litezap
```

| Arquivo | Se perder |
|---|---|
| `instances/<id>/session.db` | O número precisa ser pareado de novo. |
| `litezap.db` | Perde instâncias, tokens, fila, chats e histórico. |
| `media_key` | URLs assinadas já emitidas param de funcionar (é recriado sozinho). |
| `admin_token` | Um novo é gerado; atualize suas integrações. |
| `media/` | Mídia recebida é baixada de novo do WhatsApp enquanto ainda estiver disponível lá. |

:::danger[Nunca rode duas cópias da mesma sessão]
Restaurar um backup numa segunda máquina enquanto a primeira ainda está rodando conecta a mesma sessão duas vezes, o
que derruba as duas e pode levar a logout. Pare a original antes de restaurar em outro lugar.
:::
