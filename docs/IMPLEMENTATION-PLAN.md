# Sawt (Go) — Implementation Plan

> Source: the 2026-07 codebase audit reflected in [`BLUEPRINT.md`](BLUEPRINT.md) §3/§9/§11 (current-state gap table, security, risks).
> Scope: this plan closes the gap between the current Go binary and a system safe to point at real WhatsApp traffic and a live `mshalia`. It does **not** cover `mshalia`-side work (Gateway policy enforcement, new tool implementations there) — that's tracked in `mshalia`, not here.
> Ordering principle: **fix what's cheap now and expensive later first.** Phase 0 and 1 are almost pure risk-reduction with no new user-facing behavior; do them before adding any more scope (M6 accounting/administration), otherwise every new feature gets built on top of the same missing safety net.

Each phase is independently shippable. Do not start a phase before the previous one's acceptance criteria are met, except Phase 0 and Phase 1 which can run concurrently (different files, no shared risk).

---

## Phase 0 — Critical security & hygiene fixes

**Why first:** the hardcoded admin credential is a live vulnerability the moment this binary runs anywhere reachable outside a laptop. The rest of this phase is cheap now, expensive to retrofit once more code depends on current behavior.

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P0-1 | Remove the hardcoded default admin seed. Replace with: on first boot, if `users` is empty, read `ADMIN_USERNAME`/`ADMIN_PASSWORD` from env if set; otherwise generate a random 16+ char password, bcrypt-hash it, print it once to stdout, and require it to be changed (or at minimum log it loudly and never repeat it in a future boot) | `main.go:60-75` | No plaintext password exists in source or git history going forward; a fresh boot with no env vars produces a usable, non-guessable credential that's only ever shown once |
| P0-2 | Delete the dead `GatewaySharedSecret` config field and `GATEWAY_SHARED_SECRET` env var — leftover from the pre-consolidation gateway↔backend split | `config/config.go:13,90` | `grep -r GatewaySharedSecret` returns nothing; `go build ./...` still passes |
| P0-3 | Add a `.gitignore` covering at minimum: compiled binaries (`*.exe`, `sawt-gateway`), `.env`, local whisper models (`models/`) | repo root | `git status` no longer shows `sawt-gateway.exe` as untracked-by-accident |
| P0-4 | Commit `go.sum` — it must be tracked for reproducible builds | repo root | `git status` shows `go.sum` tracked, not untracked |
| P0-5 | Add CSRF protection to all state-changing dashboard routes: `POST /login`, `POST /dashboard/workflows/update`, `POST /dashboard/whatsapp/pair-code`. A double-submit cookie token is sufficient for this dashboard's threat model — full session-bound CSRF middleware is overkill for a single-operator control panel, but *some* token check is not | `web/server.go`, `web/auth.go` | A POST to any of these routes without a valid CSRF token is rejected (403); the existing SSE log stream (`GET`) is unaffected |
| P0-6 | Add basic rate limiting on `POST /login` (e.g. per-IP token bucket, `github.com/didip/tollbooth` or a hand-rolled `golang.org/x/time/rate` limiter — no new heavy dependency needed) | `web/server.go` | 10+ rapid failed login attempts from one IP get throttled (429), not silently retried forever |
| P0-7 | Add per-WhatsApp-number throttling on the inbound message handler so one number can't hammer the LLM/ERP (simple in-memory sliding window keyed by `evt.Info.Chat.String()` is enough for a single-instance deploy) | `main.go: handleIncomingMessage` | A burst of messages from one number in a short window gets a "please slow down" reply instead of N parallel LLM+ERP calls |

**Acceptance for the whole phase:** `go build ./... && go vet ./...` clean, no plaintext credential anywhere in source, `git status` clean of accidental untracked build artifacts.

---

## Phase 1 — Test & CI baseline

