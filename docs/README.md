# Sawt Gateway — Documentation Index

`sawt-gateway` (Go module `sawt-go`) is **one always-on Go binary** that runs the whole platform:
the WhatsApp socket (whatsmeow), the LLM reasoning + tool-calling loop, the STT/TTS speech
pipeline, and the operator web dashboard — in a single process, on a GCP e2-micro (1 GB RAM).

> **Single-instance by design.** The WhatsApp session, in-memory rate limiters, and session
> secret are process-local — run exactly one instance.

## Start here

| If you want to… | Read |
|---|---|
| Understand the architecture & product intent | [BLUEPRINT.md](BLUEPRINT.md) |
| Set up a dev machine, build, deploy, or operate it | [DEPLOYMENT.md](DEPLOYMENT.md) |
| Run and verify the workflow locally before deploying | [LOCAL-TESTING.md](LOCAL-TESTING.md) |
| See production-readiness status, scores & the go-live roadmap | [IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md) |
| See the remaining feature backlog (dashboard/observability) | [BACKLOG.md](BACKLOG.md) |
| Implement the ERP gateway on the `mshalia` side | [mshalia-side.md](mshalia-side.md) |
| Reference agent/LLM/ERP design patterns | [REFERENCE_REPO_SKILLS.md](REFERENCE_REPO_SKILLS.md) |

## Document map

- **[BLUEPRINT.md](BLUEPRINT.md)** — target architecture, current-vs-target gap table, roadmap,
  security model, assumptions. The architectural source of truth.
- **[DEPLOYMENT.md](DEPLOYMENT.md)** — the authoritative runbook: Windows 11 dev setup, `.env`
  reference, build/test/debug, e2-micro optimization, GCP production deploy (systemd, TLS,
  firewall, backups), and security hardening.
- **[LOCAL-TESTING.md](LOCAL-TESTING.md)** — how to exercise the workflow locally before GCP: a
  tiered plan using the bundled mock ERP (`cmd/mockerp`) and workflow driver (`cmd/wfcli`).
- **[IMPLEMENTATION-PLAN.md](IMPLEMENTATION-PLAN.md)** — the authoritative status doc: a weighted
  **Project Ready %** KPI, a 15-category scorecard, code-vs-docs inconsistencies, the risk
  register, and a prioritized 5-phase go-live roadmap.
- **[BACKLOG.md](BACKLOG.md)** — the feature backlog (Epics H/O/S/T) plus the delivery record for
  Phase A fixes and the voice-note archive.
- **[mshalia-side.md](mshalia-side.md)** — the external brief for the `mshalia` ERP team: the exact
  HMAC contract and the 39 tools (across 6 agents) our client already calls.
- **[REFERENCE_REPO_SKILLS.md](REFERENCE_REPO_SKILLS.md)** — reference-only patterns (agent
  orchestration, human-in-the-loop, ERP gateway) distilled from external projects. Some examples
  are LangGraph/Python-flavored; treat as conceptual reference, not the Go implementation.

> **History note:** docs describing the earlier three-runtime design (Next.js dashboard + Python
> LangGraph backend + a separate Go gateway) have been removed — they no longer match this
> consolidated single-binary repo. See `git log` for that history if needed.
