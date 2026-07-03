# Local Dev & Testing Runbook

Goal: run `sawt-gateway` locally to develop/test, then produce the same Linux binary you'll ship to the GCP e2-micro (1 GB) instance described in [DEPLOYMENT.md](DEPLOYMENT.md).

**Recommendation: use WSL Ubuntu, not native Windows cmd.** The target VM is Linux/amd64, so building and running inside WSL gives you the exact same binary and behavior you'll deploy — no cross-compile surprises, and `apt install ffmpeg` just works. Command Prompt works too (steps below) but you'll cross-compile for the final GCP build either way.

No local Postgres and no git required anywhere in this flow — both are unnecessary weight on a 1 GB VM and aren't needed for dev either.

## 1. Prerequisites

| Tool | WSL Ubuntu | Windows cmd |
|---|---|---|
| Go 1.25+ | `sudo apt update && sudo apt install -y golang-go` (or [go.dev/dl](https://go.dev/dl/) if apt's version is older than 1.25) | Install from [go.dev/dl](https://go.dev/dl/) (MSI) |
| ffmpeg | `sudo apt install -y ffmpeg` | `winget install ffmpeg` or set `ALLOW_MISSING_FFMPEG=true` to skip voice notes for now |
| Postgres | none — use Neon (see §2) | none — use Neon (see §2) |
| git | none — you already have the code locally; nothing is cloned on the VM either | none |

Check versions: `go version` (need 1.25+, per `go.mod`).

You already have the repo (this folder). No clone step needed — just work from it directly, in WSL that's typically `/mnt/c/Users/Asus/Documents/GitHub/gobasim`.

## 2. Database: use a Neon dev branch, not local Postgres

The project already targets Neon in prod, and a local/Docker Postgres just burns RAM you don't have to spare on the 1 GB target anyway. Instead:

1. In the [Neon console](https://console.neon.tech), open your existing project and create a **branch** off it (e.g. `dev`) — gives you an isolated database with its own connection string, so local testing can't touch prod data, without needing another service running.
2. Copy that branch's connection string (`postgresql://user:pass@ep-xxxx.neon.tech/dbname?sslmode=require`) — this is your dev `DATABASE_URL`.
3. Schema is applied automatically at boot (`schema.sql`, idempotent `CREATE TABLE IF NOT EXISTS`) — nothing to migrate manually.

## 3. Configure environment

Create `.env` in the repo root (already gitignored):

```bash
DATABASE_URL=postgresql://user:pass@ep-xxxx.neon.tech/dbname?sslmode=require   # your Neon dev branch
SESSION_SECRET=dev-only-not-for-prod
ADMIN_USERNAME=admin
ADMIN_PASSWORD=devpassword123
PORT=8080
ALLOW_MISSING_FFMPEG=true   # drop this once ffmpeg is installed and you want to test voice notes
```

Everything else in the table in DEPLOYMENT.md (`NIM_API_KEY`, `GROQ_API_KEY`, `AGENT_GATEWAY_SECRET`, `MSHALIA_API_URL`, etc.) is only needed to exercise the LLM/STT/ERP paths — leave them unset to just verify the app boots, migrates its schema, and serves the dashboard.

Load the file before running (WSL/bash):

```bash
export $(grep -v '^#' .env | xargs)
```

On Windows cmd, set them per-session instead:

```cmd
set DATABASE_URL=postgresql://user:pass@ep-xxxx.neon.tech/dbname?sslmode=require
set SESSION_SECRET=dev-only-not-for-prod
set ADMIN_USERNAME=admin
set ADMIN_PASSWORD=devpassword123
set ALLOW_MISSING_FFMPEG=true
```

## 4. Run the CI checks locally first

Mirrors `.github/workflows/ci.yml` — catches most issues before you even boot the app:

```bash
go build ./...
go vet ./...
go test ./... -race -cover
```

## 5. Run it

```bash
go run .
```

Expect in the log: schema bootstrap against your Neon dev branch, a generated/seeded admin password (only if `ADMIN_PASSWORD` wasn't set), a WhatsApp QR code printed to the terminal, and `Web Dashboard serving at http://localhost:8080`.

- Open `http://localhost:8080/login`, sign in with `ADMIN_USERNAME`/`ADMIN_PASSWORD`.
- Dashboard, logs, and workflow pages live under `/dashboard`, `/dashboard/logs`, `/dashboard/workflows`.
- The WhatsApp socket connects to real WhatsApp servers even in dev — scan the QR with a real (ideally a spare/test) WhatsApp number if you want to test the messaging pipeline end to end. Skip this if you only want to test the dashboard/ERP/workflow code.
- `Ctrl+C` to stop.

Iterate with `go run .` (recompiles each time) or `air`/`reflex` if you want autoreload — not currently wired in this repo.

## 6. Build the artifact you'll actually ship

From WSL Ubuntu (native match for the e2-micro VM, which is linux/amd64):

```bash
CGO_ENABLED=0 go build -o sawt-gateway .
file sawt-gateway   # sanity check: ELF 64-bit LSB executable, x86-64
```

From Windows cmd (cross-compiling):

```cmd
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -o sawt-gateway .
```

No cgo dependencies in this module (pgx, lib/pq, whatsmeow are pure Go), so cross-compiling from Windows is safe.

## 7. Ship it

Copy just the compiled `sawt-gateway` binary + a production `.env` to the VM (`scp`, or `gcloud compute scp`) — no git clone, no source tree, no build toolchain needed on the VM itself. Then follow the systemd steps already in [DEPLOYMENT.md](DEPLOYMENT.md) (`/opt/sawt/`, `sawt.service`, `systemctl enable --now sawt`). Point `.env` on the VM at the **production** Neon branch, not the dev branch from §2.

Remember: only one instance may run at a time — the WhatsApp session and in-memory rate limiter are process-local, and Postgres/Neon is the only stateful dependency (device keys, contacts, conversation memory all live there, not on the VM disk).
