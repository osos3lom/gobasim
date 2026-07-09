# Sawt Gateway — Feature Backlog & Delivery Record

> **What this is.** The remaining feature backlog for the operator dashboard and observability
> surfaces, plus a record of the Phase A behavioral fixes and the voice-note archive already
> delivered. It consolidates the former `NEXTJS-MIGRATION-PLAN.md` (roadmap gap analysis) and
> `PHASE-A-AND-VOICE-STORAGE.md` (delivery report) into one place.
>
> **For production-readiness status, scores, and the go-live roadmap, see
> [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md)** — that is the authoritative status doc.
> This file is the *feature* backlog (dashboard/UX/observability epics), not the production-gating
> roadmap.
>
> **Companion docs:** [`BLUEPRINT.md`](BLUEPRINT.md) (architecture) · [`DEPLOYMENT.md`](DEPLOYMENT.md)
> (deploy/ops) · [`mshalia-side.md`](mshalia-side.md) (external ERP-gateway brief).

---

## 1. Origin & Method

The original product roadmap (42 features across 8 epics) was written against the **old
three-runtime design** (Next.js dashboard + Python FastAPI/LangGraph backend + separate Go
gateway). This repo replaced all three with a **single Go binary** (`sawt-gateway`) on a 1 GB
GCE instance. Each roadmap feature was gap-analyzed against the code that actually exists.

**Feature migration summary:**

| Bucket | Count | Meaning |
|---|--:|---|
| **Done** (Direct or Modified) | 24 | Exists in Go; verify, don't rebuild. Includes the whole core loop: pair → configure agent → voice conversation (STT → LLM tool loop → TTS) → activity history. |
| **Gap** | 10 | Genuine remaining work — almost all thin dashboard surface over data that already exists (Epics H/O/S/T below). |
| **Not Applicable** | 5 | Obsolete in a single-process design (monorepo tooling, the gateway⇄backend webhook legs + their HMAC channel, local model-folder opener, realtime-call tuning). The HMAC scheme survives on the external ERP channel. |
| **Deferred** | 3 | Habibi/SILMA voice cloning (infeasible in 1 GB), MCP tool-calling (Go-native tool packs cover the need), usage analytics/CSV (premature before live traffic). |

---

## 2. Delivered — Phase A behavioral fixes

Four discrepancies between the roadmap's business rules and the code, all fixed **with regression
tests** (`go build`/`go vet`/`go test ./...` green; `-race` runs in CI on Linux).

| # | Fix | Where | Test |
|---|---|---|---|
| **D1** | New WhatsApp contacts default to **disabled** (explicit operator opt-in before the bot messages a real person); triggering message is dropped. | `main.go` (`newContactParams` + `handleIncomingMessage`) | `TestNewContactParamsDefaultsDisabled` |
| **D2** | Conversation memory honors the agent's `max_history` (clamped `[1,20]`, default 8). | `internal/workflow/memory.go` (`conversationTurnLimit`) | `TestLoadConversationHonorsAgentMaxHistory` |
| **D3** | Logout is `POST` + CSRF (was `GET`, CSRF-able). | `web/server.go`, `web/templates/layout.html` | `TestLogout_*` (405 / 403 / 303+clear) |
| **D4** | Publish gate (non-empty prompt required) + `last_published` stamped only on transition + "unpublished changes" badge. | `web/server.go` (`validatePublishTransition`, `agentRow`), `web/templates/workflow.html`, `query.sql` | `TestValidatePublishTransition` |

**Also hardened:** `config.LoadConfig()` **fails fast** when `SESSION_SECRET` is unset while
`SECURE_COOKIE=true` (production signal) — prevents silent per-restart logout storms.

---

## 3. Delivered — Voice-note archive

Optional, nil-safe archival of WhatsApp voice notes (both directions + operator sends) to a
Firebase/GCS bucket. **Disabled unless `VOICE_STORAGE_BUCKET` is set.** Design highlights
(full detail in `internal/voicenotes/*` and ops steps in [`DEPLOYMENT.md`](DEPLOYMENT.md) §12/§17):

