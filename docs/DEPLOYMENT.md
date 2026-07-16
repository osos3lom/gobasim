# Sawt Gateway — Deployment & Development Guide

> **Scope.** This is the single source of truth for taking `sawt-gateway` from a
> fresh Windows 11 workstation all the way to a hardened production deployment on
> a Google Cloud **e2-micro** VM (1 vCPU, 1 GB RAM, ~10–30 GB disk). It covers
> local development, debugging, building, testing, resource optimization,
> security hardening, and day-2 operations.

The entire platform is **one Go binary** (`sawt-gateway`, module `sawt-go`):
the WhatsApp socket, the LLM reasoning loop, the speech (STT/TTS) pipeline, and
the operator web dashboard all run inside a single always-on process.

> [!IMPORTANT]
> **It must run as exactly one instance.** The WhatsApp device session, the
> in-memory rate limiters, the log broker, and the session-cookie secret are all
> **process-local**. Running two copies against the same WhatsApp number will
> break pairing and cause duplicate replies. On Cloud Run this means
> `min-instances=1` **and** `max-instances=1`; on GCE it means one VM, one
> `systemd` unit. A small GCE VM is simpler and cheaper for an always-on socket.

---

## Table of Contents

1. [Architecture & Stack](#1-architecture--stack)
2. [Prerequisites](#2-prerequisites)
3. [Windows 11 Development Environment](#3-windows-11-development-environment)
4. [Get the Code](#4-get-the-code)
5. [Environment Configuration (`.env`)](#5-environment-configuration-env)
6. [Install Dependencies](#6-install-dependencies)
7. [Run Locally](#7-run-locally)
8. [Debug in VS Code](#8-debug-in-vs-code)
9. [Build the Binary](#9-build-the-binary)
10. [Run the Tests](#10-run-the-tests)
11. [Local Troubleshooting](#11-local-troubleshooting)
12. [Optimizing for the e2-micro (1 vCPU / 1 GB)](#12-optimizing-for-the-e2-micro-1-vcpu--1-gb)
13. [Production Deployment to GCP](#13-production-deployment-to-gcp)
14. [Security Hardening](#14-security-hardening)
15. [Logging & Observability](#15-logging--observability)
16. [Health & Monitoring](#16-health--monitoring)
17. [Backups & Disaster Recovery](#17-backups--disaster-recovery)
18. [Update & Rollback Strategy](#18-update--rollback-strategy)
19. [Operational Tooling (dbreset / harness)](#19-operational-tooling-dbreset--harness)
20. [Completeness Checklist](#20-completeness-checklist)

---

## 1. Architecture & Stack

### 1.1 Components (one process)

```
                         ┌──────────────────────────────────────────┐
   WhatsApp servers ◀───▶│  whatsmeow socket  (internal/whatsmeow)  │
   (outbound TLS)        │        │                                 │
                         │        ▼                                 │
                         │  handleIncomingMessage (main.go)         │
                         │   ├─ ffmpeg OGG→WAV   (internal/audio)   │
                         │   ├─ STT cascade      (internal/speech)  │──▶ Groq / HF / Google
                         │   ├─ Workflow engine  (internal/workflow)│──▶ NIM / OpenAI (LLM)
                         │   ├─ ERP tools (HMAC)  (internal/erp)    │──▶ mshalia API
                         │   ├─ TTS cascade + ffmpeg WAV→Opus       │
                         │   └─ voice archival   (internal/voicenotes)──▶ GCS bucket
                         │                                          │
   Operator browser ◀───▶│  chi web dashboard    (web/)  :8080      │
                         └──────────────────┬───────────────────────┘
                                            ▼
                              Neon Postgres (pgx/v5 pool)
                     device keys · contacts · activity · memory · users
```

### 1.2 Technology

| Layer | Choice | Notes |
|---|---|---|
| Language / runtime | **Go 1.25+** | `go.mod` pins `go 1.25.8`; newer toolchains build it fine. Pure Go — **no cgo**, so cross-compiling from Windows is safe. |
| HTTP router | `go-chi/chi/v5` | Middleware chain: `RequestID`, `RealIP`, `Logger`, panic reporter, `Recoverer`, security headers. |
| Database | **Neon Postgres** via `jackc/pgx/v5` | Pool tuned for 1 GB (`MaxConns=5`). Schema auto-applied at boot from embedded `schema.sql`. |
| WhatsApp | `go.mau.fi/whatsmeow` | Device/session state stored in Postgres via whatsmeow's `sqlstore` (pgx driver). |
| UI | `html/template` + **Tailwind CSS** + **HTMX** | Templates and compiled CSS are **embedded** into the binary (`go:embed`). HTMX is loaded from a pinned, SRI-verified CDN. |
| Object storage | `cloud.google.com/go/storage` | Voice-note archival to a GCS/Firebase bucket. Optional (disabled if unset). |
| Media | **ffmpeg** (external, on `PATH`) | OGG/Opus ⇄ WAV transcoding for voice notes. |
| Auth | bcrypt + HMAC-signed cookies | `golang.org/x/crypto/bcrypt`; sessions signed with `SESSION_SECRET`. |

### 1.3 Where state lives (important for backups & DR)

| State | Location | Survives VM loss? |
|---|---|---|
| WhatsApp device keys / pairing | Neon Postgres (`whatsmeow_*` tables) | ✅ Yes |
| Contacts, activity, conversation memory, users, agents | Neon Postgres | ✅ Yes |
| Voice-note **audio** | GCS bucket | ✅ Yes |
| Voice-note **spool** (in-flight uploads only) | VM disk (`VOICE_SPOOL_DIR`) | ⚠️ Transient — re-derivable from the ledger |
| Rate-limiter counters, log buffer, session secret | Process memory | ❌ No (reset on restart — by design) |

Because nothing durable lives on the VM disk, **the VM is disposable**: rebuild
it, drop in the binary + `.env`, and the WhatsApp session resumes from Postgres.

---

## 2. Prerequisites

### 2.1 Accounts / external services

- **Neon** (or any Postgres) — a connection string for `DATABASE_URL`. Create a
  separate **dev branch** so local testing never touches production data.
- **GCP project** with billing enabled — for the e2-micro VM and (optionally) the
  voice-notes GCS bucket.
- **At least one STT/TTS provider key** — Groq, Hugging Face, or a Google Cloud
  API key. Without one, voice replies are disabled (text still works).
- **An LLM key** — NVIDIA NIM (`NIM_API_KEY`) and/or OpenAI (`OPENAI_API_KEY`)
  for the reasoning loop.
- **A WhatsApp number** you can link as a device (Linked Devices — no second SIM
  needed).

### 2.2 Local tooling (installed in §3)

Go 1.25+, Git, Visual Studio Code, PowerShell 7 (optional but recommended), and
`ffmpeg` (needed for voice notes; skippable with `ALLOW_MISSING_FFMPEG=true`).

---

## 3. Windows 11 Development Environment

All commands below assume **PowerShell** (Windows Terminal → PowerShell tab).

> [!TIP]
> The production VM is Linux/amd64. You can develop natively on Windows and
> cross-compile (the module is pure Go), **or** use WSL 2 Ubuntu for a
> byte-identical build environment. Both are documented; native Windows is the
> default here.

### 3.1 Install the toolchain

Use `winget` (bundled with Windows 11) for reproducible installs:

```powershell
winget install --id GoLang.Go               -e   # Go toolchain
winget install --id Git.Git                 -e   # Git
winget install --id Microsoft.VisualStudioCode -e
winget install --id Microsoft.PowerShell    -e   # PowerShell 7 (pwsh)
winget install --id Gyan.FFmpeg             -e   # ffmpeg (voice notes)
```

Close and reopen the terminal so `PATH` updates take effect, then verify:

```powershell
go version        # expect go1.25 or newer
git --version
ffmpeg -version   # first line only is fine
code --version
```

> [!NOTE]
> If `ffmpeg` is not on `PATH`, the app **refuses to boot** unless you set
> `ALLOW_MISSING_FFMPEG=true` (text-only mode). See §5.

### 3.2 Optional: Node.js (only to rebuild the dashboard CSS)

The compiled Tailwind stylesheet (`web/static/app.css`) is **committed**, so the
Go build stays Node-free. You only need Node if you edit templates/styles:

```powershell
winget install --id OpenJS.NodeJS.LTS -e
```

### 3.3 Optional: Google Cloud CLI (for deployment)

```powershell
winget install --id Google.CloudSDK -e
gcloud init
```

### 3.4 VS Code extensions

Install the essentials from the terminal:

```powershell
code --install-extension golang.go                    # Go language support, debugging, test UI
code --install-extension ms-azuretools.vscode-docker  # optional: Dockerfile/registry viewing
code --install-extension bradlc.vscode-tailwindcss    # optional: Tailwind class IntelliSense
code --install-extension redhat.vscode-yaml           # CI / config editing
```

After opening the repo, run **`Go: Install/Update Tools`** from the Command
Palette (`Ctrl+Shift+P`) and accept all — this installs `gopls`, `dlv`
(debugger), `staticcheck`, etc.

---

## 4. Get the Code

You already have the repository locally (this folder). No clone is required for
development, and **nothing is cloned onto the production VM** — you ship only the
compiled binary. If you are setting up a new machine:

```powershell
git clone <your-repo-url> gobasim
cd gobasim
```

---

## 5. Environment Configuration (`.env`)

The app reads **environment variables** (via `config.LoadConfig`). It does *not*
auto-load a `.env` file at runtime; you either export the variables into your
shell (dev) or let `systemd`'s `EnvironmentFile` load them (prod). A `.env` file
is simply a convenient place to keep them.

> [!WARNING]
> **Never commit secrets.** `.gitignore` already excludes `.env` and `.env.*`.
> The repo ships a template `.env.production` that has been pre-filled with
> **real-looking values** for convenience — treat any secret that has ever been
> written to a file on disk as **potentially exposed** and rotate it before going
> live (see [§14.1](#141-secrets-management)). Verified: this file is **not**
> tracked by git and has **never** been in git history — keep it that way.

### 5.1 Full variable reference

Legend: **R** = required, **P** = required for that feature path, **○** = optional.

| Variable | Req | Default | Purpose & security notes |
|---|:--:|---|---|
| `DATABASE_URL` | **R** | — | Neon/Postgres DSN. Holds *all* durable state incl. WhatsApp keys. Use `?sslmode=require`. **Secret.** |
| `SESSION_SECRET` | **R**† | random per boot | HMAC key that signs dashboard session cookies. **Fatal to omit when `SECURE_COOKIE=true`.** If it changes, everyone is logged out (useful kill-switch). Generate: `openssl rand -hex 32`. **Secret.** |
| `SECURE_COOKIE` | **R**‡ | `false` | Adds the `Secure` flag to cookies. **Set `true` in production** (behind HTTPS); leave `false` for plain-HTTP local dev. |
| `PORT` | ○ | `8080` | Dashboard HTTP listen port. |
| `ADMIN_USERNAME` | ○ | `admin` | Seeds the first dashboard user **only when the `users` table is empty**. |
| `ADMIN_PASSWORD` | ○ | generated | Seed password. If omitted, a random one is generated and **printed once** to the log. **Secret.** |
| `ALLOW_MISSING_FFMPEG` | ○ | `false` | `true` lets the app boot without ffmpeg (voice notes disabled). |
| `RETENTION_DAYS` | ○ | `90` | Daily purge/redaction of PII (transcripts, conversation turns, voice-note rows). `0` disables. |
| `MAX_INFLIGHT` | ○ | `32` | Max concurrent inbound message handlers (semaphore; guarded to ≥1). |
| `LOG_FORMAT` | ○ | `text` | `json` emits structured `log/slog` lines (with `trace_id`) for Cloud Logging; default is human-readable text. |
| `NIM_API_KEY` | P | — | Primary LLM (NVIDIA NIM, OpenAI-compatible). **Secret.** |
| `NIM_BASE_URL` | ○ | `https://integrate.api.nvidia.com/v1` | Primary LLM endpoint. |
| `NIM_MODEL` | ○ | `meta/llama-3.3-70b-instruct` | Primary LLM model id. |
| `OPENAI_API_KEY` | ○ | — | Fallback LLM. **Secret.** |
| `OPENAI_API_BASE` | ○ | `https://api.openai.com/v1` | Fallback LLM endpoint. |
| `LLM_FALLBACK_MODEL` | ○ | `gpt-4o-mini` | Fallback LLM model id. |
| `GROQ_API_KEY` | P§ | — | STT provider (Whisper, rank 1). **Secret.** |
| `HF_API_KEY` | P§ | — | STT provider (Hugging Face, rank 2). **Secret.** |
| `GCP_API_KEY` | P§ | — | STT **and** TTS (Google Cloud). **Secret.** |
| `STT_PROVIDER` / `STT_MODEL` | ○ | `groq` / `whisper-large-v3` | Stored in config; the STT cascade is actually **selected by which keys are present**. |
| `TTS_PROVIDER` / `TTS_MODEL` | ○ | `google` / — | Same: TTS cascade is key-driven. |
| `AGENT_GATEWAY_SECRET` | P | — | HMAC secret shared with mshalia's `/api/agent/v1/*` (ERP tools). **Secret.** |
| `MSHALIA_API_URL` | P | `http://localhost:3001` | mshalia ERP base URL. |
| `DEFAULT_ORG_ID` | ○ | — | Fallback organization ID for resolved-but-orgless privileged identities (closes the M9 gap). |
| `PAIR_PHONE_NUMBER` | ○ | — | If set, auto-requests a WhatsApp pairing code at boot (e.g. `9665XXXXXXXXX`). |
| `ERROR_WEBHOOK_URL` | ○ | — | Slack/Discord-compatible JSON webhook for errors & panics. Recommended in prod. |
| `VOICE_STORAGE_BUCKET` | ○ | — | GCS bucket for voice-note archival. **Empty disables the feature entirely.** |
| `VOICE_STORAGE_PREFIX` | ○ | `voice-notes` | Object-name prefix inside the bucket. |
| `VOICE_SPOOL_DIR` | ○ | `voice-spool` | On-disk staging dir for in-flight uploads (created `0700`). |
| `GOOGLE_APPLICATION_CREDENTIALS` | ○ | — | Path to a GCS service-account key **for local dev only**. In prod, use the VM's attached service account (ADC). **Secret.** |

† Effectively required in production. ‡ Required semantics in production.
§ At least **one** of `GROQ_API_KEY` / `HF_API_KEY` / `GCP_API_KEY` is needed for
speech; without any, only the local `whisper-cli` fallback (if installed) works.

### 5.2 Create a dev `.env`

Create `.env` in the repo root (already gitignored):

```dotenv
# .env — LOCAL DEV ONLY. Never use these values in production.
DATABASE_URL=postgresql://user:pass@ep-xxxx.neon.tech/dbname?sslmode=require
SESSION_SECRET=dev-only-not-for-prod
ADMIN_USERNAME=admin
ADMIN_PASSWORD=devpassword123
PORT=8080
SECURE_COOKIE=false
ALLOW_MISSING_FFMPEG=true    # drop once ffmpeg is installed and you want to test voice
```

Everything else (LLM/STT/TTS/ERP keys) is only needed to exercise those paths —
leave them unset to just verify the app boots, migrates its schema, and serves
the dashboard.

### 5.3 Load the variables (PowerShell)

The app reads the **process environment**, so load `.env` into the session
before running. Save this helper as `scripts/Load-DotEnv.ps1` (or paste it):

```powershell
# Loads KEY=VALUE lines from a .env file into the current PowerShell session.
Get-Content .env | Where-Object { $_ -match '^\s*[^#].*=' } | ForEach-Object {
    $name, $value = $_ -split '=', 2
    $name  = $name.Trim()
    # strip trailing " # inline comment" and surrounding whitespace
    $value = ($value -replace '\s+#.*$', '').Trim()
    [System.Environment]::SetEnvironmentVariable($name, $value, 'Process')
}
Write-Host "Loaded .env into this session."
```

Run it before each `go run`:

```powershell
. .\scripts\Load-DotEnv.ps1
```

### 5.4 Generate secrets

```powershell
# 32-byte hex SESSION_SECRET (PowerShell-native, no OpenSSL needed):
-join ((1..32) | ForEach-Object { '{0:x2}' -f (Get-Random -Max 256) })

# Or with Git's bundled OpenSSL:
openssl rand -hex 32
```

### 5.5 Securing the `.env` file (production)

On the VM, restrict the file so only the service account can read it:

```bash
sudo install -o sawt -g sawt -m 600 .env /opt/sawt/.env   # rw for owner only
```

Rules of thumb: `chmod 600`, owned by the service user, **never** world-readable,
never in a web-served directory, never committed. For higher assurance, store
secrets in **GCP Secret Manager** and inject them at boot (see §14.1).

---

## 6. Install Dependencies

Go modules resolve automatically, but you can pre-fetch and tidy:

```powershell
. .\scripts\Load-DotEnv.ps1
go mod download        # fetch all module deps
go mod verify          # verify checksums against go.sum
```

If you edit the dashboard styling (optional, needs Node):

```powershell
npm install            # installs Tailwind toolchain (dev only)
npm run build:css      # regenerates web/static/app.css (commit the result)
```

---

## 7. Run Locally

```powershell
. .\scripts\Load-DotEnv.ps1
go run .
```

Expected log sequence:

1. `Starting Sawt Unified Daemon...`
2. Database pool established + `Schema bootstrap complete.`
3. A seeded admin password (**only** if `ADMIN_PASSWORD` was unset) — copy it.
4. A WhatsApp **QR code** printed to the terminal (unless already paired).
5. `Web Dashboard serving at http://localhost:8080`.

Then:

- Open <http://localhost:8080/login> and sign in with your admin credentials.
- Pages: `/dashboard`, `/dashboard/whatsapp`, `/dashboard/workflows`,
  `/dashboard/logs` (live SSE log stream).
- To exercise the full messaging pipeline, scan the QR with a **spare/test**
  WhatsApp number. The socket talks to real WhatsApp servers even in dev.
- `Ctrl+C` to stop (the daemon disconnects WhatsApp cleanly).

> [!NOTE]
> New WhatsApp chats are **auto-created disabled**. A human must toggle a
> contact **on** in the dashboard before the agent will reply to it (explicit
> opt-in; enforced by `wa_contacts.enabled DEFAULT FALSE`).

For autoreload during UI work, `air` or `reflex` work but are not wired in;
plain `go run .` recompiles each time.

---

## 8. Debug in VS Code

Create `.vscode/launch.json`:

```jsonc
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Debug sawt-gateway",
      "type": "go",
      "request": "launch",
      "mode": "debug",
      "program": "${workspaceFolder}",
      "cwd": "${workspaceFolder}",
      "envFile": "${workspaceFolder}/.env",   // VS Code loads .env for you here
      "env": { "ALLOW_MISSING_FFMPEG": "true" }
    },
    {
      "name": "Debug web harness (no WhatsApp)",
      "type": "go",
      "request": "launch",
      "mode": "debug",
      "program": "${workspaceFolder}/cmd/harness",
      "cwd": "${workspaceFolder}",
      "envFile": "${workspaceFolder}/.env"
    }
  ]
}
```

- Set breakpoints in the gutter, press **F5**. The `envFile` key means you do
  **not** need to pre-load `.env` for debug sessions.
- The **web harness** (`cmd/harness`) boots the dashboard against the live DB
  **without** a WhatsApp connection and adds a `/preview-login` bypass — ideal
  for iterating on templates/handlers. It listens on `:8091` by default.
- Use the **Testing** side panel (from the Go extension) to run/debug individual
  tests with inline pass/fail markers.

---

## 9. Build the Binary

The production target is **linux/amd64**, static (`CGO_ENABLED=0`).

### 9.1 Cross-compile from Windows (PowerShell)

```powershell
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w" -o sawt-gateway .
# reset your shell afterwards:
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
```

- `-trimpath` removes local filesystem paths from the binary (smaller, no info leak).
- `-ldflags "-s -w"` strips the symbol table and DWARF debug info — a smaller
  binary, which matters on a 10 GB disk. (Drop these if you need stack symbols.)

### 9.2 From WSL / Linux

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o sawt-gateway .
file sawt-gateway   # → ELF 64-bit LSB executable, x86-64
```

The repo also ships `build-for-gcp.sh` (runs `go vet` then the linux/amd64
build). A native `sawt-gateway.exe` for Windows testing is just `go build .`.

---

## 10. Run the Tests

CI (`.github/workflows/ci.yml`) runs exactly these three steps on every push/PR —
run them locally first:

```powershell
. .\scripts\Load-DotEnv.ps1
go build ./...
go vet ./...
go test ./... -race -cover
```

- The `-race` detector catches data races in the concurrency-heavy WhatsApp/log
  paths. Keep it on.
- Some tests exercise the web layer, audio validation, rate limiter, workflow
  engine, and voice-note store. A few integration-flavored tests read
  `DATABASE_URL`; unit tests do not.
- Coverage per package:

  ```powershell
  go test ./... -cover -coverprofile=coverage.out
  go tool cover -html=coverage.out   # opens a browser report
  ```

> [!NOTE]
> `scratch_connect.go` in the repo root carries a `//go:build ignore` tag and is
> **excluded** from `go build ./...` and the test run. It is a standalone QR
> diagnostics tool — run it directly with `go run scratch_connect.go`.

---

## 11. Local Troubleshooting

| Symptom | Cause & fix |
|---|---|
| `Fatal: ffmpeg not found on PATH` | Install ffmpeg (§3.1) or set `ALLOW_MISSING_FFMPEG=true`. |
| `Fatal: Database initialization failed` / ping timeout | Wrong/blocked `DATABASE_URL`; missing `?sslmode=require`; Neon branch paused. |
| `FATAL: SESSION_SECRET must be set when SECURE_COOKIE=true` | You enabled prod cookies without a secret. Set `SESSION_SECRET` or unset `SECURE_COOKIE` for local HTTP. |
| Logged out after every restart | `SESSION_SECRET` not set → random per boot. Set a stable value. |
| Can't log in — no password | If `ADMIN_PASSWORD` was unset, the generated one printed **once** at first boot. Re-seed by wiping the `users` table (`cmd/dbreset -mode=app`) or set `ADMIN_PASSWORD` and reset. |
| Dashboard loads but WhatsApp stays "disconnected" | Scan the QR (terminal or dashboard) or use `PAIR_PHONE_NUMBER`. QR codes rotate ~6 times then the channel closes — click **Generate new QR**. |
| Agent never replies to a chat | The contact is disabled by default — enable it on the WhatsApp page. |
| Voice notes fail but text works | ffmpeg missing, or no STT/TTS key set. Check the STT/TTS "provider registered/skipped" lines at boot. |
| `unknown driver "pgx"` | Only happens if the `pgx/v5/stdlib` blank import is removed — don't. |
| Build error: two `main` functions | You removed the `//go:build ignore` tag from `scratch_connect.go`. Restore it. |

Every inbound message logs with a `[trace=<whatsapp-msg-id>]` prefix — grep one
message's full pipeline with that id.

---

## 12. Optimizing for the e2-micro (1 vCPU / 1 GB)

The codebase is already tuned for a tiny host; this section documents the levers
and the few you should add at deploy time.

### 12.1 Already in the code

- **DB pool bounded** (`database/conn.go`): `MaxConns=5`, `MinConns=1`,
  idle 10 min, lifetime 30 min — prevents connection-storm memory growth.
- **Streaming, single-worker voice uploads** (`internal/voicenotes`): exactly one
  upload goroutine, and the GCS writer `ChunkSize` is dropped from the 16 MB
  default to **256 KB**, so resident memory stays flat regardless of file size.
- **Bounded rate-limiter maps**: stale keys are swept once the map exceeds 1024
  entries, so counters can't grow unbounded.
- **Log broker back-pressure**: log lines are dropped rather than blocking when a
  slow SSE client can't keep up — no unbounded buffering.
- **ffmpeg via pipes**: transcoding streams stdin→stdout, no temp files.
- **Static binary, embedded assets**: no runtime asset directory, no Node.

### 12.2 Add at deploy time

Set Go runtime limits in the systemd unit (see §13.6) so the GC caps peak RSS:

```ini
Environment=GOMEMLIMIT=750MiB   # soft memory ceiling → GC works harder before OOM
Environment=GOGC=50             # collect more often; trades a little CPU for lower RSS
```

Add a **1 GB swap file** as an OOM safety net (ffmpeg spikes + a reverse proxy on
a 1 GB box can get tight):

```bash
sudo fallocate -l 1G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
sudo sysctl -w vm.swappiness=10        # prefer RAM; use swap only under pressure
```

### 12.3 Disk (10–30 GB)

- The binary (~20–30 MB stripped) + logs + spool is all that lives on disk.
- **Cap journald** so logs never fill the disk (see §15).
- The **spool dir** self-drains: files are deleted right after a successful
  upload. Put `VOICE_SPOOL_DIR` on the boot disk (`/opt/sawt/voice-spool`).
- Set a **GCS bucket lifecycle rule** matching `RETENTION_DAYS` so archived audio
  is auto-deleted (the DB retention job only purges the ledger rows, not objects).

---

## 13. Production Deployment to GCP

Target: a single always-on **GCE e2-micro**, `systemd`-managed, behind a
TLS-terminating reverse proxy. The e2-micro free tier is **one instance per
billing account**, only in `us-west1` / `us-central1` / `us-east1`, with up to
30 GB standard persistent disk.

### 13.1 Create the VM

```bash
gcloud compute instances create sawt-gateway \
  --zone=us-west1-b \
  --machine-type=e2-micro \
  --image-family=debian-12 --image-project=debian-cloud \
  --boot-disk-size=30GB --boot-disk-type=pd-standard \
  --tags=sawt-gateway
```

whatsmeow's connection to WhatsApp is **outbound**, so the VM needs *no* inbound
public port just to run the bot. Inbound ports are only for the dashboard (§13.3).

### 13.2 Secure SSH

Use IAP tunneling — no public port 22, no key management on the box:

```bash
gcloud compute ssh sawt-gateway --zone=us-west1-b --tunnel-through-iap
```

If you must allow direct SSH, restrict it to your IP only (never `0.0.0.0/0`):

```bash
gcloud compute firewall-rules create sawt-ssh \
  --direction=INGRESS --action=ALLOW --rules=tcp:22 \
  --source-ranges=YOUR.IP.ADDR.0/32 --target-tags=sawt-gateway
```

### 13.3 Firewall for the dashboard

> [!WARNING]
> Do **not** open port `8080` to the internet. The dashboard speaks plain HTTP
> and, when directly exposed, `middleware.RealIP` will trust spoofable
> `X-Forwarded-For` headers — letting an attacker sidestep the login rate limit
> (see §14.6). Terminate TLS at a reverse proxy and expose only 80/443.

Open only the proxy ports:

```bash
gcloud compute firewall-rules create sawt-web \
  --direction=INGRESS --action=ALLOW --rules=tcp:80,tcp:443 \
  --source-ranges=0.0.0.0/0 --target-tags=sawt-gateway
```

For a private tool, prefer **restricting `--source-ranges` to your office/VPN IP**
or fronting it with **IAP** / a **Cloudflare Tunnel** (no open inbound ports at
all — see §14.2).

### 13.4 Prepare the host

```bash
sudo apt-get update && sudo apt-get install -y ffmpeg
sudo useradd --system --home /opt/sawt --shell /usr/sbin/nologin sawt
sudo mkdir -p /opt/sawt/voice-spool
sudo chown -R sawt:sawt /opt/sawt
```

### 13.5 Ship the binary + config

From your workstation (build per §9), copy **only** the binary and the `.env` —
no source tree, no Go toolchain on the VM:

```bash
gcloud compute scp sawt-gateway .env.production \
  sawt-gateway:~ --zone=us-west1-b --tunnel-through-iap
```

On the VM, install them with tight permissions:

```bash
sudo install -o sawt -g sawt -m 755 ~/sawt-gateway /opt/sawt/sawt-gateway
sudo install -o sawt -g sawt -m 600 ~/.env.production /opt/sawt/.env
rm -f ~/sawt-gateway ~/.env.production   # don't leave copies in your home dir
```

Make sure `/opt/sawt/.env` has `SECURE_COOKIE=true` and a real `SESSION_SECRET`.

### 13.6 systemd service (hardened, auto-restart)

```bash
sudo tee /etc/systemd/system/sawt.service >/dev/null << 'EOF'
[Unit]
Description=Sawt WhatsApp ERP Gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=sawt
Group=sawt
WorkingDirectory=/opt/sawt
ExecStart=/opt/sawt/sawt-gateway
EnvironmentFile=/opt/sawt/.env
Environment=GOMEMLIMIT=750MiB
Environment=GOGC=50

# --- resilience ---
Restart=always
RestartSec=5
StartLimitIntervalSec=0

# --- least privilege / sandboxing ---
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictNamespaces=true
RestrictSUIDSGID=true
LockPersonality=true
# writable paths the app actually needs (spool dir):
ReadWritePaths=/opt/sawt/voice-spool

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now sawt
```

First boot: watch the log for the generated admin password (if you didn't set
`ADMIN_PASSWORD`), then pair WhatsApp:

```bash
journalctl -u sawt -f
```

> [!NOTE]
> `ProtectSystem=strict` makes the whole filesystem read-only **except**
> `ReadWritePaths`. If you relocate `VOICE_SPOOL_DIR`, add it there too, or the
> spool write (and voice archival) will fail.

### 13.7 TLS / HTTPS via reverse proxy (Caddy)

The app has **no built-in TLS** (assumption: TLS is terminated at a proxy —
implement in-app TLS only if you can't run a proxy). Caddy gives you automatic
Let's Encrypt certificates with almost no config. You need a DNS `A` record
pointing your domain at the VM's external IP.

```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update && sudo apt-get install -y caddy
```

`/etc/caddy/Caddyfile`:

```caddy
sawt.example.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8080 {
        header_up X-Forwarded-For {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }
    # Belt-and-suspenders: enforce HSTS at the edge (the app doesn't send it).
    header Strict-Transport-Security "max-age=31536000; includeSubDomains"
}
```

```bash
sudo systemctl reload caddy
```

Now the dashboard is HTTPS-only, and because the proxy sets a trustworthy
`X-Forwarded-For`, `middleware.RealIP` reports the true client IP for the login
limiter. With TLS in front, keep `SECURE_COOKIE=true`.

---

## 14. Security Hardening

Each item notes what the code **already does** vs. what you must **add**.

### 14.1 Secrets management

- **Already:** secrets are read from the environment; nothing is hardcoded;
  `.gitignore` excludes `.env`/`.env.*`; the seeded admin password is bcrypt-hashed
  and only printed once.
- **Add / act on:**
  - **Rotate every value in the shipped `.env.production` before go-live.** Any
    secret written to a file on disk should be considered exposed. This includes
    the `DATABASE_URL` password and `ADMIN_PASSWORD`.
  - Prefer **GCP Secret Manager**: store `DATABASE_URL`, `SESSION_SECRET`, LLM/STT
    keys as secrets; grant the VM's service account `roles/secretmanager.secretAccessor`;
    fetch them into `/opt/sawt/.env` at boot with an `ExecStartPre` script. This is now
    implemented — [`gcp/fetch-secrets.sh`](../gcp/fetch-secrets.sh), wired into
    [`gcp/sawt.service`](../gcp/sawt.service) — see [`gcp/README.md`](../gcp/README.md) §5a. It's a
    starting point: check the script's secret-id names against what you actually created in Secret
    Manager before relying on it.
  - Give the VM a **dedicated service account** with only the roles it needs
    (`storage.objectAdmin` scoped to the voice bucket; `iam.serviceAccountTokenCreator`
    on itself for signed URLs) — not the default editor SA.

### 14.2 TLS / HTTPS

- **Already:** none in-app (plain HTTP on `:8080`).
- **Add:** terminate TLS at Caddy/nginx (§13.7) **or** front with a Cloudflare
  Tunnel / GCP IAP so no HTTP port is ever exposed. Set `SECURE_COOKIE=true`.

### 14.3 Secure cookies

- **Already:** session and CSRF cookies are `HttpOnly` (session) / `SameSite=Lax`,
  and gain the `Secure` flag when `SECURE_COOKIE=true`. Sessions are HMAC-signed,
  expire in 24 h, and are cleared on logout/invalid.
- **Add:** always run `SECURE_COOKIE=true` in prod. Consider shortening the 24 h
  lifetime for a higher-sensitivity deployment. Rotating `SESSION_SECRET`
  instantly invalidates all sessions (emergency logout-everyone switch).

### 14.4 CSRF protection

- **Already:** every state-changing route (`POST` login/logout, all dashboard
  mutations) is wrapped in `requireCSRF` using the **double-submit cookie**
  pattern with a 32-byte random token compared in constant time (`hmac.Equal`).
- **Add:** nothing required. Keep new `POST`/mutating routes behind
  `s.requireCSRF` and include the `{{.CSRFToken}}` hidden field in their forms.

### 14.5 XSS mitigation

- **Already:** all UI is rendered through Go's `html/template`, which
  context-escapes by default. A strict **CSP** is sent on every response
  (`default-src 'self'`; `script-src 'self' https://unpkg.com`;
  `object-src 'none'`; `frame-ancestors 'none'`) plus `X-Content-Type-Options: nosniff`.
  The one deliberate `template.URL` cast is for a server-generated QR **PNG data
  URI**, not user input.
- **Add:** avoid introducing raw `template.HTML`/`template.JS` on user-controlled
  data. To drop the CDN dependency entirely, **self-host HTMX and the font** so
  `script-src`/`font-src` can be `'self'` only (also removes an external-availability
  and supply-chain surface).

### 14.6 Input validation & trusted proxy

- **Already:** phone numbers are stripped to digits and length-checked (8–15);
  agent-status transitions are whitelisted; agent assignment rejects
  unpublished/unknown ids; publish is gated on a non-empty prompt; object/spool
  names are sanitized to `[A-Za-z0-9_-]` and length-capped; voice payloads are
  size- and magic-byte-validated (`OggS`).
- **Add:** `middleware.RealIP` trusts `X-Forwarded-For`/`X-Real-IP`. This is
  **only safe behind a trusted proxy**. Ensure the app is unreachable except via
  Caddy (firewall to 80/443; the app listens on `:8080` but must not be public).
  If you ever expose it directly, remove `RealIP` or the login limiter is trivially
  bypassed by spoofing the header.

### 14.7 SQL injection prevention

- **Already:** all queries go through **sqlc-generated** methods and `pgx`
  parameter binding (`$1, $2, …`) — no string-concatenated SQL. The only literal
  SQL (`schema.sql`, admin/agent seed, `dbreset`) is static, not user-derived.
- **Add:** keep new queries in `query.sql` and regenerate with `sqlc`; never
  build SQL by concatenation.

### 14.8 Rate limiting

- **Already:** in-memory sliding-window limiters — **inbound** WhatsApp at
  **8 msgs/min per chat** (protects the STT→LLM→ERP pipeline and your provider
  bills), and **login** at **10 attempts / 5 min per IP** (throttles credential
  stuffing). Single-instance-appropriate; counters reset on restart by design.
- **Add:** tune to taste (`ratelimit.New(...)` in `main.go` / `web/server.go`).
  If you ever scale out (you shouldn't — single instance is a hard constraint),
  move these to Redis.

### 14.9 Least privilege

- **Already:** runs as a dedicated non-login `sawt` user (not root); the DB pool
  is capped.
- **Add:** the hardened systemd unit in §13.6 (`NoNewPrivileges`,
  `ProtectSystem=strict`, `PrivateTmp`, restricted `ReadWritePaths`, etc.) and a
  minimally-scoped GCP service account (§14.1).

### 14.10 Graceful panic recovery

- **Already:** two layers. HTTP handlers are wrapped by `reportPanics` +
  chi `Recoverer` (panic → error webhook → clean 500, server stays up). The
  per-message worker in `handleIncomingMessage` has its own `defer recover()`
  that reports the panic and sends the user a friendly error instead of crashing
  the daemon.
- **Also already:** on `SIGTERM` the HTTP server is gracefully drained
  (`http.Server.Shutdown`, 5 s bound, force-`Close` fallback) **before** WhatsApp
  disconnects, and in-flight message handlers are drained (`inflightWG`, 25 s
  bound). The server sets `ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout` (Slowloris
  guard; `WriteTimeout` is left 0 so the `/api/logs` SSE stream stays open). Inbound
  handling is bounded by `MAX_INFLIGHT` (default 32) with a per-message 120 s deadline.

---

## 15. Logging & Observability

- **Structured tracing:** every inbound message carries a `trace_id` equal to its
  WhatsApp message id; all pipeline logs are prefixed `[trace=<id>]`. To
  reconstruct one message end-to-end: `journalctl -u sawt | grep 'trace=<id>'`.
- **Request logging:** chi's `RequestID` + `Logger` middleware log every HTTP
  request with a request id.
- **Live log stream:** the dashboard `/dashboard/logs` page streams stdout in
  real time over SSE (the app tees `log` output to stdout **and** an in-memory
  broker). Slow clients drop lines rather than block the daemon.
- **Error/panic reporting:** set `ERROR_WEBHOOK_URL` to a Slack/Discord-compatible
  endpoint; `internal/monitor` posts every error and recovered panic (with stack
  and `trace_id`) asynchronously.
- **journald caps (do this):** stop logs from filling a 10 GB disk —

  ```bash
  sudo mkdir -p /etc/systemd/journald.conf.d
  printf '[Journal]\nSystemMaxUse=200M\nMaxRetentionSec=1month\n' \
    | sudo tee /etc/systemd/journald.conf.d/sawt-cap.conf
  sudo systemctl restart systemd-journald
  ```

> [!TIP]
> Consider adding a structured logger (`log/slog`, stdlib since Go 1.21, or
> `rs/zerolog` which is already an indirect dependency) if you want JSON logs for
> ingestion into Cloud Logging. Current logging is line-oriented `log.Printf`,
> which is perfectly adequate for `journalctl` + grep on a single VM.

---

## 16. Health & Monitoring

- **Liveness / readiness:** the app exposes three **unauthenticated** endpoints — `GET /healthz`
  (liveness, `{"status":"ok"}`), `GET /readyz` (readiness — DB ping + WhatsApp state; `503` when the
  DB is down), and `GET /metrics` (JSON uptime / goroutines / WhatsApp state / voice-note counters).
  Point a GCP Uptime Check at `https://sawt.example.com/healthz` (or `/readyz` to gate on the DB).
- **WhatsApp link state:** the dashboard home shows connection status and uptime;
  a debounced banner warns only after a **sustained** (>15 s) disconnect, so brief
  reconnect blips don't cry wolf.
- **Service health:** `systemctl status sawt` and `journalctl -u sawt -f`.
- **Voice-note worker:** the store tracks lifetime `uploaded`/`failed` counters
  (logged); failed uploads retry with exponential backoff (30 s → 1 h, max 5
  attempts) and survive restarts via the Postgres ledger + on-disk spool.

---

## 17. Backups & Disaster Recovery

- **Database (everything durable):** WhatsApp device keys, contacts, activity,
  conversation memory, users, agents, and pending confirmations all live in Neon.
  Rely on **Neon's PITR/branch backups**; optionally take a periodic `pg_dump` to
  a separate bucket for defense in depth.
- **Voice audio:** in the GCS bucket. Enable **Object Versioning** and/or a
  lifecycle rule aligned to `RETENTION_DAYS`.
- **VM:** disposable. Losing it means: re-create the VM (§13.1), re-install the
  binary + `.env` (§13.5–13.6). The WhatsApp session **resumes from Postgres** —
  no re-pairing needed unless the number was unlinked.
- **If WhatsApp bans/unlinks the number:** re-pair from the dashboard (QR or phone
  code) and slow your send patterns.
- **DR drill (recommended quarterly):** spin up a scratch VM, deploy the binary
  against a **Neon branch** of prod, confirm the dashboard loads and the session
  restores. Never point two live instances at the same WhatsApp number.

---

## 18. Update & Rollback Strategy

**Deploy a new version:**

1. Locally: `go build ./...` → `go vet ./...` → `go test ./... -race -cover`
   (CI enforces the same on `main`).
2. Cross-compile the linux/amd64 binary (§9).
3. Copy it up **beside** the running one and swap atomically:

   ```bash
   gcloud compute scp sawt-gateway sawt-gateway:~/sawt-gateway.new --zone=us-west1-b --tunnel-through-iap
   sudo install -o sawt -g sawt -m 755 ~/sawt-gateway.new /opt/sawt/sawt-gateway
   sudo systemctl restart sawt
   ```

4. Watch `journalctl -u sawt -f` for a clean boot + reconnect.

**Schema changes:** `schema.sql` is idempotent (`CREATE TABLE/INDEX IF NOT EXISTS`)
and re-applied at every boot — **additive changes apply automatically**.
Destructive changes (drops, type changes, renames) are **not** handled: do those
as a deliberate manual migration during a maintenance window.

**Rollback:** keep the previous binary (e.g. `/opt/sawt/sawt-gateway.prev`).
To revert: `sudo install` the old one back over `/opt/sawt/sawt-gateway` and
`systemctl restart sawt`. Because the WhatsApp session and all data are in
Postgres, a binary rollback is safe as long as you didn't run a destructive
migration.

---

## 19. Operational Tooling (dbreset / harness)

Two helper commands ship in `cmd/` (run from the repo root, with `DATABASE_URL`
set):

- **`cmd/dbreset`** — wipe & rebuild the DB from `schema.sql`:

  ```powershell
  go run ./cmd/dbreset -mode=app          # drops ONLY Sawt tables; keeps WhatsApp pairing
  go run ./cmd/dbreset -mode=full -yes    # drops the whole public schema; you must re-pair
  ```

  `-mode=app` is the safe default (WhatsApp session survives). `-mode=full` also
  wipes the whatsmeow tables — you'll re-link the number. Interactive `RESET`
  confirmation unless `-yes`.

- **`cmd/harness`** — boot the dashboard against the live DB **without** a
  WhatsApp connection, with a `/preview-login` bypass on `:8091`. Great for UI/
  handler iteration. It reads `.env.production` if present and forces
  `SECURE_COOKIE=false` for local HTTP.

- **`scratch_connect.go`** (root, `//go:build ignore`) — standalone WhatsApp QR
  diagnostics: `go run scratch_connect.go`.

> [!CAUTION]
> `dbreset` is **destructive and irreversible**. Never run it against a
> production `DATABASE_URL` unless you intend to wipe data. Double-check which
> `.env` is loaded first.

---

## 20. Completeness Checklist

Mapping of this guide to the deployment/development requirements:

- [x] **Windows 11 dev setup** — Go, Git, VS Code (+ extensions), PowerShell, ffmpeg (§3)
- [x] **Every prerequisite, env var, and command** — §2, §5, §6, §7
- [x] **`.env` creation, security, full variable reference** — §5 (no secrets hardcoded)
- [x] **Install / run / debug / build / test / troubleshoot** — §6–§11
- [x] **e2-micro optimization** (memory/CPU/disk) — §12 (pool caps, streaming uploads, `GOMEMLIMIT`, swap, journald caps)
- [x] **Production GCP deploy** — VM, firewall, secure SSH, env, binary, systemd, auto-restart, logs, backups, updates (§13, §15, §17, §18)
- [x] **Security hardening** — headers, TLS, secure cookies, CSRF, XSS, input validation, SQLi prevention, secrets, least privilege, panic recovery (§14)
- [x] **Structured request/security logging** — §15 (trace ids, request logging, error webhook, journald)
- [x] **IP-based rate limiting** — §14.8 (login 10/5 min/IP, inbound 8/min/chat) with the trusted-proxy caveat (§14.6)
- [x] **Safe HTML-template UI exposure** — embedded templates/static, CSP, reverse proxy, `/static` routing (§1, §13.7, §14.5)
- [x] **Performance optimizations for a constrained server** — §12
- [x] **Identified gaps & operational risks** — rotate shipped secrets (§14.1), add HSTS (§13.7), trusted-proxy requirement for `RealIP` (§14.6), graceful HTTP shutdown (§14.10), self-host CDN assets (§14.5)

> **Stated assumptions where functionality is absent (not invented):** the app
> has no in-process TLS (terminate at a proxy), no HSTS header (set it at the
> proxy), and no in-dashboard password-change flow (rotate via `ADMIN_PASSWORD` +
> re-seed). These are documented as recommendations, not assumed-existing
> features. (Graceful `http.Server.Shutdown` on `SIGTERM`, server timeouts, and
> `/healthz`·`/readyz`·`/metrics` **are** implemented — see §14.10 and §16.)
