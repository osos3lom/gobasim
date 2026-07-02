---
name: codebase-map
description: Fast navigation map of the Sawt monorepo (this stt-tts repo) and the mshalia ERP (C:\Users\osama\Documents\GitHub\mshalia). Use this BEFORE broad exploration when locating data models, key functions, API routes, auth/RBAC, the WhatsApp gateway, the LangGraph backend, or conventions in either codebase — it tells you which file to open instead of searching. Covers the apps/* + packages/* workspace layout, the Drizzle schema, the @sawt/* scope, and the Sawt↔ERP integration boundary.
---

# Codebase Map — Sawt monorepo + mshalia ERP

Two repositories collaborate to make WhatsApp the UI for an ERP. **Read the relevant section, open the named file, skip the search.** Verify exact line numbers before relying on them.

> Architecture overview: [README.md](../../../README.md) · Full blueprint: [docs/BLUEPRINT.md](../../../docs/BLUEPRINT.md) · LangGraph/ERP patterns: [docs/REFERENCE_REPO_SKILLS.md](../../../docs/REFERENCE_REPO_SKILLS.md)

## The two repos at a glance

| Repo | Path | What it is | Stack | DB |
|---|---|---|---|---|
| **Sawt monorepo** | `C:\Users\osama\Documents\GitHub\stt-tts` (this repo, `sawt-monorepo`) | The AI brain + WhatsApp edge + dashboard. npm workspaces. | Next.js 16, **Python + LangGraph**, Baileys, Drizzle | **Neon** (serverless Postgres) |
| **mshalia ERP** | `C:\Users\osama\Documents\GitHub\mshalia` | Multi-vertical ERP (equine/agriculture/industrial/holding) with a real double-entry GL. The source of truth. | Next.js 16 | **Firestore** (+ Upstash Redis) |

**Three cross-repo facts that shape everything:**
1. `firebase-admin` **bypasses all `firestore.rules`** (god-mode). The platform must NEVER hold admin creds directly; all ERP writes go through an **ERP Agent Gateway** (to be built at `mshalia/app/api/agent/v1/*`).
2. Every ERP mutation today lives in **client-SDK `lib/api/*` functions** (TypeScript-only validation, implicit browser auth) — NOT server-callable as-is. The Gateway re-exposes them server-side with Zod + RBAC.
3. WhatsApp users are identified by **phone**: `Client.userId`, `Client.phoneVerifiedAt`, `Client.primaryContactMethod` (`phone|email|whatsapp`); ERP users store phone as a synthetic email `{phone}@meshalia.app`.

---

## Repo A — Sawt monorepo (`stt-tts`)

### Workspace layout (npm workspaces: `apps/*`, `packages/*`)
```
apps/
  dashboard/         Next.js 16 control plane (UI + API routes). Code under src/.
  backend/           Python FastAPI + LangGraph agent & speech (STT/TTS).
  gateway-whatsmeow/ Go WhatsApp edge (whatsmeow client).
packages/            Shared TypeScript, scope @sawt/*
  core/              @sawt/core — env, logger, types, crypto, db, signing (subpath exports)
  database/          @sawt/database — Drizzle schema (single source of truth) + db client
  security/          @sawt/security — HMAC request signing
legacy/
  wa-bot/            DEPRECATED whatsapp-web.js/Puppeteer bot (reference only; not deployed)
tsconfig.base.json   Shared TS base (extended by workspace tsconfigs)
docs/                BLUEPRINT.md, REFERENCE_REPO_SKILLS.md
```
Root scripts (`package.json`): `dev` → dashboard · `dev:backend` → `python apps/backend/server.py` · `dev:gateway` → `go run apps/gateway-whatsmeow/main.go` · `dev:all` → all three.

### `apps/dashboard` (Next.js control plane)
```
src/lib/db.ts          Data access — imports db + tables from @sawt/database (Drizzle); exposes typed CRUD
src/lib/{auth.ts, stt-pipeline.ts, tts-pipeline.ts, audio-utils.ts, model-loader.ts, pipeline-status.ts, stt-models.ts, wa-bot.ts}
src/proxy.ts           Route protection
src/app/api/*          Routes: agents, auth/{login,logout,register}, stt, tts, webhook/{stt,tts,logs},
                       events (SSE), health, history/{stt,tts}, models, ref-audio, db-init,
                       integrations/whatsapp, wa-bot/*  (← these target the LEGACY bot, see note)
src/app/{dashboard,agents,login,integrations}/   pages
src/components/         panels + assistant/{ActivityFeed,AgentPanel,ConnectionCard,MessagesView,PipelineHealth} + ui/*
```
- **Agent config** lives in the `agents` Drizzle table (system_prompt, `llm {vendor,url,model}` — OpenAI-compatible, NVIDIA-ready — asr/tts, maxHistory, mcpServers, skills).
- Conventions: dynamic routes use `export const dynamic = "force-dynamic"`. The "NOT the Next.js you know" rule (see `AGENTS.md`) applies here; Next docs are under `apps/dashboard/node_modules/next/dist/docs/`.

### `apps/backend` (Python — the agent brain + speech)
```
server.py              FastAPI: speech (F5-TTS / SILMA TTS, Whisper STT) + agent entrypoints
agent/graph.py         LangGraph supervisor + subgraphs
agent/state.py         Graph state (TypedDict)
requirements.txt
models/                reference audio (gitignored — apps/backend/models/)
```

### `apps/gateway-whatsmeow` (Go — WhatsApp edge)
```
main.go                entrypoint, event handler, and HTTP REST server
go.mod                 Go package module definition
README.md              developer setup & API documentation
deploy/{setup-vm.sh, install-service.sh, sawt-gateway.service}   GCE e2-micro deploy — see docs/GCP-GATEWAY-SETUP.md
```

### `packages/*` (shared, `@sawt/*`)
- `@sawt/core` (`packages/core/src/`): `env.ts`, `logger.ts`, `types.ts`, `crypto/`, `db/` (Drizzle client + table re-exports), `signing/` (HMAC sign/verify). Subpath `exports` map in its package.json.
- `@sawt/database` (`packages/database/src/schema.ts`): **the schema source of truth** — `settings, tts_history, stt_history, webhook_logs, agents, users` + WhatsApp tables `wa_contacts, wa_activity, health_check`. `src/index.ts` exports the `db` client + tables.
- `@sawt/security` (`packages/security/src/hmac.ts`): HMAC helpers.

> **Conventions:** all shared packages use the **`@sawt/*`** scope (never `@swa/*`). drizzle-orm is pinned to `^0.30.0` across workspaces. Schema changes go in `packages/database/src/schema.ts` (Drizzle); `db-init` bootstraps tables (drizzle-kit migrations are the consolidation target).

> **Note (WhatsApp paths):** the dashboard's `/integrations` page + `api/wa-bot/*` routes proxy to the **legacy** bot's control server (`127.0.0.1:${WA_CONTROL_PORT:-3100}`, `legacy/wa-bot`). The supported edge is `apps/gateway-whatsmeow`; rewiring the dashboard to it is pending.

---

## Repo B — mshalia ERP

### Data layer & types
```
lib/firebase.ts         Client SDK (lazy proxy): auth, db, storage  — used by lib/api/*
lib/firebase-admin.ts   Admin SDK (lazy singleton): adminAuth, adminDb, adminStorage — BYPASSES rules
types/domain.ts         UI domain types + ALL enums/status unions + PermissionScope
types/firestore.ts      Raw Firestore shapes (Timestamp/FieldValue)
lib/mappers/*.mapper.ts  Firestore <-> domain conversion
firestore.rules         Role hierarchy + org isolation + default-deny (admin SDK ignores these)
firestore.indexes.json  ~43 composite indexes
```

### Multi-tenancy
- Path-nested: **all org data at `organizations/{orgId}/{collection}/{doc}`**; only `organizations` and `users` are top-level.
- Tenancy via `users/{uid}.orgIds` map + `firestore.rules: hasOrgAccess()`. super_admin bypasses.
- Verticals: `equine`, `agriculture`, `industrial`, `holding`. `Organization.type` = `holding_company|subsidiary`; `industryContext`.

### The operation layer — `lib/api/*` (these become the Gateway's tools)
Pattern: client component → `lib/api/{domain}.ts` fn → client SDK write + best-effort `addActivityEvent` + best-effort GL `postJournal`. TS-only validation; risky ops use `runTransaction`.

| Domain | File | Key functions (file:line) |
|---|---|---|
| Horses | `lib/api/horses.ts` | createHorse:116, updateHorse:138, softDelete:151, restore:184, **hardDelete:203 (never expose)**, **assignStall:235 (atomic)**, changeOwnership:285 |
| Clients | `lib/api/clients.ts` | createClient:130, update:156, softDelete:176, restore:194, **linkClientToUser:213**, updateStatus:253; `fetchClientsMerged:37` (MDM) |
| Invoices | `lib/api/invoices.ts` | createInvoice:147 (+ `nextInvoiceNumber:100`), update:196; PDF via `app/api/invoices/generate/route.ts` |
| Contracts | `lib/api/contracts.ts` | create:119, update:157, delete:176; PDF via `app/api/contracts/generate/route.ts` |
| Vendors/Expenses/Payments | `vendors.ts` (create:59), `expenses.ts` (create:69 → `postExpenseJournal`), `payments.ts` (create:69 → `postPaymentJournal`) |
| Inventory | `lib/api/inventoryTransactions.ts` | recordRestock:62, recordConsumption:163, recordAdjustment:208 (atomic, append-only) |
| Tasks | `lib/api/tasks.ts` | create:66, updateStatus:87, update:104, delete:116 |
| Health | `incidents.ts` (log:79), `vetAppointments.ts` (schedule:64), `carePlans.ts`, `treatmentPlans.ts`, `incidentEscalations.ts` |
| **Accounting** | `lib/api/accounting/` | **`postJournal.ts` (post:199, reverse:406)**, `coa.ts`, `balances.ts`, `close.ts` (closeYear:44), `subledgers.ts` (postInvoiceJournal:41, postExpenseJournal:79), `banking.ts`, `reconciliation.ts`, `bioAssets.ts` |
| Shared | `activityFeed.ts` (addActivityEvent:87), `auth.ts` (verifyRequest:9, roleHierarchy:67), `rate-limit.ts`, `pagination.ts`, `storage.server.ts` |

### Auth / RBAC
- **AuthN:** Firebase email/password; phone = `{phone}@meshalia.app`. Session cookies `meshalia_session` + `meshalia_role`. Protection in `proxy.ts`; session exchange `app/api/auth/session/route.ts`. Bearer verify: `lib/api/auth.ts: verifyRequest:9`.
- **AuthZ:** app roles `super_admin|admin|manager|viewer|client` + `roleHierarchy` (`lib/api/auth.ts:67`). Staff roles `owner|general_manager|barn_manager|trainer|groom|vet_external|finance`. **`PermissionScope`** (`types/domain.ts`) is **defined but NOT enforced** — the Gateway becomes its first enforcer.
- **Rate limiting:** `lib/api/rate-limit.ts` (Upstash; read 60 / write 20 / expensive 10 per min).

### Patterns & gotchas
- **Soft deletes** (`deletedAt`); **posted journals immutable** (correct via `reverseJournalEntry`); **deterministic-idempotent GL** keyed `je-{sourceType}-{sourceId}-{event}`; **subledger dims** `dims.{horseId|clientId|vendorId|staffId|costCenterId|projectId}`; **append-only** `inventory_transactions`.
- **`activity_feed` audit has gaps** (health, tasks, most updates, period-close, stall create/delete not logged) → the Gateway should add complete audit.
- **Cloud Functions** (`functions/src/*`): incident escalation, payroll scheduler, appointment reminder, salary trigger, inventory-expense sync, orphan-PDF sweep.

---

## The integration boundary (where the two repos meet)
```
gateway-whatsmeow ─signed(HMAC)─▶ apps/backend (LangGraph) ─signed service token + {actingUserUid, orgId}─▶ mshalia/app/api/agent/v1/* (Gateway, to build)
                                                                                                              │ authN→authZ(RBAC+scopes)→Zod→idempotency
                                                                                                              │ →non-destructive policy→ lib/api/* →audit
                                                                                                              ▼  Firestore
```
- The ERP Gateway is the **only write path**: allow-listed, single-entity, soft-delete-only, idempotent, threshold-gated, org-scoped, fully audited (blueprint §11–12).
- Identity: WhatsApp JID → phone → ERP `AppUser`/`Client` → role + `orgIds`; cache in Neon.

## Quick "where do I find…" index
- **An ERP DB write** → `mshalia/lib/api/{domain}.ts` (table above).
- **An ERP entity's fields/enums** → `mshalia/types/domain.ts`.
- **GL posting** → `mshalia/lib/api/accounting/postJournal.ts` + `subledgers.ts`.
- **Platform DB schema** → `packages/database/src/schema.ts` (Drizzle, source of truth).
- **Platform data access** → `apps/dashboard/src/lib/db.ts` + `@sawt/core/db`.
- **Agent/LLM config shape** → the `agents` table in `packages/database/src/schema.ts`.
- **WhatsApp socket / whatsmeow** → `apps/gateway-whatsmeow/main.go`.
- **Agent orchestration** → `apps/backend/agent/{graph,state}.py` (+ patterns in `docs/REFERENCE_REPO_SKILLS.md`).
