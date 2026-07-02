# Sprint 1 — "First real conversation"

> Cadence: ~2 weeks · Source of priorities: the WhatsApp feature map (P1 rows) reconciled with the [BLUEPRINT roadmap](BLUEPRINT.md) (M2–M4, text-first slice).

## Sprint Goal

**A verified staff member texts the WhatsApp number and gets a real, identity-scoped answer** — intent classified by a real LLM (no more echo), horse/care-plan/task queries answered from live ERP data, and a task markable as complete — all through a minimal, audited ERP Agent Gateway.

## Why this slice first

- The feature map's P1 row — *identity resolver: "blocks everything"* — plus its two highest-ROI flows (horse/care-plan query for owners-by-proxy, task query+complete for staff).
- **Text-first**: transport (gateway ⇄ backend, HMAC) is already built for text; voice (roadmap M1) adds STT risk without changing the architecture, so it moves to Sprint 2. This de-risks the LLM+tools core.
- **Baileys stays** the channel. The feature map's Twilio/Business-API suggestion is recorded as a future `ChannelPort` adapter, not a change of direction.

## Backlog (committed)

| # | Story | Repo | Depends on | Acceptance criteria |
|---|---|---|---|---|
| S1-1 | Gateway skeleton + **identity resolver** | mshalia | — | HMAC-authed `POST /api/agent/v1/identity/resolve`: phone → `{uid, role, orgIds, verified}`; unknown → `resolved:false`; bad signature → 401; every call audited |
| S1-2 | Tool endpoint + **4 tools** (`get_horse`, `get_care_plan`, `list_tasks`, `update_task_status`) | mshalia | S1-1 | allow-list only; Zod-validated; org-scoped; role-checked; audited; unknown tool → 404 |
| S1-3 | **Real LLM nodes** (NVIDIA NIM) in LangGraph | sawt | — | Arabic/English intent classification → routed agent; contextual response; echo gone |
| S1-4 | **Tool-calling loop** + gateway client + identity on inbound | sawt | S1-2, S1-3 | operations subgraph calls the 4 tools with actor assertion; tool errors self-correct; unknown phone gets polite "not linked" reply |
| S1-5 | **E2E demo** + sprint review | both | S1-4 | text → identity → LLM → tool → reply, verified live; BLUEPRINT gap table updated |

## Out of scope (Sprint 2 candidates, from feature map)

Voice in/out (M1) · overdue-invoice push + low-stock push (needs outbound cron infra) · client-facing flows (invoice PDF retrieval, "how is my horse") · payments/incidents writes · confirmation/approval flow (M4) — the gateway policy fields land now, the interrupt() flow next sprint.

## Working agreements

- Gateway is the **only** ERP write path; no `firebase-admin` outside mshalia.
- Every story lands with typecheck clean + small focused commits; mshalia work committed in mshalia.
- Sprint review = live demo against a test WhatsApp number + updated gap table.

---

## Sprint Review

**Delivered:** S1-1 through S1-4, all committed (mshalia: `7599c31`, `dee97d1`; sawt: `a06a9b4`, `df08a55`). typecheck/lint clean on the TS side (mshalia); Python verified via `py_compile` + review, not execution.

**In parallel, outside this sprint board:** a full voice pipeline (`agent/stt.py`, `agent/audio.py`, `agent/db.py`) and the `db-init` → drizzle-kit migration cleanup landed directly on `main`. Net effect: **M1 (voice in/out) is essentially done**, ahead of the original roadmap ordering — pulled into the BLUEPRINT gap table and doesn't need to be Sprint 2 scope.

**Not done — deviation from the plan:** S1-5's acceptance criterion was "verified live." That did not happen — this environment has no installed Python deps, no NIM key, no deployed mshalia, and no paired WhatsApp number. Substituted with:
- [SPRINT-01-VERIFICATION.md](SPRINT-01-VERIFICATION.md) — a 10-step manual test script + known gaps, ready to run.
- The BLUEPRINT gap table updated to `✅ (unverified live)` rather than `✅`, everywhere that's the honest state.

**This is a real gap, not a formality.** Nothing in S1-1–S1-4 has executed end-to-end. Specific risks that only a live run will surface: whether `langchain_openai.ChatOpenAI.bind_tools()` behaves as expected against NIM's actual tool-calling support for the chosen model; whether the phone-normalization candidates in `identity/resolve` actually match real stored formats; whether Firestore silently requires a composite index somewhere the design assumed it wouldn't.

**Carries to Sprint 2:**
1. **Run [SPRINT-01-VERIFICATION.md](SPRINT-01-VERIFICATION.md) for real** — first priority, before adding scope.
2. Confirmation/approval (`interrupt()`) for `update_task_status` and future financial tools (M4 remainder).
3. `PermissionScope` enforcement in the Gateway (currently `minRole` only).
4. Identity cache (currently resolves fresh every message).
5. Voice note through the *tool-calling* path specifically (voice + STT was verified in isolation by the parallel track; not through S1-4's operations loop).
6. Dashboard convergence (M5) and Accounting/Administration tool packs (M6) remain queued, unchanged.
