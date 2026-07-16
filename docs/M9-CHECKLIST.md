# M9 — Live Verification Checklist & Run Log

> **Purpose.** A concrete, repeatable runbook to verify the whole `sawt-gateway` pipeline against
> **live** services, plus the log of the latest run. M9 is the last production gate — see
> [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) §6 Phase 2a.
>
> **Two execution surfaces:**
> - **Programmatic (this checklist, §A–§B):** `cmd/erpcheck` (no LLM) and `cmd/wfcli` (full
>   reasoning) drive the exact same identity→classify→tool-loop path an inbound WhatsApp message
>   takes, minus the WhatsApp/voice transport. **Fully runnable from a dev box** against a local or
>   deployed `mshalia`.
> - **Real WhatsApp (§C):** pair a physical device and send real voice/text. Requires a human with
>   the phone — cannot be scripted.

---

## Prerequisites

| Need | How | Verify |
|---|---|---|
| `mshalia` ERP running | `npm run dev` in `../mshalia` (port 3000) | `curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:3000/api/agent/v1/identity/resolve -d '{}'` → `401` (HMAC enforced = up) |
| Shared secret matches | `AGENT_GATEWAY_SECRET` identical in `gobasim/.env` and `mshalia/.env.local` | signed calls return non-401 |
| `MSHALIA_API_URL` | `http://localhost:3000` (local) or the deployed URL | `grep MSHALIA_API_URL .env` |
| LLM key | `NIM_API_KEY` (primary) or `OPENAI_API_KEY` | `wfcli` doesn't print "no LLM providers configured" |
| `DATABASE_URL` | a Neon branch (memory + confirmations). **Neon serverless cold-starts — the first `wfcli` call may need a retry.** | `wfcli` doesn't fail "failed to ping database" |
| `DEFAULT_ORG_ID` | `org-demo` — the fallback org for privileged (super_admin/admin/owner) numbers that resolve with no org | `grep DEFAULT_ORG_ID .env` |
| Firestore actor | a resolvable `super_admin` phone, e.g. `966546906905` | §A step 1 |

> **Test actor used below:** `super_admin` phone `966546906905` → uid `LmkDXIlpuqOfqlB4MTUppoe4lfs1`.
> It resolves with an **empty `orgIds`**, which is exactly why `DEFAULT_ORG_ID` exists (see the
> `internal/erp/fallback.go` `ApplyDefaultOrg` note). For direct tool calls, bypass identity with
> `-uid <uid> -org org-demo`.

---

## §A — ERP path, direct (no LLM) — `cmd/erpcheck`

```powershell
# 1. Identity resolve (proves HMAC + identity/resolve). Super_admin resolves but org is EMPTY —
#    this reproduces the live blocker; erpcheck's -phone path does NOT apply DEFAULT_ORG.
go run ./cmd/erpcheck -phone 966546906905

# 2. Direct tool call, read-only (bypass identity with explicit uid+org):
go run ./cmd/erpcheck -uid LmkDXIlpuqOfqlB4MTUppoe4lfs1 -org org-demo
#    → expect: "list_horses: N horse(s)" with real Firestore rows.

# 3. Direct write (registers a real, soft-deletable horse), then re-count:
go run ./cmd/erpcheck -uid LmkDXIlpuqOfqlB4MTUppoe4lfs1 -org org-demo -add -name "Barq (sawt test)"
```

**Pass:** step 2 lists real horses; step 3 count goes N → N+1.

---

## §B — Full reasoning path (identity → classify → tool loop → reply) — `cmd/wfcli`

