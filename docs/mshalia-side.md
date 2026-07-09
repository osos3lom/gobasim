# mshalia — ERP Agent Gateway Implementation Brief

> **Audience:** the `mshalia` (Next.js + Firestore ERP) development team.
> **From:** the `sawt-gateway` (Go / WhatsApp assistant) team.
> **Purpose:** `sawt-gateway` already ships a generic client that calls your ERP Agent Gateway
> over a signed HTTP contract. The **operations** tools are live and working; the **accounting**
> and **administration** tools are called by our client but currently **`404`** because they don't
> exist on your side yet. This document specifies exactly what to build so our two sides line up
> with **zero code changes on our end**.
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
4. [Tool Catalogue (12 ids)](#4-tool-catalogue-12-ids)
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

**URL:** `POST {MSHALIA_API_URL}/api/agent/v1/tools/{toolId}` where `{toolId}` is one of the 12 ids
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

## 4. Tool Catalogue (12 ids)

Field names, types, and required markers below are **verbatim** from `internal/workflow/tools.go`.
The LLM calls these via OpenAI tool-calling; you receive them under `args`. Implement input
validation (Zod) matching these exactly.

Legend: **R** = required. `risk` drives our confirmation flow (see §5).

### 4.1 Operations agent — **LIVE** (already implemented; listed for contract completeness)

| Tool id | Purpose | Args | Risk |
|---|---|---|---|
| `get_horse` | Look up a horse by id, or search by name (AR/EN). Provide `horseId` **or** `nameQuery`. | `horseId` (string), `nameQuery` (string) | low |
| `get_care_plan` | Get a horse's care plan (turnout, feeding, instructions). | `horseId` (string) **R** | low |
| `list_tasks` | List tasks, optionally filtered. | `status` (string: pending/in-progress/completed/missed), `assigneeId` (string), `horseId` (string), `limit` (integer, default 20) | low |
| `update_task_status` | Update a task's status. | `taskId` (string) **R**, `status` (string: pending/in-progress/completed/missed) **R** | **medium** |

### 4.2 Accounting agent — **TO BUILD** (currently `404`)

| Tool id | Purpose | Args | Risk |
|---|---|---|---|
| `list_invoices` | List invoices, optionally filtered. | `status` (string: draft/sent/paid/overdue), `clientId` (string), `limit` (integer, default 20) | low |
| `get_invoice` | Get one invoice by id (line items, totals, payment status). | `invoiceId` (string) **R** | low |
| `record_expense` | Record a business expense (e.g. a feed bill). | `amount` (number, SAR) **R**, `category` (string, e.g. feed/vet/maintenance) **R**, `description` (string), `vendorId` (string) | **high** |
| `record_payment` | Record a payment received against an invoice. | `invoiceId` (string) **R**, `amount` (number, SAR) **R**, `method` (string, e.g. cash/transfer/card) | **high** |

### 4.3 Administration agent — **TO BUILD** (currently `404`)

| Tool id | Purpose | Args | Risk |
|---|---|---|---|
| `list_clients` | List clients, optionally filtered by name (AR/EN). | `nameQuery` (string), `limit` (integer, default 20) | low |
| `get_client` | Get one client by id (contact info, linked horses). | `clientId` (string) **R** | low |
| `list_contracts` | List contracts, optionally filtered. | `clientId` (string), `status` (string: active/expired/draft), `limit` (integer, default 20) | low |
| `get_contract` | Get one contract by id (terms, linked client). | `contractId` (string) **R** | low |

> **Note on ids/amounts:** our agents are prompted to **resolve entities by id via the `list`/`get`
> tools before acting**, and to **restate amounts exactly**. Your read tools should therefore return
> stable ids and enough disambiguating fields (names in AR/EN, amounts, statuses) for the model to
> pick the right entity.

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

1. **Per-tool reference** for all 8 new ids (and, ideally, the 4 live operations tools):
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

- [ ] A request signed with a valid vector returns **200** for each of the 8 new tool ids.
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
