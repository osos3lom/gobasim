# Sawt — Engineering Blueprint & Roadmap

> **WhatsApp voice as the primary UI for the `mshalia` ERP** — a domain-agnostic, multi-agent, tool-using AI platform.
> This document is the **single source of truth**: target architecture, current-vs-target gap, and the phased roadmap.
> Companion docs: [`REFERENCE_REPO_SKILLS.md`](REFERENCE_REPO_SKILLS.md) (LangGraph/ERP patterns) · [`../.claude/skills/codebase-map/SKILL.md`](../.claude/skills/codebase-map/SKILL.md) (where things live) · [`../README.md`](../README.md) (overview).

---

## 1. Context & Goal

`mshalia` is a mature multi-vertical Next.js 16 + Firestore ERP (equine / agriculture / industrial / holding) with a real double-entry ledger. Its field users — grooms, stable managers, accountants — find forms high-friction; some have limited literacy. **Goal:** let them *speak* to the ERP over WhatsApp ("check Najm into stall A-12", "log a 1,200 SAR feed bill") and have an AI operations-manager understand, confirm when risky, execute the real ERP operation, and reply in Arabic text + voice — preserving the ERP's integrity, RBAC, audit, and multi-tenancy.

**Domain-agnostic:** the equine center is tenant #1; any ERP onboards by registering a new **tool pack** + prompts.

---

## 2. Target Architecture

Three runtimes, one `sawt-monorepo` (npm workspaces) + the ERP:

| System | Path / runtime | Role | Scope |
|---|---|---|---|
| **WhatsApp Gateway** | `apps/gateway-whatsmeow` (Go, **GCP always-on**) | Owns the whatsmeow socket; HMAC-forwards inbound; sends replies | edge |
| **Backend (brain)** | `apps/backend` (**Python FastAPI + LangGraph**) | STT → reason → tools → response → TTS; hosts the agent graph + speech | core |
| **Dashboard** | `apps/dashboard` (Next.js 16) | Control plane: agents, config, activity, over Neon | control |
| **Shared** | `packages/{core,database,security}` (`@sawt/*`) | env/logger/signing/crypto/types, Drizzle schema, HMAC | libs |
| **ERP Agent Gateway** | `mshalia/app/api/agent/v1/*` (**Sprint 1: identity + 4 tools**) | Scoped, validated, audited, non-destructive tool API over `lib/api/*` | ERP |

### The pipeline (with the real, already-wired contract)

```
WhatsApp voice/text
  │  Baileys socket
apps/gateway-whatsmeow ──POST WEBHOOK_URL (HMAC)──▶ apps/backend  POST /webhook/inbound
  ▲                                                   │ verify sig → sawt_graph.ainvoke(state)
  │  POST {GATEWAY_URL}/send (reply)  ◀───────────────┘ STT → LangGraph → tools → response → TTS
  ▼
WhatsApp reply (text + voice)
                                                        │ tool calls (signed service token + actor)
                                                        ▼
                                   mshalia /api/agent/v1/*  → lib/api/* → Firestore
```

**Signed channel contract (implemented, gateway ↔ backend both honor it):**
- Headers `x-swa-signature`, `x-swa-timestamp`; signature = `HMAC-SHA256(secret, "{timestamp}.{rawBody}")` hex; ±5 min skew. Secret = `GATEWAY_SHARED_SECRET`.
- Inbound body (`InboundMessage`): `{ sessionId, messageId, from, pushName?, timestamp, text, isGroup }` (voice/`ptt` is M1).
- Gateway env: `SESSION_ID`, `PORT` (8080), `GATEWAY_SHARED_SECRET`, `WEBHOOK_URL` (→ backend `/webhook/inbound`), `AUTH_ENCRYPTION_KEY`, `DATABASE_URL`. Backend env: `GATEWAY_URL` (→ gateway `/send`), `GATEWAY_SHARED_SECRET`, `DATABASE_URL`.

---

## 3. Current State → Target (the gap)

Legend: ✅ built · ⚠️ partial · ⛔ missing.

*Updated after Sprint 1 (see [SPRINT-01.md](SPRINT-01.md) / [SPRINT-01-VERIFICATION.md](SPRINT-01-VERIFICATION.md)). "✅ (unverified live)" = built, syntax/typecheck-clean, reviewed against the cross-repo contract, but not yet exercised against real NIM/mshalia/WhatsApp credentials.*

