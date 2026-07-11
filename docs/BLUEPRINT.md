# Sawt — Engineering Blueprint & Roadmap

> **WhatsApp voice as the primary UI for the `mshalia` ERP** — a domain-agnostic, tool-using AI operations assistant.
> This document is the **single source of truth**: target architecture, current-vs-target gap, and the phased roadmap.
> **Architecture note (2026-07):** the platform was originally designed as three runtimes (Go gateway / Python FastAPI+LangGraph backend / Next.js dashboard, see git history). It has since been **consolidated into a single Go binary** (`module sawt-go`, built as `sawt-gateway`) that owns the WhatsApp socket, the reasoning loop, speech, and the operator dashboard in one process. This document describes the **current Go implementation** — it supersedes the old three-runtime diagram.
> Companion docs: [`README.md`](README.md) (docs index) · [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) (readiness status, scorecard, roadmap + feature backlog) · [`DEPLOYMENT.md`](DEPLOYMENT.md) (deploy/ops runbook) · [`LOCAL-TESTING.md`](LOCAL-TESTING.md) (local test tiers) · [`mshalia-side.md`](mshalia-side.md) (external ERP-gateway brief).

---

## 1. Context & Goal

`mshalia` is a mature multi-vertical Next.js 16 + Firestore ERP (equine / agriculture / industrial / holding) with a real double-entry ledger. Its field users — grooms, stable managers, accountants — find forms high-friction; some have limited literacy. **Goal:** let them *speak* to the ERP over WhatsApp ("check Najm into stall A-12", "log a 1,200 SAR feed bill") and have an AI operations-manager understand, confirm when risky, execute the real ERP operation, and reply in Arabic text + voice — preserving the ERP's integrity, RBAC, audit, and multi-tenancy.

**Domain-agnostic:** the equine center is tenant #1; any ERP onboards by registering a new **tool pack** + prompts.

This goal is unchanged by the Go rewrite. What changed is *how* it's delivered.

---

## 2. Current Architecture (as implemented)

One Go binary, one process, one deploy artifact:

| Component | Path | Role |
|---|---|---|
| WhatsApp connection manager | `internal/whatsmeow/client.go` | Owns the whatsmeow socket, Postgres-backed device store (`sqlstore`), QR/pairing-code linking, connection state |
| Speech orchestrators | `internal/speech/{stt,tts}.go`, `internal/speech/providers/*` | Provider-cascade STT and TTS (see §8) |
| Audio transcoding | `internal/audio/audio.go` | ffmpeg-based OGG/Opus ↔ WAV conversion |
| Workflow / reasoning engine | `internal/workflow/engine.go` | Intent classification + tool-calling loop against the LLM (replaces the old LangGraph graph) |
| ERP Gateway client | `internal/erp/client.go` | HMAC-signed identity resolution + tool calls against `mshalia` |
| Web dashboard | `web/server.go`, `web/auth.go`, `web/templates/*` | Operator control plane: login, activity feed, contact/agent config, WhatsApp pairing, live log stream (SSE) |
| Database layer | `database/*.go`, `schema.sql`, `query.sql` | pgx pool + sqlc-generated queries against Neon/Postgres |
| Entry point | `main.go` | Wires everything together, handles each inbound WhatsApp event synchronously per-message |

### The pipeline (as it actually runs today)

