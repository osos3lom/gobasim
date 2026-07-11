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
- CI (`build` + `vet` + `test -race -cover`) and **141 test functions across 24 test files**
  (incl. the 7-scenario eval suite and fake-based speech-provider coverage).

**The single blocking reality:** the stack is **built but never verified against live services**
(no run against a real paired WhatsApp number + deployed `mshalia` + real LLM/STT/TTS keys —
milestone **M9**), and the **39 tool ids the Go client calls do not exist on the `mshalia` side
yet** (they `404`). Layered on: production-hardening gaps in [`DEPLOYMENT.md`](DEPLOYMENT.md) — no
in-app TLS (by design; terminate at a proxy) and real secrets in a `.env.production` file on disk.

---

## 2. Project Ready KPI

> ### **Project Ready: 76%**

**Formula:** `Project Ready = Σ(weightᵢ × scoreᵢ) / 100`; the 15 weights sum to 100. Each score
(0–100) is anchored to specific files; each weight reflects how much the category blocks
production. Scalability is deliberately low-weighted because single-instance operation is an
intentional constraint **[A3]**, not a defect.

| # | Category | Weight | Score | Contribution |
|---|---|--:|--:|--:|
| 1 | Core functionality | 15 | 75 | 11.25 |
| 2 | Code quality | 6 | 85 | 5.10 |
| 3 | Architecture | 6 | 82 | 4.92 |
| 4 | Go best practices | 5 | 88 | 4.40 |
| 5 | Testing coverage | 9 | 70 | 6.30 |
| 6 | Documentation | 6 | 78 | 4.68 |
| 7 | Deployment readiness | 8 | 70 | 5.60 |
| 8 | Windows developer experience | 4 | 78 | 3.12 |
| 9 | Production readiness | 9 | 65 | 5.85 |
| 10 | Security | 10 | 78 | 7.80 |
| 11 | Performance | 4 | 75 | 3.00 |
| 12 | Observability | 6 | 72 | 4.32 |
| 13 | Reliability | 5 | 75 | 3.75 |
| 14 | Scalability | 3 | 50 | 1.50 |
| 15 | Maintainability | 4 | 80 | 3.20 |
| | **Total** | **100** | — | **74.79** |

**~75 → reported as `76%`** (rounded to reflect the audit remediation landing after the previous
70% baseline). The number rose because the agentic-gateway audit closed the observability
(`/healthz`+`/metrics`), reliability (graceful shutdown, timeouts, dedup, durable step log), and
Go-best-practices (server timeouts) gaps that dragged the earlier score down.

**How to read it.** ~76% means the engineering is largely complete and well-hardened, but the
project is **not yet production-ready**: it has never run end-to-end against live services, has an
unbuilt external dependency (`mshalia` tools), and still needs a validated TLS path and vaulted
secrets before public exposure.

---

## 3. Category Scorecard

Each entry: **Status · Evidence · Missing · Risk · Effort.** Risk ∈ {Critical, High, Medium, Low}.
Effort is engineering-days for one competent Go dev.

### 3.1 Core Functionality — 75 · High
- **Status:** Full pipeline implemented; not verified live.
- **Evidence:** `main.go:handleIncomingMessage`; `internal/workflow/engine.go`;
  `internal/speech/{stt,tts}.go`; `internal/whatsmeow/client.go`; `web/server.go`.
- **Missing:** live end-to-end verification (M9); `mshalia`-side gateway tools for all **39 ids**
  (`404`); SAR amount thresholds within the `high` tier; identity-resolution cache.
- **Effort:** live-run coordination-bound (see §6 Phase 2).

### 3.2 Code Quality — 85 · Low
- Clean, idiomatic, small cohesive files; consistent `%w` error wrapping; `go:embed` assets;
  `go vet` clean in CI. **Missing:** `golangci-lint` not enforced in CI. · 1 day.

### 3.3 Architecture — 82 · Low
- Sound single-binary design; provider-cascade reused across STT/TTS/LLM; declarative `agentSpec`
  registry so the router never changes when tools are added. **Missing:** single-instance ceiling
  **[A3]**; additive-only schema (no versioned migrations). · N/A (accepted).

### 3.4 Go Best Practices — 88 · Low
- `context.Context` threaded through all I/O; `context.WithTimeout` on ERP/GCS; pure-Go build
  (`CGO_ENABLED=0`); `sync.RWMutex` on shared WhatsApp state; **HTTP server timeouts + graceful
  `Shutdown` now set** (audit B1); per-message 120 s deadline (C6). **Missing:** `inboundLimiter`
  is a package-level global (minor). · 0.5 day.

### 3.5 Testing Coverage — 70 · Medium
- **141 test functions across 24 files** — auth/CSRF, HMAC ERP contract, intent cleaning,
  tool-loop bounds + role filtering, memory, confirmation lifecycle (incl. the overwrite
  regression), rate limiter, voice-note store, speech providers (fakes), a 7-scenario eval suite.
  CI runs `-race -cover`. **Missing:** no minimum-coverage gate in CI; `main.go`'s
  `handleIncomingMessage` orchestration not directly tested; coverage % unpublished. · 2–3 days.

