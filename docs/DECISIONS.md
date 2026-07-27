# Decision Record

Short, dated notes on choices that are easy to mistake for oversights. Each
entry says what was decided, why, and what would change the answer — so the
next person doesn't re-litigate it from scratch or "fix" it by accident.

---

## 2026-07-28 — `stt_history` and `tts_history` are write-only by design

**Decision:** keep both tables. They are written on every speech call and
purged by the retention job, but nothing in the application reads them back.

**Why:** they are a forensic trail. When a voice reply is wrong, these rows are
the only record of what the STT actually heard and what the TTS was asked to
say — `wa_activity` keeps the turn summary, not the per-call speech detail.
They cost almost nothing (13 rows each at the time of writing) and retention
already bounds their growth.

The two `SELECT` queries that fed them (`GetSttHistory`, `GetTtsHistory`) had
no callers anywhere and were deleted. That is what makes the tables
*deliberately* write-only rather than half-built: the write and purge paths are
wired, the read path is intentionally absent.

**Revisit if:** a UI needs speech-call history (re-add a query then), or the
tables grow enough to matter — in which case shorten retention rather than
dropping the tables.

---

## 2026-07-28 — No index added for `ListWaChatsSummary`

**Decision:** leave the query unindexed for its sort key.

**Why:** `DISTINCT ON (chat_id) … ORDER BY chat_id, created_at DESC, id DESC`
has no supporting index, so Postgres seq-scans `wa_messages` and sorts. Verified
with `EXPLAIN (ANALYZE, BUFFERS)` against production: **0.148 ms over 39 rows**.
At that size Postgres would ignore an index even if one existed, and an unused
index still costs write amplification on the hot inbound-message path.

**Revisit if:** `wa_messages` passes roughly 50k rows. Re-run `EXPLAIN ANALYZE`
first; if the sort dominates, add
`CREATE INDEX ON wa_messages (chat_id, created_at DESC, id DESC)`.

---

## 2026-07-28 — Columns are declared twice, on purpose

**Decision:** keep every post-hoc column in both `CREATE TABLE` and a matching
`ALTER TABLE … ADD COLUMN IF NOT EXISTS`.

**Why:** `schema.sql` is re-applied on every boot and there is no migration
tool. Fresh databases get columns from `CREATE TABLE`; databases created before
a column existed get it from the `ALTER`. Both are needed.

The risk is silent divergence — change a default in one place and fresh
installs behave differently from upgraded ones. `TestSchemaAlterColumnsAlsoDeclaredInCreateTable`
(in `schema_test.go`) now fails if an ALTER-added column is missing from its
`CREATE TABLE`, so the pair cannot drift unnoticed.

**Revisit if:** a real migration tool is adopted, at which point both the
duplication and the test go away.

---

## 2026-07-28 — Content-Security-Policy allows scripts only from `'self'`

**Decision:** `script-src 'self'` — no `'unsafe-inline'`, no `'unsafe-eval'`,
no CDN.

**Why:** this is an authenticated operator console that renders contact-supplied
strings. Reaching a strict policy required three things, all of which must hold
for it to stay strict:

1. htmx is vendored into `web/static/htmx.min.js` and embedded in the binary,
   not loaded from unpkg.
2. No template contains an inline `<script>` or an `on*=` attribute. Behaviour
   is bound from `web/static/ui.js` via `data-show` / `data-hide` /
   `data-toggle` / `data-tab-group` attributes, using delegated listeners that
   survive htmx swaps.
3. No `hx-on` attributes — htmx implements them with `new Function()`, which
   `'unsafe-eval'` would be required for. Use an htmx event listener in a
   static `.js` file instead.

`TestTemplatesContainNoInlineScripts` and `TestCSPDisallowsInlineAndEvalScripts`
enforce all three. Verified in a real browser: all four dashboard pages, tab
switching, modals, htmx fragment loads and the SSE log stream run with zero
console errors.

**Revisit if:** a dependency genuinely requires `eval`. Prefer replacing the
dependency; failing that, use a per-request nonce rather than blanket
`'unsafe-inline'`.
