# Sawt Gateway — Implementation Plan & Production-Readiness Audit

> **Purpose.** This is a forward-looking roadmap and a production-readiness scorecard for
> `sawt-go` (the single binary `sawt-gateway`). It audits the codebase against
> [`DEPLOYMENT.md`](DEPLOYMENT.md), scores readiness with a weighted **Project Ready %** KPI,
> enumerates every remaining gap with priority and effort, and defines a **Go-Live Readiness
> Checklist**. It supersedes the previous historical plan (delivered Phases 0–6 are folded into
> the "current state" baseline below, not re-litigated).
>
> **Companion docs:** [`DEPLOYMENT.md`](DEPLOYMENT.md) (authoritative deploy/ops runbook) ·
> [`BLUEPRINT.md`](BLUEPRINT.md) (architecture & product intent) ·
> [`mshalia-side.md`](mshalia-side.md) (external ERP-gateway brief for the `mshalia` team).
>
> **Method:** correctness over optimism. Nothing is scored as "done" unless it is verified in
> the code. Where a capability is absent, it is stated as a gap — never assumed. Assumptions are
> called out explicitly with **[A]** tags.

---

## Table of Contents

1. [Overview & Current State](#1-overview--current-state)
2. [Project Ready KPI & Methodology](#2-project-ready-kpi--methodology)
3. [Category Scorecard](#3-category-scorecard)
4. [Inconsistencies Between Code and Docs](#4-inconsistencies-between-code-and-docs)
5. [Technical Debt & Risk Register](#5-technical-debt--risk-register)
6. [Prioritized Roadmap (Phases 1–5)](#6-prioritized-roadmap-phases-15)
7. [Improvement Recommendations](#7-improvement-recommendations)
8. [Go-Live Readiness Checklist](#8-go-live-readiness-checklist)
9. [Executive Summary](#9-executive-summary)

---

## 1. Overview & Current State

`sawt-gateway` is **one Go binary** that runs the whole platform in a single always-on process:
the WhatsApp socket (`internal/whatsmeow`), the STT/TTS speech cascades (`internal/speech`),
ffmpeg transcoding (`internal/audio`), the LLM intent-classification + bounded tool-calling loop
(`internal/workflow`), the HMAC-signed ERP client (`internal/erp`), conversation memory +
risk-gated confirmations (Postgres-backed), and the operator web dashboard (`web/`).

**What is built and code-complete** (verified in-repo):

- WhatsApp transport with Postgres device store, QR + phone-code pairing, reconnect
  (`internal/whatsmeow/client.go`).
- STT (4-provider cascade) and TTS (3-provider cascade), key-driven fallback (`internal/speech/*`).
- LLM reasoning: intent classification → per-agent tool loop bounded at 4 iterations, NIM →
  OpenAI-compatible fallback (`internal/workflow/engine.go`, `tools.go`).
- Cross-turn memory (last 8 turns + rolling summary) and confirmation flow for medium/high-risk
  tools with a 10-minute TTL (`internal/workflow/memory.go`, `confirmation.go`).
- Dashboard: bcrypt login, HMAC-signed sessions, CSRF, CSP + hardening headers, activity feed,
  contact/agent config, WhatsApp pairing, live SSE logs (`web/*`).
- PII retention job, error/panic webhook, per-message trace ids, voice-note archival to GCS
  (`main.go`, `internal/monitor`, `internal/trace`, `internal/voicenotes`).
- CI (`build` + `vet` + `test -race -cover`) and **75 test functions across 14 test files**.

**The single blocking reality:** the entire stack is **built but never verified against live
services** (no run against a real paired WhatsApp number + deployed `mshalia` + real LLM/STT/TTS
keys — milestone **M9**), and the **39 tool ids the Go client calls (across 6 agents) do not exist
on the `mshalia` side yet** (they will `404`). Layered on top are a handful of
production-hardening gaps surfaced by [`DEPLOYMENT.md`](DEPLOYMENT.md): no in-app TLS, no
`/healthz`, no graceful HTTP shutdown, no HTTP server timeouts, and real secrets sitting in a
`.env.production` file on disk.

---

## 2. Project Ready KPI & Methodology

> ### **Project Ready: 70%**

**Formula:** `Project Ready = Σ(weightᵢ × scoreᵢ) / 100`, where the 15 category weights sum to
100. Each `scoreᵢ` (0–100) is an evidence-based judgement anchored to specific files; each
weight reflects how much that category **blocks production** (core functionality, security,
production readiness, and testing carry the most weight; scalability is deliberately low because
single-instance operation is an intentional design constraint, not a defect).

| # | Category | Weight | Score | Contribution |
|---|---|--:|--:|--:|
| 1 | Core functionality | 15 | 75 | 11.25 |
| 2 | Code quality | 6 | 85 | 5.10 |
| 3 | Architecture | 6 | 82 | 4.92 |
| 4 | Go best practices | 5 | 80 | 4.00 |
| 5 | Testing coverage | 9 | 62 | 5.58 |
| 6 | Documentation | 6 | 70 | 4.20 |
| 7 | Deployment readiness | 8 | 68 | 5.44 |
| 8 | Windows developer experience | 4 | 78 | 3.12 |
| 9 | Production readiness | 9 | 55 | 4.95 |
| 10 | Security | 10 | 72 | 7.20 |
| 11 | Performance | 4 | 75 | 3.00 |
| 12 | Observability | 6 | 55 | 3.30 |
| 13 | Reliability | 5 | 62 | 3.10 |
| 14 | Scalability | 3 | 50 | 1.50 |
| 15 | Maintainability | 4 | 78 | 3.12 |
| | **Total** | **100** | — | **69.78** |

**69.78 → rounded to `70%`.**

**How to read this number.** 70% means the engineering is largely complete and of good quality,
but the project is **not yet production-ready**: it has never run end-to-end against live
services, it has an unbuilt external dependency (`mshalia` tools), and it is missing several
operational safety nets that must exist before exposing an always-on public service. The score
is intentionally conservative — a well-built system that has never been switched on in anger is
a 70%, not a 90%.

---

## 3. Category Scorecard

Each category lists: **Status · Evidence · Missing · Risk · Effort · Dependencies · Acceptance.**
Risk uses {Critical, High, Medium, Low}. Effort is engineering-days for a single competent Go dev.

### 3.1 Core Functionality — Score 75 · Risk: High

- **Status:** Full pipeline implemented and code-complete; not verified live.
- **Evidence:** `main.go:handleIncomingMessage` (download→STT→identity→workflow→TTS→send→audit);
  `internal/workflow/engine.go` (classify + tool loop); `internal/speech/{stt,tts}.go` cascades;
  `internal/whatsmeow/client.go`; `web/server.go` dashboard.
- **Missing:** live end-to-end verification (M9); `mshalia`-side gateway tools for all **39 ids
  across 6 agents** (`404` — see [`mshalia-side.md`](mshalia-side.md)); SAR amount thresholds within
  the `high` risk tier; identity-resolution cache (resolves fresh every message).
- **Dependencies:** live WhatsApp number, deployed `mshalia`, real LLM/STT/TTS keys.
- **Acceptance:** the 7 eval scenarios pass as real WhatsApp conversations (voice + text);
  operations tools mutate real ERP state through confirmation; accounting/admin tools return
  data instead of `404`.

### 3.2 Code Quality — Score 85 · Risk: Low

- **Status:** Clean, idiomatic, densely and accurately commented; small cohesive files.
- **Evidence:** consistent error wrapping (`fmt.Errorf("...: %w", err)`), narrow interfaces
  (`voicenotes.uploader`/`ledger`), `go:embed` for schema/templates/CSS, no file over ~800 lines.
  `go vet` is clean in CI.
- **Missing:** `staticcheck`/`golangci-lint` not enforced in CI; `config.LoadConfig()` is called
  a second time deep inside `handleIncomingMessage` (minor redundant work per message).
- **Risk/Effort:** Low · 1–2 days to add linters + hoist the duplicate config load.
- **Acceptance:** `golangci-lint run` clean and wired into CI.

### 3.3 Architecture — Score 82 · Risk: Low

- **Status:** Sound single-binary design with clear package boundaries and declarative agent
  registration.
- **Evidence:** provider-cascade pattern reused across STT/TTS/LLM; sqlc repository layer
  (`database/*`); `agentSpec` registry (`internal/workflow/tools.go`) so the router never changes
  when tools are added; nil-safe optional `voicenotes.Store`.
- **Missing:** single-instance is a hard architectural ceiling (**[A3]**); schema is
  additive-only (no versioned migrations); channel abstraction for a second transport is absent.
- **Risk/Effort:** Low · N/A (accepted constraints) — revisit only if multi-channel/multi-instance
  becomes a requirement.
- **Acceptance:** documented constraints; no accidental second instance possible in the deploy.

### 3.4 Go Best Practices — Score 80 · Risk: Medium

- **Status:** Strong idiomatic Go; a few server-hardening idioms missing.
- **Evidence:** `context.Context` threaded through all I/O; `context.WithTimeout` on ERP/GCS
  calls; pure-Go build (`CGO_ENABLED=0`); `sync.RWMutex` guarding shared WhatsApp state.
- **Missing:** the `http.Server` in `main.go` sets only `Addr`+`Handler` — **no `ReadHeaderTimeout`,
  `ReadTimeout`, `WriteTimeout`, or `IdleTimeout`** (Slowloris exposure); no graceful
  `server.Shutdown(ctx)`; `inboundLimiter` is a package-level global.
- **Risk/Effort:** Medium · 0.5 day.
- **Acceptance:** server has explicit timeouts and shuts down gracefully on `SIGTERM`.

### 3.5 Testing Coverage — Score 62 · Risk: High

- **Status:** Solid unit baseline on the logic-heavy packages; thin on integration/live paths.
- **Evidence:** **75 test functions across 14 `*_test.go` files** — auth cookies, CSRF, HMAC ERP
  contract, intent cleaning, tool-loop bounds, memory, confirmation lifecycle, rate limiter,
  voice-note store, a 7-scenario eval suite, and templates. CI runs `go test ./... -race -cover`.
- **Missing:** no minimum-coverage gate in CI; `internal/speech/*` untested; `main.go`'s
  `handleIncomingMessage` orchestration is not directly tested; no automated end-to-end / live
  smoke; coverage percentage is unpublished.
- **Risk/Effort:** High · 3–4 days (coverage gate + speech tests + a scripted handler test).
- **Acceptance:** CI publishes coverage and fails below an agreed floor (e.g. 60% overall, 80% on
  `workflow`/`web`/`erp`).

### 3.6 Documentation — Score 70 · Risk: Medium

- **Status:** Excellent deploy/architecture docs, now consolidated (13 → 7 files); still no
  repo-root entry-point.
- **Evidence:** [`DEPLOYMENT.md`](DEPLOYMENT.md) is comprehensive; [`BLUEPRINT.md`](BLUEPRINT.md)
  is thorough; a [`docs/README.md`](README.md) index now ties the set together; the stale
  three-runtime docs (GCP-GATEWAY-SETUP, WALKTHROUGH, SPRINT-01*, codebase-map) were removed and
  the roadmap/feature docs were consolidated into [`BACKLOG.md`](BACKLOG.md).
- **Missing:** **no `README.md` at the repo root** (only a `docs/` index); no `CONTRIBUTING.md`;
  `BLUEPRINT.md` test count and schema table are still out of date (see §4, I-2/I-3).
- **Risk/Effort:** Low–Medium · 1 day.
- **Acceptance:** a repo-root `README` exists; BLUEPRINT counts match reality.

### 3.7 Deployment Readiness — Score 68 · Risk: High

- **Status:** A strong manual runbook exists; no automation and a secrets-hygiene gap.
- **Evidence:** [`DEPLOYMENT.md`](DEPLOYMENT.md) covers VM, firewall, IAP SSH, hardened systemd,
  Caddy TLS, journald caps, backups; `build-for-gcp.sh` produces the linux/amd64 binary.
- **Missing:** no automated deploy (scp-by-hand); secrets live in `.env.production` on disk (not
  a vault); no `/healthz` for an uptime check to target; no smoke test post-deploy.
- **Risk/Effort:** High · 2–3 days.
- **Acceptance:** a documented, repeatable deploy that pulls secrets from Secret Manager and
  passes a post-deploy health probe.

### 3.8 Windows Developer Experience — Score 78 · Risk: Low

- **Status:** Well documented for Windows 11 + VS Code + PowerShell.
- **Evidence:** [`DEPLOYMENT.md`](DEPLOYMENT.md) §3/§7/§8 (winget installs, extensions,
  `launch.json`, PowerShell `.env` loader, cross-compile); `cmd/harness` for UI iteration without
  WhatsApp; pure-Go cross-compile from Windows.
- **Missing:** the referenced `scripts/Load-DotEnv.ps1` is inlined in the doc but not committed as
  a file; no `Makefile`/`Taskfile` for common commands; no `.vscode/` committed.
- **Risk/Effort:** Low · 0.5 day.
- **Acceptance:** committed helper script(s) and a task runner so a new dev is productive in minutes.

### 3.9 Production Readiness — Score 55 · Risk: Critical

- **Status:** Not production-ready — never run live, and missing operational safety nets.
- **Evidence:** app serves plain HTTP on `:8080` (`main.go`); `SECURE_COOKIE=true` requires an
  external HTTPS terminator that hasn't been validated end-to-end.
- **Missing:** in-app TLS (by design, needs proxy — **[A]**); graceful shutdown; HTTP timeouts;
  `/healthz`; secret rotation + vaulting; a real live smoke run.
- **Risk/Effort:** Critical · 3–4 days (excluding the live-run coordination itself).
- **Acceptance:** HTTPS enforced, health probe green, graceful restart with zero dropped requests,
  secrets vaulted, one successful live conversation recorded.

### 3.10 Security — Score 72 · Risk: High

- **Status:** Strong application-layer security; a few edge gaps and a secrets-hygiene issue.
- **Evidence:** bcrypt password hashing + HMAC-signed sessions (`web/auth.go`); double-submit CSRF
  (`web/csrf.go`); CSP + `X-Content-Type-Options`/`X-Frame-Options`/`Referrer-Policy`
  (`web/server.go:securityHeaders`); in-memory rate limiters (`internal/ratelimit`); PII retention
  (`main.go:runRetention`); panic reporting (`internal/monitor`); parameterized SQL via sqlc;
  prompt-injection-safe typed tool calls.
- **Missing:** no HSTS (must be set at the proxy); `middleware.RealIP` trusts spoofable
  `X-Forwarded-For` if the app is ever directly reachable; **real credentials in `.env.production`
  on disk must be rotated + vaulted**; no HTTP server timeouts; no in-dashboard password-change flow.
- **Risk/Effort:** High · 2 days (excluding proxy standup, covered in §3.9).
- **Acceptance:** secrets rotated + in Secret Manager; HSTS live; app reachable only via the proxy;
  server timeouts set.

### 3.11 Performance — Score 75 · Risk: Low

- **Status:** Deliberately tuned for a 1 GB host; unmeasured under real load.
- **Evidence:** pgx pool capped (`MaxConns=5`, `database/conn.go`); GCS writer `ChunkSize` dropped
  to 256 KB and a single upload worker (`internal/voicenotes`); bounded limiter maps; ffmpeg via
  pipes (no temp files); `GOMEMLIMIT`/`GOGC` guidance in `DEPLOYMENT.md` §12.
- **Missing:** no load/latency testing; no per-provider latency budget; identity resolves every
  message (no cache).
- **Risk/Effort:** Low · 1–2 days.
- **Acceptance:** a documented load test shows stable RSS under `GOMEMLIMIT` and acceptable
  message-round-trip latency on an e2-micro.

### 3.12 Observability — Score 55 · Risk: High

- **Status:** Good tracing/logging primitives; no metrics or health surface.
- **Evidence:** per-message trace id = WhatsApp message id on every pipeline log line
  (`internal/trace`); chi `RequestID`+`Logger`; live SSE log stream; `ERROR_WEBHOOK_URL`
  error/panic reporting with trace attached (`internal/monitor`).
- **Missing:** **no `/health`, `/healthz`, `/metrics`, expvar, or Prometheus endpoint** (verified
  absent across the codebase); no structured JSON logs (line-oriented `log.Printf`); no
  metrics/dashboards; no uptime alerting beyond the error webhook.
- **Risk/Effort:** High · 2–3 days.
- **Acceptance:** a health endpoint and a metrics endpoint exist; an uptime check + alert fire on
  outage; logs are optionally JSON for ingestion.

### 3.13 Reliability — Score 62 · Risk: High

- **Status:** Good crash-recovery and retry behavior; gaps in graceful lifecycle and self-healing.
- **Evidence:** systemd `Restart=always` (`DEPLOYMENT.md` §13.6); voice-note exponential-backoff
  retry + on-disk spool recovery; two-layer panic recovery (HTTP + per-message); WhatsApp reconnect
  and debounced disconnect alert.
- **Missing:** no graceful HTTP shutdown; no HTTP timeouts; single-instance SPOF; no automated
  health-based restart/alert; retention/summary goroutines have no supervised restart.
- **Risk/Effort:** High · 1–2 days (overlaps §3.4/§3.9).
- **Acceptance:** graceful shutdown verified; a health probe drives systemd/uptime restart.

### 3.14 Scalability — Score 50 · Risk: Low (by design)

- **Status:** Single-instance by design; a hard ceiling, intentionally accepted.
- **Evidence:** **[A3]** — process-local session secret, in-memory limiters, log broker, and one
  WhatsApp socket; `DEPLOYMENT.md` warns two instances break pairing.
- **Missing:** horizontal scaling would require externalizing session/limiter state (Redis) and a
  single-owner WhatsApp socket election — explicitly out of scope for the current product.
- **Risk/Effort:** Low · N/A (deferred).
- **Acceptance:** deploy guarantees exactly one instance (`max-instances=1` / one VM + one unit).

### 3.15 Maintainability — Score 78 · Risk: Low

- **Status:** Highly maintainable; one migration-strategy gap.
- **Evidence:** small feature-oriented packages; sqlc-generated queries from `query.sql`; strong
  comments; broad unit tests; declarative agent/tool registration.
- **Missing:** schema is idempotent but **not versioned** — a future rename/drop needs a manual
  migration story; no lint gate; no `CODEOWNERS`.
- **Risk/Effort:** Low · 1–2 days.
- **Acceptance:** a documented migration approach (even a numbered-SQL convention) and a lint gate.

---

## 4. Inconsistencies Between Code and Docs

| # | Inconsistency | Reality (verified) | Authoritative source | Action |
|---|---|---|---|---|
| I-1 | `docs/GCP-GATEWAY-SETUP.md` instructed `curl localhost:8080/health`, `git clone` + `go run main.go`, and referenced `GATEWAY_SHARED_SECRET` / `WEBHOOK_URL`. | **No `/health` endpoint exists**; deploy ships a prebuilt binary (no source on the VM); `GATEWAY_SHARED_SECRET`/`WEBHOOK_URL` are dead (old three-runtime design). | **`DEPLOYMENT.md`** | ✅ **Resolved** — deleted in the docs consolidation. |
| I-2 | `BLUEPRINT.md` §3 said "~40 unit tests" and listed 3 agents / 9 tables. | Actual: **75 test functions across 14 files**, **6 agents / 39 tools**, **14 tables**. | This plan / codebase | ✅ **Resolved** — BLUEPRINT §3/§5/§7 refreshed. |
| I-3 | `BLUEPRINT.md` §7 table lists only the original tables and says whatsmeow is untested. | Schema now also has `conversation_turns`, `conversation_state`, `pending_confirmations`, `wa_messages`, `wa_voice_notes`; `internal/whatsmeow` has tests. | `schema.sql` / codebase | Refresh BLUEPRINT §7 (Phase 5). |
| I-4 | `DEPLOYMENT.md` §16 defines liveness as "any 200/3xx from `/login`". | Correct — it explicitly acknowledges there is no health endpoint. | `DEPLOYMENT.md` (consistent) | Not a defect, but an operational weakness → add `/healthz` (Phase 1c). |
| I-5 | `DEPLOYMENT.md` Caddy config proxies `127.0.0.1:8080`, but the app binds `:8080` on all interfaces. | Consistent **only because** the GCP firewall never opens 8080; the app is not bound to loopback. | `DEPLOYMENT.md` + `main.go` | Bind the listener to `127.0.0.1` as defense-in-depth (Phase 3). |

---

## 5. Technical Debt & Risk Register

| ID | Item | Type | Severity | Notes |
|---|---|---|---|---|
| D-1 | Real secrets in `.env.production` on disk | Security | Critical | Correctly gitignored & never in git history, but must be rotated + vaulted before go-live. |
| D-2 | No `/healthz` / `/metrics` | Observability | High | Uptime checks hit an auth redirect; no metrics at all. |
| D-3 | No HTTP server timeouts | Reliability/Security | High | Slowloris/DoS exposure; trivial fix in `main.go`. |
| D-4 | No graceful `http.Server.Shutdown` | Reliability | High | In-flight dashboard requests dropped on restart. |
| D-5 | `mshalia` gateway tools missing | Functionality | High | 39 tool ids across 6 agents `404`; external dependency — see `mshalia-side.md`. |
| D-6 | Never verified live (M9) | Functionality | Critical | Whole stack tested only against fakes. |
| D-7 | No in-app TLS; `SECURE_COOKIE=true` needs a proxy | Security | High | Reverse-proxy path documented but not validated end-to-end. |
| D-8 | Identity resolved every message (no cache) | Performance | Medium | Extra HMAC round-trip per inbound message. |
| D-9 | Schema not versioned (additive-only) | Maintainability | Medium | Rename/drop needs a manual migration story. |
| D-10 | `middleware.RealIP` trusts spoofable headers | Security | Medium | Safe only behind a trusted proxy; must not be directly exposed. |
| D-11 | Speech packages untested; `main.go` handler untested | Testing | Medium | Coverage concentrated in workflow/web/erp. |
| D-12 | No repo-root `README.md` / `CONTRIBUTING.md` | Documentation | Low | Entry-point gap (docs consolidated; a `docs/README.md` index now exists, but not a top-level repo README). |
| D-13 | No lint gate (`golangci-lint`) in CI | Code quality | Low | CI runs build/vet/test only. |

---

## 6. Prioritized Roadmap (Phases 1–5)

Phases are ordered by production-blocking priority. Phase 1 is gating for any public go-live.
Effort is engineering-days for one Go dev. Priority ∈ {P0, P1, P2, P3}.

### Phase 1 — Critical Blockers (go-live gating)

#### 1a. Rotate & vault all production secrets
- **Objective:** eliminate on-disk plaintext credentials.
- **Description:** rotate the Neon password, `ADMIN_PASSWORD`, `SESSION_SECRET`, and all API keys
  currently in `.env.production`; store them in **GCP Secret Manager**; inject into `/opt/sawt/.env`
  at boot via an `ExecStartPre` fetch (or templating), granting the VM SA `secretmanager.secretAccessor`.
- **Priority:** P0 · **Impact:** removes a critical exposure · **Complexity:** Medium · **Time:** 1 day.
- **Files:** `DEPLOYMENT.md` (Secret Manager procedure), systemd unit; **no Go changes**.
- **Definition of Done:** no secret is stored in a repo-adjacent file; the running service reads
  secrets from Secret Manager; old credentials are revoked.

#### 1b. HTTP server timeouts + graceful shutdown
- **Objective:** close the Slowloris/DoS window and stop dropping in-flight requests on restart.
- **Description:** set `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` on the
  `http.Server` in `main.go`; on `SIGTERM`, call `server.Shutdown(ctx)` (bounded) **before**
  `waMgr.Client.Disconnect()`.
- **Priority:** P0 · **Impact:** reliability + DoS resilience · **Complexity:** Low · **Time:** 0.5 day.
- **Files:** `main.go`.
- **Definition of Done:** a slow-header client is timed out; `systemctl restart` drains in-flight
  requests; a unit/integration test asserts the timeouts are set.

#### 1c. Health endpoint (`/healthz`)
- **Objective:** give uptime checks and load balancers a real, cheap liveness/readiness signal.
- **Description:** add an **unauthenticated** `GET /healthz` that pings the DB (short timeout) and
  reports WhatsApp connection state as JSON (`{"ok":bool,"db":bool,"whatsapp":"connected|..."}`),
  outside the `RequireAuth` group.
- **Priority:** P0 · **Impact:** enables monitoring & restart automation · **Complexity:** Low ·
  **Time:** 0.5 day.
- **Files:** `web/server.go` (new route + handler), test in `web/server_test.go`.
- **Definition of Done:** `curl /healthz` returns 200 when healthy, 503 when the DB is unreachable;
  covered by a test; referenced from `DEPLOYMENT.md` §16.

#### 1d. Validate the TLS reverse-proxy runbook end-to-end
- **Objective:** prove HTTPS + `SECURE_COOKIE=true` works before go-live.
- **Description:** stand up Caddy per `DEPLOYMENT.md` §13.7 on a test VM/domain; confirm cert
  issuance, cookie `Secure` flag, correct `X-Forwarded-For` to the login limiter, and HSTS header.
- **Priority:** P0 · **Impact:** unblocks secure prod · **Complexity:** Medium · **Time:** 0.5 day.
- **Files:** none (ops); annotate `DEPLOYMENT.md` with the verified result.
- **Definition of Done:** dashboard reachable only over HTTPS; login works; cookies are `Secure`;
  HSTS present.

#### 1e. Docs consolidation — ✅ Done
- **Objective:** remove misleading deploy instructions (I-1) and de-duplicate the docs set.
- **Done:** deleted the stale three-runtime docs (`GCP-GATEWAY-SETUP.md`, `WALKTHROUGH.md`,
  `SPRINT-01*.md`, `codebase-map/SKILL.md`); folded `LOCAL-DEV-TESTING.md` into `DEPLOYMENT.md`;
  consolidated `NEXTJS-MIGRATION-PLAN.md` + `PHASE-A-AND-VOICE-STORAGE.md` into
  [`BACKLOG.md`](BACKLOG.md); added a [`docs/README.md`](README.md) index. Docs went 13 → 7.
- **Remaining follow-up (P2):** add a **repo-root** `README.md` + `CONTRIBUTING.md` and refresh the
  stale `BLUEPRINT.md` counts (I-2/I-3) — see Phase 5a/5c.
- **Definition of Done:** ✅ no stale deploy path remains; a fresh operator following the docs
  cannot hit dead instructions.

### Phase 2 — Core Functionality Completion

#### 2a. M9 live verification
- **Objective:** prove the whole pipeline works against real services.
- **Description:** pair a real (spare) WhatsApp number; point at a deployed `mshalia` with a real
  `AGENT_GATEWAY_SECRET`; supply real NIM/Groq/Google keys; run the 7 eval scenarios
  (`internal/workflow/eval_test.go`) as real voice + text conversations; capture transcripts.
- **Priority:** P0 · **Impact:** the single biggest readiness unblock · **Complexity:** High ·
  **Time:** 2–3 days (coordination-bound).
- **Files:** none (verification); log results in `BLUEPRINT.md` §3 / a new `docs/M9-VERIFICATION.md`.
- **Definition of Done:** each scenario produces a correct reply; operations writes go through
  confirmation; failures are triaged and fixed.

#### 2b. `mshalia`-side gateway tools (external dependency)
- **Objective:** make agent intents functional instead of `404`.
- **Description:** the `mshalia` team implements the **39 gateway tool ids across 6 agents** per
  [`mshalia-side.md`](mshalia-side.md) — enforcing the per-tool `min-role` server-side — and returns
  a reference MD; our side then adds no code (the client is generic) beyond confirming the schemas.
- **Priority:** P1 · **Impact:** unlocks all 6 agents (operations/accounting/administration/client/
  sales/breeding) · **Complexity:** External · **Time:** tracked in `mshalia`.
- **Files:** `internal/erp/client.go` (no change expected); `internal/workflow/tools.go` (schema
  reconciliation only).
- **Definition of Done:** all 39 ids return structured data for a signed request; contract-test
  vectors from their reference MD pass against our client.

#### 2c. Identity-resolution cache/TTL
- **Objective:** cut a per-message HMAC round-trip.
- **Description:** add a small in-memory cache (phone → identity) with a short TTL in
  `internal/erp/client.go` (or a thin wrapper), invalidated on error.
- **Priority:** P2 · **Impact:** latency + `mshalia` load · **Complexity:** Low · **Time:** 1 day.
- **Files:** `internal/erp/client.go`, test.
- **Definition of Done:** repeated messages from one number within the TTL resolve from cache;
  covered by a test.

#### 2d. SAR amount thresholds within the `high` risk tier
- **Objective:** differentiate confirmation strictness by amount, not just risk tier.
- **Description:** extend `internal/workflow/confirmation.go` so financial writes above a
  configurable SAR threshold get stricter handling (e.g. explicit amount read-back is already
  present; add threshold-driven routing hooks for future manager approval).
- **Priority:** P2 · **Impact:** financial safety · **Complexity:** Medium · **Time:** 1–2 days.
- **Files:** `internal/workflow/confirmation.go`, `config/config.go`, tests.
- **Definition of Done:** a configurable threshold changes confirmation behavior; unit-tested.

### Phase 3 — Production Hardening

#### 3a. Structured logging (JSON option)
- **Objective:** machine-ingestible logs for Cloud Logging.
- **Description:** adopt stdlib `log/slog` (or `rs/zerolog`, already an indirect dep) behind the
  existing log sink; keep the SSE broker and trace-id prefixing.
- **Priority:** P2 · **Impact:** observability · **Complexity:** Medium · **Time:** 1–2 days.
- **Files:** `internal/trace`, `internal/monitor`, `web/server.go` log wiring, `main.go`.
- **Definition of Done:** a flag/env toggles JSON logs carrying `trace_id`; SSE stream unaffected.

#### 3b. Metrics endpoint + uptime alerting
- **Objective:** quantitative visibility and outage alerts.
- **Description:** expose `/metrics` (expvar for a minimal footprint, or Prometheus) with message
  counts, provider fallbacks, tool-call latencies, voice-note uploaded/failed (the store already
  tracks counters via `Stats()`); wire a GCP uptime check + alert on `/healthz`.
- **Priority:** P2 · **Impact:** observability/reliability · **Complexity:** Medium · **Time:** 2 days.
- **Files:** `web/server.go`, `internal/monitor`, `internal/voicenotes` (expose Stats), `DEPLOYMENT.md`.
- **Definition of Done:** `/metrics` returns live counters; an alert fires on simulated outage.

#### 3c. Bind listener to loopback + self-host front-end assets
- **Objective:** defense-in-depth and a stricter CSP.
- **Description:** bind the HTTP server to `127.0.0.1` (proxy-only reachability); self-host HTMX +
  the web font so `script-src`/`font-src` can drop to `'self'`.
- **Priority:** P3 · **Impact:** security · **Complexity:** Low · **Time:** 1 day.
- **Files:** `main.go` (bind addr), `web/templates/layout.html`, `web/static/*`, `web/server.go` CSP.
- **Definition of Done:** app not reachable except via the proxy; CSP no longer allows `unpkg.com`.

#### 3d. Automated backup + DR drill
- **Objective:** provable recovery.
- **Description:** scheduled `pg_dump` of Neon to a separate bucket (belt-and-suspenders over
  Neon PITR); document and perform a DR drill against a Neon branch.
- **Priority:** P2 · **Impact:** recoverability · **Complexity:** Low · **Time:** 1 day.
- **Files:** `DEPLOYMENT.md` §17, a small backup script/cron.
- **Definition of Done:** a restore from backup is demonstrated on a scratch environment.

### Phase 4 — Performance Optimization

#### 4a. Load & latency testing on a real e2-micro
- **Objective:** validate the 1 GB tuning under real pressure.
- **Description:** drive concurrent voice/text messages; measure RSS under `GOMEMLIMIT=750MiB`,
  GC behavior, ffmpeg spikes, message round-trip latency, and provider fallbacks.
- **Priority:** P3 · **Impact:** confidence/capacity · **Complexity:** Medium · **Time:** 1–2 days.
- **Files:** a test harness/script; results into `DEPLOYMENT.md` §12.
- **Definition of Done:** documented numbers show stable RSS and acceptable latency; a tuning
  recommendation is recorded.

#### 4b. Provider latency budgets & connection reuse
- **Objective:** bound worst-case pipeline latency.
- **Description:** add per-provider timeouts/budgets in the STT/TTS/LLM cascades; confirm
  `http.Client` reuse (keep-alive) across calls.
- **Priority:** P3 · **Impact:** tail latency · **Complexity:** Medium · **Time:** 1–2 days.
- **Files:** `internal/speech/*`, `internal/workflow/engine.go`, `internal/erp/client.go`.
- **Definition of Done:** each external call has an explicit budget; clients are reused.

### Phase 5 — Nice-to-Have Improvements

- **5a. `README.md` + `CONTRIBUTING.md`** — P2 · Low · 1 day · repo root. DoD: a newcomer can build,
  run, and test from the README alone.
- **5b. CI coverage gate** — P2 · Low · 0.5 day · `.github/workflows/ci.yml`. DoD: CI fails below
  the agreed floor.
- **5c. Refresh `BLUEPRINT.md`** (I-2, I-3) — P3 · Low · 0.5 day. DoD: counts/tables match reality.
- **5d. Versioned-migration convention** — P3 · Medium · 1–2 days · `schema.sql`/`docs`. DoD: a
  documented approach for non-additive changes.
- **5e. Speech-package tests** — P3 · Medium · 1–2 days · `internal/speech/*`. DoD: cascade
  selection + fallback covered with fakes.
- **5f. Per-agent configurable `maxIterations`** — P3 · Low · 0.5 day · `internal/workflow`. DoD:
  loop bound is per-agent configurable.
- **5g. Branded TTS voice decision** — P3 · Product · N/A. DoD: an explicit product ruling
  (generic cascade vs. branded clone).
- **5h. Allow-list "who gets any reply" mode** — P3 · Low · 1 day · `main.go`. DoD: optional mode
  where only allow-listed numbers get any reply.
- **5i. Committed dev tooling** (`scripts/Load-DotEnv.ps1`, `.vscode/`, task runner) — P3 · Low ·
  0.5 day. DoD: committed and referenced by docs.

---

## 7. Improvement Recommendations

| Domain | Recommendation | Ties to |
|---|---|---|
| **Security** | Rotate + vault secrets (Secret Manager); add HSTS at the proxy; set HTTP timeouts; keep the app behind the proxy (RealIP trust); add an in-dashboard password-change flow. | D-1, D-3, D-7, D-10 |
| **Performance** | Load-test on a real e2-micro; add an identity cache; set per-provider latency budgets; reuse HTTP clients. | 4a, 4b, 2c |
| **e2-micro resource efficiency** | Keep `GOMEMLIMIT=750MiB`/`GOGC=50`; add a 1 GB swap; cap journald; GCS lifecycle rule aligned to `RETENTION_DAYS`; single upload worker (already done). | DEPLOYMENT §12 |
| **Logging** | Move to structured `slog`/`zerolog` JSON with `trace_id`; keep SSE broker + per-message trace ids. | 3a |
| **Monitoring** | Add `/healthz` + `/metrics`; GCP uptime check + alert; surface voice-note `Stats()`. | 1c, 3b |
| **Error handling** | Keep two-layer panic recovery; add graceful shutdown; ensure background goroutines (retention/summary) log-and-recover, not silently die. | 1b, 3.13 |
| **Config management** | Validate required env at boot (already partial); fail fast on missing prod values; centralize via Secret Manager; document every var (done in DEPLOYMENT §5). | 1a |
| **CI/CD** | Add `golangci-lint` + a coverage gate; optionally auto-build the linux/amd64 artifact on tag. | 5b, D-13 |
| **Backup** | Scheduled `pg_dump` beside Neon PITR; GCS object versioning for voice notes. | 3d |
| **Disaster recovery** | Documented + rehearsed restore against a Neon branch; VM is disposable (state in Postgres/GCS). | 3d, DEPLOYMENT §17 |
| **Maintainability** | Versioned-migration convention; lint gate; `README`/`CONTRIBUTING`; refresh stale docs. | 5a–5d |

---

## 8. Go-Live Readiness Checklist

> **All P0 items must be checked before any production exposure.**

**Security & Secrets**
- [ ] All secrets rotated and moved to GCP Secret Manager; `.env.production` plaintext removed from disk (P0)
- [ ] `SESSION_SECRET` set to a stable 32-byte value; `SECURE_COOKIE=true` (P0)
- [ ] HSTS enforced at the proxy; HTTP server timeouts set (P0)
- [ ] App reachable only via the reverse proxy (RealIP trust safe); firewall does not expose `:8080` (P0)
- [ ] Admin password rotated from any seeded/generated value (P1)

**Configuration**
- [ ] `DATABASE_URL` points at the **production** Neon branch with `sslmode=require` (P0)
- [ ] At least one STT/TTS key and one LLM key configured; `ALLOW_MISSING_FFMPEG=false` with ffmpeg installed (P0)
- [ ] `RETENTION_DAYS` set; GCS bucket lifecycle rule aligned (P1)

**Deployment**
- [ ] Hardened systemd unit installed; `Restart=always`; runs as non-root `sawt` (P0)
- [ ] TLS reverse proxy (Caddy) validated end-to-end (P0)
- [ ] Graceful shutdown verified on `systemctl restart` (P0)
- [x] Stale docs removed + consolidated (`GCP-GATEWAY-SETUP.md` retired; `docs/README.md` index added)

**Observability**
- [ ] `/healthz` live and wired to a GCP uptime check + alert (P0/P2)
- [ ] `ERROR_WEBHOOK_URL` configured and tested (P1)
- [ ] journald capped; log retention bounded (P1)
- [ ] `/metrics` exposed (P2)

**Verification (M9)**
- [ ] Real WhatsApp number paired; one full voice + text conversation succeeds (P0)
- [ ] Operations tool write executes only after explicit confirmation (P0)
- [ ] The 7 eval scenarios pass as live conversations (P0)

**External dependency (`mshalia`)**
- [ ] All 39 gateway tools (6 agents) implemented per `mshalia-side.md` with server-side role enforcement; reference MD delivered (P1)
- [ ] Accounting/admin intents return data, not `404` (P1)

**Backup & DR**
- [ ] Neon PITR confirmed; scheduled `pg_dump` running (P2)
- [ ] DR restore rehearsed against a Neon branch (P2)

---

## 9. Executive Summary

- **Current Project Ready:** **70%** (weighted; see §2).
- **Production-ready?** **Partially.** The engineering is largely complete and of good quality,
  but the system has never run against live services, depends on unbuilt `mshalia`-side tools, and
  lacks several operational safety nets (TLS validated, health endpoint, graceful shutdown, HTTP
  timeouts, vaulted secrets). Do **not** expose it publicly until Phase 1 + M9 are complete.

- **Top 5 blockers to production:**
  1. **Never verified live (M9)** — the whole stack is tested only against fakes (D-6).
  2. **`mshalia`-side gateway tools don't exist** — all 39 tool ids (6 agents) `404` (D-5).
  3. **No validated TLS path** — `SECURE_COOKIE=true` requires an HTTPS terminator not yet proven
     end-to-end (D-7).
  4. **Real secrets on disk** — must be rotated and moved to Secret Manager (D-1).
  5. **No `/healthz`, no graceful shutdown, no HTTP timeouts** — operational/DoS gaps for an
     always-on public service (D-2, D-3, D-4).

- **Highest-risk issues:** secrets exposure (Critical); observability blindness — no metrics/health
  (High); single-instance SPOF (accepted, but real); Slowloris/DoS via missing HTTP timeouts (High).

- **Recommended next milestone:** **"M9 — Live Verification & Production Hardening"** = Phase 1
  (critical blockers) + Phase 2a/2b (live run + the `mshalia` dependency). Completing these moves
  the project from *Partially* to *production-ready* and would lift Project Ready into the mid-80s.

> **Assumptions ([A]) stated, not assumed-away:** in-app TLS is intentionally absent (terminate at
> a proxy); HSTS is a proxy responsibility; single-instance operation is a deliberate constraint
> **[A3]**; NIM is the primary LLM with an OpenAI-compatible fallback **[A4]**. Any item marked
> "verified" was confirmed in the code; any "missing" item was confirmed absent — none are guessed.