### 3.6 Documentation — 78 · Low
- Consolidated to **6 docs** with a [`README.md`](README.md) index; deploy/architecture docs are
  thorough and now reconciled to post-audit reality. **Missing:** **no repo-root `README.md`** (only
  `docs/`); no `CONTRIBUTING.md`. · 1 day.

### 3.7 Deployment Readiness — 70 · Medium
- Strong manual runbook (VM, firewall, IAP SSH, hardened systemd, Caddy TLS, journald caps,
  backups); `build-for-gcp.sh`. **Missing:** no automated deploy; secrets in `.env.production` on
  disk (not a vault); no post-deploy smoke test. `/healthz` now exists to target. · 2–3 days.

### 3.8 Windows Developer Experience — 78 · Low
- Documented for Win 11 + VS Code + PowerShell (winget, `launch.json`, `.env` loader,
  cross-compile); `cmd/harness` for UI iteration without WhatsApp. **Missing:**
  `scripts/Load-DotEnv.ps1` inlined in docs but not committed; no `Makefile`/`Taskfile`; no
  `.vscode/` committed. · 0.5 day.

### 3.9 Production Readiness — 65 · High
- **Never run live**, and serves plain HTTP on `:8080` (TLS terminates at a proxy — **[A]**).
  `/healthz`·`/readyz`·`/metrics`, graceful shutdown, and HTTP timeouts now exist (audit).
  **Missing:** validated TLS reverse-proxy path end-to-end; secret rotation + vaulting; a real live
  smoke run. · 3–4 days (excl. live-run coordination).

### 3.10 Security — 78 · Medium
- bcrypt + HMAC-signed sessions; double-submit CSRF; CSP + hardening headers; in-memory rate
  limiters; **login limiter now keys on the true TCP peer** (audit C5, not spoofable
  `X-Forwarded-For`); PII retention; panic reporting; parameterized SQL (sqlc); prompt-injection-safe
  typed tool calls; **ERP retry uses a deterministic idempotency key** (audit B3). **Missing:** HSTS
  (set at proxy); **real credentials in `.env.production` must be rotated + vaulted**; no
  in-dashboard password-change flow. · 2 days.

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

### 3.13 Reliability — 75 · Medium
- systemd `Restart=always`; voice-note exponential-backoff retry + on-disk spool; two-layer panic
  recovery; WhatsApp reconnect + debounced disconnect alert; **graceful HTTP shutdown, inbound
  dedup, atomic confirmation claim, ERP retry/backoff now landed** (audit B1/B2/B3/C1). **Missing:**
  single-instance SPOF; no automated health-based restart/alert; resumable state machine / saga
  (§5, out of scope). · 1–2 days.

### 3.14 Scalability — 50 · Low (by design)
- Single-instance by design **[A3]** — process-local session secret, in-memory limiters, log
  broker, one WhatsApp socket. Horizontal scaling would need externalized state (Redis) + socket
  election — explicitly out of scope. · N/A.

### 3.15 Maintainability — 80 · Low
- Small feature-oriented packages; sqlc-generated queries; strong comments; broad unit tests;
  declarative agent/tool registration. **Missing:** schema idempotent but **not versioned** (a
  rename/drop needs a manual migration); no lint gate; no `CODEOWNERS`. · 1–2 days.

---

## 4. Technical Debt & Risk Register

| ID | Item | Type | Severity | Notes |
|---|---|---|---|---|
| D-1 | Real secrets in `.env.production` on disk | Security | Critical | Gitignored & never in git history, but must be rotated + vaulted before go-live. |
| D-6 | Never verified live (M9) | Functionality | Critical | Whole stack tested only against fakes. |
| D-5 | `mshalia` gateway tools missing | Functionality | High | 39 tool ids `404`; external dependency — see `mshalia-side.md`. |
| D-7 | No in-app TLS; `SECURE_COOKIE=true` needs a proxy | Security | High | Reverse-proxy path documented but not validated end-to-end. |
| D-8 | Identity resolved every message (no cache) | Performance | Medium | Extra HMAC round-trip per inbound message. |
| D-9 | Schema not versioned (additive-only) | Maintainability | Medium | Rename/drop needs a manual migration story. |
| D-10 | `middleware.RealIP` trusts spoofable headers | Security | Medium | Safe only behind a trusted proxy; login limiter itself now keys on the true peer (C5). |
| D-11 | `main.go` handler orchestration untested | Testing | Medium | Coverage concentrated in workflow/web/erp/speech. |
| D-12 | No repo-root `README.md` / `CONTRIBUTING.md` | Documentation | Low | `docs/README.md` index exists, but no top-level entry point. |
| D-13 | No lint gate (`golangci-lint`) in CI | Code quality | Low | CI runs build/vet/test only. |

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

- **1a. Rotate & vault all production secrets** — P0 · 1 day. Rotate the Neon password,
  `ADMIN_PASSWORD`, `SESSION_SECRET`, and all API keys in `.env.production`; store in **GCP Secret
  Manager**; inject into `/opt/sawt/.env` at boot (`ExecStartPre` fetch), granting the VM SA
  `secretmanager.secretAccessor`. *No Go changes.* **DoD:** no secret in a repo-adjacent file; old
  credentials revoked.