```
WhatsApp voice/text
  │  whatsmeow socket (in-process, no network hop)
main.go: handleIncomingMessage()
  │  1. look up / auto-create wa_contacts row; drop if disabled or group
  │  2. if audio: download → ffmpeg OGG→WAV → STT cascade
  │  3. resolve actor identity: erpClient.ResolveIdentity(phone) → mshalia (HMAC POST)
  │  4. load conversation memory (last 8 turns + rolling summary) and call wfEngine.Execute()
  │     → pending confirmation? resolve it first (yes → execute parked tool; else cancel)
  │     → client-role identities skip classification → client self-service agent
  │     → else ClassifyIntent (1 LLM call) → route to operations | accounting | administration | sales | breeding | general chat
  │     → bounded 4-iteration tool-calling loop over that agent's tools (filtered by the actor's role);
  │       medium/high-risk calls park in pending_confirmations and ask the user first
  │  5. persist the exchange to conversation_turns (summary folds in background)
  │  6. if original message was audio: TTS cascade → ffmpeg WAV→Opus
  │  7. send reply (text or voice note) back over the same whatsmeow socket
  │  8. write one row to wa_activity (transcript, reply, timings, tool_calls) for the dashboard feed
  ▼
WhatsApp reply (text + voice)
                                    │ tool calls (HMAC-signed service secret + actingUserUid + orgId)
                                    ▼
                     mshalia /api/agent/v1/*  → lib/api/* → Firestore
```

**What this eliminates from the old design:** the gateway↔backend signed HTTP channel (`/webhook/inbound` ↔ `/send`, `GATEWAY_SHARED_SECRET`) no longer exists — there's nothing to forward to, it's the same process. That channel-contract section of the old blueprint is obsolete, and the dead `GATEWAY_SHARED_SECRET` config was removed in Phase 0.

**What's unchanged:** the *external* signed channel from this binary to `mshalia`'s ERP Agent Gateway (`x-swa-signature` / `x-swa-timestamp`, `HMAC-SHA256(secret, "{timestamp}.{rawBody}")`, secret = `AGENT_GATEWAY_SECRET`) — implemented identically in `internal/erp/client.go`.

**Env vars actually consumed** (`config/config.go` + `main.go`): `DATABASE_URL`, `PORT`, `AGENT_GATEWAY_SECRET`, `MSHALIA_API_URL`, `NIM_API_KEY`, `NIM_BASE_URL`, `NIM_MODEL`, `STT_PROVIDER`, `STT_MODEL`, `OPENAI_API_KEY`, `OPENAI_API_BASE`, `LLM_FALLBACK_MODEL`, `HF_API_KEY`, `TTS_PROVIDER`, `TTS_MODEL`, `PAIR_PHONE_NUMBER`, `SESSION_SECRET`, `SECURE_COOKIE`, `GROQ_API_KEY`, `GCP_API_KEY`, `ADMIN_USERNAME`, `ADMIN_PASSWORD`, `ERROR_WEBHOOK_URL`, `RETENTION_DAYS`, `VOICE_STORAGE_BUCKET`, `VOICE_STORAGE_PREFIX`, `VOICE_SPOOL_DIR`, `ALLOW_MISSING_FFMPEG`, `MAX_INFLIGHT`, `LOG_FORMAT`, and `GOOGLE_APPLICATION_CREDENTIALS` (GCS ADC in dev). See [`DEPLOYMENT.md`](DEPLOYMENT.md) §5 for the full reference table. (The old `GATEWAY_SHARED_SECRET` is gone.)

---

## 3. Current State → Target (the gap)

Legend: ✅ built · ⚠️ partial · ⛔ missing · 🛑 critical defect (must fix before any real deployment).

*Updated after executing [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) Phases 0–6 and the agentic-gateway audit remediation (all Blocker/Critical/Minor items closed — see IMPLEMENTATION-PLAN §5). "✅ (unverified live)" = built, unit-tested where testable, but not yet exercised against a real paired WhatsApp number + live `mshalia` + real LLM/STT/TTS credentials in this environment.*

