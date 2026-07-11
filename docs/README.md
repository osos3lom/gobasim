# Sawt Gateway — Documentation Index

`sawt-gateway` (Go module `sawt-go`) is **one always-on Go binary** that runs the whole platform:
the WhatsApp socket (whatsmeow), the LLM reasoning + tool-calling loop, the STT/TTS speech
pipeline, and the operator web dashboard — in a single process, on a GCP e2-micro (1 GB RAM).

> **Single-instance by design.** The WhatsApp session, in-memory rate limiters, log broker, and
> session secret are process-local — run exactly one instance.

## Start here

| If you want to… | Read |
|---|---|
| Understand the architecture & product intent | [BLUEPRINT.md](BLUEPRINT.md) |
| Set up a dev machine, build, deploy, or operate it | [DEPLOYMENT.md](DEPLOYMENT.md) |
| Run and verify the workflow locally before deploying | [LOCAL-TESTING.md](LOCAL-TESTING.md) |
| See readiness status, the scorecard, roadmap & feature backlog | [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md) |
| Implement the ERP gateway on the `mshalia` side | [mshalia-side.md](mshalia-side.md) |

## Document map (6 docs)

- **[BLUEPRINT.md](BLUEPRINT.md)** — target & current architecture, the current-vs-target gap
  table, agent architecture, ERP integration, schema, security model, and assumptions. The
  architectural source of truth.
- **[DEPLOYMENT.md](DEPLOYMENT.md)** — the authoritative runbook: Windows 11 dev setup, `.env`
  reference, build/test/debug, e2-micro optimization, GCP production deploy (systemd, TLS,
  firewall, backups), and security hardening.
- **[LOCAL-TESTING.md](LOCAL-TESTING.md)** — how to exercise the workflow locally before GCP: tiered
  tests against a locally-running `mshalia` (or the bundled `cmd/mockerp`) with real LLM/STT.
- **[IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md)** — the status doc: a weighted **Project
  Ready %** KPI, a 15-category scorecard, the risk register, the closed agentic-gateway audit, a
  prioritized go-live roadmap, and the dashboard/observability feature backlog (Epics H/O/S/T).
- **[mshalia-side.md](mshalia-side.md)** — the external brief for the `mshalia` ERP team: the exact
  HMAC contract and the 39 tools (across 6 agents) our client already calls.

> **History note.** Docs for the earlier three-runtime design (Next.js dashboard + Python LangGraph
> backend + a separate Go gateway) were removed — they no longer match this consolidated
> single-binary repo. Two large historical docs were also retired: `REFERENCE_REPO_SKILLS.md`
> (LangGraph/Python reference patterns — the implementation is pure Go with no LangGraph) and
> `AGENTIC-GATEWAY-AUDIT.md` (all findings implemented; folded into `IMPLEMENTATION-PLAN.md` §5).
> `BACKLOG.md` was merged into `IMPLEMENTATION-PLAN.md` §7. See `git log` for that history.
