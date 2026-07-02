# Sawt Gateway — Deployment Runbook

The whole platform is **one Go binary** (`sawt-gateway`): WhatsApp socket, reasoning loop, speech pipeline, and operator dashboard in one always-on process. It must run as a **single instance** — the WhatsApp device session, in-memory rate limiters, and session-cookie secret are all process-local.

## Build

```bash
go build -o sawt-gateway .        # Linux/GCE
GOOS=linux GOARCH=amd64 go build -o sawt-gateway .   # cross-compile from Windows
```

Runtime dependency: **ffmpeg** on `PATH` (voice-note transcoding). The binary refuses to start without it unless `ALLOW_MISSING_FFMPEG=true` (text-only mode).

## Environment variables

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `DATABASE_URL` | ✅ | — | Neon/Postgres connection string |
| `AGENT_GATEWAY_SECRET` | ✅ (for ERP tools) | — | HMAC secret shared with mshalia's `/api/agent/v1/*` |
| `MSHALIA_API_URL` | ✅ (for ERP tools) | `http://localhost:3001` | mshalia base URL |
| `NIM_API_KEY` | ✅ (reasoning) | — | Primary LLM (NVIDIA NIM, OpenAI-compatible) |
| `NIM_BASE_URL` / `NIM_MODEL` | — | NIM defaults | Primary LLM endpoint/model |
| `OPENAI_API_KEY` / `OPENAI_API_BASE` / `LLM_FALLBACK_MODEL` | recommended | `gpt-4o-mini` | Fallback LLM chain |
| `GROQ_API_KEY` / `HF_API_KEY` / `GCP_API_KEY` | at least one | — | STT/TTS provider cascade |
| `SESSION_SECRET` | ✅ in prod | random per boot | Dashboard session signing (set it, or logins drop on restart) |
| `SECURE_COOKIE` | ✅ behind HTTPS | `false` | `Secure` flag on cookies |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | first boot | `admin` / generated | Seeded only when the users table is empty; generated password prints once |
| `PORT` | — | `8080` | Dashboard HTTP port |
| `PAIR_PHONE_NUMBER` | — | — | Auto-request a WhatsApp pairing code at boot |
| `RETENTION_DAYS` | — | `90` | Purge STT/TTS history + conversation turns, redact wa_activity transcripts older than N days (0 disables) |
| `ERROR_WEBHOOK_URL` | recommended | — | JSON webhook for error/panic reports (Slack/Discord-compatible) |
| `ALLOW_MISSING_FFMPEG` | — | `false` | Permit boot without ffmpeg (voice disabled) |

## GCE (e2-micro) — recommended

The pgx pool is already tuned for a 1 GB instance (`MaxConns=5`).

```bash
sudo apt-get update && sudo apt-get install -y ffmpeg
sudo cp sawt-gateway /opt/sawt/
sudo tee /etc/systemd/system/sawt.service << 'EOF'
[Unit]
Description=Sawt WhatsApp ERP Gateway
After=network-online.target

[Service]
ExecStart=/opt/sawt/sawt-gateway
EnvironmentFile=/opt/sawt/.env
Restart=always
RestartSec=5
User=sawt

[Install]
WantedBy=multi-user.target
EOF
sudo systemctl enable --now sawt
```

First boot: check `journalctl -u sawt` for the generated admin password (printed once), then pair WhatsApp via the dashboard (`http://<host>:8080`) — QR or phone-pairing code.

**Cloud Run note:** possible with `min-instances=1` + `max-instances=1` (the WhatsApp socket must never scale to 2), but a tiny GCE VM is simpler and cheaper for an always-on socket.

## Health & monitoring

- Liveness: any HTTP 200/3xx from `/login`.
- WhatsApp link state: dashboard home, or the `health_check` table.
- Errors/panics: set `ERROR_WEBHOOK_URL`; every report carries a `trace_id` equal to the WhatsApp message id — grep the logs with `[trace=<id>]` to reconstruct one message's full pipeline.

## Upgrades

1. `go build` + run `go test ./...` (CI enforces this on `main`).
2. Copy the new binary, `sudo systemctl restart sawt`.
3. Schema changes apply automatically at boot (`schema.sql` is idempotent `CREATE TABLE IF NOT EXISTS` — additive only; destructive changes need a manual migration).
4. WhatsApp session survives restarts (device state lives in Postgres via whatsmeow's sqlstore).

## Disaster recovery

- Everything stateful lives in Postgres (Neon): WhatsApp device keys, contacts, activity, conversation memory, pending confirmations. Neon's PITR/backups cover it.
- Losing the VM = redeploy binary + `.env`; the WhatsApp session resumes from the DB.
- If WhatsApp bans/unlinks the number: re-pair from the dashboard; consider slowing send patterns (see BLUEPRINT §11).
