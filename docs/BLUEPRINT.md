# Sawt — Engineering Blueprint & Roadmap

> **WhatsApp voice as the primary UI for the `mshalia` ERP** — a domain-agnostic, tool-using AI operations assistant.
> This document is the **single source of truth**: target architecture, current-vs-target gap, and the phased roadmap.
> **Architecture note (2026-07):** the platform was originally designed as three runtimes (Go gateway / Python FastAPI+LangGraph backend / Next.js dashboard, see git history). It has since been **consolidated into a single Go binary** (`module sawt-go`, built as `sawt-gateway`) that owns the WhatsApp socket, the reasoning loop, speech, and the operator dashboard in one process. This document describes the **current Go implementation** — it supersedes the old three-runtime diagram.
> Companion docs: [`SPRINT-01.md`](SPRINT-01.md) / [`SPRINT-01-VERIFICATION.md`](SPRINT-01-VERIFICATION.md) (history from the pre-consolidation sprint) · [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) (phased plan to close the gaps below) · [`REFERENCE_REPO_SKILLS.md`](REFERENCE_REPO_SKILLS.md) / [`codebase-map/SKILL.md`](codebase-map/SKILL.md) (⚠️ **stale** — describe the old LangGraph/Drizzle/TypeScript design, kept only for the `mshalia`-side ERP Gateway tool-contract patterns, which are still accurate).

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
  │  4. build a **single-turn** State{Messages: [this message only]} and call wfEngine.Execute()
  │     → ClassifyIntent (1 LLM call) → route to operations | accounting(stub) | administration(stub) | general chat
  │     → operations: bounded 4-iteration tool-calling loop against 4 ERP tools
  │  5. if original message was audio: TTS cascade → ffmpeg WAV→Opus
  │  6. send reply (text or voice note) back over the same whatsmeow socket
  │  7. write one row to wa_activity (transcript, reply, timings, tool_calls) for the dashboard feed
  ▼
