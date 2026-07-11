# Sawt Gateway (`sawt-go`)

> WhatsApp voice as the primary UI for the **mshalia** ERP — a domain-agnostic, tool-using AI operations assistant.

One Go binary (`sawt-gateway`) that owns the WhatsApp socket, LLM reasoning loop, speech cascades (STT/TTS), and the operator web dashboard in a single always-on process.

## Architecture

```
WhatsApp voice/text
  │  whatsmeow socket (in-process)
main.go: handleIncomingMessage()
  │  1. lookup / auto-create contact
  │  2. audio → ffmpeg OGG→WAV → STT cascade
  │  3. resolve actor identity via ERP (HMAC-signed)
  │  4. workflow engine: memory → confirmation → classify → tool loop
  │  5. TTS cascade → ffmpeg WAV→Opus
  │  6. send reply
  ▼
WhatsApp reply (text + voice)
                              │ tool calls (HMAC-signed)
                              ▼
               mshalia /api/agent/v1/*  → Firestore
```

**39 tools across 6 agents** (operations, accounting, administration, client self-service, sales, breeding), role-gated, with human-in-the-loop confirmation for medium/high-risk writes.

## Quick Start

### Prerequisites

- **Go 1.23+**
- **PostgreSQL** (Neon or local)
- **ffmpeg** on PATH (required for voice notes; set `ALLOW_MISSING_FFMPEG=true` for text-only dev)
- At least one LLM API key (`NIM_API_KEY` or `OPENAI_API_KEY`)

### Setup

```bash
# 1. Clone and configure
cp .env.example .env   # or edit .env directly
# Fill in DATABASE_URL, API keys, etc.

# 2. Run
go run .

# 3. Open dashboard
# http://localhost:8080/dashboard
```

### Windows (PowerShell)

```powershell
# Load .env into your shell session
. ./scripts/Load-DotEnv.ps1

# Run
go run .
```

## Testing

```bash
# Full suite with race detection
go test ./... -race -cover

# Lint (requires golangci-lint)
golangci-lint run
```

**141 test functions across 24 test files** covering auth, CSRF, HMAC contracts, intent classification, tool-loop bounds, role filtering, memory, confirmation lifecycle, provider fallback, speech providers (fakes), voice-note store, and a 7-scenario eval suite.

## Documentation

Detailed docs live in [`docs/`](docs/):

| Document | Purpose |
|---|---|
| [BLUEPRINT.md](docs/BLUEPRINT.md) | Architecture & product intent |
| [IMPLEMENTATION-PLAN.md](docs/IMPLEMENTATION-PLAN.md) | Readiness scorecard, roadmap & backlog |
| [DEPLOYMENT.md](docs/DEPLOYMENT.md) | Deploy & ops runbook |
| [LOCAL-TESTING.md](docs/LOCAL-TESTING.md) | Local test tiers |
| [mshalia-side.md](docs/mshalia-side.md) | External ERP gateway brief |
| [README.md](docs/README.md) | Docs index |

## License

Private — all rights reserved.