| Capability | State | Gap to close |
|---|---|---|
| WhatsApp transport | ✅ `internal/whatsmeow/client.go` — Postgres device store, QR + pairing-code linking, reconnect | ban/health monitoring & alerting |
| In-process pipeline (replaces gateway↔backend channel) | ✅ single binary; dead `GATEWAY_SHARED_SECRET` config removed (Phase 0) | — |
| STT | ✅ `internal/speech/stt.go` — 4-provider cascade: Groq Whisper → Hugging Face → Google Cloud STT → local whisper.cpp | Arabic dialect accuracy unverified; never tested live through the tool-calling loop |
| TTS | ✅ `internal/speech/tts.go` — 3-provider cascade: Google Cloud TTS → Hugging Face Spaces → local gTTS | **deviates from the original design intent** (Habibi/SILMA voice-clone sidecar) — generic TTS voices instead of a branded clone voice; a deliberate call the team should confirm |
| Audio transcode | ✅ `internal/audio/audio.go` — ffmpeg OGG/Opus ↔ WAV; checked at boot, fail-fast (`ALLOW_MISSING_FFMPEG=true` for text-only) (Phase 6) | — |
| LLM reasoning + tool-calling | ✅ (unverified live) `internal/workflow/engine.go` — provider cascade NIM → OpenAI-compatible fallback (`OPENAI_API_KEY` + `LLM_FALLBACK_MODEL`), real intent classification, bounded 4-iteration tool loop (Phase 4) | structured-output response node (plain text only, as designed); live verification |
| Conversation memory across turns | ✅ (unverified live) `internal/workflow/memory.go` — replays the assigned agent's `max_history` turns (default 8, clamped `[1,20]`) from `conversation_turns` per chat; rolling summary folds threads >20 turns via background LLM call; summary injected into system prompts | live verification of pronoun resolution quality |
| ERP Agent Gateway client (this repo's side) | ✅ (unverified live) `internal/erp/client.go` — HMAC-signed `identity/resolve` + `tools/{toolId}`, generic over tool id | server-side enforcement (`PermissionScope`, the **39 tool ids across 6 agents**, amount thresholds) lives in `mshalia`, out of this repo's scope — see [`mshalia-side.md`](mshalia-side.md) |
| Identity resolution | ✅ (unverified live) resolves on every inbound message via `ResolveIdentity` | no cache/TTL — resolves fresh every message (same gap as before the rewrite) |
| Confirmation / approval for risky or financial ops (M4) | ✅ built (Phase 3) — per-tool risk tags (unknown → medium), medium/high calls park in `pending_confirmations` (10-min TTL), affirmation executes / anything else cancels / expiry cancels silently, full audit trail in `wa_activity.tool_calls` | SAR amount thresholds within a risk tier (all financial writes are simply `high` today); live verification |
| Operations agent | ✅ Go-side built — **15 tools**: horses, care plans, tasks, stalls (list/availability/assign), register/check-in/check-out, incidents, vet appointments, treatment plans | mshalia-side tools for the write ids (assign_stall, register_horse, check_in/out, report_incident, book_vet_appointment, record_treatment_plan) pending — see [`mshalia-side.md`](mshalia-side.md) §4.1 |
| Accounting agent | ✅ Go-side built — `list_invoices`, `get_invoice`, `record_expense` (high), `record_payment` (high); financial writes carry a required `idempotencyKey` | **mshalia-side gateway tools do not exist yet** — calls 404 until built |
| Administration agent | ✅ Go-side built — `list_clients`, `get_client`, `list_contracts`, `get_contract` | mshalia-side tools pending |
| Client self-service agent | ✅ Go-side built — 6 owner-scoped read tools (`list_my_horses`/`_invoices`/`_contracts`, `get_my_horse`/`_balance`/`_statement`); `client`-role identities route here directly | mshalia-side tools pending |
| Sales / CRM agent | ✅ Go-side built — 5 tools (`list_available_horses`/`_stalls`, `list_packages`, `book_tour`, `submit_inquiry`) | mshalia-side tools pending |
| Breeding agent | ✅ Go-side built — 5 tools (`list_breeding_stock`, `book_breeding`, `get_pregnancy_status`, `list_foals`, `recommend_bloodline`) | mshalia-side tools pending |
| Role-based tool access (RBAC) | ✅ Go-side built — per-tool `toolMinRole` gating in `executeToolLoop`, fail-closed (`client < viewer < manager < admin < super_admin`) | UX-side only; `mshalia` must enforce the same minimum server-side. If a resolved role isn't in the hierarchy the actor sees no tools (M9 verification risk) |
| Dashboard | ✅ `web/server.go` — session-authed login, activity feed, contact/agent config, WhatsApp pairing, SSE logs; CSRF-protected POSTs (Phase 0) | — |
| "One WhatsApp stack" (old M5 goal) | ✅ true by construction — one in-process `whatsmeow` client, dashboard controls it directly | — |
| Database schema | ✅ `schema.sql` embedded, idempotent on boot | no versioned migrations — additive-only; a future rename/drop needs a manual migration story |
| Default admin credential | ✅ fixed (Phase 0) — seeded from `ADMIN_USERNAME`/`ADMIN_PASSWORD`, or a random one-time password printed once at first boot | — |
| CSRF protection | ✅ double-submit cookie on `/login`, `workflows/update`, `pair-code` (Phase 0) | — |
| Rate limiting | ✅ login 10/5min per IP; inbound WhatsApp 8/min per chat with one Arabic warn reply (Phase 0) | limits are in-memory — reset on restart, single-instance only |
| Session management | ⚠️ HMAC-signed cookie, ephemeral `SESSION_SECRET` if unset (warned at boot) | set `SESSION_SECRET` in prod; still single-instance by design ([A3]) |
| PII / retention controls | ✅ (Phase 6) `RETENTION_DAYS` (default 90): stt/tts history + conversation turns purged, `wa_activity` transcripts redacted in place, daily | region pinning / encryption-at-rest remain whatever Neon provides |
| Observability | ✅ per-message trace id (= WhatsApp message id) on every pipeline log line; `ERROR_WEBHOOK_URL` error/panic reporting with trace attached; `/healthz`·`/readyz`·`/metrics` endpoints; `log/slog` (text or `LOG_FORMAT=json`); durable `tool_executions` step log (audit) | Prometheus-style metrics/dashboards; uptime alerting; LangSmith-style LLM tracing |
| Agentic durability & resilience | ✅ (audit) inbound dedup (`processed_messages`), atomic confirmation claim (no double-execute), ERP retry/backoff + deterministic idempotency key + trace header, graceful HTTP shutdown + server timeouts, per-message 120s deadline | resumable state machine / saga / deterministic replay remain out of scope (IMPLEMENTATION-PLAN §5) |
| Testing | ✅ **149 test functions across 24 files** — auth cookies, CSRF, HMAC contract, intent cleaning, tool-loop bounds + role filtering, memory, confirmation lifecycle (incl. overwrite regression), provider fallback, speech providers (fakes), voice-note store, whatsmeow, web handlers, 7-scenario eval suite | no coverage gate; `main.go`'s handler remains thin |
| CI | ✅ `.github/workflows/ci.yml` — build + vet + `test -race -cover` on push/PR | — |
| Repo hygiene | ✅ `.gitignore` added, `go.sum` tracked (Phase 0) | — |

---

## 4. Roadmap (milestones, done-when)

The original M0–M7 numbering is preserved for continuity with the pre-consolidation sprint history (in `git log`), updated to reflect the Go rewrite. A new **M8** captures production-readiness debt this audit surfaced that wasn't in the original list at all.

- **M0 — Consolidation.** ✅ Done (historical). Superseded in spirit by the Go rewrite itself, which is a much larger consolidation than originally scoped.
- **M1 — Voice in/out.** ✅ Done, re-implemented in Go (`internal/speech/*`, `internal/audio/audio.go`) instead of Python. **Remaining:** dialect-accuracy tuning; never verified live through the operations tool loop together.
- **M2 — Real reasoning.** ✅ Built in Go (`internal/workflow/engine.go`), unverified live. **Remaining:** provider fallback (no longer optional — see §3); structured-output response node.
- **M3 — ERP Agent Gateway MVP (in `mshalia`).** Unchanged from before — this repo's client side (`internal/erp/client.go`) is done and unverified live; the gateway itself lives in `mshalia`, out of this repo.
- **M4 — Tools + identity + confirmation.** ✅ Built (Phases 2+3), unverified live. Conversation memory (`internal/workflow/memory.go`) + risk-gated confirmation flow (`internal/workflow/confirmation.go`). **Done-when:** confirmed ✅, audited ✅, multi-turn ✅ — end-to-end live verification still pending.
- **M5 — Dashboard convergence.** ✅ Done, as a side effect of the consolidation — one WhatsApp stack, dashboard controls it directly.
- **M6 — Accounting + Administration agents.** ✅ Go side built (Phase 5) and since **expanded well beyond the original scope**: alongside accounting + administration, the Go side now also ships client self-service, sales/CRM, and breeding agents plus an enriched operations tool set — **39 tools across 6 agents**, role-gated, financial writes confirmation-gated at `high` risk with required idempotency keys. **Remaining:** the matching `mshalia`-side gateway tools (tracked in `mshalia`); PDF tools; manager-approval routing (beyond user self-confirmation).
- **M7 — Hardening.** ✅ Built (Phases 4+6): trace ids, webhook error/panic reporting, retention job, CI, eval suite, [`DEPLOYMENT.md`](DEPLOYMENT.md) runbook. **Remaining:** metrics/dashboards; live smoke run.
- **M8 — Go rewrite production-readiness debt.** ✅ Closed (Phase 0): admin credential from env/generated, CSRF, rate limiting, `.gitignore`, `go.sum` tracked.
- **M9 — Live verification (next).** ⛔ The whole stack is now built-but-unverified-live: pair a real WhatsApp number, point at a deployed `mshalia` with real `AGENT_GATEWAY_SECRET`, real NIM/Groq keys, and run the eval scenarios as real conversations (voice + text). Also: implement the 39 gateway tool ids (6 agents) in `mshalia` (see [`mshalia-side.md`](mshalia-side.md)).

---

## 5. Agent Architecture (Go tool-calling loop)

There is no LangGraph and no supervisor/subgraph compilation. The equivalent logic is plain Go control flow in `internal/workflow/engine.go`, with agents declared in `internal/workflow/tools.go`:

```
resolvePendingConfirmation → (client role? → client agent) → ClassifyIntent (1 LLM call, no tools) → route → executeToolLoop(agentSpec, role-filtered tools) | general chat → FinalReply
```

- Six declarative `agentSpec`s *(name + default persona prompt + tool list)*, **39 tools** total: **operations** (15), **accounting** (4, financial writes `high` risk), **administration** (4), **client** self-service (6), **sales/CRM** (5), **breeding** (5). Adding an agent or tool means adding a spec entry + a risk tag + a min-role — the router (`Execute`) never changes, matching the "register declaratively" principle.
- **Role-based access control:** each tool has a minimum app-role (`toolMinRole`; hierarchy `client < viewer < manager < admin < super_admin`). `executeToolLoop` exposes only the tools the actor's resolved role satisfies — **fail-closed**, so an unmapped or empty role sees no tools. A resolved identity whose role is `client` is routed **directly** to the client self-service agent, bypassing intent classification.
- Each routed request sees **only that agent's tools**, further narrowed by role (verified by test).
- The loop is bounded at `maxIterations = 4` (not yet configurable per-agent); tool errors are fed back to the model as tool messages for self-correction.
- Cross-turn state lives in Postgres: `conversation_turns` (last 8 replayed) + `conversation_state` (rolling summary) + `pending_confirmations` (the pause-and-resume equivalent of LangGraph's `interrupt()`).

---

## 6. ERP Integration — the Agent Gateway (unchanged, lives in `mshalia`)

**Why a gateway, not direct access:** `firebase-admin` **bypasses all `firestore.rules`** (god-mode), and every ERP mutation lives in **client-SDK `lib/api/*`** with TS-only validation — not server-callable as-is. The Gateway is a thin server-side re-exposure inside `mshalia` (`app/api/agent/v1/*`) that wraps the existing transactional/idempotent logic with real validation, RBAC, and guardrails. It is the **only write path**. None of this lives in the Go repo — it's `mshalia`'s responsibility, and this section is unchanged from the original design.

**Auth (two-factor):** platform service credential (HMAC, `internal/erp/client.go`) **+** an acting-user assertion `{ actingUserUid, orgId }` resolved from the WhatsApp phone — the agent can never exceed the human's own authority.

**Non-destructive guardrails** (unchanged, enforced server-side in `mshalia`): allow-listed tools only; single-entity id-addressed writes; soft-delete only; Zod validation; idempotency keys; amount/risk thresholds → `requires_approval`; per-actor/tenant rate limits; org-scoping; immutable audit.

**Tool contract** (unchanged — `mshalia/lib/agent-gateway/tools/types.ts`):
```ts
interface ToolDefinition<I, O> {
  id; agent; purpose;
  input: ZodSchema<I>;
  permissions: { scopes: PermissionScope[]; minRole: AppUserRole };
  risk: "low" | "medium" | "high";
  idempotent: boolean; output: ZodSchema<O>;
  failureModes: string[];
  rollback: "transactional" | "gl_reversal" | "compensating" | "none";
  handler(ctx, input): Promise<O>;
}
```
Our client now calls **39 tool ids across 6 agents** (see [`mshalia-side.md`](mshalia-side.md) §4); the matching gateway handlers must be built in `mshalia`. None are verified against a live gateway yet — this all lives outside this repo.

---

## 7. Data & Schema

- **Platform:** Neon Postgres. Schema source of truth is now the raw `schema.sql` at the repo root (embedded via `//go:embed schema.sql` in `main.go`), executed idempotently on every boot — **not** Drizzle/TypeScript anymore. Tables (16): `settings, tts_history, stt_history, webhook_logs, agents, users, health_check, wa_contacts, wa_activity, conversation_turns, conversation_state, pending_confirmations, wa_messages, wa_voice_notes, processed_messages` (inbound dedup), `tool_executions` (durable per-tool step log) (`whatsmeow`'s own `sqlstore` package separately manages its own device/session tables in the same database).
- **Agent config** lives in the `agents` table (`system_prompt`, `llm {vendor,url,model}`, `asr/tts`, `max_history`, `mcp_servers`, `skills`) — the assigned agent's prompt is resolved per contact/agent by `internal/workflow/engine.go`'s `resolveSystemPrompt` (contact prompt override → assigned agent → default agent → the spec's built-in prompt).
- **ERP:** Firestore, path-nested `organizations/{orgId}/**` (multi-tenant), unchanged, lives in `mshalia`.

---

## 8. Speech Pipeline

- **STT (`internal/speech/stt.go`):** cascades through up to 4 providers in order, using whichever are configured: Groq (Whisper Large v3) → Hugging Face Serverless Inference → Google Cloud Speech-to-Text → local whisper.cpp (`whisper-cli` + a `ggml` model file). Falls through to the next provider on any error.
- **TTS (`internal/speech/tts.go`):** Google Cloud TTS → Hugging Face Spaces → local gTTS, same cascade pattern.
- **Transcoding (`internal/audio/audio.go`):** shells out to `ffmpeg` for OGG/Opus ↔ WAV conversion in both directions. Hard runtime dependency, not checked at startup.
- This is a real deviation from the original Habibi/SILMA voice-clone design — worth an explicit product decision (generic cascade vs. a consistent branded voice), not just an implementation detail.

---

## 9. Security

- **Channel to `mshalia`:** HMAC-SHA256 signed (`internal/erp/client.go`), same contract as before. *Not independently verified in this audit that `mshalia`'s side still enforces the ±5 min timestamp skew — that check lives in `mshalia`, out of this repo.*
- **ERP access:** unchanged — Gateway-only writes, no raw `firebase-admin` in this repo (it never had any), least-privilege service identity via the shared secret.
- **Prompt-injection:** the model only calls typed tools via the OpenAI tool-calling schema in `engine.go` — never raw queries; unchanged design principle, still followed.
- **Dashboard auth:** admin credentials come from `ADMIN_USERNAME`/`ADMIN_PASSWORD` or a generated one-time password printed once at first boot; bcrypt-hashed at rest; login rate-limited (10/5min per IP); sessions are HMAC-signed cookies (set `SESSION_SECRET` in prod).
- **CSRF:** double-submit cookie token required on all state-changing dashboard POSTs.
- **Rate limiting:** login per-IP + inbound WhatsApp per-chat (8/min, in-memory, single-instance).
- **PII:** `RETENTION_DAYS` (default 90) purges STT/TTS history and conversation turns and redacts `wa_activity` transcripts in place daily; encryption-at-rest and region pinning remain whatever Neon provides.
- **Abuse:** per-number throttling built; identity resolution still gates *ERP tool access*, not whether the bot replies at all — an allow-list mode for who gets any reply is a possible future tightening.
- **Risky writes:** medium/high-risk tools never execute without an explicit user confirmation on the following message (10-min TTL, audited).

---

## 10. Conversation Design

Persona: a seasoned operations manager. Infer from context; ask the single most useful question only when blocked; restate high-risk/financial actions for confirmation; prevent errors (no two horses in one stall without explicit override); one-line "what changed"; recover gracefully. Replies short, voice-friendly (no markdown), in the user's language.

**Status check against the current implementation:** built. The last 8 turns are replayed per chat (plus a rolling summary for long threads), so pronoun resolution across turns has real context to work with; high-risk/financial actions are restated verbatim (including amounts) and require an explicit "نعم"/"yes" on the next message before executing. Quality of pronoun resolution and dialect handling still needs live verification (M9).

---

## 11. Risks & Mitigations

| Risk | Mitigation |
|---|---|
| whatsmeow/WhatsApp ban/instability (SPOF — now also the LLM/dashboard's host) | conservative send patterns; per-chat inbound throttle (built); health alerts via `ERROR_WEBHOOK_URL`; device state in Postgres survives redeploys; consider a `ChannelPort` abstraction if a second channel (Meta Cloud API) is ever needed |
| Arabic dialect STT errors | confirmation on risky ops (built); domain-vocab biasing; entity read-back in confirmation text (built for amounts/ids); text fallback; never act on low confidence — the last remains prompt-level only |
| LLM bad tool args / wrong task update | strict JSON-schema tool definitions; resolve-don't-invent ids in prompts; risk-gated confirmation with the action restated (built); post-condition validation still not implemented |
| LLM provider outage | NIM → OpenAI-compatible fallback cascade (built); both down → graceful error reply |
| Whole stack unverified live | everything above is unit-tested against fakes, not real services — M9 (live verification) is the top-priority next step before onboarding real users |
| mshalia-side gaps | the 39 tool ids (across operations/accounting/administration/client/sales/breeding) and `PermissionScope` + role enforcement live in the `mshalia` repo and are not yet built there — see [`mshalia-side.md`](mshalia-side.md) |
| Schema drift | `schema.sql` is idempotent but not versioned — additive changes only; a rename/drop needs a manual migration story |

---

## 12. Assumptions

- **[A1]** STT/TTS run in-process on the same host as the WhatsApp socket and dashboard (no separate backend host anymore).
- **[A2]** WhatsApp users map 1:1 to an ERP `AppUser`/`Client` via phone and must be verified (resolved by `mshalia`) to access ERP tools; unresolved numbers still get a reply, just not tool access.
- **[A3]** The Go binary runs always-on as a single instance (multi-instance scaling is not yet supported — session secret and in-memory `WhatsAppManager` state are process-local).
- **[A4]** NVIDIA NIM (OpenAI-compatible) is the primary LLM, with an OpenAI-compatible fallback chain (`OPENAI_API_KEY` + `LLM_FALLBACK_MODEL`).
- **[A5]** Risk level gates auto-execution vs. user confirmation (built); a configurable SAR amount threshold *within* a risk tier and manager (third-party) approval routing remain future work.
- **[A6]** Reads may hit Firestore directly via the Gateway's read tools for latency; writes always go through the Gateway — unchanged.