- **Flow:** `Save()` validates (OGG magic + 64 B–16 MB) → spools to disk (`0600`) → inserts a
  `pending` ledger row (`wa_voice_notes`) → nudges a **single** background worker → worker streams
  the spool file to GCS (`io.Copy`, `ChunkSize=256 KB`) → marks `uploaded` → deletes the spool.
- **1 GB budget:** ~256 KB resident per upload regardless of file size; one worker goroutine;
  Postgres `pending` set is the queue (no unbounded channel); per-upload 2-minute timeout.
- **Reliability:** exponential backoff `30s→1h`, terminal `failed` after 5 attempts; spool +
  `pending` row survive restarts; deterministic object names make retries idempotent.
- **Security:** ADC auth (instance SA in prod, `GOOGLE_APPLICATION_CREDENTIALS` in dev); V4
  signed GET-only URLs; path-traversal-safe object names; least-privilege single-bucket IAM
  (`storage.objectAdmin` + `serviceAccountTokenCreator` on the SA for V4 signing).
- **Ops:** set a bucket **lifecycle rule = `RETENTION_DAYS`** to delete the bytes; the daily
  retention job (`PurgeWaVoiceNotesBefore`) deletes the ledger rows — keep the windows aligned.

---

## 4. Remaining Backlog (Epics H / O / S / T)

All items are thin handlers/templates over data, orchestrators, and state that **already exist**.
Estimates are engineer-hours. Each carries the standard DoD: code complete · reviewed · tested ·
production-ready. Recommended order: **H → O → S → T** (health first; everything reads from it).

### Epic H — System Observability & Health

> A passive health aggregator + status surfaces (no paid-API probe traffic). Ties directly to the
> missing `/healthz`/`/metrics` gap in [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) §3.12.

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| H1 | Health aggregator + `GET /api/health` | High | 4h | New `internal/health` package; cached checks (WA `GetConnectionInfo`, `pool.Ping` cached ≥10s, ffmpeg boot result, per-provider `LastResult()`, `voicenotes.Store.Stats()`); one failing check degrades one field, not the whole response. |
| H2 | Status badge in the shell | High | 3h | Badge in `layout.html`; HTMX `hx-trigger="every 30s"` → `/api/health`; text+icon (not color-only); "not configured" vs "failing" vs "ok". |
| H3 | Dashboard home widgets | Medium | 3h | `CountAgentsByStatus`; provider summary from H1; "Create agent"/"Connect WhatsApp" quick-action cards; per-card fallback so one failure doesn't blank the page. |

> **Note:** H1 overlaps with the Phase 1c `/healthz` task in `IMPLEMENTATION-PLAN.md`. Build a
> single health surface that satisfies both (an **unauthenticated** cheap liveness probe **and**
> the richer authenticated snapshot).

### Epic O — Activity Observability

> Filters/pagination + live feed + pipeline-health aggregates over `wa_activity`. No new tables.

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| O1 | Activity filters + pagination | High | 4h | Filtered keyset query (`chat`/`status`/`ts<$before ORDER BY ts DESC LIMIT`); filter controls + "load older" HTMX fragment. |
| O2 | Live activity feed (SSE) | High | 5h | `ActivityBroker` (sibling of `LogBroker`); publish at the `CreateWaActivity` write site; `GET /api/events` SSE (auth); prepend+dedupe; subscriber cap ~10, drop-on-full. |
| O3 | Pipeline-health aggregates | Medium | 4h | `avg(...) FILTER` / error-rate over 1h/24h/7d + previous period for trend; 1-min in-process cache; degraded thresholds; sample-size disclaimer. |
| O4 | Webhook-logs page | Medium | 2h | `GetWebhookLogs(limit)`; read-only, grouped by status class; `input_preview` truncated at write time. |

### Epic S — Settings & Speech Operator Tools

