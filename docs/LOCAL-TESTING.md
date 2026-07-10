# Local Testing — Exercise the Workflow Against a Local `mshalia`

> Goal: run and verify `sawt-gateway` on your Windows machine, wired to a **locally-running
> `mshalia` ERP**, before deploying to GCP. The mshalia **Agent Gateway is implemented**
> (`app/api/agent/v1/*`), speaks the exact HMAC contract our Go client uses
> ([`mshalia-side.md`](mshalia-side.md)), and reaches a live Firestore — so we test against the
> real ERP, not a stub. An offline mock (`cmd/mockerp`) is kept as a fallback (§6).
>
> **First test target:** the horse workflows — *list the horses*, *how many horses*, *add a horse*.

## What talks to what

```
cmd/erpcheck ─┐  (direct: HMAC-signed tool calls, no LLM)
cmd/wfcli    ─┼─ HMAC ─▶ mshalia  http://localhost:3000  ─▶ Firestore
go run .     ─┘  (full app: WhatsApp + LLM + STT/TTS)      /api/agent/v1/{identity,tools}
```

The HMAC scheme is identical on both sides (`HMAC-SHA256(secret, "{ts}.{rawBody}")`,
`x-swa-signature` / `x-swa-timestamp`, ±5 min), so `AGENT_GATEWAY_SECRET` just has to match.

---

## 1. Requirements & one-time setup

| Component | Needs | Status on this machine |
|---|---|---|
| Go tools (`erpcheck`, `wfcli`, `go run .`) | Go 1.25+, this repo's `.env` | ✅ Go 1.26; `.env` created (points at `:3000`) |
| **mshalia** | `node_modules`, Firebase Admin creds, `AGENT_GATEWAY_SECRET`, `npm run dev` | ✅ deps installed; Firebase creds ✅; secret ✅ matched |
| **Firestore data** | a resolvable actor + horses in an org | ✅ found: 2 `super_admin` users + org **`org-demo`** (1 horse). See §5 for the caveat (all users have empty `orgIds`). |
| LLM (only for the AI path, `wfcli` / `go run .`) | `NIM_API_KEY` or `OPENAI_API_KEY` in `.env` | ⚠️ you supply |

**Already done for you:**
- A shared `AGENT_GATEWAY_SECRET` was generated and written to **both** `mshalia/.env.local` and
  this repo's `.env` (they match).
- This repo's `.env` was created from `.env.production` with `MSHALIA_API_URL=http://localhost:3000`
  and `SECURE_COOKIE=false`. It is gitignored.

**Start mshalia** (from `C:\Users\Asus\Documents\GitHub\mshalia`):

```powershell
npm install          # one-time (running now)
npm run dev          # Next.js dev server on http://localhost:3000
```

mshalia needs its Firebase Admin creds (`FIREBASE_CLIENT_EMAIL`, `FIREBASE_PRIVATE_KEY` — already
set in its `.env.local`) to reach Firestore. Leave it running in its own terminal.

### Recent hardening (agentic-gateway audit)

[`AGENTIC-GATEWAY-AUDIT.md`](AGENTIC-GATEWAY-AUDIT.md) landed three Blocker fixes (build/vet/test
**✅ verified**) that change a few things this plan assumes:

- **B1 — graceful shutdown + HTTP timeouts + bounded fan-out.** Optional env var **`MAX_INFLIGHT`**
  (default 32) caps concurrent message handlers; `go run .` now drains in-flight work on Ctrl+C / SIGTERM.
- **B2 — atomic confirmation claim.** `pending_confirmations` gains `status` + `claimed_at` columns,
  applied automatically on boot (idempotent `ALTER TABLE`). No action needed — two concurrent "نعم"
  replies can no longer double-execute a write.
- **B3 — ERP retry/backoff + deterministic idempotency.** `CallTool` / `ResolveIdentity` now retry
  transient failures (429 / 5xx / transport, ~200 ms→3 s ×3) and send `x-swa-idempotency-key` +
  `x-swa-trace-id` headers. mshalia already reads the trace id and dedups financial writes on
  `args.idempotencyKey`, so retries are safe for `record_expense` / `record_payment`. **Caveat:**
  `register_horse` is *not* idempotency-keyed on either side, so a retried `-add` under a transient
  error could create two horses — fine for a one-shot local test, noted for awareness.

Still-open audit items you may observe locally: no inbound dedup on `evt.Info.ID` (**C1** — a
redelivered WhatsApp message re-runs the pipeline) and single-slot confirmations (**C7** — a second
risky request overwrites the first pending one).

---

## 2. First test — horse workflows (direct, no LLM)

`cmd/erpcheck` calls the gateway with our real signing client and runs the three horse workflows.
It needs only `MSHALIA_API_URL` + `AGENT_GATEWAY_SECRET` (both already in `.env`).

```powershell
# list the horses + how many (resolve identity from a staff phone)
go run ./cmd/erpcheck -phone 9665XXXXXXXX

# list, add a horse, then re-count to confirm +1
go run ./cmd/erpcheck -phone 9665XXXXXXXX -add -name "Barq"

# skip identity resolution if you already know a valid uid + org:
go run ./cmd/erpcheck -uid <userUid> -org <orgId> -add
```

Expected output:

