# Sawt Gateway — Status, Roadmap & Feature Backlog

> **Purpose.** The single "where are we / what's left" document for `sawt-go` (the one binary
> `sawt-gateway`). It scores production-readiness with a weighted **Project Ready %**, lists every
> remaining gap with priority/effort, records the **Go-Live Checklist**, and carries the
> dashboard/observability **feature backlog**. It folds in the former `BACKLOG.md` and the
> now-closed `AGENTIC-GATEWAY-AUDIT.md` (all audit findings are implemented — see §5).
>
> **Companion docs:** [`DEPLOYMENT.md`](DEPLOYMENT.md) (deploy/ops runbook) ·
> [`BLUEPRINT.md`](BLUEPRINT.md) (architecture & product intent) ·
> [`LOCAL-TESTING.md`](LOCAL-TESTING.md) (local test tiers) ·
> [`mshalia-side.md`](mshalia-side.md) (external ERP-gateway brief).
>
> **Method:** correctness over optimism. Nothing is "done" unless verified in the code. Gaps are
> stated, never assumed away. Assumptions are tagged **[A]**.

---

## Table of Contents

1. [Overview & Current State](#1-overview--current-state)
2. [Project Ready KPI](#2-project-ready-kpi)
3. [Category Scorecard](#3-category-scorecard)
4. [Technical Debt & Risk Register](#4-technical-debt--risk-register)
5. [Agentic-Gateway Audit — Closed](#5-agentic-gateway-audit--closed)
6. [Prioritized Roadmap](#6-prioritized-roadmap)
7. [Feature Backlog (Dashboard/Observability Epics)](#7-feature-backlog-dashboardobservability-epics)
8. [Go-Live Readiness Checklist](#8-go-live-readiness-checklist)
9. [Executive Summary](#9-executive-summary)

---

## 1. Overview & Current State

`sawt-gateway` is **one Go binary** running the whole platform in a single always-on process: the
WhatsApp socket (`internal/whatsmeow`), STT/TTS cascades (`internal/speech`), ffmpeg transcoding
(`internal/audio`), the LLM intent-classification + bounded tool-calling loop (`internal/workflow`),
the HMAC-signed ERP client (`internal/erp`), conversation memory + risk-gated confirmations
(Postgres-backed), and the operator web dashboard (`web/`).

**Built and code-complete** (verified in-repo):

- WhatsApp transport: Postgres device store, QR + phone-code pairing, reconnect.
- STT (4-provider cascade) and TTS (3-provider cascade), key-driven fallback.
- LLM reasoning: intent classification → per-agent tool loop (max 4 iterations), NIM →
  OpenAI-compatible fallback. **39 tools across 6 agents**, role-gated, financial writes
  confirmation-gated at `high` risk with required idempotency keys.
- Cross-turn memory (per-agent `max_history`, default 8, + rolling summary) and a durable,
  single-slot confirmation flow (10-minute TTL) for medium/high-risk tools.
- Dashboard: bcrypt login, HMAC-signed sessions, CSRF, CSP + hardening headers, activity feed,
  contact/agent config, WhatsApp pairing, live SSE logs.
- **Agentic hardening (audit remediation, §5):** inbound dedup (`processed_messages`), durable
  per-tool step log (`tool_executions`), atomic confirmation claim, ERP retry/backoff +
  deterministic idempotency + trace header, `/healthz` · `/readyz` · `/metrics`, `log/slog`
  (text/JSON), graceful shutdown + HTTP server timeouts, per-message 120 s deadline.
- PII retention job, error/panic webhook, per-message trace ids, voice-note archival to GCS.
- CI (`build` + `vet` + `test -race -cover`) and **151 test functions across 24 test files**
  (incl. the 7-scenario eval suite and fake-based speech-provider coverage).

**M9 progress (2026-07-13):** the front half (WhatsApp QR pairing, voice-note send/receive, STT,
LLM, TTS) was live-confirmed in an earlier partial run. The **ERP half is now verified against a
local `mshalia`** (port 3000) — see [`M9-CHECKLIST.md`](M9-CHECKLIST.md). Two things that were
previously believed blocking are resolved:
- The identity blocker (super_admin `966546906905` resolving with no org) is fixed by the
  `DEFAULT_ORG_ID` fallback (`internal/erp/fallback.go`) and **verified** via `cmd/wfcli`.
- **The `mshalia` gateway tools exist** — all **39 tool ids are implemented with real handlers and
  match the Go client id-for-id**; the HMAC contract matches exactly; `identity/resolve` works.
  (The prior claim that they "`404`" was stale.) Verified live: identity resolve, `DEFAULT_ORG`
  fallback, classify, tool loop with self-correction, RBAC filtering, the confirmation gate, and a
  live read **and** write (horse count 2 → 3, independently read back).

Remaining for full M9 sign-off: the same §B/§C scenarios against a **deployed** `mshalia`, and the
real-WhatsApp voice round-trip (human-in-the-loop). One behavior gap surfaced — **F-1**:
confirmation-gated writes can't self-correct malformed model args (see [`M9-CHECKLIST.md`](M9-CHECKLIST.md)).

**Production deployment (2026-07-13):** `sawt-gateway` is now live on GCE (`gateway-go`,
`us-central1-a`, e2-micro, static IP), running under the hardened systemd unit as the non-root
`sawt` user, `Restart=always`, `/healthz`/`/readyz` both green (`{"db":true,"ready":true,"whatsapp":"connected"}`).
**TLS is validated end-to-end**: Caddy reverse-proxies `https://sawt.osamamaalam.com` to
`127.0.0.1:8080` with an auto-issued Let's Encrypt cert, confirmed via a live HTTPS request; the
static IP (`34.31.194.71`) is reserved so the DNS `A` record won't go stale. This closes D-7 and
Phase 1 item 1b. `SECURE_COOKIE` is being flipped to `true` now that HTTPS is confirmed live.
Secrets are **not yet vaulted**: all 12 config values exist in **GCP Secret Manager** with the VM's
service account granted `roles/secretmanager.secretAccessor` and the `cloud-platform` API scope
enabled, but the running service still reads a static, manually-installed `/opt/sawt/.env`
(0600, `sawt:sawt`) rather than fetching from Secret Manager at boot — D-1 remains open until an
`ExecStartPre` fetch script replaces the static file. Swap file and journald log cap (§12.2/§15 of
[`DEPLOYMENT.md`](DEPLOYMENT.md)) are also still outstanding.

---

## 2. Project Ready KPI

> ### **Project Ready: ~79%**

**Formula:** `Project Ready = Σ(weightᵢ × scoreᵢ) / 100`; the 15 weights sum to 100. Each score
(0–100) is anchored to specific files; each weight reflects how much the category blocks
production. Scalability is deliberately low-weighted because single-instance operation is an
intentional constraint **[A3]**, not a defect. Scores mirror the §3 category scorecard.

| # | Category | Weight | Score | Contribution |
|---|---|--:|--:|--:|
| 1 | Core functionality | 15 | 82 | 12.30 |
| 2 | Code quality | 6 | 88 | 5.28 |
| 3 | Architecture | 6 | 82 | 4.92 |
| 4 | Go best practices | 5 | 88 | 4.40 |
| 5 | Testing coverage | 9 | 77 | 6.93 |
| 6 | Documentation | 6 | 84 | 5.04 |
| 7 | Deployment readiness | 8 | 80 | 6.40 |
| 8 | Windows developer experience | 4 | 80 | 3.20 |
| 9 | Production readiness | 9 | 72 | 6.48 |
| 10 | Security | 10 | 82 | 8.20 |
| 11 | Performance | 4 | 75 | 3.00 |
| 12 | Observability | 6 | 72 | 4.32 |
| 13 | Reliability | 5 | 77 | 3.85 |
| 14 | Scalability | 3 | 50 | 1.50 |
| 15 | Maintainability | 4 | 83 | 3.32 |
| | **Total** | **100** | — | **79.14** |

**~79%** (up from 77% after this round). Landed and **verified live**: `sawt-gateway` is deployed to
production GCE (`gateway-go`), running under a hardened systemd unit, and **TLS is validated
end-to-end** — `https://sawt.osamamaalam.com` reverse-proxies to the app via Caddy with an
auto-issued Let's Encrypt cert (D-7 closed, Phase 1 item 1b done). Also still standing: the identity
default-org fallback (`internal/erp/fallback.go` + `DEFAULT_ORG_ID`) that unblocks the ERP path for
privileged actors (D-6a); CI enforces `golangci-lint` (pinned v1.64.8) + a 60% coverage gate;
repo-root `README.md` + `CONTRIBUTING.md` present; 151 tests green. Still capped by items that are
**not** in-repo code — a full M9 over real WhatsApp voice against the **deployed** `mshalia` (all 39
tools), and fully vaulted secrets (Secret Manager holds all 12 values and the VM SA can read them,
but the boot path still loads a static `.env` file rather than fetching live).

**How to read it.** ~79% means the engineering is largely complete and well-hardened, and the
deployment/TLS path is now proven live, but the project is **not yet production-ready**: the ERP
workflow has not completed end-to-end against a deployed `mshalia`, and secrets are not yet fetched
from the vault at boot. Reaching the mid-80s is gated on those two ops/live items, not on more code
here.

---

## 3. Category Scorecard

Each entry: **Status · Evidence · Missing · Risk · Effort.** Risk ∈ {Critical, High, Medium, Low}.
Effort is engineering-days for one competent Go dev.

### 3.1 Core Functionality — 82 · High
- **Status:** Full pipeline implemented; front half (WhatsApp pairing, voice send/receive, STT,
  LLM, TTS) verified live. The identity blocker that broke the ERP path is fixed **and verified**:
  the default-org fallback for privileged actors (`internal/erp/fallback.go`, `DEFAULT_ORG_ID`) was
  confirmed via `cmd/wfcli` — `0546906905` → `org-demo` → `operations` → `list_horses` executed
  against a local `mshalia`. Remaining: the same over real WhatsApp voice against the deployed
  `mshalia` (all 39 tools).
- **Evidence:** `main.go:handleIncomingMessage`; `internal/workflow/engine.go`;
  `internal/erp/fallback.go`; `internal/speech/{stt,tts}.go`; `internal/whatsmeow/client.go`.
- **Missing:** M9 sign-off against a **deployed** `mshalia` + the real-WhatsApp voice round-trip (the
  ERP path is verified against a **local** `mshalia`, M9-CHECKLIST); the F-1 confirmation-write
  self-correction gap; SAR amount thresholds within the `high` tier; identity-resolution cache.
- **Effort:** live-run coordination-bound (see §6 Phase 2).

### 3.2 Code Quality — 88 · Low
- Clean, idiomatic, small cohesive files; consistent `%w` error wrapping; `go:embed` assets;
  `go vet` clean in CI; **`golangci-lint` now enforced in CI** (`.golangci.yml` +
  `.github/workflows/ci.yml`, action pinned to v1.64.8 to match the v1-format config). **Missing:**
  no `gosec` security-lint pass. · 0.5 day.

### 3.3 Architecture — 82 · Low
- Sound single-binary design; provider-cascade reused across STT/TTS/LLM; declarative `agentSpec`
  registry so the router never changes when tools are added. **Missing:** single-instance ceiling
  **[A3]**; additive-only schema (no versioned migrations). · N/A (accepted).

### 3.4 Go Best Practices — 88 · Low
- `context.Context` threaded through all I/O; `context.WithTimeout` on ERP/GCS; pure-Go build
  (`CGO_ENABLED=0`); `sync.RWMutex` on shared WhatsApp state; **HTTP server timeouts + graceful
  `Shutdown` now set** (audit B1); per-message 120 s deadline (C6). **Missing:** `inboundLimiter`
  is a package-level global (minor). · 0.5 day.

### 3.5 Testing Coverage — 77 · Medium
- **151 test functions across 24 files** — auth/CSRF, HMAC ERP contract, intent cleaning,
  tool-loop bounds + role filtering, memory, confirmation lifecycle (incl. the overwrite
  regression), rate limiter, voice-note store, speech providers (fakes), the identity default-org
  fallback, a 7-scenario eval suite. **CI now runs `-race` with a 60% minimum-coverage gate**
  (`.github/workflows/ci.yml`). **Missing:** `main.go`'s `handleIncomingMessage` orchestration not
  directly tested; `internal/speech/providers` at 55.8% pulls the total down. · 1–2 days.

### 3.6 Documentation — 84 · Low
- Consolidated to **6 docs** with a [`README.md`](README.md) index; deploy/architecture docs are
  thorough and reconciled to post-audit reality; **repo-root `README.md` + `CONTRIBUTING.md` now
  present**. **Missing:** no doc lint / link-checker in CI. · 0.5 day.

### 3.7 Deployment Readiness — 80 · Low
- **Live on GCE** (`gateway-go`, `us-central1-a`, e2-micro, reserved static IP): hardened systemd
  unit installed and running as non-root `sawt`, `Restart=always`; **Caddy TLS reverse proxy
  validated end-to-end** against `sawt.osamamaalam.com` with an auto-issued Let's Encrypt cert;
  `/healthz`/`/readyz` checked post-deploy as a manual smoke test. **Missing:** no automated
  (scripted/CI) deploy — this run was manual; secrets still load from a static `.env` file rather
  than Secret Manager at boot; journald cap and swap file not yet added. · 1–2 days.

### 3.8 Windows Developer Experience — 80 · Low
- Documented for Win 11 + VS Code + PowerShell (winget, `launch.json`, `.env` loader,
  cross-compile); `cmd/harness` for UI iteration without WhatsApp; root `CONTRIBUTING.md` gives a
  quick-start. **Missing:** no `Makefile`/`Taskfile`; committed `.vscode/` and `scripts/` present
  but no task runner. · 0.5 day.

### 3.9 Production Readiness — 72 · Medium
- **ERP path never run live against a deployed `mshalia`** (front half — pairing/voice/LLM/STT/TTS —
  is live-confirmed; identity + ERP is verified only against **local** `mshalia`). The app **is now
  running in production** behind a validated TLS reverse proxy (`sawt.osamamaalam.com` via Caddy),
  no longer serving plain HTTP publicly. `/healthz`·`/readyz`·`/metrics`, graceful shutdown, and HTTP
  timeouts exist (audit) and were confirmed live (`{"db":true,"ready":true,"whatsapp":"connected"}`).
  **Missing:** secret rotation + vaulting (Secret Manager holds the values but boot still reads a
  static file); a full M9 live smoke run against the deployed `mshalia`. · 2–3 days (excl. live-run
  coordination).

### 3.10 Security — 82 · Low
- bcrypt + HMAC-signed sessions; double-submit CSRF; CSP + hardening headers; in-memory rate
  limiters; **login limiter now keys on the true TCP peer** (audit C5, not spoofable
  `X-Forwarded-For`); PII retention; panic reporting; parameterized SQL (sqlc); prompt-injection-safe
  typed tool calls; **ERP retry uses a deterministic idempotency key** (audit B3). **HSTS is now set**
  at the Caddy proxy (`Strict-Transport-Security: max-age=31536000; includeSubDomains`) and the app
  is only reachable over HTTPS. **Missing:** real credentials must still be rotated and the boot path
  switched to fetch from Secret Manager instead of a static file; no in-dashboard password-change
  flow. · 1 day.

### 3.11 Performance — 75 · Low
- Tuned for a 1 GB host: pgx pool `MaxConns=5`; GCS `ChunkSize=256 KB` + single upload worker;
  bounded limiter maps; ffmpeg via pipes; `GOMEMLIMIT`/`GOGC` guidance. **Missing:** no load/latency
  test; identity resolves every message (no cache). · 1–2 days.

### 3.12 Observability — 72 · Medium
- Per-message trace id = WhatsApp message id on every pipeline log line; chi `RequestID`+`Logger`;
  live SSE stream; `ERROR_WEBHOOK_URL` error/panic reporting; **`/healthz`·`/readyz`·`/metrics` and
  `log/slog` (text/JSON) now exist** (audit C3/C4); durable `tool_executions` step log (C2).
  **Missing:** metrics are minimal JSON (no Prometheus histograms); no uptime alerting beyond the
  webhook; no dashboards. · 1–2 days.

### 3.13 Reliability — 77 · Medium
- systemd `Restart=always`; voice-note exponential-backoff retry + on-disk spool; two-layer panic
  recovery; WhatsApp reconnect + debounced disconnect alert; **graceful HTTP shutdown, inbound
  dedup, atomic confirmation claim, ERP retry/backoff now landed** (audit B1/B2/B3/C1); the
  identity default-org fallback removes a hard dead-end for privileged actors. **Missing:**
  single-instance SPOF; no automated health-based restart/alert; resumable state machine / saga
  (§5, out of scope). · 1–2 days.

### 3.14 Scalability — 50 · Low (by design)
- Single-instance by design **[A3]** — process-local session secret, in-memory limiters, log
  broker, one WhatsApp socket. Horizontal scaling would need externalized state (Redis) + socket
  election — explicitly out of scope. · N/A.

### 3.15 Maintainability — 83 · Low
- Small feature-oriented packages; sqlc-generated queries; strong comments; broad unit tests;
  declarative agent/tool registration; **`golangci-lint` gate now enforced in CI**. **Missing:**
  schema idempotent but **not versioned** (a rename/drop needs a manual migration); no `CODEOWNERS`.
  · 1 day.

---

## 4. Technical Debt & Risk Register

| ID | Item | Type | Severity | Notes |
|---|---|---|---|---|
| D-1 | Real secrets loaded from a static file, not fetched from the vault | Security | High | All 12 values now exist in **GCP Secret Manager** and the VM's service account can read them (`roles/secretmanager.secretAccessor` + `cloud-platform` scope granted 2026-07-13), but `sawt.service` still reads a manually-installed `/opt/sawt/.env` (0600, `sawt:sawt`) rather than fetching live at boot. Was Critical; downgraded now that the file is properly permissioned, non-public, and the vault plumbing (IAM) is ready — closing it needs only an `ExecStartPre` fetch script. |
| D-6 | Full M9 not signed off | Functionality | High | ERP path now verified against **local** `mshalia` (M9-CHECKLIST §B); remaining: the same against a **deployed** `mshalia` + the real-WhatsApp voice round-trip. (Was Critical when the ERP half was wholly unverified.) |
| D-6a | Super-admin phone identity resolution returns no org | Functionality | ~~High~~ **Resolved (verified locally)** | Configurable default-org fallback for privileged roles (`internal/erp/fallback.go`, `DEFAULT_ORG_ID`, wired in `main.go` + `cmd/wfcli`; 8-case unit test). **Verified** via `wfcli`: `0546906905` → `org-demo` → `list_horses` executed against local `mshalia`. Remaining: same over real WhatsApp against the deployed `mshalia`; `DEFAULT_ORG_ID` must be set in the deploy env. |
| ~~D-5~~ | ~~`mshalia` gateway tools missing~~ | Functionality | **Resolved** | **All 39 tool ids are implemented in `mshalia`** (`lib/agent-gateway/tools/*`), match the Go client id-for-id, and returned live data through the signed gateway (M9-CHECKLIST, 2026-07-13). Remaining: confirm they're deployed to the production `mshalia`. |
| F-1 | Confirmation-gated writes can't self-correct bad model args | Functionality | Medium | Parked args are frozen at confirm time; a malformed tool call (e.g. `name` vs required `nameEn`/`nameAr`) fails on execute with no retry. Fix in `internal/workflow/confirmation.go` — see M9-CHECKLIST F-1. |
| D-7 | ~~No in-app TLS; `SECURE_COOKIE=true` needs a proxy~~ | Security | ~~High~~ **Resolved** | Caddy reverse-proxy validated end-to-end live on `sawt.osamamaalam.com` (Let's Encrypt cert, HSTS header, `X-Forwarded-*` set for the trusted-proxy login limiter); `SECURE_COOKIE=true` follows now that HTTPS is confirmed. |
| D-8 | Identity resolved every message (no cache) | Performance | Medium | Extra HMAC round-trip per inbound message. |
| D-9 | Schema not versioned (additive-only) | Maintainability | Medium | Rename/drop needs a manual migration story. |
| D-10 | `middleware.RealIP` trusts spoofable headers | Security | Medium | Safe only behind a trusted proxy; login limiter itself now keys on the true peer (C5). |
| D-11 | `main.go` handler orchestration untested | Testing | Medium | Coverage concentrated in workflow/web/erp/speech. |
| D-12 | No repo-root `README.md` / `CONTRIBUTING.md` | Documentation | ~~Low~~ **Resolved** | Repo-root `README.md` + `CONTRIBUTING.md` now present. |
| D-13 | No lint gate (`golangci-lint`) in CI | Code quality | ~~Low~~ **Resolved** | CI now runs `golangci-lint` (`.golangci.yml`, action pinned to v1.64.8) + a 60% coverage gate. |

> **Resolved & removed** (were D-2/D-3/D-4 in the pre-audit plan): no `/healthz`/`/metrics`, no HTTP
> server timeouts, no graceful shutdown — all now implemented (§5).

---

## 5. Agentic-Gateway Audit — Closed

A readiness audit (2026-07-10) of the system as an "Agentic Gateway" found 3 Blocker, 7 Critical,
and 7 Minor items. **All 17 are implemented and verified** (`go build`/`vet`/`test ./...` clean; CI
adds `-race`). The audit doc itself has been retired into this section.

**Verdict at audit time:** a secure-by-default **stateless request/response LLM router with a
single-slot human-in-the-loop confirmation gate** — hardened, but *not* a fully durable agentic
workflow engine.

**Blockers (fixed):**
- **B1 — Graceful shutdown + HTTP server timeouts + bounded fan-out.** `*http.Server` hoisted out
  of its goroutine; `ReadHeaderTimeout` 10s / `ReadTimeout` 30s / `IdleTimeout` 120s (`WriteTimeout`
  0 to keep SSE open); `MAX_INFLIGHT` (default 32) semaphore + `inflightWG` drained on SIGTERM.
- **B2 — Confirmation could double-execute financial writes.** New `ClaimPendingConfirmation`
  (`UPDATE … WHERE status='pending' RETURNING`) gives exactly one winner; claim → execute → delete.
- **B3 — ERP money-path had no retry/backoff/idempotency.** `doSignedPOST`: jittered exponential
  backoff (~200ms→3s, 3 attempts), re-signs per attempt, retries only transport/429/5xx; sends
  `x-swa-idempotency-key` (SHA-256 of the body, stable across retries) + `x-swa-trace-id`.

**Critical (fixed):** C1 inbound dedup (`processed_messages`); C2 durable `tool_executions` step
log; C3 `/healthz`·`/readyz`·`/metrics`; C4 `log/slog` (text or `LOG_FORMAT=json`); C5 login limiter
on the true TCP peer; C6 per-message 120 s deadline; C7 `requestConfirmation` refuses to overwrite a
live pending confirmation.

**Minor (fixed):** M1 sanitized client-facing errors; M2 seeded admin password to stderr only;
M3 dummy-bcrypt timing equalization; M4 summarizer on the app-lifetime context + trace propagation;
M5 removed the redundant hardcoded-8 history truncation; M6 `config` threaded into the handler;
M7 mutex-guarded `Client` write.

**Integration note for the `mshalia` team:** the header names `x-swa-idempotency-key` and
`x-swa-trace-id` must match what the gateway reads, and the gateway **must dedup writes on the
idempotency key** — that dedup is what makes `CallTool` retries safe for financial writes. See
[`mshalia-side.md`](mshalia-side.md) §1.

**Still out of scope** (larger design work, not in the audit's lists — the path from "router +
confirmation gate" to a fully durable workflow engine): a **resumable state machine**,
**saga/compensation** for partial multi-tool failures, and **deterministic replay**. If iteration N
of the 4-iteration loop fails after earlier side-effecting tools ran, there is no rollback beyond
the durable `tool_executions` record.

---

## 6. Prioritized Roadmap

Ordered by production-blocking priority. Priority ∈ {P0, P1, P2, P3}; effort in Go-dev-days.

### Phase 1 — Critical Blockers (go-live gating)

- **1a. Rotate & vault all production secrets** — P0 · 0.5 day remaining. All 12 values now live in
  **GCP Secret Manager**, and the VM SA has `secretmanager.secretAccessor` + the `cloud-platform`
  scope (granted 2026-07-13). **Remaining:** replace the static `/opt/sawt/.env` with an
  `ExecStartPre` fetch script that pulls from Secret Manager at boot, then rotate the values that
  were ever written to a file on disk (Neon password, `ADMIN_PASSWORD`, `SESSION_SECRET`, API keys).
  *No Go changes.* **DoD:** no secret in a repo-adjacent or static VM file; old credentials revoked.
- **1b. Validate the TLS reverse-proxy runbook end-to-end** — ✅ **Done (2026-07-13).** Caddy stood
  up per [`DEPLOYMENT.md`](DEPLOYMENT.md) §13.7 on `sawt.osamamaalam.com` (static IP
  `34.31.194.71`); Let's Encrypt cert issued and confirmed live over HTTPS
  (`curl https://sawt.osamamaalam.com/healthz` → `200 {"status":"ok"}`); HSTS header present;
  `X-Forwarded-For`/`X-Forwarded-Proto` set for the trusted-proxy login limiter. `SECURE_COOKIE=true`
  follows now that HTTPS is confirmed.

### Phase 2 — Core Functionality Completion

- **2a. M9 live verification (partial — complete the ERP path)** — P0 · 2–3 days
  (coordination-bound). A partial run is done (log below); identity resolution (D-6a) is now fixed
  and verified locally via `cmd/wfcli`. Remaining: point at a deployed `mshalia` with all 39 tools
  live, and run the 7 eval scenarios (`internal/workflow/eval_test.go`) as real voice + text
  WhatsApp conversations with `DEFAULT_ORG_ID` set. **DoD:** each scenario replies correctly;
  operations writes go through confirmation; failures triaged and fixed.

  **M9 partial live run — log (folded in from the former `docs/M9-VERIFICATION.md`).** Recorded
  against real services, a paired WhatsApp device, live LLM/STT/TTS, and the `mshalia` ERP.

  | # | Area | Result | Notes |
  |---|---|---|---|
  | 1 | WhatsApp connection & session transport | **PASS** | QR generated via `/dashboard/whatsapp`, scanned on a physical phone; reconnect + remote-logout recovery (`RecreateClient()`) validated. |
  | 2 | Identity resolution & fallback | **FAIL → FIXED** | On the WhatsApp run, super-admin `0546906905` did **not** resolve to an org. Fixed by the `DEFAULT_ORG_ID` fallback (D-6a) and **re-verified via `cmd/wfcli`**: `0546906905` now maps to `org-demo`. |
  | 3 | LLM reasoning loop | **PASS** | NIM `meta/llama-3.1-70b-instruct` primary; intent routing worked (e.g. greetings → `other` → general chat). |
  | 4 | Voice pipeline (STT/TTS) | **PASS** | Arabic voice notes sent **and** received; Groq Whisper STT + Wavenet/MMS/local TTS; ffmpeg Ogg/Opus granule-seek fix (`internal/audio/audio.go:45`) and dynamic waveform (`main.go:770`) verified on-device. |
  | 5 | ERP tool execution (operations) | **PASS (local)** | Full path verified against local `mshalia` on 2026-07-13 (M9-CHECKLIST): read (`list_horses`) + confirmation-gated write (`register_horse`, count 2 → 3, independently read back). All 39 `mshalia` tools exist and match the Go client id-for-id — the earlier "`404`" belief was wrong. Not yet run over real WhatsApp against a **deployed** `mshalia`. |

  > **Net:** the transport + speech + reasoning half is live-validated; the identity + ERP half is
  > not. A prior standalone `M9-VERIFICATION.md` claimed a full end-to-end PASS (incl. identity
  > resolution) — that was inaccurate and has been superseded by this log.
- **2b. `mshalia`-side gateway tools — BUILT & verified locally** ✅ (was P1, external). All **39
  gateway tool ids across 6 agents** are implemented in `mshalia` (`lib/agent-gateway/tools/*`),
  match the Go client id-for-id, enforce `min-role` + `PermissionScope` server-side, dedup writes on
  the idempotency key, and returned live data through the signed gateway (M9-CHECKLIST, 2026-07-13).
  **Remaining:** (i) confirm the tools are deployed to the **production** `mshalia`; (ii) the
  `client`-role phone `identity/resolve` path needs a Firestore composite index (LOCAL-TESTING §8);
  (iii) a formal `mshalia-agent-gateway-reference.md` + contract-test vectors (mshalia-side §7)
  would be nice-to-have but the live calls already prove parity.
- **2c. Identity-resolution cache/TTL** — P2 · 1 day. Small in-memory phone→identity cache with a
  short TTL in `internal/erp/client.go`, invalidated on error.
- **2d. SAR amount thresholds within the `high` tier** — P2 · 1–2 days. Extend
  `internal/workflow/confirmation.go` + `config/config.go` so financial writes above a configurable
  SAR threshold get stricter routing (future manager approval).

### Phase 3 — Production Hardening

- **3a. Prometheus-style metrics + uptime alerting** — P2 · 2 days. Upgrade `/metrics` from minimal
  JSON to expvar/Prometheus (message counts, provider fallbacks, tool latencies, voice-note
  uploaded/failed); wire a GCP uptime check + alert on `/healthz`.
- **3b. Bind listener to loopback + self-host front-end assets** — P3 · 1 day. Bind to `127.0.0.1`
  (proxy-only); self-host HTMX + the web font so `script-src`/`font-src` drop to `'self'`.
- **3c. Automated backup + DR drill** — P2 · 1 day. Scheduled `pg_dump` of Neon to a separate bucket
  (over Neon PITR); rehearse a restore against a Neon branch.

### Phase 4 — Performance & Nice-to-Have

- **4a. Load & latency testing on a real e2-micro** — P3 · 1–2 days. Concurrent voice/text; measure
  RSS under `GOMEMLIMIT=750MiB`, GC, ffmpeg spikes, round-trip latency, provider fallbacks.
- **4b. Provider latency budgets & HTTP client reuse** — P3 · 1–2 days.
- **4c. `README.md` + `CONTRIBUTING.md`** (repo root) — P2 · 1 day.
- **4d. CI coverage gate + `golangci-lint`** — P2 · 0.5 day.
- **4e. Versioned-migration convention** — P3 · 1–2 days.
- **4f. Committed dev tooling** (`scripts/Load-DotEnv.ps1`, `.vscode/`, task runner) — P3 · 0.5 day.
- **4g. Branded TTS voice decision** — P3 · Product. An explicit ruling: generic cascade vs. a
  branded clone (Habibi/SILMA infeasible in 1 GB — see §7 Deferred).
- **4h. Allow-list "who gets any reply" mode** — P3 · 1 day.

---

## 7. Feature Backlog (Dashboard/Observability Epics)

These epics (formerly `BACKLOG.md`) are thin handlers/templates over data, orchestrators, and state
that **already exist**. Estimates are engineer-hours. Recommended order **H → O → S → T** (health
first; everything reads from it). Each carries the standard DoD: code complete · reviewed · tested.

> **Origin:** the original 42-feature roadmap was written against the old three-runtime design
> (Next.js dashboard + Python LangGraph backend + separate Go gateway). This repo replaced all three
> with one Go binary. Migration tally: **24 Done** (verify, don't rebuild — the whole core loop:
> pair → configure agent → voice conversation → activity history), **10 Gap** (Epics H/O/S/T below),
> **5 Not Applicable** (monorepo tooling, the gateway⇄backend webhook + its `GATEWAY_SHARED_SECRET`,
> local model-folder opener, realtime-call tuning), **3 Deferred** (see below).

### Epic H — System Observability & Health
> A passive health aggregator + status surfaces (no paid-API probe traffic). Overlaps the `/healthz`
> that audit C3 already added — build the richer authenticated snapshot on top of it.

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| H1 | Health aggregator + `GET /api/health` | High | 4h | New `internal/health`; cached checks (WA `GetConnectionInfo`, `pool.Ping` cached ≥10s, ffmpeg boot result, per-provider `LastResult()`, `voicenotes.Store.Stats()`); one failing check degrades one field. |
| H2 | Status badge in the shell | High | 3h | Badge in `layout.html`; HTMX `hx-trigger="every 30s"` → `/api/health`; text+icon (not color-only). |
| H3 | Dashboard home widgets | Medium | 3h | `CountAgentsByStatus`; provider summary; quick-action cards; per-card fallback. |

### Epic O — Activity Observability
> Filters/pagination + live feed + pipeline-health aggregates over `wa_activity`. No new tables.

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| O1 | Activity filters + pagination | High | 4h | Keyset query (`chat`/`status`/`ts<$before ORDER BY ts DESC LIMIT`); filter controls + "load older" HTMX fragment. |
| O2 | Live activity feed (SSE) | High | 5h | `ActivityBroker` (sibling of `LogBroker`); publish at `CreateWaActivity`; `GET /api/events` SSE (auth); prepend+dedupe; subscriber cap ~10. |
| O3 | Pipeline-health aggregates | Medium | 4h | `avg(...) FILTER` / error-rate over 1h/24h/7d + previous period; 1-min cache; degraded thresholds. |
| O4 | Webhook-logs page | Medium | 2h | `GetWebhookLogs(limit)`; read-only, grouped by status class. |

### Epic S — Settings & Speech Operator Tools
> Settings UI + TTS/STT test panels + history pages + voice-note playback. Write paths already exist.

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| S1 | Global settings page | Medium | 4h | `GET/POST /dashboard/settings` (CSRF); speed clamp `[0.5,2.0]`; `assistant_agent_id` restricted to published agents; validate `bot_config` JSON. |
| S2 | TTS test panel | Medium | 3h | `POST /dashboard/speech/tts-test` (CSRF); reuse orchestrator + `WavToOpus`; 1k-char cap; write `tts_history`; per-IP rate limit. |
| S3 | STT test panel | Medium | 3h | `POST /dashboard/speech/stt-test` (CSRF, multipart, `MaxBytesReader` 10 MB); transcode + orchestrator (`ar`); write `stt_history`. |
| S4 | TTS/STT history pages | Low | 3h | Keyset pagination on `GetSttHistory`/`GetTtsHistory`; `GET /dashboard/speech`. |
| S5 | Voice-note playback | Low | 3h | `GET /dashboard/voice/{id}/url` → short-TTL V4 signed URL via `voiceStore.SignedURL`; only for `status='uploaded'`. |

### Epic T — Agent Testing

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| T1 | LLM test action (tool-less) | Medium | 4h | "Test prompt" on the workflow editor; `POST /dashboard/workflows/{id}/test` (CSRF); one LLM call with `tools=nil`; 30s timeout; per-IP rate limit; ephemeral (never persisted). |

### Deferred (decision-gated)
- **Voice cloning (Habibi/SILMA)** — infeasible in 1 GB RAM; the generic STT/TTS cascade is the
  deliberate substitute (product decision to confirm — §6 4g).
- **MCP tool-calling adapter** — Go-native declarative tool packs cover the need today.
- **Usage analytics / CSV export** — premature before live traffic exists to measure.

---

## 8. Go-Live Readiness Checklist

> **All P0 items must be checked before any production exposure.**

**Security & Secrets**
- [ ] All secrets rotated; boot path switched from static `.env` to a GCP Secret Manager fetch (P0) — values are in Secret Manager and the VM SA can read them; rotation + the `ExecStartPre` fetch script are still open
- [x] `SESSION_SECRET` set to a stable value
- [ ] `SECURE_COOKIE=true` (P0) — HTTPS is confirmed live, so this is safe to flip; not yet applied on the VM (still `false` from initial bring-up)
- [x] HSTS enforced at the proxy (Caddy `Strict-Transport-Security` header, confirmed live)
- [x] App reachable only via the reverse proxy; firewall does not expose `:8080` (only 80/443 open; `:8080` bound to `127.0.0.1` traffic via Caddy)
- [ ] Admin password rotated from any seeded/generated value (P1)

**Configuration**
- [x] `DATABASE_URL` points at the **production** Neon branch with `sslmode=require`
- [x] At least one STT/TTS key and one LLM key configured; `ALLOW_MISSING_FFMPEG=false` with ffmpeg installed
- [ ] `RETENTION_DAYS` set; GCS bucket lifecycle rule aligned (P1)

**Deployment**
- [x] Hardened systemd unit installed; `Restart=always`; runs as non-root `sawt` — live on `gateway-go` (us-central1-a)
- [x] TLS reverse proxy (Caddy) validated end-to-end — `https://sawt.osamamaalam.com`, Let's Encrypt cert confirmed
- [x] Graceful shutdown + HTTP server timeouts (audit B1)
- [x] Stale docs removed + consolidated (9 → 6; `docs/README.md` index)
- [ ] Swap file (1 GB) and journald log cap added (P1)

**Observability**
- [x] `/healthz` · `/readyz` · `/metrics` live (audit C3)
- [ ] `/healthz` wired to a GCP uptime check + alert (P2)
- [ ] `ERROR_WEBHOOK_URL` configured and tested (P1)
- [ ] journald capped; log retention bounded (P1)

**Verification (M9 — see [`M9-CHECKLIST.md`](M9-CHECKLIST.md))**
- [x] Real WhatsApp number paired; a voice round-trip (send/receive + STT + LLM + TTS) succeeds (P0)
- [x] Identity resolution + `DEFAULT_ORG` fallback succeeds for super-admin `966546906905` (P0)
- [x] Operations tool write executes only after explicit confirmation — verified vs **local** `mshalia` (P0)
- [ ] The 7 eval scenarios pass as live conversations against a **deployed** `mshalia` (P0)
- [ ] Real-WhatsApp voice round-trip of a confirmation-gated write (P0)

**External dependency (`mshalia`)**
- [x] All 39 gateway tools implemented with server-side role enforcement + idempotency dedup (verified live, local)
- [ ] Confirm the 39 tools are deployed to the **production** `mshalia`; `client`-role Firestore index added (P1)

**Backup & DR**
- [ ] Neon PITR confirmed; scheduled `pg_dump` running (P2)
- [ ] DR restore rehearsed against a Neon branch (P2)

---

## 9. Executive Summary

- **Current Project Ready:** **~79%** (weighted; §2).
- **Production-ready?** **Not yet, but closer — deployment + TLS are now live.** `sawt-gateway` is
  running in production on GCE behind a validated Caddy/Let's Encrypt TLS proxy
  (`https://sawt.osamamaalam.com`), `/healthz`/`/readyz` both green including a live WhatsApp
  connection. The M9 ERP path is verified end-to-end against a **local** `mshalia` (identity +
  `DEFAULT_ORG` fallback + classify + tool loop + confirmation-gated read **and** write —
  M9-CHECKLIST), and **all 39 `mshalia` tools exist and match the Go client** (the old "`404`" belief
  was wrong). What's left is ops/live, not code: sign off M9 against a **deployed** `mshalia` + a
  real-WhatsApp write, flip `SECURE_COOKIE=true`, and switch the boot path from a static `.env` to a
  live Secret Manager fetch. Do **not** expose write-capable flows publicly until those are complete.
- **Top blockers to production:**
  1. **M9 not signed off against a deployed `mshalia`** — the local run passes; confirm the tools are
     deployed and re-run the 7 eval scenarios + a real-WhatsApp write (D-6).
  2. **Secrets not fetched from the vault at boot** — values are in Secret Manager and IAM is wired,
     but `sawt.service` still reads a static file; needs an `ExecStartPre` fetch script + rotation (D-1).
  3. **`SECURE_COOKIE` not yet flipped to `true`** — safe to do now that TLS is confirmed live; a
     one-line `.env` change + service restart.
- **Resolved this round:** D-7 (no validated TLS path) — Caddy reverse proxy is live end-to-end with
  an auto-issued cert, HSTS, and a reserved static IP.
- **Secondary:** F-1 (confirmation-gated writes can't self-correct malformed model args) degrades
  the write UX — worth a fix in `internal/workflow/confirmation.go`.
- **Recommended next milestone:** flip `SECURE_COOKIE=true`, wire the Secret Manager boot fetch,
  confirm the `mshalia` tools are deployed, then run M9-CHECKLIST §B/§C against that deployment.
  These are ops/live/external — not more code in this repo — and they are what lifts Project Ready
  into the mid-80s.

> **Assumptions ([A]), stated not assumed-away:** in-app TLS is intentionally absent (terminate at a
> proxy); HSTS is a proxy responsibility; single-instance operation is deliberate **[A3]**; NIM is
> the primary LLM with an OpenAI-compatible fallback **[A4]**. Any "verified" item was confirmed in
> code; any "missing" item was confirmed absent.
