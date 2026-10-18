---
title: Backup, restauração e upgrade
description: Copiar a pasta de dados com segurança, restaurar em outra máquina e atualizar o binário.
---

Tudo o que o LiteZap guarda está em `data_dir`: o banco SQLite, as sessões do WhatsApp, a mídia baixada e os
segredos. Fazer backup é copiar essa pasta; restaurar é colocá-la de volta.

## Backup

O banco e as sessões são SQLite em modo WAL. Copie **com o serviço parado** (ou de um snapshot do disco/volume), para
não pegar arquivos pela metade:

```bash
# systemd
sudo systemctl stop litezap
sudo tar czf /backup/litezap-$(date +%F).tgz -C /var/lib/litezap data
sudo systemctl start litezap

# Docker Compose
docker compose stop litezap
docker run --rm -v litezap_litezap-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/litezap-$(date +%F).tgz -C /data .
docker compose start litezap
```

Durante a parada, mensagens enviadas pela API ficam recusadas (o servidor está fora) e as recebidas ficam no
WhatsApp, que as entrega quando a instância volta. Envios já enfileirados continuam na fila.

O backup contém as sessões dos números: guarde-o criptografado e com acesso restrito.

## Restaurar

1. Pare o LiteZap de origem (nunca rode as duas cópias juntas).
2. Extraia a pasta no `data_dir` do destino, com dono e permissão do usuário do serviço (`0700`).
3. Suba o LiteZap. As instâncias reconectam sem QR e a fila continua.

Se o `admin_token` estava só em `data/admin_token`, ele vem junto. Se estava em variável de ambiente, configure-a no
destino.

## Upgrade

```bash
# binário
sudo install -m 0755 litezap-novo /usr/local/bin/litezap
sudo systemctl restart litezap

# Docker
git pull && docker compose up -d --build
```

No restart, o supervisor encerra os workers de forma graciosa (envios em andamento ganham até 10s), aplica migrações
do banco na subida e reconecta as instâncias. Envios interrompidos voltam para a fila com o mesmo `wa_message_id`
(sem duplicar no celular). Entregas de webhook pendentes continuam de onde pararam.

- Leia o [changelog](/docs/changelog/) antes: mudanças que quebram compatibilidade são marcadas.
- Faça backup antes de upgrades que trazem migrações de banco.
- Voltar para uma versão anterior depois de uma migração pode não funcionar; restaure o backup nesse caso.
