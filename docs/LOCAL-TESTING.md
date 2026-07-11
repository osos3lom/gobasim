# Local Testing — Exercise the Workflow Against a Local `mshalia`

> Goal: run and verify `sawt-gateway` on your Windows machine, wired to a **locally-running
> `mshalia` ERP** and a **real LLM + STT** (NIM + Groq), before deploying to GCP. mshalia's Agent
> Gateway is implemented (`app/api/agent/v1/*`), speaks the exact HMAC contract our Go client uses
> ([`mshalia-side.md`](mshalia-side.md)), and reaches a live Firestore — so we test against the real
> ERP, not a stub. An offline mock (`cmd/mockerp`) is the fallback (§7).
>
> **Verified so far:** the direct horse workflows (§3) ran green against the real gateway + Firestore
> (`org-demo`): `list_horses → register_horse → list_horses`, count `1 → 2`.

## What talks to what

```
cmd/erpcheck ─┐  (direct: HMAC-signed tool calls, no LLM)         Groq (STT)   NIM (LLM)
cmd/wfcli    ─┼─ HMAC ─▶ mshalia  http://localhost:3000 ─▶ Firestore    ▲          ▲
go run .     ─┘  (full app: WhatsApp → STT → LLM → tools → reply)  ──────┘──────────┘
```

Same HMAC on both sides (`HMAC-SHA256(secret, "{ts}.{rawBody}")`, `x-swa-signature` /
`x-swa-timestamp`, ±5 min) — `AGENT_GATEWAY_SECRET` just has to match.

---

## 1. Requirements & status

| Component | Needs | Status on this machine |
|---|---|---|
| Go tools (`erpcheck`, `wfcli`, `go run .`) | Go 1.25+, this repo's `.env` | ✅ Go 1.26; `.env` points at `:3000`, secret matched |
| **LLM** (reasoning) | `NIM_API_KEY` (primary) or `OPENAI_API_KEY` (fallback) | ✅ `NIM_API_KEY` set |
| **STT** (voice-in) | `GROQ_API_KEY` / `HF_API_KEY` / `GCP_API_KEY` | ✅ `GROQ_API_KEY` set (Whisper) |
| **TTS** (voice-out) | `GCP_API_KEY` (Google) / `HF_API_KEY` / local gTTS | ⚠️ none set → voice replies fall back to text |
| **mshalia** | `node_modules`, Firebase Admin creds, matching secret, `npm run dev` | ✅ deps + creds set; **restart with `npm run dev`** |
| **Firestore data** | a resolvable actor + horses in an org | ✅ 2 `super_admin` users + org **`org-demo`** (see §6 caveat) |
| **`DATABASE_URL`** | a Neon branch for conversation memory | ⚠️ currently the **prod** branch — see the warning in §4 |
| **ffmpeg** | on `PATH` (voice transcoding) | ✅ installed |

**Already wired for you:** a shared `AGENT_GATEWAY_SECRET` in both `mshalia/.env.local` and this
repo's gitignored `.env`; `MSHALIA_API_URL=http://localhost:3000`; `SECURE_COOKIE=false`.

**Start mshalia** (own terminal, from `C:\Users\Asus\Documents\GitHub\mshalia`):

```powershell
npm run dev          # Next.js dev server on http://localhost:3000  (deps already installed)
```

### Audit hardening applied (all fixed & verified)

