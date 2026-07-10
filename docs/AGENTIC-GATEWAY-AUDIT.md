# Agentic-Gateway Readiness Audit — `sawt-go`

**Repo:** `gobasim` (Go module `sawt-go`) · **Date:** 2026-07-10 · **Scope:** readiness as an "Agentic Gateway"–compatible system, plus the Blocker-severity fixes applied as a result.

`sawt-go` is a single-binary daemon that bridges **WhatsApp voice notes ⇄ an ERP backend ("mshalia")**. Inbound audio is transcribed (STT), routed by an LLM to a per-agent tool set, executed against the ERP via a signed HTTP gateway, and answered back as text/voice.

All findings were verified by reading source directly. Citations use `path:line`.

---

## Implementation status

The three **Blocker** items were implemented (see [Remediation](#remediation--blockers-implemented)). The **Critical** and **Minor** items are documented for triage and were **not** changed.

> Note: the Blocker changes were not compiled/tested locally (no toolchain run against this repo was performed at implementation time). Validate with CI before merging:
> ```
> go build ./... && go vet ./... && go test ./... -race
> ```

---

## Executive summary

**Verdict:** a cleanly organized, secure-by-default **stateless request/response LLM router with a single-slot human-in-the-loop confirmation gate** — *not yet* a durable agentic workflow engine.

**Strengths:** parameterized SQL everywhere (no SQLi), bcrypt + HMAC-signed sessions, HMAC-signed ERP calls, CSRF double-submit, CSP, correct mutex usage, and CI runs `go test ./... -race -cover` (`.github/workflows/ci.yml:26`). The durable-retry pattern an agentic gateway needs already exists in the voice-note subsystem (`internal/voicenotes/store.go`) — it simply had not been applied to the tool-execution (money) path.

**The three things that blocked enterprise go-live (now fixed):**
1. No graceful shutdown & no HTTP server timeouts — deploys killed in-flight ERP money writes; the dashboard was Slowloris-exposed.
2. The confirmation flow could double-execute financial writes — two concurrent "نعم" replies could both post money.
3. The ERP money-path had zero resilience — no retry/backoff/idempotency.

### Agentic-capability scorecard

| Capability a production agentic gateway needs | Status | Evidence |
|---|---|---|
| Declarative per-agent tool-calling + OpenAI-schema | **EXISTS** | `internal/workflow/tools.go:10-357` |
| Per-agent tool scoping + RBAC | **EXISTS** | `engine.go:362-372`, `tools.go:360-436` |
| Human-in-the-loop confirmation (durable; single-slot) | **PARTIAL** | `confirmation.go`, `schema.sql` |
| Conversation memory + rolling summarization (bounded) | **EXISTS** | `internal/workflow/memory.go:31-171` |
| `context.Context` threading | **EXISTS** | `main.go`, `engine.go`, `erp/client.go` |
| Trace correlation | **PARTIAL** → improved (trace id now sent on ERP calls) | `internal/trace/trace.go`, `erp/client.go` |
| Tool-loop bounding (4 iters) | **EXISTS** | `engine.go:386/446` |
| Durable step/event log | **MISSING** | `engine.go:427`, `main.go` audit blob |
| Resumable state machine | **MISSING** | `engine.go:270-448` |
| Durable job queue + backpressure | **PARTIAL** (bounded fan-out added; no durable queue) | `main.go` |
| Inbound idempotency / dedup | **MISSING** (no dedup on `evt.Info.ID`) | — |
| Concurrency-safe confirmation execution | **FIXED** (atomic claim) | `confirmation.go`, `ClaimPendingConfirmation` |
| Retry / backoff on ERP | **FIXED** | `erp/client.go` `doSignedPOST` |
| Partial-failure recovery / compensation / saga | **MISSING** | `engine.go:390-392` |
| Per-request deadline | **MISSING** (only per-HTTP timeouts) | `main.go` |
| Deterministic replay | **MISSING** | `engine.go:389` |

---

## Phase 1 — ERP entry-point map

Two inbound network surfaces, one primary outbound ERP integration. The daemon exposes **no inbound HTTP API for the ERP** — WhatsApp is the only path that drives ERP calls; the chi HTTP server is an operator dashboard only.

**Inbound**
- **WhatsApp socket (the pipeline trigger).** `main.go` registers a whatsmeow event handler; every `*events.Message` is dispatched to `handleIncomingMessage`. `internal/whatsmeow/client.go` holds only connection/send primitives.
- **Dashboard HTTP (chi) on `:$PORT`.** Signed-cookie sessions (`web/auth.go`) + CSRF (`web/csrf.go`) + CSP/security headers (`web/server.go`). ~25 operator routes; **none call the ERP.** The only outbound routes are operator→WhatsApp `send-text` and `send-voice`.

**Outbound / ERP boundary** — `internal/erp/client.go` is the *only* module that talks to the ERP. Two methods, both `POST`+JSON, both HMAC-SHA256 signed (`x-swa-timestamp` + `x-swa-signature`):

| Method | Endpoint | Timeout | Notes |
|---|---|---|---|
| `ResolveIdentity` | `POST {MSHALIA_API_URL}/api/agent/v1/identity/resolve` | 10s | phone → `Identity{uid,role,orgIds}` |
| `CallTool` | `POST {MSHALIA_API_URL}/api/agent/v1/tools/{toolID}` | 15s | transport errors wrapped as `{ok:false,code:NETWORK_ERROR}` |

`MSHALIA_API_URL` defaults to plaintext `http://localhost:3001`; `AGENT_GATEWAY_SECRET` empty disables all ERP calls (returns `UNCONFIGURED`). Only 3 non-test call sites: identity resolve (`main.go`), low-risk tool inline (`engine.go`), confirmed medium/high-risk tool (`confirmation.go`).

**Secondary outbound:** LLM chat-completions cascade NIM→OpenAI (`engine.go`, 30s); STT cascade Groq→HF→Google→local whisper; TTS cascade Google→HF→gTTS; GCS voice archival (`internal/voicenotes/gcs.go`); error webhook (`internal/monitor/monitor.go`).

**Async layers (no external broker):** per-message goroutine (now bounded + drained); in-memory inbound rate limiter 8/min/chat; durable Postgres-backed pending-confirmation "queue"; background rolling-summary goroutine; durable voice-note upload worker w/ spool + backoff; daily retention purge; SSE log broker.

**End-to-end trace (voice note → ERP → reply):** receive → filter self/group → rate-limit → opt-in gate (unknown numbers auto-created *disabled*) → download+archive+`OggToWav`+STT → **ERP #1 `ResolveIdentity`** → load memory → `Execute` (classify intent → tool loop) → for each tool: `low` → **ERP #2 `CallTool` inline**; `medium/high` → park pending confirmation, ask user; next "نعم" → **deferred `CallTool`** → persist turns (+bg summary) → TTS → send voice/text → audit rows.

---

## Phase 2 — Agentic capability & gaps

**Strong:** declarative `agentSpec` bundles persona + allowed tools; orthogonal **role gate** (`toolMinRole`) and **risk gate** (`toolRisk`) both fail *safe* (unknown tool → role `manager`, risk `medium`). Confirmation captures full acting context so a confirmed action survives a restart. Memory is bounded (per-agent turn cap ≤20; rolling ≤150-word summary over a `summarized_through` cursor).

**Remaining gaps:**
- **No durable step/event log.** Tool outcomes live only in in-memory `state.ToolResults`, flushed once as a best-effort, write-only `wa_activity.tool_calls` JSON blob. A crash mid-loop persists nothing.
- **No resumable state / no saga.** If iteration N of the 4-iteration loop fails after earlier side-effecting tools ran, the loop returns an error with no rollback, compensation, or durable record.
- **No inbound idempotency.** Nothing keys on `evt.Info.ID`; WhatsApp's at-least-once redelivery can re-run classification/tools.
- **Single-slot confirmation.** `pending_confirmations` PK is `chat_id` (`ON CONFLICT DO UPDATE`) — a second risky request silently overwrites the first.
- **No per-request deadline** — the pipeline runs on the app-lifetime context; only individual HTTP calls are bounded.
- The engine applies a **second, redundant** history truncation (hardcoded `8`) that overrides the memory subsystem's per-agent `max_history`.

---

## Phase 3 — Production readiness

**Concurrency & races.** Mutex usage is correct (`ratelimit`, `whatsmeow`, `LogBroker`); voice-store worker has clean lifecycle + `atomic` counters; CI runs `-race`. Fixed: bounded fan-out + drain on shutdown (B1) and the confirmation execution race (B2). Remaining minor: `WhatsAppManager.Client` written outside the mutex (`client.go:135`), safe only by boot ordering.

**Resilience.** Every outbound HTTP call has an explicit timeout, with graceful-degradation cascades (LLM/STT/TTS) and a resilient voice-upload worker. Fixed: ERP retry/backoff/idempotency (B3) and HTTP-server timeouts + graceful `Shutdown` (B1).

**Security & validation.** No SQLi surface (sqlc parameterized throughout); bcrypt; HMAC sessions w/ `hmac.Equal`; CSRF constant-time; CSP + `X-Frame-Options: DENY` + `nosniff`; `HttpOnly`/`Secure`/`SameSite=Lax` cookies; no secrets committed; fail-fast if `SECURE_COOKIE` set without `SESSION_SECRET`. Remaining: login throttle keys on spoofable `X-Forwarded-For`; raw DB error strings returned to operator clients; seeded admin one-time password written to the log/SSE stream; bcrypt-timing user enumeration + no per-account lockout / session rotation.

**Observability.** A real post-hoc audit trail exists (`wa_activity` per message: transcript, reply, `stt_ms/llm_ms/tts_ms/total_ms`, status, `tool_calls`). Improved: trace id now propagated to the ERP via `x-swa-trace-id`. Remaining: no `/healthz`·`/readyz`·`/metrics`; stdlib `log` only (no structured logging/levels).

---

## Findings ranked by severity

### 🔴 Blocker — fixed

- **B1 — No graceful shutdown + no HTTP server timeouts + unbounded goroutines.** `main.go`. The `*http.Server` was scoped inside its goroutine (unreachable for `Shutdown`); SIGTERM returned immediately, killing in-flight `record_payment`/`record_expense` handlers; no `Read/Write/Idle/ReadHeaderTimeout`.
- **B2 — Confirmation flow could double-execute financial writes.** `confirmation.go`. `GetPendingConfirmation` → `DeletePendingConfirmation` → `CallTool` were three non-transactional steps; two concurrent affirmations could both read the row before either deleted and both post the payment. Delete-before-execute also lost a confirmed action on crash.
- **B3 — ERP money-path had no retry/backoff/idempotency.** `erp/client.go`. A single transient error failed a payment; the idempotency key was LLM-invented, so a naive retry could double-post.

### 🟠 Critical — open (recommended next milestone)

- **C1** No inbound dedup on `evt.Info.ID` → at-least-once redelivery re-runs the pipeline.
- **C2** No durable step/event log → multi-tool failures after side effects are unrecoverable; the audit blob is best-effort write-only.
- **C3** No `/healthz` / `/readyz` / `/metrics`.
- **C4** Stdlib `log` only (no structured logging/levels). *(Trace-id propagation on ERP calls is addressed by B3.)*
- **C5** Login rate-limiter keyed on spoofable `X-Forwarded-For` (`web/server.go`).
- **C6** No per-request/workflow deadline; app-lifetime ctx.
- **C7** Single-slot pending confirmation silently overwrites (`schema.sql`).

> C1 (inbound dedup) and C2 (durable step log) can reuse the durable-queue-with-retry pattern already proven in `internal/voicenotes/store.go`.

### 🟡 Minor — open

- **M1** Raw DB error strings to operator clients (`web/server.go`).
- **M2** Seeded admin password logged to SSE (`main.go`).
- **M3** bcrypt-timing user enumeration + no lockout/session rotation (`web/auth.go`).
- **M4** Detached summarizer on `context.Background()` drops trace/shutdown (`memory.go`).
- **M5** Contradictory history truncation (`engine.go` hardcoded 8 vs memory limit).
- **M6** `config.LoadConfig()` re-read per message (`main.go`).
- **M7** `Client` field written outside mutex (`internal/whatsmeow/client.go:135`).

---

## Remediation — Blockers implemented

### B1 — Graceful shutdown, server timeouts, bounded fan-out
Files: `config/config.go`, `main.go`.
- New `MaxInflight` config (`MAX_INFLIGHT`, default 32; guarded to ≥1 to avoid an unbuffered-channel deadlock).
- Hoisted the `*http.Server` out of its goroutine and set `ReadHeaderTimeout` (10s), `ReadTimeout` (30s), `IdleTimeout` (120s). `WriteTimeout` is left 0 so the `/api/logs` SSE stream stays open.
- Every message handler is tracked by `inflightWG` and bounded by `inflightSem` (acquired *inside* the goroutine so the whatsmeow event loop is never blocked; releases on `ctx.Done()` during shutdown).
- Signal handler: `server.Shutdown(5s)` → force `Close` fallback → `Client.Disconnect()` → drain `inflightWG` (25s bound) → `cancel()` → deferred `pool.Close()`.

### B2 — Atomic confirmation execution
Files: `schema.sql`, `query.sql`, `database/confirmations.sql.go`, `database/querier.go`, `internal/workflow/confirmation.go`, `internal/workflow/confirmation_test.go`.
- Added `status` (`'pending'`/`'executing'`) and `claimed_at` columns (with idempotent `ALTER TABLE … ADD COLUMN IF NOT EXISTS` so existing deployments migrate on boot).
- New `ClaimPendingConfirmation`: `UPDATE pending_confirmations SET status='executing', claimed_at=NOW() WHERE chat_id=$1 AND status='pending' RETURNING …`. Exactly one of two concurrent resolvers matches `status='pending'`; the loser gets no row.
- `resolvePendingConfirmation` now claims → executes → **then** deletes (terminal), so a crash mid-execution leaves a visible `executing` row instead of silently dropping the action.
- The test fake models Postgres's single-winner claim, keeping the existing confirmation tests valid.
- *sqlc is not installed in the dev environment, so the generated file was hand-edited to match `query.sql`. Re-running `sqlc generate` later is compatible.*

### B3 — ERP resilience: retry/backoff + deterministic idempotency + trace header
Files: `internal/erp/client.go`, `internal/workflow/confirmation.go`.
- New `doSignedPOST`: jittered exponential backoff (~200ms→3s, up to 3 attempts), re-signs each attempt with a fresh timestamp, retries only on transport errors and 429/5xx, and respects the caller's `ctx` deadline.
- Sends `x-swa-idempotency-key` (SHA-256 of the exact request body → stable across retries) and `x-swa-trace-id` (from the trace context) on every ERP request.
- `ResolveIdentity` (idempotent read) and `CallTool` both route through it; `CallTool` still surfaces exhausted failures as a `NETWORK_ERROR` tool result rather than aborting the turn.
- The confirmed-write path replaces the model-invented `idempotencyKey` in tool args with a deterministic value (`chat_id + tool_id + args`), but only when the tool already declares that field — never adding it to tools that don't.

**Integration note for the mshalia gateway team:** the header names (`x-swa-idempotency-key`, `x-swa-trace-id`) must match what the gateway reads, and the gateway must dedup writes on the idempotency key — that dedup is what makes `CallTool` retries safe for financial writes.

---

## Verification

Run from the repo root:
1. `go build ./...` and `go vet ./...` — must stay clean.
2. `go test ./... -race -cover` — existing suite (incl. `internal/workflow/confirmation_test.go`, `internal/erp/client_test.go`) must pass under the race detector.
3. **B2 (recommended new test):** fire two simultaneous affirmations at `resolvePendingConfirmation` for the same `chat_id` against a stub ERP counting `CallTool` invocations — assert exactly one execution, under `-race`.
4. **B3 (recommended new test):** stub ERP HTTP server returning 503 then 200 — assert `ResolveIdentity` retries and succeeds; assert the same idempotency key is sent on each retry; assert `ctx` cancellation aborts promptly.
5. **B1 (manual):** start the daemon, open the dashboard + an `/api/logs` SSE stream, send SIGTERM mid-request — confirm `server.Shutdown` drains, the in-flight handler completes, the process exits within the shutdown deadline, and the SSE connection isn't severed by a write timeout; confirm a slow/partial request is cut by `ReadHeaderTimeout`.
