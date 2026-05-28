# Docker Deployment

## Quick start

```bash
docker run -d \
  --name soulacy \
  -p 8080:8080 \
  -v $(pwd)/config.yaml:/app/config.yaml \
  -v $(pwd)/agents:/app/agents \
  -v $(pwd)/data:/app/data \
  ghcr.io/vmodekurti/soulacy:latest
```

---

## Docker Compose (recommended)

### Basic setup (SQLite)

```yaml title="docker-compose.yml"
version: "3.9"

services:
  soulacy:
    image: ghcr.io/vmodekurti/soulacy:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./agents:/app/agents:ro
      - soulacy_data:/app/data
    environment:
      - SOULACY__SERVER__PORT=8080

volumes:
  soulacy_data:
```

### Full stack (Postgres + Redis + Jaeger)

```yaml title="docker-compose.yml"
version: "3.9"

services:
  soulacy:
    image: ghcr.io/vmodekurti/soulacy:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./agents:/app/agents:ro
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    environment:
      - SOULACY__STORAGE__TYPE=postgres
      - SOULACY__STORAGE__POSTGRES__DSN=postgres://soulacy:secret@postgres:5432/soulacy?sslmode=disable
      - SOULACY__STORAGE__REDIS__ADDR=redis:6379

  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: soulacy
      POSTGRES_PASSWORD: secret
      POSTGRES_DB: soulacy
    volumes:
      - pg_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U soulacy"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

  jaeger:
    image: jaegertracing/all-in-one:latest
    ports:
      - "16686:16686"    # Jaeger UI
      - "4318:4318"      # OTLP HTTP

volumes:
  pg_data:
```

---

## Environment variable overrides

All config values can be set via environment variables (useful for secrets in CI/CD):

```bash
SOULACY__SERVER__API_KEY=sy_secret
SOULACY__LLM__PROVIDERS__OPENAI__API_KEY=sk-...
SOULACY__CHANNELS__TELEGRAM__TOKEN=1234:AAH...
```

---

## Health check

```bash
curl http://localhost:8080/v1/health
# {"status":"ok","version":"0.1.0"}
```

Docker healthcheck in Compose:

```yaml
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:8080/v1/health"]
  interval: 30s
  timeout: 10s
  retries: 3
```

---

## Using a reverse proxy (nginx)

```nginx title="nginx.conf"
server {
    listen 443 ssl;
    server_name yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://soulacy:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 120s;    # allow time for long LLM responses
    }
}
```
