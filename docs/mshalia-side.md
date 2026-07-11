# mshalia — ERP Agent Gateway Implementation Brief

> **Audience:** the `mshalia` (Next.js + Firestore ERP) development team.
> **From:** the `sawt-gateway` (Go / WhatsApp assistant) team.
> **Purpose:** `sawt-gateway` already ships a generic client that calls your ERP Agent Gateway
> over a signed HTTP contract. It now calls **39 tool ids across 6 agents** (operations, accounting,
> administration, client self-service, sales, breeding — see §4). None are verified against a live
> `mshalia` yet, so every call currently **`404`s** until you implement these endpoints. This
> document specifies exactly what to build so our two sides line up with **zero code changes on our
> end** (our `CallTool` is generic over `toolId`).
>
> **What we need back from you:** once you implement these endpoints, publish a **reference
> Markdown file** (`mshalia-agent-gateway-reference.md`) documenting each tool's exact
> request/response JSON schema, permission scopes, and error codes, plus a small set of **HMAC
> contract-test vectors**. Our dev uses that to finalize the tool schemas and integration-test.
> See [§7 Handshake](#7-handshake--what-to-deliver-back).

> **Ground truth for this brief:** our client is `internal/erp/client.go`; our tool declarations
> are `internal/workflow/tools.go`. Every field below is copied from that code — treat it as the
> contract, not a suggestion.

---

## Table of Contents

1. [Transport & Authentication](#1-transport--authentication)
2. [`identity/resolve` endpoint](#2-identityresolve-endpoint)
3. [`tools/{toolId}` endpoint](#3-toolstoolid-endpoint)
4. [Tool Catalogue (39 ids across 6 agents)](#4-tool-catalogue-39-ids-across-6-agents)
5. [Server-Side Guardrails](#5-server-side-guardrails)
6. [Error Model](#6-error-model)
7. [Handshake — What to Deliver Back](#7-handshake--what-to-deliver-back)
8. [Acceptance Criteria](#8-acceptance-criteria)

---

## 1. Transport & Authentication

All calls are `POST`, JSON, over HTTPS, to a base URL our side configures as `MSHALIA_API_URL`:

```
POST {MSHALIA_API_URL}/api/agent/v1/identity/resolve
POST {MSHALIA_API_URL}/api/agent/v1/tools/{toolId}
```

**Every request carries three headers** (set by `internal/erp/client.go`):

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `x-swa-timestamp` | Unix time in **milliseconds** (string), e.g. `1751760000000` |
| `x-swa-signature` | `HMAC-SHA256( AGENT_GATEWAY_SECRET, "{timestamp}.{rawBody}" )`, lowercase hex |
| `x-swa-idempotency-key` | SHA-256 of the exact request body, hex. **Stable across our retries** (we re-sign each attempt with a fresh timestamp but keep this key). |
| `x-swa-trace-id` | Correlation id (= the WhatsApp message id) for cross-system tracing. Log it. |

> **Idempotency is a hard requirement, not a hint.** Our client retries transport errors and
> `429`/`5xx` with jittered exponential backoff (~200 ms → 3 s, up to 3 attempts). For financial
> writes (`record_expense`, `record_payment`) this is only safe if **you dedup on
> `x-swa-idempotency-key`** — persist it and make a retried call with the same key a no-op that
> returns the original result. (The per-tool `idempotencyKey` in `args`, §4.2, is a second,
> arg-level guard; honor both.)

**Signature construction (must match exactly):**

- The signed string is `x-swa-timestamp` + `"."` + the **raw request body bytes** (the exact JSON
  string we send — sign the bytes, do not re-serialize).
- The key is the shared secret `AGENT_GATEWAY_SECRET` (our env var; your `GATEWAY_SECRET` or
  equivalent — same value on both sides).
- Output is hex-encoded SHA-256 HMAC.

**Reference (our Go side):**

```go
func computeSignature(secret, timestamp, body string) string {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(timestamp + "." + body))
    return hex.EncodeToString(mac.Sum(nil))
}
```

**Node/TypeScript equivalent you should implement to verify:**

```ts
import { createHmac, timingSafeEqual } from "node:crypto";

function verify(secret: string, timestamp: string, rawBody: string, sig: string): boolean {
  const expected = createHmac("sha256", secret)
    .update(`${timestamp}.${rawBody}`)
    .digest("hex");
  // constant-time compare
  const a = Buffer.from(sig, "hex");
  const b = Buffer.from(expected, "hex");
  return a.length === b.length && timingSafeEqual(a, b);
}
```

**Requirements:**
- **Verify the signature** on every request; reject with `401` on mismatch.
- **Enforce timestamp skew** of **±5 minutes**; reject stale/future timestamps with `401`. (Our
  client sends a fresh timestamp per call; it does not retry with an old one.)
- **Client timeouts** (so size your handlers accordingly): identity resolution **10s**, tool
  calls **15s**. Exceeding these surfaces as a `NETWORK_ERROR` on our side.

---

## 2. `identity/resolve` endpoint

Resolves a WhatsApp phone number into an ERP identity. Called on **every inbound message** before
any tool runs (an identity cache/TTL is planned on our side to reduce this load).

**Request body:**

```json
{ "phone": "9665XXXXXXXX" }
```

- `phone` is digits-only (no `+`, no `@` suffix), as extracted from the WhatsApp JID.

**Response body (HTTP 200):**

```json
{
  "resolved": true,
  "identity": {
    "uid": "user_abc123",
    "phone": "9665XXXXXXXX",
    "role": "stable_manager",
    "displayName": "Ali Al-...",
    "orgIds": ["org_equine_01"]
  }
}
```

**Unlinked number (still HTTP 200):**

```json
{ "resolved": false, "identity": null }
```

**Our handling (from `internal/erp/client.go`):**
- Non-200 → error surfaced to our monitor; the user still gets a reply, just no ERP tool access.
- `resolved: false` or `identity: null` → treated as **unlinked**; the assistant replies but tools
  are unavailable for that number.

**Identity fields consumed by our side** (`erp.Identity`): `uid`, `phone`, `role`, `displayName`,
`orgIds[]`. The `uid` becomes `actingUserUid` and the first/selected `orgIds` entry becomes `orgId`
in subsequent tool calls (see §3).

---

## 3. `tools/{toolId}` endpoint

Executes a single allow-listed tool as a specific acting user within a specific org.

**URL:** `POST {MSHALIA_API_URL}/api/agent/v1/tools/{toolId}` where `{toolId}` is one of the 39 ids
in §4.

**Request body (from `CallTool`):**

```json
{
  "orgId": "org_equine_01",
  "actingUserUid": "user_abc123",
  "args": { "...": "tool-specific, see §4" }
}
```

**Response body:** our client parses the response as a **generic JSON object** and hands it back to
the LLM verbatim as the tool result. It does **not** require a fixed shape — **but we strongly
recommend a stable envelope** so the model reasons over consistent structure and errors:

```json
{ "ok": true,  "data": { "...": "..." } }
```
```json
{ "ok": false, "error": "human-readable reason", "code": "MACHINE_CODE" }
```

**Two-factor authority (must enforce):** the platform HMAC proves it's *us*; `actingUserUid` +
`orgId` prove *on whose behalf*. **A tool must never let the acting user exceed their own ERP
authority** — apply the same RBAC you would for that user in the UI.

**Our failure handling (from `CallTool`):** network/transport failures return
`{"ok":false,"error":...,"code":"NETWORK_ERROR"}` to the model; an unconfigured secret returns
`code:"UNCONFIGURED"`. HTTP-level errors (e.g. `404` for an unimplemented tool) are returned to the
model as the parsed body — so **return a structured error body even on `404`**, not an HTML page.

---

## 4. Tool Catalogue (39 ids across 6 agents)

Field names and required markers below are **verbatim** from `internal/workflow/tools.go`.
The LLM calls these via OpenAI tool-calling; you receive the arguments under `args`. Implement Zod
input validation matching these exactly.

**Two access controls apply to every tool — enforce both server-side:**
- **`risk`** drives *our* confirmation flow — `medium`/`high` tools are restated to the user and
  require an explicit "yes" before we call you (see §5). Financial writes are always `high`.
- **`min-role`** is the minimum app-role (`client < viewer < manager < admin < super_admin`) we
  require before a tool is even offered to the model. **Enforce the same minimum on your side**
  against the `actingUserUid`'s role — our filter is best-effort UX; yours is the security boundary.

Legend: **R** = required arg. (Every `list_*` tool also accepts an optional `limit`, default 20.)

### 4.1 Operations agent (15 tools)

| Tool id | Purpose | Key args | Risk | Min-role |
|---|---|---|---|---|
| `get_horse` | Look up a horse by id or name (AR/EN). | `horseId` / `nameQuery` (one of) | low | viewer |
| `get_care_plan` | Care plan (turnout, feeding, instructions). | `horseId` **R** | low | viewer |
| `list_tasks` | List tasks, filterable. | `status`, `assigneeId`, `horseId` | low | viewer |
| `update_task_status` | Update a task's status. | `taskId` **R**, `status` **R** (pending/in-progress/completed/missed) | medium | manager |
| `list_horses` | List horses, filterable. | `breed`, `status`, `gender` | low | viewer |
| `list_stalls` | List stalls, filterable. | `barnId`, `status` | low | viewer |
| `get_stall_availability` | Stall availability. | `barnId` | low | viewer |
| `assign_stall` | Assign a horse to a stall. | `horseId` **R**, `stallId` **R** | medium | manager |
| `register_horse` | Register a new horse. | `nameEn` **R**, `nameAr` **R**, `breed` **R**, `color` **R**, `gender` **R**, `ownerId` | medium | manager |
| `check_in_horse` | Check a horse into a stall. | `horseId` **R**, `stallId` | medium | manager |
| `check_out_horse` | Check a horse out. | `horseId` **R** | medium | manager |
| `report_incident` | Report an injury/asset incident. | `horseId` **R**, `title` **R**, `description` **R**, `severity` **R** (low/medium/high/critical) | medium | manager |
| `list_incidents` | List incidents, filterable. | `horseId`, `resolved` | low | viewer |
| `book_vet_appointment` | Book a vet/farrier appointment. | `horseId` **R**, `vetName` **R**, `type` **R** (routine/emergency/farrier/dental/vaccination), `scheduledAt` **R** (ISO), `notes` | medium | manager |
| `record_treatment_plan` | Record a vet treatment plan. | `horseId` **R**, `diagnosis` **R**, `medications` **R** (JSON array), `notes` | medium | manager |

### 4.2 Accounting agent (4 tools)

| Tool id | Purpose | Key args | Risk | Min-role |
|---|---|---|---|---|
| `list_invoices` | List invoices, filterable. | `status` (draft/sent/paid/overdue/cancelled), `clientId` | low | manager |
| `get_invoice` | One invoice (line items, totals, payment status). | `invoiceId` **R** | low | manager |
| `record_expense` | Record a business expense. | `amount` **R** (SAR), `category` **R**, `idempotencyKey` **R**, `description`, `vendorId`, `vatAmount`, `horseId`, `expenseDate` | **high** | manager |
| `record_payment` | Record a payment against an invoice. | `invoiceId` **R**, `amount` **R** (SAR), `idempotencyKey` **R**, `method` | **high** | manager |

> **Idempotency:** `record_expense` and `record_payment` send a **required** `idempotencyKey`.
> Persist it and dedupe — a retried call with the same key must not double-post.

### 4.3 Administration agent (4 tools)

| Tool id | Purpose | Key args | Risk | Min-role |
|---|---|---|---|---|
| `list_clients` | List clients, filterable by name (AR/EN). | `nameQuery` | low | manager |
| `get_client` | One client (contact info, linked horses). | `clientId` **R** | low | manager |
| `list_contracts` | List contracts, filterable. | `clientId`, `status` (active/expired/pending/draft/terminated) | low | manager |
| `get_contract` | One contract (terms, linked client). | `contractId` **R** | low | manager |

### 4.4 Client self-service agent (6 tools)

> Routed to **directly** when the resolved identity's role is `client` (bypasses intent
> classification). All tools are **scoped to the acting user** — return only the caller's own data.

| Tool id | Purpose | Key args | Risk | Min-role |
|---|---|---|---|---|
| `list_my_horses` | The caller's horses. | — | low | client |
| `get_my_horse` | One of the caller's horses. | `horseId` **R** | low | client |
| `list_my_invoices` | The caller's invoices. | `status` | low | client |
| `get_my_balance` | The caller's outstanding balance. | — | low | client |
| `get_my_statement` | The caller's statement of account. | — | low | client |
| `list_my_contracts` | The caller's contracts. | `status` | low | client |

### 4.5 Sales / CRM agent (5 tools)

| Tool id | Purpose | Key args | Risk | Min-role |
|---|---|---|---|---|
| `list_available_horses` | Horses for sale. | `breed` | low | viewer |
| `list_available_stalls` | Empty stalls for boarding. | — | low | viewer |
| `list_packages` | Service/boarding packages + pricing. | `category` | low | viewer |
| `book_tour` | Book a site tour for a lead. | `name` **R**, `phone` **R**, `scheduledAt` **R** (ISO), `leadId`, `notes` | medium | viewer |
| `submit_inquiry` | Submit a CRM inquiry. | `name` **R**, `phone` **R**, `inquiryType` **R** (boarding/breeding/purchase/other), `email`, `notes` | medium | viewer |

### 4.6 Breeding agent (5 tools)

| Tool id | Purpose | Key args | Risk | Min-role |
|---|---|---|---|---|
| `list_breeding_stock` | Mares/stallions in breeding stock. | `gender` (mare/stallion) | low | viewer |
| `book_breeding` | Book a mare×stallion breeding. | `mareId` **R**, `stallionId` **R**, `bookingDate` **R** (YYYY-MM-DD), `notes` | medium | manager |
| `get_pregnancy_status` | Pregnancy/ultrasound history for a mare. | `mareId` **R** | low | viewer |
| `list_foals` | Foal records / recent births. | — | low | viewer |
| `recommend_bloodline` | Breeding compatibility recommendation. | `mareId` **R**, `stallionId` **R** | low | viewer |

> **Note on ids/amounts:** our agents are prompted to **resolve entities by id via the `list`/`get`
> tools before acting**, and to **restate amounts exactly**. Your read tools should return stable
> ids and enough disambiguating fields (AR/EN names, amounts, statuses) for the model to pick the
> right entity.

---

## 5. Server-Side Guardrails

These live entirely on your side — our client is intentionally thin. Mirror the existing
`mshalia` tool-contract (`lib/agent-gateway/tools/types.ts`):

- **Allow-listed tools only** — reject any `toolId` not in the catalogue with a structured `404`.
- **Zod input validation** — validate `args` against the schema; reject invalid input with a
  structured `400` (`code:"INVALID_INPUT"`).
- **Single-entity, id-addressed writes** — no bulk mutations from the agent.
- **Soft-delete only**; no hard deletes via the gateway.
- **Idempotency keys** on writes (`record_expense`, `record_payment`, `update_task_status`) so a
  retried call cannot double-post.
- **Amount / risk thresholds → `requires_approval`** — financial writes are `high` risk on our
  side; if an amount exceeds a policy threshold, return a response indicating approval is required
  (we already gate `medium`/`high` behind an explicit user confirmation; a stricter server-side
  gate is welcome and future-proofs manager-approval routing).
- **RBAC via `actingUserUid` + `orgId`** — enforce the acting user's real permissions; org-scope
  every read and write to `orgId`.
- **Per-actor / per-tenant rate limits** on your side (ours throttles inbound WhatsApp at
  8 msg/min/chat, but that is not a substitute for server-side limits).
- **Immutable audit log** — record every tool call (who, what, args, result) server-side.

**Tool metadata to expose in your reference MD** (per the existing `ToolDefinition` shape):
`id`, `agent`, `purpose`, `input` schema, `permissions {scopes[], minRole}`, `risk`, `idempotent`,
`output` schema, `failureModes[]`, `rollback` ("transactional" | "gl_reversal" | "compensating" |
"none").

---

## 6. Error Model

Return a **structured JSON body on every response, including errors** (never an HTML error page —
our client `json.Unmarshal`s the body and fails hard on non-JSON).

| Situation | HTTP | Recommended body |
|---|---|---|
| Success | 200 | `{"ok":true,"data":{…}}` |
| Bad signature / skew | 401 | `{"ok":false,"error":"signature invalid","code":"UNAUTHORIZED"}` |
| Invalid `args` | 400 | `{"ok":false,"error":"…","code":"INVALID_INPUT"}` |
| Unknown tool id | 404 | `{"ok":false,"error":"unknown tool","code":"UNKNOWN_TOOL"}` |
| Acting user lacks permission | 403 | `{"ok":false,"error":"forbidden","code":"FORBIDDEN"}` |
| Requires approval (amount/risk) | 200 | `{"ok":false,"error":"requires approval","code":"REQUIRES_APPROVAL"}` |
| Server error | 500 | `{"ok":false,"error":"…","code":"INTERNAL"}` |

> The model reads `error`/`code` to self-correct within our bounded tool loop (max 4 iterations),
> so clear, specific messages materially improve agent behavior.

---

## 7. Handshake — What to Deliver Back

After implementing §2–§6, publish **`mshalia-agent-gateway-reference.md`** containing:

1. **Per-tool reference** for all 39 tool ids (§4):
   exact request `args` schema, exact success `data` schema, `permissions {scopes, minRole}`,
   `risk`, `idempotent`, `rollback`, and `failureModes[]`.
2. **Error catalogue** — every `code` your gateway can return and when.
3. **HMAC contract-test vectors** — at least: a known `secret`, `timestamp`, `rawBody`, and the
   expected `x-swa-signature`, so we can assert our signer matches your verifier byte-for-byte.
4. **A sample signed request/response** per tool (curl or equivalent) against a staging deploy.
5. **Timestamp-skew policy** confirmation (±5 min) and any per-actor rate-limit values.

Send us the file (or a link); our dev reconciles it against `internal/workflow/tools.go` and, if
your `data` shapes differ from what our prompts expect, we adjust prompts/schemas on our side — the
**client transport needs no changes** because `CallTool` is generic over `toolId`.

---

## 8. Acceptance Criteria

The integration is "done" when:

- [ ] A request signed with a valid vector returns **200** for each of the 39 tool ids (§4).
- [ ] A request with a **skewed timestamp** (> ±5 min) is rejected with **401**.
- [ ] A request with a **bad signature** is rejected with **401**.
- [ ] An **unknown tool id** returns a structured **404** JSON body (not HTML).
- [ ] `record_expense` / `record_payment` enforce **idempotency** (a retried call does not
      double-post) and honor the amount/risk approval policy.
- [ ] Every read/write is **org-scoped** to `orgId` and RBAC-checked against `actingUserUid`.
- [ ] `identity/resolve` returns the documented shape for both linked and unlinked numbers.
- [ ] The **HMAC contract-test vectors** in your reference MD pass against our Go signer.
- [ ] `mshalia-agent-gateway-reference.md` is published and shared.

Once these pass, our roadmap item **Phase 2b** (see [`IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md))
closes and the accounting/administration agents go from `404` to fully functional.