WhatsApp reply (text + voice)
                                    │ tool calls (HMAC-signed service secret + actingUserUid + orgId)
                                    ▼
                     mshalia /api/agent/v1/*  → lib/api/* → Firestore
```

**What this eliminates from the old design:** the gateway↔backend signed HTTP channel (`/webhook/inbound` ↔ `/send`, `GATEWAY_SHARED_SECRET`) no longer exists — there's nothing to forward to, it's the same process. That channel-contract section of the old blueprint is obsolete. **The `GATEWAY_SHARED_SECRET` env var is still read into `config.Config` (`config/config.go:13,90`) but is never referenced anywhere else in the codebase — it is dead configuration left over from the old split and should be deleted.**

**What's unchanged:** the *external* signed channel from this binary to `mshalia`'s ERP Agent Gateway (`x-swa-signature` / `x-swa-timestamp`, `HMAC-SHA256(secret, "{timestamp}.{rawBody}")`, secret = `AGENT_GATEWAY_SECRET`) — implemented identically in `internal/erp/client.go`.

**Env vars actually consumed** (`config/config.go`): `DATABASE_URL`, `PORT`, `AGENT_GATEWAY_SECRET`, `MSHALIA_API_URL`, `NIM_API_KEY`, `NIM_BASE_URL`, `NIM_MODEL`, `STT_PROVIDER`, `STT_MODEL`, `OPENAI_API_KEY`, `OPENAI_API_BASE`, `HF_API_KEY`, `TTS_PROVIDER`, `TTS_MODEL`, `PAIR_PHONE_NUMBER`, `SESSION_SECRET`, `GROQ_API_KEY`, `GCP_API_KEY`, `SECURE_COOKIE`. (`GATEWAY_SHARED_SECRET` is loaded but dead — see above.)

---

## 3. Current State → Target (the gap)

Legend: ✅ built · ⚠️ partial · ⛔ missing · 🛑 critical defect (must fix before any real deployment).

*Updated 2026-07-03 after executing [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) Phases 0–6 (commits `a7f914f`..`2dbaa80`). "✅ (unverified live)" = built, unit-tested where testable, but not yet exercised against a real paired WhatsApp number + live `mshalia` + real LLM/STT/TTS credentials in this environment.*

| Capability | State | Gap to close |
|---|---|---|
| WhatsApp transport | ✅ `internal/whatsmeow/client.go` — Postgres device store, QR + pairing-code linking, reconnect | ban/health monitoring & alerting |
| In-process pipeline (replaces gateway↔backend channel) | ✅ single binary; dead `GATEWAY_SHARED_SECRET` config removed (Phase 0) | — |
| STT | ✅ `internal/speech/stt.go` — 4-provider cascade: Groq Whisper → Hugging Face → Google Cloud STT → local whisper.cpp | Arabic dialect accuracy unverified; never tested live through the tool-calling loop |
| TTS | ✅ `internal/speech/tts.go` — 3-provider cascade: Google Cloud TTS → Hugging Face Spaces → local gTTS | **deviates from the original design intent** (Habibi/SILMA voice-clone sidecar) — generic TTS voices instead of a branded clone voice; a deliberate call the team should confirm |
| Audio transcode | ✅ `internal/audio/audio.go` — ffmpeg OGG/Opus ↔ WAV; checked at boot, fail-fast (`ALLOW_MISSING_FFMPEG=true` for text-only) (Phase 6) | — |
| LLM reasoning + tool-calling | ✅ (unverified live) `internal/workflow/engine.go` — provider cascade NIM → OpenAI-compatible fallback (`OPENAI_API_KEY` + `LLM_FALLBACK_MODEL`), real intent classification, bounded 4-iteration tool loop (Phase 4) | structured-output response node (plain text only, as designed); live verification |
| Conversation memory across turns | ✅ (unverified live) `internal/workflow/memory.go` — last 8 turns replayed from `conversation_turns` per chat; rolling summary folds threads >20 turns via background LLM call; summary injected into system prompts (Phase 2) | live verification of pronoun resolution quality |
| ERP Agent Gateway client (this repo's side) | ✅ (unverified live) `internal/erp/client.go` — HMAC-signed `identity/resolve` + `tools/{toolId}`, generic over tool id | server-side enforcement (`PermissionScope`, the 8 new accounting/administration tool ids, amount thresholds) lives in `mshalia`, out of this repo's scope |
| Identity resolution | ✅ (unverified live) resolves on every inbound message via `ResolveIdentity` | no cache/TTL — resolves fresh every message (same gap as before the rewrite) |
| Confirmation / approval for risky or financial ops (M4) | ✅ built (Phase 3) — per-tool risk tags (unknown → medium), medium/high calls park in `pending_confirmations` (10-min TTL), affirmation executes / anything else cancels / expiry cancels silently, full audit trail in `wa_activity.tool_calls` | SAR amount thresholds within a risk tier (all financial writes are simply `high` today); live verification |
| Accounting agent | ✅ Go-side built (Phase 5) — `list_invoices`, `get_invoice`, `record_expense` (high), `record_payment` (high) | **mshalia-side gateway tools for these ids do not exist yet** — calls will 404 until built in `mshalia` |
| Administration agent | ✅ Go-side built (Phase 5) — `list_clients`, `get_client`, `list_contracts`, `get_contract` | same: mshalia-side tools pending |
| Dashboard | ✅ `web/server.go` — session-authed login, activity feed, contact/agent config, WhatsApp pairing, SSE logs; CSRF-protected POSTs (Phase 0) | — |
| "One WhatsApp stack" (old M5 goal) | ✅ true by construction — one in-process `whatsmeow` client, dashboard controls it directly | — |
| Database schema | ✅ `schema.sql` embedded, idempotent on boot | no versioned migrations — additive-only; a future rename/drop needs a manual migration story |
| Default admin credential | ✅ fixed (Phase 0) — seeded from `ADMIN_USERNAME`/`ADMIN_PASSWORD`, or a random one-time password printed once at first boot | — |
| CSRF protection | ✅ double-submit cookie on `/login`, `workflows/update`, `pair-code` (Phase 0) | — |
| Rate limiting | ✅ login 10/5min per IP; inbound WhatsApp 8/min per chat with one Arabic warn reply (Phase 0) | limits are in-memory — reset on restart, single-instance only |
| Session management | ⚠️ HMAC-signed cookie, ephemeral `SESSION_SECRET` if unset (warned at boot) | set `SESSION_SECRET` in prod; still single-instance by design ([A3]) |
| PII / retention controls | ✅ (Phase 6) `RETENTION_DAYS` (default 90): stt/tts history + conversation turns purged, `wa_activity` transcripts redacted in place, daily | region pinning / encryption-at-rest remain whatever Neon provides |
| Observability | ✅ (Phase 4) per-message trace id (= WhatsApp message id) on every pipeline log line; `ERROR_WEBHOOK_URL` error/panic reporting with trace attached | metrics/dashboards; LangSmith-style LLM tracing |
| Testing | ✅ baseline (Phases 1–6) — ~40 unit tests: auth cookies, HMAC contract, intent cleaning, tool-loop bounds, memory, confirmation lifecycle, provider fallback, 7-scenario eval suite | coverage is concentrated in `workflow`/`web`/`erp`; speech/whatsmeow packages untested (need live services) |
| CI | ✅ `.github/workflows/ci.yml` — build + vet + `test -race -cover` on push/PR | — |
| Repo hygiene | ✅ `.gitignore` added, `go.sum` tracked (Phase 0) | — |

---

## 4. Roadmap (milestones, done-when)

The original M0–M7 numbering is preserved for continuity with `SPRINT-01.md`, updated to reflect the Go rewrite. A new **M8** captures production-readiness debt this audit surfaced that wasn't in the original list at all.

- **M0 — Consolidation.** ✅ Done (historical). Superseded in spirit by the Go rewrite itself, which is a much larger consolidation than originally scoped.
- **M1 — Voice in/out.** ✅ Done, re-implemented in Go (`internal/speech/*`, `internal/audio/audio.go`) instead of Python. **Remaining:** dialect-accuracy tuning; never verified live through the operations tool loop together.
- **M2 — Real reasoning.** ✅ Built in Go (`internal/workflow/engine.go`), unverified live. **Remaining:** provider fallback (no longer optional — see §3); structured-output response node.
- **M3 — ERP Agent Gateway MVP (in `mshalia`).** Unchanged from before — this repo's client side (`internal/erp/client.go`) is done and unverified live; the gateway itself lives in `mshalia`, out of this repo.
- **M4 — Tools + identity + confirmation.** ✅ Built (Phases 2+3), unverified live. Conversation memory (`internal/workflow/memory.go`) + risk-gated confirmation flow (`internal/workflow/confirmation.go`). **Done-when:** confirmed ✅, audited ✅, multi-turn ✅ — end-to-end live verification still pending.
- **M5 — Dashboard convergence.** ✅ Done, as a side effect of the consolidation — one WhatsApp stack, dashboard controls it directly.
- **M6 — Accounting + Administration agents.** ✅ Go side built (Phase 5): 8 tools across the two agents, financial writes confirmation-gated at `high` risk. **Remaining:** the matching `mshalia`-side gateway tools (tracked in `mshalia`); PDF tools; manager-approval routing (beyond user self-confirmation).
- **M7 — Hardening.** ✅ Built (Phases 4+6): trace ids, webhook error/panic reporting, retention job, CI, eval suite, [`DEPLOYMENT.md`](DEPLOYMENT.md) runbook. **Remaining:** metrics/dashboards; live smoke run.
- **M8 — Go rewrite production-readiness debt.** ✅ Closed (Phase 0): admin credential from env/generated, CSRF, rate limiting, `.gitignore`, `go.sum` tracked.
- **M9 — Live verification (next).** ⛔ The whole stack is now built-but-unverified-live: pair a real WhatsApp number, point at a deployed `mshalia` with real `AGENT_GATEWAY_SECRET`, real NIM/Groq keys, and run the eval scenarios as real conversations (voice + text). Also: implement the 8 accounting/administration tool ids in `mshalia`.

---

## 5. Agent Architecture (Go tool-calling loop)

There is no LangGraph and no supervisor/subgraph compilation anymore. The equivalent logic is a plain Go control flow in `internal/workflow/engine.go`:

```
ClassifyIntent (1 LLM call, no tools) → route → { operations (tool loop) | accounting (stub) | administration (stub) | other (general chat) } → FinalReply
```

- **Operations** — the only fully wired path: horses, care plans, tasks. Bounded to `maxIterations = 4` tool-calling rounds against 4 tools (`get_horse`, `get_care_plan`, `list_tasks`, `update_task_status`).
- **Accounting / Administration** — routing exists (`ClassifyIntent` can select them), but both branches immediately return a canned apology string. No tools, no subgraph, nothing to extend yet.
- **Other** — a plain single-call general-chat completion, no tools.

What's intentionally *not* present compared to the old LangGraph design, and why it matters:
- No `recursion_limit` framework beyond the hardcoded `maxIterations = 4` loop bound (fine as a stopgap, but not configurable per-agent).
- No compiled/reusable subgraph registration — adding accounting/administration tools means writing a new Go function similar to `executeOperations`, not registering a declarative tool pack. This is a real extensibility gap the original blueprint's "new agents register declaratively" principle assumed away.
- No `thread_id`-scoped checkpointer — see the conversation-memory finding in §3. This is the most consequential omission: without it, "Operations" tool loop and general chat both reason from a single message every time.

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
4 operations tools are live (`get_horse`, `get_care_plan`, `list_tasks`, `update_task_status`); `AssignHorseToStall`/`RecordExpense`/accounting/administration tools are still to build — same state as before the Go rewrite, since this all lives outside this repo.

---

## 7. Data & Schema

- **Platform:** Neon Postgres. Schema source of truth is now the raw `schema.sql` at the repo root (embedded via `//go:embed schema.sql` in `main.go`), executed idempotently on every boot — **not** Drizzle/TypeScript anymore. Tables: `settings, tts_history, stt_history, webhook_logs, agents, users, health_check, wa_contacts, wa_activity` (`whatsmeow`'s own `sqlstore` package separately manages its own device/session tables in the same database).
- **Agent config** lives in the `agents` table (`system_prompt`, `llm {vendor,url,model}`, `asr/tts`, `max_history`, `mcp_servers`, `skills`) — read by `internal/workflow/engine.go`'s `executeOperations` per contact/agent assignment.
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
- **Dashboard auth — 🛑 critical:** `main.go:60-75` hardcodes and auto-seeds a default admin login (`osos` / `Password@2026`) in source. This must be removed before any deployment anyone else can reach.
- **CSRF:** none. State-changing dashboard POST routes rely only on `SameSite=Lax`.
- **Rate limiting:** none, anywhere — not on the dashboard login, not per-WhatsApp-number on the message pipeline.
- **PII:** voice/transcripts are stored indefinitely in plaintext in `wa_activity` — no encryption-at-rest story beyond whatever Neon provides by default, no retention/purge policy, no log redaction.
- **Abuse:** no rate limits per number; any WhatsApp number that messages the bot gets a reply (identity resolution gates *ERP tool access*, not whether the bot responds at all).

---

## 10. Conversation Design

Persona: a seasoned operations manager. Infer from context; ask the single most useful question only when blocked; restate high-risk/financial actions for confirmation; prevent errors (no two horses in one stall without explicit override); one-line "what changed"; recover gracefully. Replies short, voice-friendly (no markdown), in the user's language.

**Status check against the current implementation:** the persona's system prompt (`main.go:84`, also stored in the `agents` table) still asks the model to behave this way, and the prompt text itself is intact. But **"memory resolves pronouns across turns" does not hold today** — see §3. Any conversational behavior that depends on remembering a previous message (confirmation restatement across a turn boundary, pronoun resolution, "what changed" relative to an earlier state) cannot work until conversation memory is implemented, regardless of what the system prompt asks for.

---

## 11. Risks & Mitigations

| Risk | Mitigation |
|---|---|
| whatsmeow/WhatsApp ban/instability (SPOF — now also the LLM/dashboard's host) | conservative send patterns; verified-number allow-list; health alerts; database backup; consider a `ChannelPort` abstraction if a second channel (Meta Cloud API) is ever needed |
| No conversation memory | see M4 in §4 — needs to land before confirmation/approval, since resume-after-confirmation implies *some* persisted turn state |
| Hardcoded default credential shipping to production | remove before any deployment reachable outside localhost — see [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) Phase 0 |
| Arabic dialect STT errors | confirmation on risky ops (once built); domain-vocab biasing; entity read-back; text fallback; never act on low confidence |
| LLM bad tool args / wrong task update | strict JSON-schema tool definitions in `engine.go`; resolve-don't-invent ids (already in the system prompt); confirmation + approval thresholds (still missing, see M4); no post-condition validation implemented |
| Single LLM provider (NIM only) | no fallback exists today — an outage or key issue takes down all reasoning; add a provider-fallback chain analogous to the STT/TTS cascades |
| Zero test/CI safety net | any change to `engine.go` or `erp/client.go` today ships with no regression protection against a system that mutates real ERP data |
| Schema drift | `schema.sql` is idempotent but not versioned — no rollback path for a future column rename/drop |

---

## 12. Assumptions

- **[A1]** STT/TTS run in-process on the same host as the WhatsApp socket and dashboard (no separate backend host anymore).
- **[A2]** WhatsApp users map 1:1 to an ERP `AppUser`/`Client` via phone and must be verified (resolved by `mshalia`) to access ERP tools; unresolved numbers still get a reply, just not tool access.
- **[A3]** The Go binary runs always-on as a single instance (multi-instance scaling is not yet supported — session secret and in-memory `WhatsAppManager` state are process-local).
- **[A4]** NVIDIA NIM (OpenAI-compatible) is the only LLM provider today — no fallback, see §9/§11.
- **[A5]** A configurable SAR amount / risk level is meant to gate auto-execution vs. manager approval, per the original design — **not implemented anywhere in this repo yet**.
- **[A6]** Reads may hit Firestore directly via the Gateway's read tools for latency; writes always go through the Gateway — unchanged.