**Why now, not later:** every phase after this one touches `engine.go` and `erp/client.go` — the two files that talk to the LLM and mutate real ERP state. Adding tests *after* Phase 2-5 land means testing against a moving target; adding them now gives every later phase a regression net for free.

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P1-1 | Unit tests for cookie signing/verification: valid cookie round-trips, tampered signature rejected, expired cookie rejected, malformed input rejected | new `web/auth_test.go` | `go test ./web/...` covers `GenerateCookieValue`/`VerifyCookieValue` happy path + the 3 failure modes above |
| P1-2 | Unit tests for HMAC signing in the ERP client: signature matches a known vector, `getSignedHeaders` errors when secret is empty | new `internal/erp/client_test.go` | `go test ./internal/erp/...` passes |
| P1-3 | Unit tests for `ClassifyIntent`'s response-cleaning logic and the tool-loop's iteration bound, using an injectable/mocked `chatCompletions` (extract the HTTP call behind a small interface so it's mockable without hitting the network) | `internal/workflow/engine.go` (refactor to inject the completion function), new `internal/workflow/engine_test.go` | Tests cover: valid intent strings, garbage LLM output falling back to `"operations"`, the `maxIterations` cutoff producing the fallback reply instead of looping forever |
| P1-4 | Unit tests for the audio transcode wrapper's error path (empty input) — no need to shell out to real ffmpeg in CI for the happy path, but the input-validation branch should be tested | new `internal/audio/audio_test.go` | `go test ./internal/audio/...` passes without requiring ffmpeg installed in CI for this specific test |
| P1-5 | CI workflow: `go build ./...`, `go vet ./...`, `go test ./... -race -cover` on every push/PR | new `.github/workflows/ci.yml` | A broken build or failing test blocks merge, visible in the PR checks |

**Acceptance for the whole phase:** CI is green on a clean checkout; `go test ./... -cover` reports non-zero coverage on every package touched above.

---

## Phase 2 — Conversation memory (prerequisite for Phase 3)

**Why before confirmation/approval:** a "confirm this risky action" flow requires the system to remember, on the *next* incoming message, that it's waiting for a yes/no about a specific pending action. Without any cross-turn memory (§3 of the blueprint — currently `main.go` builds a single-message `State` on every inbound event), there's nowhere to hang that pending state.

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P2-1 | Add a `conversation_turns` table (or reuse/extend `wa_activity`) keyed by `chat_id`, storing role + content + timestamp per turn, with an index on `(chat_id, ts DESC)` | `schema.sql`, `query.sql` (+ regenerate `database/query.sql.go` via sqlc) | New table exists after boot; a `ListRecentTurns(chatID, limit)` query returns the last N turns oldest-first |
| P2-2 | Load the last N turns (start with 6-8, matching the old windowed-history target) for `state.ChatID` **before** constructing `State.Messages`, instead of seeding it with only the current message | `main.go: handleIncomingMessage`, `internal/workflow/engine.go` | Sending a follow-up like "and schedule a vet visit for her" after a prior turn about a specific horse resolves the pronoun correctly (manual test, plus a unit test with a fixed message history asserting the right context is passed to `chatCompletions`) |
| P2-3 | Persist each turn (user message, assistant reply, and tool-call turns) after a successful `Execute()` call | `main.go`, `internal/workflow/engine.go` | After a multi-turn exchange, `conversation_turns` (or equivalent) contains every turn in order |
| P2-4 | Add a rolling-summary step once a thread exceeds ~20 turns: summarize older turns into one system-style message instead of dropping them silently | `internal/workflow/engine.go` | A long-running thread (20+ turns) still gets a coherent reply referencing early context, verified with a scripted test conversation |

**Acceptance for the whole phase:** a multi-message WhatsApp conversation (manual test against a real or sandboxed number) demonstrably carries context across turns; unit tests cover the history-loading and trimming logic without needing a live LLM call.

---

## Phase 3 — Confirmation / approval for risky operations (closes M4)

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P3-1 | Add a `risk` classification per tool (`low`/`medium`/`high`) mirroring the `mshalia`-side tool contract's `risk` field (§6 of the blueprint) — for now, hardcode `update_task_status` as `medium` and the 3 read-only tools as `low` | `internal/workflow/engine.go` | Tool definitions carry a risk tag the engine can branch on |
| P3-2 | Before executing a `medium`/`high` risk tool call, instead of calling `erpClient.CallTool` immediately, store a "pending confirmation" record (tool, args, chat ID, expiry) and reply asking the user to confirm | `internal/workflow/engine.go`, new pending-confirmation table or reuse `conversation_turns` state from Phase 2 | A request like "mark task X complete" gets a confirmation question back instead of an immediate ERP write |
| P3-3 | On the next inbound message, check for a pending confirmation for that `ChatID` first; a clear yes/affirmation executes the stored tool call, anything else (including silence past the expiry) cancels it | `main.go`, `internal/workflow/engine.go` | Replying "yes" executes the original pending action; replying with something else, or waiting past expiry, does not |
| P3-4 | Log every confirmation request/response/expiry to `wa_activity` (or the audit trail) so there's a record of what was asked and what was confirmed | `internal/workflow/engine.go` | Audit trail shows the full confirm/execute sequence, not just the final write |

**Acceptance for the whole phase:** `update_task_status` (and any future medium/high-risk tool) never executes without an explicit user confirmation in the immediately following message; this closes the M4 "confirmed ⛔" gap in the blueprint.

---

## Phase 4 — LLM provider fallback + observability

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P4-1 | Extract `chatCompletions` behind the same provider-cascade pattern already used for STT/TTS (`internal/speech/stt.go` is a good template); add at least one fallback provider (e.g. a second OpenAI-compatible endpoint) | `internal/workflow/engine.go`, possibly new `internal/workflow/providers/*` | If the primary LLM endpoint errors or times out, a configured fallback is attempted before giving up |
| P4-2 | Add a request/trace ID generated per inbound WhatsApp message and threaded through STT → LLM → tool calls → TTS → the `wa_activity` row and all log lines for that message | `main.go`, `internal/workflow/engine.go`, `internal/erp/client.go` | Given a `wa_activity.id`, every log line for that message's processing is grep-able by trace ID |
| P4-3 | Wire an error-monitoring integration (Sentry or equivalent) for unhandled panics and tool/LLM/ERP failures | `main.go` (recover handler), `web/server.go` (chi's `middleware.Recoverer` already catches HTTP panics — extend to report, not just recover) | A forced panic or ERP call failure shows up in the monitoring dashboard with the trace ID attached |

---

## Phase 5 — Accounting + Administration tool packs (closes M6)

**Do not start until Phase 0-3 are done** — new tool packs multiply the surface area of exactly the gaps those phases close (more state-changing operations with no confirmation flow otherwise, more code with no tests otherwise).

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P5-1 | Define the accounting tool set (mirrors `mshalia`'s `lib/api/invoices.ts` / `expenses.ts` / `payments.ts` / `accounting/postJournal.ts`) and an `executeAccounting` loop analogous to `executeOperations` | `internal/workflow/engine.go` | Accounting-classified intents run a real tool loop instead of the canned stub reply |
| P5-2 | Define the administration tool set (mirrors `lib/api/clients.ts` / `contracts.ts`) and an `executeAdministration` loop | `internal/workflow/engine.go` | Administration-classified intents run a real tool loop instead of the canned stub reply |
| P5-3 | Every new tool that writes data gets a `risk` tag and goes through the Phase 3 confirmation flow if `medium`/`high` (financial writes should default to `high`) | `internal/workflow/engine.go` | No new tool bypasses confirmation for money-moving operations |
| P5-4 | Corresponding `mshalia`-side Gateway tools exist and are called with the same HMAC contract — tracked in `mshalia`, referenced here only to confirm the Go side has nothing further to build once they exist | `internal/erp/client.go` (should need no changes — `CallTool` is already generic over `toolID`) | A new tool ID added on the `mshalia` side works from this repo with zero code changes, only a new `ToolDefinition` entry |

---

## Phase 6 — PII/retention + deployment hardening (closes M7)

| # | Task | Files | Acceptance criteria |
|---|---|---|---|
| P6-1 | Add a retention policy: purge raw audio (never stored today, good) and old `wa_activity`/`conversation_turns` transcripts after N days, keeping a redacted audit summary | new scheduled job or `main.go` startup ticker | Rows older than the configured retention window are purged or redacted on a schedule |
| P6-2 | Add a startup check that `ffmpeg` is on `PATH` and fail fast with a clear error instead of only failing on the first voice note | `main.go` or `internal/audio/audio.go` init | Missing `ffmpeg` is caught at boot, not on first user-facing voice message |
| P6-3 | Document the deploy target (GCE/Cloud Run, `min-instances=1` equivalent) and health-check endpoint for the single-instance assumption in `BLUEPRINT.md` §12 [A3] | new `docs/DEPLOYMENT.md` or extend `docs/GCP-GATEWAY-SETUP.md` | A runbook exists for deploying this binary as the always-on process it's assumed to be |
| P6-4 | Add a lightweight eval suite: a fixed set of scripted WhatsApp-style conversations (text-only, no live WhatsApp needed) run through `wfEngine.Execute` directly, asserting expected tool calls / replies | new `internal/workflow/eval_test.go` or a small CLI harness | Running the eval suite catches an obviously broken system prompt or tool schema before it reaches a real user |

---

## Summary table

| Phase | Closes | Depends on | Risk if skipped |
|---|---|---|---|
| 0 | 🛑 critical security defects | — | Live credential leak, brute-forceable login, CSRF |
| 1 | Test/CI baseline | — | Every later phase ships unverified |
| 2 | Conversation memory | 1 | Phase 3 has nowhere to hang pending-confirmation state |
| 3 | M4 (confirmation/approval) | 2 | Risky ERP writes keep executing without human sign-off |
| 4 | LLM fallback + observability | 1 | Single point of failure on reasoning; blind in production |
| 5 | M6 (accounting/administration) | 0-3 | New scope built on top of the same missing safety net |
| 6 | M7 (hardening) | 0-5 | No retention policy, no deploy runbook, no regression eval |