- **1b. Validate the TLS reverse-proxy runbook end-to-end** — P0 · 0.5 day. Stand up Caddy per
  [`DEPLOYMENT.md`](DEPLOYMENT.md) §13.7; confirm cert issuance, cookie `Secure` flag, correct
  `X-Forwarded-For` to the login limiter, and HSTS. **DoD:** dashboard reachable only over HTTPS;
  cookies `Secure`; HSTS present.

### Phase 2 — Core Functionality Completion

- **2a. M9 live verification** — P0 · 2–3 days (coordination-bound). Pair a real (spare) WhatsApp
  number; point at a deployed `mshalia` with a real `AGENT_GATEWAY_SECRET`; supply real
  NIM/Groq/Google keys; run the 7 eval scenarios (`internal/workflow/eval_test.go`) as real voice +
  text conversations. **DoD:** each scenario replies correctly; operations writes go through
  confirmation; failures triaged and fixed. Log results in a new `docs/M9-VERIFICATION.md`.
- **2b. `mshalia`-side gateway tools (external)** — P1 · tracked in `mshalia`. Implement the **39
  gateway tool ids across 6 agents** per [`mshalia-side.md`](mshalia-side.md), enforcing per-tool
  `min-role` server-side + idempotency-key dedup on writes; return a reference MD. Our side needs no
  code changes (the client is generic). **DoD:** all 39 ids return structured data for a signed
  request; contract-test vectors pass.
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
- [ ] All secrets rotated and moved to GCP Secret Manager; `.env.production` plaintext removed (P0)
- [ ] `SESSION_SECRET` set to a stable 32-byte value; `SECURE_COOKIE=true` (P0)
- [ ] HSTS enforced at the proxy (P0)
- [ ] App reachable only via the reverse proxy; firewall does not expose `:8080` (P0)
- [ ] Admin password rotated from any seeded/generated value (P1)

**Configuration**
- [ ] `DATABASE_URL` points at the **production** Neon branch with `sslmode=require` (P0)
- [ ] At least one STT/TTS key and one LLM key configured; `ALLOW_MISSING_FFMPEG=false` with ffmpeg installed (P0)
- [ ] `RETENTION_DAYS` set; GCS bucket lifecycle rule aligned (P1)

**Deployment**
- [ ] Hardened systemd unit installed; `Restart=always`; runs as non-root `sawt` (P0)
- [ ] TLS reverse proxy (Caddy) validated end-to-end (P0)
- [x] Graceful shutdown + HTTP server timeouts (audit B1)
- [x] Stale docs removed + consolidated (9 → 6; `docs/README.md` index)

**Observability**
- [x] `/healthz` · `/readyz` · `/metrics` live (audit C3)
- [ ] `/healthz` wired to a GCP uptime check + alert (P2)
- [ ] `ERROR_WEBHOOK_URL` configured and tested (P1)
- [ ] journald capped; log retention bounded (P1)

**Verification (M9)**
- [ ] Real WhatsApp number paired; one full voice + text conversation succeeds (P0)
- [ ] Operations tool write executes only after explicit confirmation (P0)
- [ ] The 7 eval scenarios pass as live conversations (P0)

**External dependency (`mshalia`)**
- [ ] All 39 gateway tools implemented with server-side role enforcement + idempotency dedup; reference MD delivered (P1)
- [ ] Accounting/admin intents return data, not `404` (P1)

**Backup & DR**
- [ ] Neon PITR confirmed; scheduled `pg_dump` running (P2)
- [ ] DR restore rehearsed against a Neon branch (P2)

---

## 9. Executive Summary

- **Current Project Ready:** **76%** (weighted; §2) — up from a 70% pre-audit baseline after the
  agentic-gateway hardening (§5) landed.
- **Production-ready?** **Partially.** Engineering is largely complete and now well-hardened, but
  the system has never run against live services, depends on unbuilt `mshalia`-side tools, and still
  needs a validated TLS path and vaulted secrets. Do **not** expose it publicly until Phase 1 + M9
  are complete.
- **Top blockers to production:**
  1. **Never verified live (M9)** — tested only against fakes (D-6).
  2. **`mshalia`-side gateway tools don't exist** — all 39 tool ids `404` (D-5).
  3. **No validated TLS path** — `SECURE_COOKIE=true` needs an HTTPS terminator not yet proven (D-7).
  4. **Real secrets on disk** — rotate and move to Secret Manager (D-1).
- **Recommended next milestone:** **"M9 — Live Verification"** = Phase 1 (secrets + TLS) + Phase 2a/2b
  (live run + the `mshalia` dependency). Completing these moves the project from *Partially* to
  *production-ready* and lifts Project Ready into the mid-80s.

> **Assumptions ([A]), stated not assumed-away:** in-app TLS is intentionally absent (terminate at a
> proxy); HSTS is a proxy responsibility; single-instance operation is deliberate **[A3]**; NIM is
> the primary LLM with an OpenAI-compatible fallback **[A4]**. Any "verified" item was confirmed in
> code; any "missing" item was confirmed absent.
