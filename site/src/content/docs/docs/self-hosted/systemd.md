---
title: Binário com systemd
description: Instalar o LiteZap como serviço systemd num servidor Linux.
---

## 1. Usuário e pastas

```bash
sudo useradd --system --home /var/lib/litezap --shell /usr/sbin/nologin litezap
sudo install -d -o litezap -g litezap -m 0700 /var/lib/litezap /var/lib/litezap/data
sudo install -d -m 0755 /etc/litezap
sudo install -m 0755 litezap /usr/local/bin/litezap
```

## 2. Configuração

```bash
sudo cp litezap.example.yaml /etc/litezap/litezap.yaml
sudo editor /etc/litezap/litezap.yaml   # data_dir, public_url, timezone...
```

Segredos ficam melhor num arquivo de ambiente legível só pelo root:

```bash
sudo install -m 0600 /dev/null /etc/litezap/litezap.env
echo "LITEZAP_ADMIN_TOKEN=$(openssl rand -hex 24)" | sudo tee /etc/litezap/litezap.env >/dev/null
```

## 3. Unit

`/etc/systemd/system/litezap.service`:

```ini
[Unit]
Description=LiteZap (API de WhatsApp)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=litezap
Group=litezap
WorkingDirectory=/var/lib/litezap
Environment=LITEZAP_DATA_DIR=/var/lib/litezap/data
EnvironmentFile=-/etc/litezap/litezap.env
ExecStart=/usr/local/bin/litezap serve --config /etc/litezap/litezap.yaml
Restart=on-failure
RestartSec=5

# O supervisor recebe SIGTERM e encerra os workers sozinho; o que sobrar leva SIGKILL no fim do prazo.
KillMode=mixed
TimeoutStopSec=45

# Endurecimento
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/litezap
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now litezap
journalctl -u litezap -f
```

`PrivateTmp=true` é seguro: os sockets dos workers ficam no `/tmp` privado do serviço, compartilhado entre o
supervisor e os workers que ele inicia.

## 4. Atualizar

```bash
sudo install -m 0755 litezap-novo /usr/local/bin/litezap
sudo systemctl restart litezap
```

No restart, envios interrompidos voltam para a fila e as instâncias reconectam sem QR.