```
ERP gateway: http://localhost:3000
identity:    uid=abc123 role=manager org=org_equine_01
list_horses: 7 horse(s)
   - Najm  (Arabian, stallion, active)
   - Sahra (Arabian, mare, active)
   ...
register_horse: adding "Barq" ...
register_horse OK: {"id":"xyz789","nameEn":"Barq","nameAr":"Barq"}
list_horses: 8 horse(s)
   ...
=> horse count: 7 -> 8  (+1)
```

**What this proves:** the HMAC contract, `identity/resolve` (phone → real user), role/scope
enforcement, `list_horses` (read against Firestore), and `register_horse` (write to Firestore +
activity event). This is the ERP half of the workflow, end-to-end, with no AI in the loop.

> `register_horse` requires the resolved user to be **role ≥ manager** with the `approve_services`
> scope; `list_horses` needs **role ≥ viewer** with `view_horses`. A `403 FORBIDDEN` means the
> user's role/scopes are too low.

---

## 3. AI-driven horse workflow (adds the LLM)

Once §2 works, drive the *same* tools through the reasoning engine — the exact path an inbound
WhatsApp message takes. Put an LLM key in `.env` first (`NIM_API_KEY=…` or `OPENAI_API_KEY=…`).

```powershell
go run ./cmd/wfcli -phone 9665XXXXXXXX "how many horses do we have?"
go run ./cmd/wfcli -phone 9665XXXXXXXX "list the horses"
go run ./cmd/wfcli -phone 9665XXXXXXXX "register a new Arabian stallion named Barq, bay colour"
go run ./cmd/wfcli -phone 9665XXXXXXXX "yes"          # confirms the register (medium-risk → gated)
```

`wfcli` resolves the identity via mshalia, lets the LLM classify intent → `operations`, call
`list_horses` / `register_horse`, and phrase a reply. It prints the intent, the tool calls (with
mshalia's real responses), and the final reply. `register_horse` is medium-risk, so the engine asks
for confirmation first — reply `yes`/`نعم` to execute.

> `wfcli` also needs `DATABASE_URL` (already in `.env`) for conversation memory. Use a Neon **dev
> branch**, not prod. Pass `-role manager` to inject an identity if you want to skip mshalia's
> `identity/resolve`.

---

## 4. Full pipeline over WhatsApp (local M9)

```powershell
go run .    # boots the app: schema bootstrap, dashboard on :8080, WhatsApp QR in the terminal
```

Scan the QR with a **spare** WhatsApp number (or set `PAIR_PHONE_NUMBER`), enable the test contact
in the dashboard's WhatsApp page (new contacts default to disabled), then message the number
"how many horses do we have?" and watch the pipeline in the logs. For voice replies, set an STT key
(`GROQ_API_KEY`) and a TTS provider (`GCP_API_KEY`) in `.env`.

---

## 5. The Firestore data dependency (read this)

The gateway is only as testable as the data behind it. For the horse workflows you need, in the
Firebase project mshalia is pointed at:

1. **A staff user** — `users/{uid}` with:
   - `phone` matching what you pass to `-phone` (the resolver accepts `9665…`, `+9665…`, `05…`),
   - `role` = `manager` (or higher) so it can both list **and** register,
   - `orgIds: { "<orgId>": true }`.
2. **An org** — `organizations/{orgId}` with some `horses` subcollection docs (so `list_horses`
   returns data; `register_horse` will create more).

If `-phone` prints `did NOT resolve (unknown_phone)`, there's no matching user — seed one (or ask
whoever owns the Firebase project for a valid staff phone + org). If calls return `403 FORBIDDEN`,
the user's role/scopes are below the tool's floor.

---

## 6. Offline fallback — `cmd/mockerp`

When you can't run mshalia (no Firebase creds, offline), start the bundled mock gateway instead —
same HMAC contract, canned horse data, always authorizes:

```powershell
$env:AGENT_GATEWAY_SECRET="devsecret"; $env:MOCK_ROLE="manager"; go run ./cmd/mockerp   # :3001
# then set MSHALIA_API_URL=http://localhost:3001 + AGENT_GATEWAY_SECRET=devsecret in .env
go run ./cmd/erpcheck -phone 966500000000 -add
```

The mock returns 3 sample horses and echoes `register_horse` success — good for exercising *our*
side when the real ERP isn't available. It is **not** a substitute for testing against Firestore.

---

## 7. Static checks (no services)

```powershell
go build ./... ; go vet ./... ; go test ./...     # 75 tests, incl. the 7-scenario eval suite
```

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `identity/resolve error: … secret not set` | `AGENT_GATEWAY_SECRET` empty in `.env` — it's set; make sure you didn't override it. |
| mshalia logs `Invalid signature` (401) | The two `AGENT_GATEWAY_SECRET` values differ. Re-sync `.env` ↔ `mshalia/.env.local`. |
| `-phone` did NOT resolve | No `users` doc with that phone in Firestore — seed one or use a known staff phone (§5). |
| `403 FORBIDDEN` on `register_horse` | Resolved user is below `manager` / lacks `approve_services` scope. |
| `connection refused` to `:3000` | mshalia isn't running (`npm run dev`) or is on another port. |
| mshalia won't boot (Firebase error) | Its `.env.local` needs valid `FIREBASE_CLIENT_EMAIL` + `FIREBASE_PRIVATE_KEY`. |
| `no LLM providers configured` (wfcli) | Add `NIM_API_KEY` or `OPENAI_API_KEY` to `.env`. |