The agentic-gateway audit ([`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) §5) — **all Blocker
(B1–B3), Critical (C1–C7), and Minor (M1–M7) items are implemented** (`go build`/`vet`/`test`
clean). What that changes for local testing:

- **New optional env vars:** `MAX_INFLIGHT` (default 32, concurrent-handler cap) and `LOG_FORMAT`
  (`json` for structured logs; default text). Both are optional.
- **New unauthenticated endpoints** on `go run .`: `/healthz`, `/readyz`, `/metrics` (test them in §5).
- **Schema auto-migrates on boot** — adds `pending_confirmations.status/claimed_at`, plus new
  `processed_messages` (inbound dedup, C1) and `tool_executions` (durable step log, C2) tables.
- **Behaviors now in effect:** a redelivered WhatsApp message is **skipped** (C1); a second risky
  request while one is pending is **refused, not overwritten** (C7); financial writes retry safely
  with a deterministic idempotency key (B3); `Ctrl+C` drains in-flight work (B1).
- **Caveat that survives:** `register_horse` is *not* idempotency-keyed on either side, so a retried
  `-add` under a transient error could create two horses — fine for one-shot tests, noted.

---

## 2. Tier 0 — static checks (no services)

```powershell
go build ./... ; go vet ./... ; go test ./...
```

151 test functions across 24 files, including the 7-scenario eval suite (`internal/workflow/eval_test.go`, fakes
— no live services), the confirmation-overwrite regression, and full fake-based unit coverage for
the AI speech providers (`internal/speech/...` — see §3.5). This is the regression net.

---

## 3. Tier 1 — horse workflows, direct (erpcheck, no LLM) ✅ verified

`cmd/erpcheck` calls the gateway with our real signing client. Needs only `MSHALIA_API_URL` +
`AGENT_GATEWAY_SECRET`. Because every user's `orgIds` map is empty in this Firestore, pass a
`super_admin` uid + org directly (super_admin bypasses org-membership and gets all scopes):

```powershell
# list + count (read-only)
go run ./cmd/erpcheck -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo

# list, add a horse, re-count (writes a real, soft-deletable horse to org-demo)
go run ./cmd/erpcheck -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo -add -name "Barq (sawt test)"

# or resolve identity from a real staff phone once one has a non-empty orgIds map:
go run ./cmd/erpcheck -phone 9665XXXXXXXX -add
```

**Proves:** HMAC contract, `identity/resolve`, role/scope enforcement, `list_horses` (Firestore
read), `register_horse` (Firestore write + activity event). The ERP half, end-to-end, no AI.

---

## 3.5. Tier 1.5 — AI speech providers, direct (`aicheck`) 🆕

`internal/speech/providers/` now has full unit coverage (`go test ./internal/speech/...` — HF,
Google REST, and the new ADC-based Google provider, all against fakes, zero live calls, zero
secrets required). This tier is the **live** counterpart: `cmd/aicheck` calls the real HF
Inference API, the real Google REST STT/TTS endpoints, the real Google Speech/TTS gRPC APIs (via
Application Default Credentials), and a real GCS bucket, directly — no LLM, no WhatsApp, same
narrative PASS/FAIL/SKIP style as `cmd/erpcheck`. It never fails the build; a missing key just
prints `SKIP`.

```powershell
go run ./cmd/aicheck                          # every provider with creds present in .env
go run ./cmd/aicheck -only hf,google-adc,gcs   # restrict to specific providers
go run ./cmd/aicheck -edge-cases               # + the QA edge-case checklist below
go run ./cmd/aicheck -bucket my-qa-bucket      # override VOICE_STORAGE_BUCKET for the GCS check
```

**Credentials it reads** (all optional — present ones get checked, absent ones `SKIP`):
`HF_API_KEY`, `GCP_API_KEY`, `GOOGLE_APPLICATION_CREDENTIALS` (a service-account JSON path —
this is the ADC path used by `GoogleADCProvider` and by `internal/voicenotes`'s `GCSUploader`;
note it is **not** the same auth mechanism as `GCP_API_KEY`, which is a separate REST-with-query-
param provider), `VOICE_STORAGE_BUCKET`. **Point `VOICE_STORAGE_BUCKET` at a dedicated QA bucket,
not production** — the GCS check uploads real (tiny, harmless) objects under a `qa/` prefix and
does not delete them.

**Edge-case checklist** (`-edge-cases`, also unit-tested without live calls in
`internal/speech/providers/*_test.go`): HF 503 "model loading", network timeout, empty/zero-byte
audio, unsupported codec / malformed payload, malformed base64 in a TTS response, GCS
permission-denied / nonexistent bucket, missing/invalid credentials, 429 rate-limiting, oversized
(413) payload, empty/non-UTF8 TTS text, concurrent requests (`-race`), and STT/TTS
fallback-chain correctness (short-circuit on success, all-fail wrapping, TTS's
zero-bytes-counts-as-failure rule).

**Opt-in live `go test` tier** (same real services, run from the test suite instead of the CLI):

```powershell
$env:RUN_LIVE_AI_TESTS = "1"
go test ./internal/speech/... ./internal/voicenotes/... -run Live -v
```

Every `Live`-prefixed test skips cleanly (not a failure) when `RUN_LIVE_AI_TESTS` or the relevant
key is unset, so this tier is always safe to leave un-run — it's opt-in, and CI never sets the
env var, so it's automatically excluded from `go test ./...`.

---

## 4. Tier 2 — AI reasoning (wfcli + NIM) 🆕

Now that `NIM_API_KEY` is set, drive the **same tools through the real LLM** — the exact reasoning
path an inbound WhatsApp message takes (identity → classify intent → role-filtered tool loop →
reply), minus the WhatsApp/voice transport. This is the AI test.

> [!WARNING]
> `wfcli` writes conversation turns to `DATABASE_URL`, which is currently the **production** Neon
> branch. Point `.env`'s `DATABASE_URL` at a **Neon dev branch** first so test chatter (conversation
> turns + `tool_executions` rows) doesn't land in prod. `register_horse` here also writes a real
> horse to `org-demo`.

```powershell
# reads — classify → list_horses → phrased answer
go run ./cmd/wfcli -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo "how many horses do we have?"
go run ./cmd/wfcli -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo "list the horses"
go run ./cmd/wfcli -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo "أخبرني عن الخيول"   # Arabic

# a write → medium-risk → confirmation gate
go run ./cmd/wfcli -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo "register an Arabian stallion named Barq, bay colour"
go run ./cmd/wfcli -uid 2PRVxnhr2kYolF4eCjQ3pq7fO5u1 -org org-demo "yes"   # confirms → executes

# RBAC + routing (use -role to inject an identity, skipping identity/resolve)
go run ./cmd/wfcli -role viewer  -org org-demo "register a horse named Test"   # viewer: refused (no write tool)
go run ./cmd/wfcli -role client  "ما هو رصيدي؟"                                  # routes straight to the client agent
```

`wfcli` prints the classified **intent**, the **tool calls** (with mshalia's real responses), and
the final **reply**.

**AI-path checklist — what to verify:**
- [ ] "how many / list horses" → intent `operations`, `list_horses` called, count phrased in the user's language.
- [ ] Arabic prompt → Arabic reply (same-language rule).
- [ ] "register …" → **confirmation question** first (nothing written); `yes`/`نعم` then executes; anything else cancels.
- [ ] A second risky request before confirming the first → "resolve the pending one first" (C7), not an overwrite.
- [ ] `-role viewer` → write tools filtered out (RBAC); `-role client` → client self-service agent.
- [ ] NIM primary works; if you blank `NIM_API_KEY` with no `OPENAI_API_KEY`, wfcli prints `no LLM providers configured`.
- [ ] Consecutive prompts on the same `-uid` carry context (conversation memory).

---

## 5. Tier 3 — full app: dashboard, observability, WhatsApp voice (Groq STT)

```powershell
go run .
```

Expect: schema bootstrap, a seeded admin password on **stderr** (M2 — not in the log stream), a
WhatsApp QR in the terminal, and the dashboard on `http://localhost:8080`.

**Observability endpoints (C3) — verify in a second terminal:**

```powershell
curl http://localhost:8080/healthz     # {"status":"ok"}                         (liveness)
curl http://localhost:8080/readyz      # {"ready":true,"db":true,"whatsapp":"…"}  (503 if DB down)
curl http://localhost:8080/metrics     # {"uptime_seconds":…,"goroutines":…,"whatsapp_state":…,…}
```

Set `LOG_FORMAT=json` in `.env` to see the structured (C4) log lines in the terminal and the
dashboard's live log page.

**Voice path (Groq STT):** scan the QR with a **spare** WhatsApp number (or `PAIR_PHONE_NUMBER`),
**enable the test contact** in the WhatsApp page (new contacts default to disabled), then send the
number a **voice note** in Arabic. Watch (log lines share a `[trace=<msgid>]` prefix):
`download → OggToWav → Groq Whisper transcript → identity → intent → tool → reply`.

> TTS has no cloud provider configured (`GCP_API_KEY` empty), so voice **replies** fall back to
> **text**. Set `GCP_API_KEY` to test voice-out. Send the **same** voice note twice to confirm the
> inbound-dedup (C1) skips the redelivery.

---

## 6. The Firestore data dependency (read this)

For the horse workflows you need, in the Firebase project mshalia points at (`meshalia`):

1. **A resolvable actor** — a `users/{uid}` doc with `phone`, `role`, and a non-empty
   `orgIds: { "<orgId>": true }` map.
2. **An org with horses** — `organizations/{orgId}/horses/*`.

**What's actually there today:** 4 users — two `super_admin` (`0562637777`, `0546906905`) and two
`client` — but **all have an empty `orgIds` map**, so phone-based `identity/resolve` returns *no
org*. Orgs: **`org-demo`** (مربط المشعلية, had 1 horse) and one empty org.

So for now use `-uid <super_admin> -org org-demo` (super_admin bypasses the org-membership check).
To exercise the **phone → identity** path (and the WhatsApp flow), give a staff user a non-empty
`orgIds` map, or seed a `manager` user with a phone. A `client`-role phone currently 500s on
`identity/resolve` — see the missing-index note in §8.

---

## 7. Offline fallback — `cmd/mockerp`

When mshalia can't run (offline / no Firebase), start the bundled mock gateway — same HMAC
contract, canned horse data, always authorizes:

```powershell
$env:AGENT_GATEWAY_SECRET="devsecret"; $env:MOCK_ROLE="manager"; go run ./cmd/mockerp   # :3001
# then set MSHALIA_API_URL=http://localhost:3001 + AGENT_GATEWAY_SECRET=devsecret in .env
go run ./cmd/erpcheck -uid u1 -org org_test -add
go run ./cmd/wfcli -role manager -org org_test "how many horses do we have?"
```

Exercises *our* side (signing, engine, LLM) without the real ERP. **Not** a substitute for
Firestore testing.

---

## 8. Remaining items / known gaps

**All agentic-gateway audit findings are resolved** (B1–B3, C1–C7, M1–M7). What still needs
attention for a full local M9 / go-live:

| Item | Status | Note |
|---|---|---|
| Firestore actors have empty `orgIds` | ⚠️ data | Use `super_admin` + `org-demo`, or seed a staff user with `orgIds` for the phone/WhatsApp path. |
| `client`-role phone `identity/resolve` 500s | ⚠️ mshalia | Missing Firestore composite index on the `clients` collection-group query (staff/`super_admin` path is fine). |
| TTS provider not configured | ⚠️ config | No `GCP_API_KEY`/`HF_API_KEY` → voice **replies** fall back to text. Fine for reasoning tests. |
| `DATABASE_URL` = prod branch | ⚠️ config | Point at a Neon **dev branch** before running `wfcli`/`go run .` so test data doesn't hit prod. |
| Resumable state machine · saga/compensation · deterministic replay | ⛔ out of scope | Larger design work, explicitly **not** in the audit's Critical/Minor lists — the path from "router + confirmation gate" to a fully durable workflow engine. |
| Live M9 sign-off over real WhatsApp | ⛔ pending | Tier 3 with a paired number + `GCP_API_KEY` for the full voice round-trip. |

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `no LLM providers configured` (wfcli) | `NIM_API_KEY` (or `OPENAI_API_KEY`) missing/blank in `.env`. |
| `identity/resolve error: … secret not set` | `AGENT_GATEWAY_SECRET` empty in `.env`. |
| mshalia logs `Invalid signature` (401) | The two `AGENT_GATEWAY_SECRET` values differ — re-sync `.env` ↔ `mshalia/.env.local`. |
| `identity/resolve` returns 500 | Client-fallback query needs a Firestore index (§8). Use `-uid`/`-org` or a staff phone. |
| `-phone` did NOT resolve | No `users` doc with that phone (or empty `orgIds`) — use `-uid`/`-org` (§6). |
| `403 FORBIDDEN` on `register_horse` | Resolved user below `manager` / lacks `approve_services`. |
| `connection refused` to `:3000` | mshalia isn't running (`npm run dev`). |
| STT fails but text works | `GROQ_API_KEY` missing, or ffmpeg not on `PATH`. |
| `aicheck` prints all `SKIP` | No AI credentials in `.env` — that's the correct behavior, not a bug. Set `HF_API_KEY` / `GCP_API_KEY` / `GOOGLE_APPLICATION_CREDENTIALS` / `VOICE_STORAGE_BUCKET` to enable each check. |
| `google-adc` checks fail with an ADC/ Application Default Credentials error | `GOOGLE_APPLICATION_CREDENTIALS` doesn't point at a valid service-account JSON, or that service account lacks the Speech-to-Text / Text-to-Speech API roles — this is a **different** credential path than `GCP_API_KEY` (REST). |
| `RUN_LIVE_AI_TESTS=1 go test ... -run Live` shows nothing but `SKIP` | Same as above — set the specific env var each `Live` test needs; it's designed to skip, not fail, when absent. |
