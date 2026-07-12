# Linux / VPS Deployment

Deploy Soulacy on a Linux VPS as a systemd service with a Caddy reverse proxy for automatic HTTPS.

## Prerequisites

- Ubuntu 22.04+ / Debian 12+ / any systemd-based distro
- A domain name pointing to your server's IP

---

## Install the binary

```bash
# Download a tagged release bundle
curl -sL https://github.com/vmodekurti/soulacy/releases/download/v0.2.0/soulacy_v0.2.0_linux_amd64.tar.gz \
  | tar -xz -C /usr/local/bin/

soulacy --version
```

---

## Create a system user

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin soulacy
```

---

## Set up directories

```bash
mkdir -p /etc/soulacy /var/lib/soulacy /etc/soulacy/agents
chown -R soulacy:soulacy /var/lib/soulacy /etc/soulacy
```

---

## Create config

```bash
cat > /etc/soulacy/config.yaml << 'EOF'
server:
  host: 127.0.0.1
  port: 18789
  api_key: "sy_CHANGE_ME"

llm:
  default_provider: openai
  providers:
    openai:
      api_key: "sk-..."

storage:
  backend: sqlite

memory:
  sqlite_path: /var/lib/soulacy/soulacy.db

agent_dirs:
  - /etc/soulacy/agents

updates:
  manifest_url: https://github.com/vmodekurti/soulacy/releases/latest/download/release-manifest.json
EOF

chown soulacy:soulacy /etc/soulacy/config.yaml
chmod 600 /etc/soulacy/config.yaml   # secrets — readable only by soulacy user
```

---

## Create systemd service

```ini title="/etc/systemd/system/soulacy.service"
[Unit]
Description=Soulacy AI Agent Server
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=soulacy
Group=soulacy
ExecStart=/usr/local/bin/soulacy serve --config /etc/soulacy/config.yaml
Restart=on-failure
RestartSec=5s

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/soulacy
ReadOnlyPaths=/etc/soulacy

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=soulacy

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable soulacy
systemctl start soulacy
systemctl status soulacy
```

---

## Install Caddy (reverse proxy + automatic TLS)

```bash
apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | tee /etc/apt/sources.list.d/caddy-stable.list
apt update && apt install caddy
```

```caddyfile title="/etc/caddy/Caddyfile"
yourdomain.com {
    reverse_proxy localhost:18789 {
        header_up X-Real-IP {remote_host}
        flush_interval -1    # required for SSE streaming
    }
}
```

```bash
systemctl reload caddy
```

Caddy automatically provisions and renews a Let's Encrypt TLS certificate.

---

## Manage the service

```bash
# View live logs
journalctl -u soulacy -f

# Restart after config change
systemctl restart soulacy

# Upgrade binary
sy update check
sy update install --dry-run
sy update install --yes
systemctl restart soulacy
```

---

## Firewall

```bash
ufw allow 22/tcp    # SSH
ufw allow 80/tcp    # HTTP (Caddy redirects to HTTPS)
ufw allow 443/tcp   # HTTPS
ufw enable
```

Port 18789 should remain closed — Soulacy binds to `127.0.0.1` and traffic arrives via Caddy.
