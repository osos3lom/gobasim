# Sprint 1 — E2E verification runbook

Everything below is written, committed, and syntax/typecheck-clean, but **not yet exercised live** — no Python deps were installed and no live NIM/mshalia/WhatsApp credentials were available in the environment this sprint was built in. Use this runbook to actually prove the sprint goal before closing it out.

## Prerequisites

1. **mshalia**: deployed (or `npm run dev`) with `AGENT_GATEWAY_SECRET` set, and at least one `users/{uid}` doc with a `phone`, `role` (`viewer` or higher), and `orgIds` map pointing at a real org with test data (a horse, a care plan, a couple of tasks).
2. **sawt backend** (`apps/backend`): `pip install -r requirements.txt` (heavy — pulls torch/f5-tts; budget time), then set in `.env.local`:
   - `NIM_API_KEY` (get a free key at [build.nvidia.com](https://build.nvidia.com))
   - `AGENT_GATEWAY_SECRET` — **must exactly match** the value set in mshalia
   - `MSHALIA_API_URL` — the mshalia deployment's base URL
   - `DATABASE_URL` (Neon) — enables the Postgres checkpointer (multi-turn memory)
3. **gateway-baileys**: paired to a real WhatsApp number (`npm run pair --workspace=@sawt/gateway`), with `WEBHOOK_URL` pointing at the backend's `/webhook/inbound` and `GATEWAY_SHARED_SECRET` matching the backend's.
4. Run all three: `npm run dev:all` from the repo root (or the three `dev:*` scripts separately).

## Test script

| # | Send (WhatsApp text) | Expect |
|---|---|---|
| 1 | From an **unlinked** number: any message | Reply: "This number isn't linked to an ERP account yet…" (no LLM/tool calls burned — this is a fixed reply) |
| 2 | From a **linked** number: "مرحبا" / "hi" | Classified `other`, a natural greeting reply in the same language, no tool calls |
| 3 | "كم مهمة عندي اليوم؟" / "what tasks are pending?" | Classified `operations` → `list_tasks` called → phrased in natural language (not raw JSON) |
| 4 | "أخبرني عن الحصان [real horse name]" / "tell me about horse [name]" | `get_horse` called (by `nameQuery`); if the org has 2+ horses with similar names, the assistant should ask which one instead of guessing |
| 5 | "ما هي خطة رعاية [horse name]؟" | `get_horse` (resolve id) → `get_care_plan` — two chained tool calls in one turn |
| 6 | "أنهِ المهمة رقم [taskId]" / "mark task [id] as done" | `update_task_status` called with `status=completed`; verify in mshalia: task doc updated + an `activity_feed` entry (`task_completed`) + an `agent_audit` entry |
| 7 | Ask about an invoice or a client | Assistant says this isn't available yet (accounting/administration are still no-ops) — must NOT invent an answer |
| 8 | Send a follow-up referencing a previous turn (e.g. "and schedule a vet visit for **her**") | Confirms the Postgres checkpointer is carrying multi-turn context (pronoun resolves from the last 6–8 messages) |
| 9 | From a **group chat** | No identity resolution attempted, no ERP tool calls — confirms the group-chat guard in `server.py` |
| 10 | Unset `NIM_API_KEY` and restart the backend, send any message | Reply: "الخدمة غير مهيأة بعد… / not configured…" — confirms the graceful-degradation path, not a crash |

## What to check in mshalia alongside the demo

- `agent_audit` collection (top-level): one entry per `identity.resolve` and per `tools.*` call, with `outcome` (`ok`/`denied`/`not_found`/`error`) and `latencyMs`.
- `organizations/{orgId}/activity_feed`: a `task_completed` entry after test #6.
- Try an insufficient-role account (`viewer` attempting `update_task_status`, which requires `manager`) → expect `403 FORBIDDEN` in `agent_audit`, and the assistant should surface this as a polite "you don't have permission" rather than a raw error (this exact phrasing isn't implemented yet — see Known Gaps below).

## Known gaps / non-goals for this sprint (by design)

- **No confirmation/approval step.** `update_task_status` executes immediately once the LLM decides to call it — no "are you sure?" gate. This is explicitly deferred (SPRINT-01.md), lands with M4's `interrupt()` flow.
- **No PermissionScope enforcement**, only `minRole` — see `lib/agent-gateway/tools/types.ts` comment in mshalia; scopes are metadata only until `config/rbac` exists.
- **Single org only** — a user in multiple orgs is silently routed to `orgIds[0]`.
- **Tool-call permission errors aren't specially phrased** — a 403 from the Gateway becomes a generic tool-error the LLM sees as any other failure; it may not clearly say "you don't have permission" vs. "something went wrong." Worth a follow-up story if it comes up in testing.
- **Voice-in-this-flow is untested against the new operations tool loop** — the voice pipeline (STT/TTS) was built in parallel by a separate track this sprint; the text path above is what's been reasoned through end-to-end. Worth explicitly testing a voice note through the full identity+tools path, not just text.