| Capability | State | Gap to close |
|---|---|---|
| WhatsApp transport (`gateway-whatsmeow`) | ✅ socket, Postgres auth-state (sqlstore), reconnect+backoff, `/send`, voice-note download | ban/health monitoring |
| Gateway ↔ backend signed channel | ✅ `/webhook/inbound` ↔ `/send`, HMAC verified both sides, audio included | — |
| Agent graph (`apps/backend/agent/graph.py`) | ✅ (unverified live) real intent classification + response generation (NIM); operations is a real bounded tool-calling loop | accounting/administration tool packs; confirmation/approval (`interrupt()`) |
| LangGraph state (`agent/state.py`) | ✅ `AgentState` + `tool_results`/`final_reply` (S1-4) | — |
| STT (speech-to-text) | ✅ `agent/stt.py` — local Whisper (transformers) or cloud OpenAI-compatible fallback | Arabic dialect accuracy tuning; not yet tested through the operations tool loop (voice + tools together) |
| TTS | ✅ Habibi/SILMA sidecar, wired into the reply path (OGG/Opus) | — |
| LLM reasoning + tool-calling | ✅ (unverified live) NVIDIA NIM via `agent/llm.py`, graceful "not configured" when no key | provider fallback (§21); structured-output response node (currently plain text) |
| ERP Agent Gateway (mshalia) | ✅ (unverified live) `identity/resolve` + `tools/[toolId]` (4 ops tools), HMAC + fresh actor re-resolution + role policy + Zod + audit | `PermissionScope` enforcement (role-only today — see `lib/agent-gateway/tools/types.ts`); accounting/administration tool packs; amount/risk thresholds → `requires_approval` |
| Identity (phone → ERP user/org/role) | ✅ (unverified live) resolver in mshalia, called on every inbound (skipped for groups) | Neon-side cache/TTL (currently resolves fresh every message); `Client`-record linkage (only top-level `users` today) |
| Conversation memory | ⚠️ Postgres checkpointer + windowed history (last 6–8 turns) | rolling summary node for long threads |
| Dashboard WhatsApp integration | ⚠️ wired to **legacy** bot (`/integrations`, `api/wa-bot/*` → `:3100`); health route now also checks `gateway-whatsmeow` as a fallback | fully rewire to `gateway-whatsmeow` (M5) |
| Legacy `legacy/wa-bot` (whatsapp-web.js) | quarantined out of workspaces | freeze → delete after M5 |
| Platform schema | ✅ Drizzle `packages/database/src/schema.ts` canonical; `db-init` now runs real drizzle-kit migrations | — (debt closed) |

---

## 4. Roadmap (milestones, done-when)

Each milestone is independently shippable. Code generation waits for approval per milestone. **Sprint 1** (see [SPRINT-01.md](SPRINT-01.md)) delivered a text-first slice of M2–M4 (real LLM, identity resolver, 4 operations tools, bounded tool-calling loop) — built and reviewed, **not yet verified live** (see [SPRINT-01-VERIFICATION.md](SPRINT-01-VERIFICATION.md)). Milestones below are updated to reflect that.