`wfcli` prints the classified intent, the tool calls (with mshalia's real JSON responses), and the
final reply. Use `-phone` to exercise real `identity/resolve` + the `DEFAULT_ORG` fallback, or
`-role <role> -org <org>` to inject a synthetic identity and test RBAC/routing.

```powershell
# B1. Read (English): classify → list_horses → phrased answer
go run ./cmd/wfcli -phone 966546906905 "how many horses do we have?"

# B2. Read (Arabic): same-language rule
go run ./cmd/wfcli -phone 966546906905 "كم عدد الخيول لدينا؟"

# B3. RBAC: a viewer cannot register (write tool filtered out, fail-closed)
go run ./cmd/wfcli -role viewer -org org-demo "register an Arabian stallion named X, bay"

# B4. Write + confirmation gate (two turns, SAME -chat so the pending confirmation is shared).
#     Both names given explicitly here for a clean single-turn run; giving only the Arabic name
#     now also works — enforceRequiredArgs derives nameEn and asks for nameAr if even that's
#     missing, instead of failing post-confirm (Finding F-1, resolved).
go run ./cmd/wfcli -phone 966546906905 -chat 966546906905@s.whatsapp.net "register a horse: English name Najm, Arabic name نجم, breed Arabian, colour grey, gender stallion"
go run ./cmd/wfcli -phone 966546906905 -chat 966546906905@s.whatsapp.net "نعم"   # confirm → executes

# B5. Read-back the write independently:
go run ./cmd/erpcheck -uid LmkDXIlpuqOfqlB4MTUppoe4lfs1 -org org-demo
```

**Pass:** B1/B2 answer the count in the user's language; B3 refuses (no write tool); B4 asks a
confirmation **first** (no write), then executes on "نعم"; B5 shows the new horse.

---

## §C — Real WhatsApp (human-in-the-loop — NOT scriptable)

```powershell
go run .                    # boots the daemon + dashboard on :8080; prints a QR
```

1. Open `http://localhost:8080/dashboard/whatsapp`, scan the QR with a **spare** WhatsApp number.
2. **Enable the test contact** (new contacts default to disabled).
3. Send a **voice note** in Arabic; watch the `[trace=<msgid>]` log line through
   `download → OggToWav → Groq Whisper → identity → intent → tool → reply`.
4. Send the **same** voice note twice → the second is skipped (inbound dedup, C1).
5. Set `GCP_API_KEY` (or `HF_API_KEY`) to get a **voice** reply; without it, replies fall back to text.

**Pass:** an Arabic voice note round-trips to a correct reply; a write asks for confirmation first.

---

## Latest run log — 2026-07-13, against **local** `mshalia` (port 3000)

Actor: `super_admin` `966546906905`. LLM: NIM `meta/llama-3.1-70b-instruct`. DB: Neon (eu-west-2).

| # | Check | Result | Notes |
|---|---|---|---|
| 0 | **mshalia has all 39 tools** | **PASS** | `lib/agent-gateway/tools/*` register **39 ids, matching the Go client id-for-id** (exact diff, 0 delta). Real handlers, incl. `record_expense`/`record_payment` with GL posting + idempotency (the "not built" header comment in `accounting.ts` is stale). HMAC (`hmac.ts`) matches the Go contract exactly (±5 min, ms ts, `x-swa-*`). `identity/resolve` implemented (users → clients collection-group fallback). |
| 1 | HMAC + `identity/resolve` (`erpcheck -phone`) | **PASS** | `966546906905` → uid `LmkDXIlpuqOfqlB4MTUppoe4lfs1`, role `super_admin`, **org empty** (reproduces the blocker; `erpcheck -phone` intentionally doesn't apply `DEFAULT_ORG`). |
| 2 | Direct `list_horses` (`erpcheck -uid -org`) | **PASS** | Live Firestore read: 2 horses (Barq, Mazen). |
| 3 | Full path read (`wfcli -phone`, EN) | **PASS** | `DEFAULT_ORG` fallback applied → `org-demo`; classify → `list_horses` → **self-corrected** a bad first arg → "We have 2 horses." |
| 4 | Full path read (Arabic) | **PASS** | "لدينا ٢ خيول." (same-language). |
| 5 | RBAC (`-role viewer` write) | **PASS** | `register_horse` filtered out; model declines. Fail-closed. |
| 6 | Write confirmation **gate** | **PASS** | `register …` parked the tool call and asked for confirmation in Arabic — **no write**. |
| 7 | Post-confirm execute (bad args) | **FAIL → Finding F-1** | On "نعم", the parked args were `{name, breed, color, gender}` — the model sent `name` instead of the required `nameEn`+`nameAr`; mshalia correctly returned `VALIDATION_ERROR`. A confirmation-gated write **cannot self-correct** (args frozen at park time). |
| 8 | Write with well-formed args | **PASS** | Explicit EN+AR names → parked → "نعم" → "تم … ✅"; independent read-back shows **2 → 3** ("Najm" persisted). Park→confirm→execute→persist is sound. |

**Net:** the full ERP path (identity + `DEFAULT_ORG` fallback + classify + tool loop + confirmation
gate + live read **and** write) is **verified against the local `mshalia`**. The transport/speech
half (real WhatsApp voice, §C) was validated in the earlier partial run
([`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) §6). Remaining for a full M9 sign-off: run §B/§C
against the **deployed** `mshalia`, and the real-WhatsApp voice round-trip (§C).

> **Test data note:** this run created one real (soft-deletable) horse **"Najm"** in `org-demo`.
> Remove it via the `mshalia` UI if you want a clean count.

### Findings

- **F-1 — Confirmation-gated writes can't self-correct malformed model args. RESOLVED.** The inline
  (low-risk read) loop feeds tool errors back to the model for a retry; the confirmation path used to
  freeze the parked args at confirm time and execute once. When the model emitted schema-non-conforming
  args (here `name` vs the required `nameEn`/`nameAr` for `register_horse` — a model-quality issue with
  llama-3.1-70b for some phrasings, **not** a schema/contract mismatch: the Go schema and mshalia
  Zod agree), the post-"yes" execution failed with `Invalid arguments` and the user had to restart.
  **Fixed** via option (a) — `enforceRequiredArgs` (`internal/workflow/clarification.go`) validates
  parsed tool-call args against the tool's own required-fields schema **before** the risk/confirmation
  gate, auto-derives fields where a derive rule is configured (e.g. `nameEn` transliterated from
  `nameAr`), and durably parks a "collecting" row asking the user for anything still missing across
  turns. See `TestEnforceRequiredArgs_MissingFieldsAsksUser` and
  `TestEnforceRequiredArgs_DeriveSuccessProceedsToRiskGate` in `clarification_test.go`.