> Settings UI + TTS/STT test panels + history pages + voice-note playback. Write paths already exist.

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| S1 | Global settings page | Medium | 4h | `GET/POST /dashboard/settings` (CSRF); speed clamp `[0.5,2.0]`; `assistant_agent_id` restricted to published agents; validate `bot_config` JSON. |
| S2 | TTS test panel | Medium | 3h | `POST /dashboard/speech/tts-test` (CSRF); reuse orchestrator + `WavToOpus`; 1k-char cap; write `tts_history`; return `audio/ogg`; per-IP rate limit. |
| S3 | STT test panel | Medium | 3h | `POST /dashboard/speech/stt-test` (CSRF, multipart, `MaxBytesReader` 10 MB); transcode + orchestrator (`ar`); write `stt_history`. |
| S4 | TTS/STT history pages | Low | 3h | Keyset pagination (`ts < $before`) on `GetSttHistory`/`GetTtsHistory`; `GET /dashboard/speech`, filterable by model. |
| S5 | Voice-note playback | Low | 3h | `GET /dashboard/voice/{id}/url` → short-TTL V4 signed URL via `voiceStore.SignedURL`; only for `status='uploaded'` rows. |

### Epic T — Agent Testing

| ID | Feature | Priority | Est | Key work |
|---|---|---|--:|---|
| T1 | LLM test action (tool-less) | Medium | 4h | "Test prompt" button on the workflow editor; `POST /dashboard/workflows/{id}/test` (CSRF); one LLM call with `tools=nil`; 30s timeout; per-IP rate limit; ephemeral (never written to `conversation_turns`/`wa_activity`). |

---

## 5. Deferred & Not-Applicable

**Deferred (decision-gated):**
- **Voice cloning (Habibi/SILMA)** — a branded clone voice is infeasible in 1 GB RAM; the
  generic STT/TTS provider cascade is the deliberate substitute (a product decision to confirm —
  see `IMPLEMENTATION-PLAN.md` §6 5g).
- **MCP tool-calling adapter** — Go-native declarative tool packs cover the need today.
- **Usage analytics / CSV export** — premature before live traffic exists to measure.

**Not Applicable (obsolete under the single-process design):**
- Monorepo workspace/build scaffolding (one `go build`).
- The gateway⇄backend `/webhook/inbound` ↔ `/send` legs and their `GATEWAY_SHARED_SECRET` HMAC
  channel (same process now — nothing to forward to).
- Local model-folder opener; realtime-call tuning knobs (no separate realtime service).

---

## 6. Risk Register (feature work)

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | D1 surprises operators (chats stop auto-answering) | Med | Med | Release note; contacts toggle exists; intended posture. |
| R2 | GCS init/permission failure at boot | Med | Low | Init only warns; archival disabled ≠ assistant down; nil-safe store. |
| R3 | Spool dir fills during a long GCS outage | Low | Med | Bounded by `pending` backlog × 16 MB; monitor pending count; spool on instance disk. |
| R4 | Binary size (~63 MB with GCS client) | Low | Low | Disk-only, mmap'd; no RAM impact; e2-micro disk ≥10 GB. |
| R5 | V4 signing needs `serviceAccountTokenCreator` on GCE | Med | Med | Documented IAM step; without it, playback (S5) degrades but archival still works. |
| R6 | Retention window drift (rows vs objects) | Low | Low | Align bucket lifecycle rule with `RETENTION_DAYS`. |
| R7 | Whole stack unverified live (M9) | High | High | Live verification is the top priority before onboarding real users — see `IMPLEMENTATION-PLAN.md` Phase 2a. |

---

## 7. Recommended Order

1. **Live verification (M9)** — the production-gating priority; see `IMPLEMENTATION-PLAN.md` Phase 1 + 2a.
2. **Epic H** (H1 → H2 → H3) — health first; everything else reads from it.
3. **Epic O** (O1 → O4) — observability over live traffic.
4. **Epic S** (S1 → S5) — operator tools, including voice playback.
5. **Epic T** (T1) — agent testing.
6. **Decision-gated / deferred** — voice-clone provider, MCP adapter, usage analytics, versioned
   migrations, multi-operator accounts.