- **M0 — Consolidation.** ✅ Done. One blueprint; `schema.sql` removed; channel contract documented; backend README corrected.
- **M1 — Voice in/out.** ✅ Done (delivered outside the sprint board, in parallel). `agent/stt.py` (local Whisper or cloud fallback), `agent/audio.py` (OGG↔WAV/Opus), TTS wired into the reply. **Remaining:** dialect-accuracy tuning; not yet tested through the operations tool loop together.
- **M2 — Real reasoning.** ✅ Built, unverified live (Sprint 1 S1-3). NIM-backed intent classification + response generation via `agent/llm.py`; windowed memory (last 6–8 turns) via the Postgres checkpointer. **Remaining:** rolling summary node for long threads; provider fallback.
- **M3 — ERP Agent Gateway MVP (in `mshalia`).** ✅ Built, unverified live (Sprint 1 S1-1/S1-2). `app/api/agent/v1/identity/resolve` + `tools/[toolId]` (4 tools: `get_horse`, `get_care_plan`, `list_tasks`, `update_task_status`) — HMAC service auth, fresh actor re-resolution, `minRole` policy, Zod, allow-list, soft-delete-only, org-scoped, audited (`agent_audit` + `activity_feed`). **Remaining:** `PermissionScope` enforcement (role-only today), amount/risk thresholds → `requires_approval`, `AssignHorseToStall`/`RecordExpense` and the rest of the originally-scoped ~6 tools.
- **M4 — Tools + identity + confirmation.** ⚠️ Partially built (Sprint 1 S1-4): LangGraph tool-calling loop + phone→identity resolver (no cache yet — resolves fresh every message). **Remaining (explicitly deferred):** human-in-the-loop confirmation via `interrupt()`/`Command(resume)` for risky/financial ops — `update_task_status` currently executes immediately. **Done-when:** confirmed, audited, end-to-end — audited ✅, confirmed ⛔.
- **M5 — Dashboard convergence.** Rewire `/integrations` + `api/wa-bot/*` to `gateway-whatsmeow` (the dashboard's health route now checks it as a fallback, not primary); delete `legacy/wa-bot`. **Done-when:** the dashboard controls the Go whatsmeow gateway; legacy gone; one WhatsApp stack.
- **M6 — Accounting + Administration agents.** Invoice/payment/vendor/contract/client tools; approval routing; PDF tools.
- **M7 — Hardening.** Observability (trace_id, LangSmith/Sentry), eval suite, deploy (GCP gateway `min-instances=1`, Vercel dashboard, backend host), CI typecheck+lint+tests.

---

## 5. Agent Architecture (LangGraph)

A **supervisor** graph (`sawt_graph`) routes to domain **subgraphs**; agents are bounded *(prompt + allowed tools + policy)* contexts — start with Supervisor, not Swarm (see REFERENCE_REPO_SKILLS §1).

```
classify_intent → route → { operations | accounting | administration } → respond → END
```
- **Operations** — horses, stalls, tasks, inventory, health, vet.
- **Accounting** — invoices, supplier bills, payments, journal postings, reports.
- **Administration** — clients, contracts, documents, scheduling.

Rules that keep it safe & maintainable: compile subgraphs once at startup; `TypedDict`/Pydantic state; `recursion_limit`; `thread_id = wa-{normalized_phone}`; **never** `.bind_tools()` + `.with_structured_output()` on one node; `ToolNode(handle_tool_errors=True)` for self-correction; route first, then expose only that agent's ≤15 tools. New agents/tools register declaratively — the supervisor never changes.

---

## 6. ERP Integration — the Agent Gateway (centerpiece; identity + 4 tools built in Sprint 1)

**Why a gateway, not direct access:** `firebase-admin` **bypasses all `firestore.rules`** (god-mode), and every ERP mutation lives in **client-SDK `lib/api/*`** with TS-only validation — not server-callable as-is. The Gateway is a thin server-side re-exposure inside `mshalia` (`app/api/agent/v1/*`) that wraps the existing transactional/idempotent logic with real validation, RBAC, and guardrails. It is the **only write path**.

**Auth (two-factor):** platform service credential (signed/HMAC, verified like `lib/api/auth.ts:verifyRequest`) **+** an acting-user assertion `{ actingUserUid, orgId }` resolved from the WhatsApp phone — the agent can never exceed the human's own authority.

**AuthZ:** enforce the currently-**unenforced** `PermissionScope` (`types/domain.ts`) + app `roleHierarchy`; a policy table `tool → { scopes, minRole, risk, maxAmount? }`; default-deny.

**Non-destructive guardrails** (the "must not crash/delete the DB" requirement): allow-listed tools only (no generic query/update); single-entity id-addressed writes (no bulk/collection deletes); **soft-delete only** (`hardDeleteHorse` never wrapped); Zod validation + referential checks; idempotency keys (leverages the ERP's deterministic GL `je-{type}-{id}-{event}` + `counters`); amount/risk thresholds → `requires_approval`; per-actor/tenant rate limits (`lib/api/rate-limit.ts`); org-scoping every call; complete immutable audit (also fills the ERP's `activity_feed` gaps).

**Tool contract** (every tool declares all seven facets):
```ts
interface ToolDefinition<I, O> {
  id; agent; purpose;
  input: ZodSchema<I>;                       // required inputs + validation
  permissions: { scopes: PermissionScope[]; minRole: AppUserRole };
  risk: "low" | "medium" | "high";           // gates confirmation/approval
  idempotent: boolean; output: ZodSchema<O>;
  failureModes: string[];                    // STALL_OCCUPIED, FORBIDDEN, ...
  rollback: "transactional" | "gl_reversal" | "compensating" | "none";
  handler(ctx, input): Promise<O>;           // wraps lib/api/*
}
```
Initial packs map 1:1 to `lib/api/*` (see codebase-map skill): Operations→`horses.ts`/`tasks.ts`/`inventoryTransactions.ts`; Accounting→`invoices.ts`/`expenses.ts`/`payments.ts`/`accounting/postJournal.ts`; Administration→`clients.ts`/`contracts.ts`.

*Sprint 1 implementation note:* `mshalia/lib/agent-gateway/tools/types.ts` ships a simplified subset of this contract — `policy: { minRole, scopes? }` (scopes captured but not enforced, `risk`/`idempotent` present, but `output`/`failureModes`/`rollback` aren't separate declared fields yet (failures throw a typed `ToolError` instead). 4 operations tools are live (`get_horse`, `get_care_plan`, `list_tasks`, `update_task_status`); `AssignHorseToStall`/`RecordExpense`/accounting/administration are still to build.

---

## 7. Data & Schema

- **Platform:** Neon Postgres. **Single schema source of truth = Drizzle `packages/database/src/schema.ts`** (`@sawt/database`); the dashboard's `db.ts` consumes it. Tables: `settings, tts_history, stt_history, webhook_logs, agents, users` + WhatsApp `wa_creds, wa_signal_keys, wa_contacts, wa_activity, health_check`. `apps/dashboard/src/app/api/db-init/route.ts` now runs real **drizzle-kit** migrations (`packages/database/drizzle/`) instead of inline DDL — the earlier consolidation debt here is closed.
- **Agent config** lives in the `agents` table (`system_prompt`, `llm {vendor,url,model}` — OpenAI-compatible → **NVIDIA NIM** slots into `url` — `asr/tts`, `maxHistory`, `mcpServers`, `skills`).
- **ERP:** Firestore, path-nested `organizations/{orgId}/**` (multi-tenant). Details in the codebase-map skill.

---

## 8. Speech Pipeline

- **STT (built, M1):** server-side Arabic STT in `apps/backend/agent/stt.py` — local `transformers` Whisper pipeline (default) or a cloud OpenAI-compatible fallback (`STT_PROVIDER=cloud`). WhatsApp voice notes are OGG/Opus → transcode (`agent/audio.py`, ffmpeg-based) → WAV.
- **TTS (built):** Habibi/SILMA voice-clone sidecar (`/synthesize/{model}`), wired into the reply path and transcoded back to OGG/Opus voice notes.
- Keep STT/TTS behind adapters so providers are swappable (the `agents.asr/tts` config already models this).

---

## 9. Security

- **Channel:** HMAC-SHA256 signed, replay-protected (±5 min), shared-secret both directions (implemented).
- **ERP access:** Gateway-only writes; no raw `firebase-admin` on the platform; least-privilege service identity; enforced scopes + org isolation; non-destructive guardrails (§6).
- **Prompt-injection:** the model only calls typed, Zod-checked tools — never raw queries/paths; retrieved ERP text is data, not instructions.
- **PII:** voice/transcripts are PII → encrypt at rest (Neon), retention policy (purge raw audio after N days, keep transcript+audit), redact logs, region-pin (Saudi residency).
- **Abuse:** rate limits per number; only verified/linked numbers (`Client.phoneVerifiedAt`) may transact.

---

## 10. Conversation Design

Persona: a seasoned operations manager. Infer from context; ask the single most useful question only when blocked; restate high-risk/financial actions for confirmation; prevent errors (no two horses in one stall without explicit override); one-line "what changed"; recover gracefully. Replies short, voice-friendly (no markdown), in the user's language. Memory resolves pronouns across turns.

---

## 11. Risks & Mitigations

| Risk | Mitigation |
|---|---|
| whatsmeow/WhatsApp ban/instability (gateway SPOF) | conservative send patterns; verified-number allow-list; health alerts; database backup; `ChannelPort` so Meta Cloud API can augment later |
| Arabic dialect STT errors | confirmation on risky ops; domain-vocab biasing; entity read-back; text fallback; never act on low confidence |
| LLM bad tool args / wrong GL posting | strict Zod; resolve-don't-invent ids; confirmation + approval thresholds; post-condition validation |
| `firebase-admin` god-mode | Gateway-only writes; allow-list; soft-delete-only; org scoping |
| Two parallel WhatsApp stacks | M5 converges on `gateway-whatsmeow`; delete legacy |
| Schema drift | Drizzle as single source; drizzle-kit migrations (closed, M0) |

---

## 12. Assumptions

- **[A1]** STT/TTS run server-side (co-located on the backend/GCP host); cloud APIs are the fallback.
- **[A2]** WhatsApp users map 1:1 to an ERP `AppUser`/`Client` via phone and must be verified to transact.
- **[A3]** Gateway runs always-on (GCP Cloud Run `min-instances=1` or GCE micro).
- **[A4]** NVIDIA NIM (free, OpenAI-compatible) is the default LLM, behind a provider abstraction with fallback.
- **[A5]** A configurable SAR amount / risk level gates auto-execution vs. manager approval.
- **[A6]** Reads may hit Firestore directly for latency; **writes always via the Gateway.**
