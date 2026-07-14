# Sawt Agentic AI Platform Audit and Next-Generation Architecture Plan

Date: 2026-07-14

## Executive Summary

Sawt has evolved from a prototype-style automation layer into a real WhatsApp-first AI operations gateway. The current Go implementation is pragmatic and already includes several strong production instincts: typed tool calling, role-gated tools, confirmation gates for risky writes, conversation summaries, durable tool logs, STT/TTS fallback chains, MCP hooks, and an operator dashboard. That is a respectable foundation.

It is not yet the architecture Sawt needs for an enterprise-scale conversational AI ecosystem. The current system is still centered on a single runtime, static intent routing, large agent prompts, JSONB agent configuration, in-process workflow execution, basic conversation memory, generic speech providers, and no first-class translation or Arabic linguistic intelligence layer. Those choices are appropriate for an early WhatsApp ERP assistant; they become bottlenecks when Sawt must support many tenants, many domains, many dialects, millions of users, long-running agent workflows, prompt experiments, enterprise approvals, voice personas, terminology governance, and observability at every reasoning step.

The target architecture should split Sawt into a modular AI platform with clear bounded contexts:

- Channel Gateway: WhatsApp, web chat, telephony, SIP, future channels.
- Conversation Runtime: turn-taking, session state, speech streaming, message normalization.
- Agent Orchestration Service: dynamic planning, typed execution, parallelism, retries, interrupts, approvals, resumability.
- Agent Registry: agents, skills, tools, policies, model profiles, releases.
- PromptOps Service: composable prompt modules, versioning, testing, rollout, telemetry.
- Tool and Integration Gateway: ERP, MCP, third-party systems, permissions, idempotency, audit.
- Memory and Knowledge Platform: thread state, user memory, organization memory, vector retrieval, knowledge graphs.
- Translation and Arabic Intelligence Platform: dialect detection, semantic disambiguation, terminology, glossary, translation memory, human review.
- Voice AI Platform: STT, dialogue policy, expressive TTS, voice persona governance, streaming audio.
- AIOps and Evaluation Platform: traces, evals, token/cost metrics, safety, model routing, regression detection.
- Admin Console: a scalable control plane for agents, workflows, prompts, tools, memories, translations, voices, analytics, and governance.

The key architectural shift is this: Sawt should stop treating "agent behavior" as text inside one master prompt and start treating it as a versioned, observable, testable software supply chain.

## Current Architecture Audit

### What Exists Today

Based on the current repo, Sawt is a single Go binary (`sawt-gateway`) that owns:

- WhatsApp socket management through `internal/whatsmeow/client.go`.
- Message handling and end-to-end pipeline wiring in `main.go`.
- Workflow routing and tool execution in `internal/workflow/engine.go`.
- Agent definitions and 39 ERP tool schemas across six agents in `internal/workflow/tools.go`.
- Agent runtime configuration in the `agents` table and `internal/agentcfg/agentcfg.go`.
- Short-term conversation replay and rolling summaries in `internal/workflow/memory.go`.
- STT and TTS fallback orchestration in `internal/speech/stt.go` and `internal/speech/tts.go`.
- ERP tool calls through an HMAC-signed gateway in `internal/erp/client.go`.
- Operator dashboard views in `web/templates/*` and handlers in `web/server.go`.
- PostgreSQL persistence through `schema.sql`, `query.sql`, and sqlc-generated code.

The request describes YAML/JSON node-definition workflows similar to n8n. The current repo appears to have already moved away from external YAML graph files toward Go-coded workflow control plus JSONB configuration. That is an improvement for type safety and testability, but not enough for enterprise orchestration.

### Strengths

- Typed tools are safer than free-form ERP access.
- Role-gated tool exposure limits the model's action surface.
- Confirmation parking for medium/high-risk writes is the right pattern.
- Durable `tool_executions` and `processed_messages` tables reduce audit and duplicate-processing risk.
- Per-agent LLM/TTS/ASR config is a useful step away from a single global bot.
- MCP and skill manifests are early signs of a plugin-capability architecture.
- Rolling summary memory is better than stateless prompting.
- The dashboard exposes agent config without restart.
- Tests cover meaningful failure modes: HMAC, role filtering, tool loop bounds, confirmations, memory, speech fakes, and auth.

### Bottlenecks

1. Single-process runtime
   The same Go process owns WhatsApp, web dashboard, workflow execution, LLM calls, speech, memory summarization, and archival. This simplifies early deployment but creates a scale and reliability ceiling.

2. Static agent routing
   Intent classification maps messages to one of six fixed agent specs. The execution model is closer to "classify then ReAct loop" than dynamic planning, delegation, and resumable workflows.

3. Prompts remain first-class only as text blobs
   The dashboard still frames the main prompt as "Master System Prompt." There is no prompt module graph, semantic diffing, release channel, eval gate, A/B assignment, dependency management, or prompt regression suite.

4. Tool definitions are compiled into Go
   `internal/workflow/tools.go` is safe and clear, but every new domain tool requires code deployment. Enterprise tenants will need tool packs, versioning, schemas, permissions, examples, evals, and owner workflows.

5. Memory is conversation-level, not knowledge-level
   Current memory stores recent turns plus a summary. It does not distinguish episodic memory, user preferences, stable facts, organizational knowledge, retrieved documents, and task/workflow state.

6. Translation is not a platform capability
   Literal translation failures like "stable" are symptoms of missing domain, intent, ontology, glossary, and translation memory layers.

7. Arabic dialect intelligence is not explicit
   The current system mostly relies on model capability and provider language settings. There is no dialect detector, dialect-specific normalization, entity transliteration, register policy, or dialect eval set.

8. TTS is generic
   Google/Hugging Face/gTTS fallback is fine for MVP. It will not deliver branded, emotionally realistic, regional Arabic speech at native quality.

9. Observability is operational, not cognitive
   Trace IDs, logs, and durable tool logs exist. Missing are prompt/component traces, reasoning step traces, retrieval traces, evaluation dashboards, model routing analytics, hallucination scoring, and cost attribution per agent/tool/tenant.

10. Schema strategy is bootstrap-oriented
   `schema.sql` is idempotent and convenient, but enterprise evolution needs versioned migrations, tenant-aware data boundaries, schema contracts, retention classes, and audit immutability.

## Root-Cause Analysis

The root cause is not "bad prompts" or "wrong framework." The root cause is an early-stage architectural compression: many distinct platform responsibilities are collapsed into a small number of abstractions.

- Agent behavior is compressed into prompts.
- Orchestration is compressed into a synchronous Go loop.
- Domain policy is compressed into static tool maps and prompt instructions.
- Translation intelligence is compressed into generic model inference.
- Arabic nuance is compressed into "reply in the user's language."
- Memory is compressed into recent turns plus summaries.
- Observability is compressed into logs and activity rows.
- Administration is compressed into one agent-editing page.

This compression is productive during prototyping. At scale it prevents independent ownership, testing, deployment, and optimization.

## Target Architecture

### Architectural Principles

- Domain-driven boundaries: separate agents, prompts, tools, memory, translation, speech, and evaluations.
- Event-driven execution: every conversation turn, tool call, approval, model call, translation, and TTS job emits events.
- Durable orchestration: long-running tasks persist state and can resume after crashes.
- Policy before prompting: permissions, safety, tenant boundaries, and approvals must be deterministic runtime checks.
- Composable prompts: no giant master prompts.
- Retrieval over prompt stuffing: load context only when relevant.
- Human review where ambiguity or risk is high.
- Model portability: use model profiles and routing, not hardcoded vendor assumptions.
- Full observability: every AI decision must be traceable, replayable where possible, and evaluable.

### Proposed Service Map

1. Channel Gateway
   Owns WhatsApp, telephony, web chat, and future channels. Converts inbound messages into normalized `ConversationTurnRequested` events. Handles delivery receipts, media storage, retries, and channel-specific rate limits.

2. Conversation Runtime
   Owns sessions, turn state, interruption, streaming, voice activity, and output modality. It decides whether a turn is text-only, voice-in/text-out, voice-in/voice-out, or realtime duplex.

3. Agent Orchestration Service
   Owns task planning, agent selection, graph execution, tool execution coordination, approval checkpoints, retries, compensation, and resumability.

4. Agent Registry
   Stores agent definitions, releases, capabilities, owners, policies, tools, skills, and model profiles. Agents become deployable artifacts, not dashboard rows only.

5. PromptOps Service
   Stores prompt modules, templates, variables, inheritance, test suites, releases, and rollout rules. Produces compiled prompts for each model call.

6. Tool Gateway
   A deterministic gateway for ERP/MCP/external tools. Enforces schema validation, RBAC/ABAC, tenant scoping, idempotency, approval requirements, audit, and commit-time authorization.

7. Memory and Knowledge Platform
   Separates thread checkpoints, episodic memory, user facts, organization facts, documents, embeddings, ontology, and graph relationships.

8. Translation and Localization Service
   Performs language/dialect detection, semantic disambiguation, glossary lookup, translation memory lookup, adaptive translation, and human review routing.

9. Voice AI Service
   Owns STT, speech normalization, pronunciation dictionaries, voice personas, expressive TTS, SSML, streaming synthesis, and voice governance.

10. AIOps and Evaluation Platform
   Owns traces, metrics, evals, red-team suites, prompt regression tests, safety classifiers, dashboards, canary releases, and cost attribution.

11. Admin Console
   A task-focused platform UI for configuring and observing every above resource.

## Orchestration Framework

### Recommendation

Replace n8n-style static YAML/JSON graphs and the current synchronous loop with a durable typed orchestration model.

Use two layers:

- Durable workflow engine for business execution: Temporal, Durable Task, or equivalent.
- Agent graph runtime for LLM decision flow: LangGraph-style checkpoints/interrupts or a custom Go state machine with equivalent semantics.

LangGraph's public docs explicitly distinguish checkpointers for thread-scoped state and stores for cross-thread memory, and its interrupt model supports persisted human-in-the-loop pauses and resumes. Temporal-style workflow engines are better for non-LLM durability, retries, timers, and sagas. Use the right tool at each layer.

### Why Not Pure YAML/JSON Graphs

Keep declarative definitions for configuration, not as the whole execution runtime.

YAML/JSON is suitable for:

- Agent manifests.
- Tool schemas.
- Prompt module metadata.
- Eval cases.
- Routing policies.
- Approval policies.
- Workflow templates.

YAML/JSON is weak for:

- Recursive decomposition.
- Parallel branches with joins.
- Long-running resumability.
- Dynamic replanning.
- Complex error handling.
- Stateful human approvals.
- Runtime policy checks.
- Rich typed testing.

### Target Execution Model

Each conversation turn creates a run:

1. Ingest and normalize message.
2. Detect language, dialect, modality, user, tenant, channel.
3. Retrieve active session checkpoint.
4. Build task frame: intent, entities, risk, required permissions.
5. Select agent or agent team.
6. Planner creates a bounded execution plan.
7. Policy engine validates allowed actions.
8. Executor runs tool calls, retrieval, translation, and sub-agents.
9. If approval is needed, persist interrupt and wait.
10. On resume, refresh authority and state before committing.
11. Generate response.
12. Run safety/localization/voice adaptation.
13. Emit traces, eval samples, metrics, and memory candidates.

### Agent Patterns

- Router Agent: classifies task and selects specialist.
- Planner Agent: decomposes task into steps.
- Executor Agent: performs tool calls.
- Critic/Verifier Agent: checks outputs against policy and facts.
- Localization Agent: adapts wording and dialect.
- Voice Director Agent: selects prosody, style, SSML/emotion controls.
- Human Approval Agent: packages approvals for admins/managers.

Do not overuse agents. A deterministic function is better than an agent whenever the rule is known.

## Prompt Management System

### Replace Master Prompt With Prompt Modules

Prompt hierarchy:

- Global platform prompt: safety, tool discipline, tenant isolation, language policy.
- Domain prompt: equine ERP, accounting, administration, sales, breeding.
- Agent prompt: role, responsibilities, tone, allowed behaviors.
- Task prompt: specific workflow instruction.
- Tool prompt: when/how to use a tool, examples, failure modes.
- Localization prompt: target language/dialect/register.
- Output prompt: structured response schema, voice-friendly style.

### Prompt Module Schema

Recommended fields:

- `id`
- `name`
- `type`: system, domain, agent, task, tool, guardrail, localization, output
- `version`
- `status`: draft, staged, canary, production, deprecated
- `owner_team`
- `locale_scope`
- `domain_scope`
- `model_family`
- `template`
- `variables`
- `dependencies`
- `eval_suite_ids`
- `risk_level`
- `created_by`
- `approved_by`
- `changelog`

### Prompt Compilation

At runtime, the PromptOps service should compile:

`platform + tenant policy + domain + agent + task + tool instructions + retrieved context + locale module + output schema`

The compiled prompt should be immutable for a run and stored by hash, with the module versions recorded in traces.

### PromptOps Capabilities

- Versioning and rollback.
- Semantic diff.
- Prompt dependency graph.
- Unit evals per prompt module.
- Regression evals across golden conversations.
- A/B and canary assignment by tenant, channel, agent, or percentage.
- Token and latency budgets per prompt.
- Model compatibility checks.
- Prompt linting: contradiction, overly broad authority, missing output schema.
- Structured output validation.
- Hallucination/factuality evals.

## Translation and Localization Architecture

### Core Problem

The word "stable" is not a translation problem alone. It is a semantic grounding problem. Sawt must know whether the context is equine housing, emotional state, software release, finance, chemistry, or something else.

### Target Pipeline

1. Language and dialect detection
   Detect English, Modern Standard Arabic, Gulf, Saudi, Najdi, Hijazi, Egyptian, Levantine, code-switching, and mixed Arabizi where relevant.

2. Domain classification
   Identify domain: equine operations, accounting, contracts, CRM, breeding, software support, general chat.

3. Sense disambiguation
   Use context, entities, ontology, and examples to map ambiguous terms to domain-specific concepts.

4. Terminology lookup
   Check multilingual terminology database before generic translation.

5. Translation memory lookup
   Reuse previous approved translations for exact or fuzzy matches.

6. Machine translation / LLM translation
   Use a provider with glossary support or an LLM with structured constraints.

7. Quality estimation
   Score ambiguity, glossary compliance, entity preservation, and dialect fit.

8. Human review
   Route high-ambiguity or high-risk translations to reviewers.

9. Continuous learning
   Approved human edits update terminology and translation memory.

### Terminology and Ontology

Create a concept-first terminology system:

- Concept: `equine.facility.stable`
- English terms: stable, barn, horse stable
- Arabic terms: إسطبل, مربط, حظيرة خيل depending on context
- Dialect variants: Saudi/Gulf preferred terms
- Forbidden translations: مستقر when referring to horse facilities
- Related entities: stall, barn, paddock, mare, stallion
- Domain: equine operations
- Examples: source and target sentences
- Approval status and owner

### Translation Memory

Use TMX-compatible storage where possible. Google Cloud Translation glossaries support domain-specific terminology and explicitly call out ambiguous terms as a use case. Store translation memory independently so Sawt is not locked to one vendor.

### Recommended Providers

- Google Cloud Translation Advanced: glossaries, adaptive translation, custom models, broad language coverage.
- Microsoft Custom Translator: custom domain models and enterprise workflows.
- Amazon Translate: enterprise translation with custom terminology support.
- LLM translation layer: for semantic disambiguation, style transfer, dialect adaptation, and ambiguity explanation.

### Human Review Workflow

Trigger review when:

- Ambiguity score exceeds threshold.
- Term has no approved equivalent.
- Text affects legal, accounting, medical, veterinary, or customer-facing commitments.
- The user asks for formal translation.
- Model confidence is low or dialect detection is uncertain.

## Arabic Linguistic Intelligence Framework

### Supported Varieties

Sawt should support:

- Modern Standard Arabic.
- Gulf Arabic.
- Saudi Arabic.
- Najdi.
- Hijazi.
- Egyptian Arabic.
- Levantine Arabic.
- English-Arabic code switching.

### Architecture

1. Arabic Normalization
   Normalize diacritics, hamza variants, alef/ya/ta marbuta variants, elongation, punctuation, numerals, and common speech-to-text artifacts.

2. Dialect Detection
   Classify dialect at utterance and segment level. Do not force one dialect for code-switched messages.

3. Entity and Transliteration Layer
   Handle horse names, person names, brands, medication names, invoice numbers, dates, and amounts. Maintain name aliases in Arabic and English.

4. Register Policy
   Decide whether response should be formal MSA, Saudi conversational, Gulf conversational, or customer-service neutral.

5. Dialectal NLU Evals
   Build test suites for each dialect with intents, slots, entities, and expected tool calls.

6. Cultural Pragmatics
   Preserve politeness, indirect requests, honorifics, and concise WhatsApp-appropriate phrasing.

### Arabic-Specific Data Assets

- Dialect phrasebook.
- Domain glossary.
- Horse and stable terminology.
- Accounting terminology.
- Vet/medical terminology.
- Named entity alias table.
- Pronunciation lexicon for TTS.
- Ambiguous term registry.
- Golden conversation evals by dialect.

## Voice AI Architecture

### Audit of Current Speech Stack

Current STT:

- Groq Whisper
- Hugging Face
- Google Cloud STT
- local whisper.cpp fallback

Current TTS:

- Google Cloud TTS
- Hugging Face Spaces
- local gTTS fallback

This is viable for MVP resilience. It is not enough for native-quality Saudi/Gulf conversational voice.

### Target Voice Stack

1. Streaming STT
   Low-latency transcription with partial hypotheses, endpointing, VAD, diarization where needed, and dialect-aware post-processing.

2. Spoken NLU
   Normalize ASR output, recover entities, detect uncertainty, and request confirmation when speech ambiguity affects action.

3. Dialogue Turn-Taking
   Support barge-in, interruption, repair, and "hold while I check" behaviors.

4. Voice Director
   Converts final reply into speech plan: emotion, pacing, emphasis, pauses, dialect, persona, SSML, and pronunciation hints.

5. Expressive TTS
   Use provider-specific controls for emotion, speed, voice persona, streaming, and pronunciation.

6. Audio Renderer
   Mix background audio where appropriate, normalize loudness, encode to channel format, and stream or send as voice note.

7. Voice Governance
   Consent, licensing, watermarking/detection where available, restricted cloning, audit logs, and abuse monitoring.

### Commercial TTS Recommendations

- ElevenLabs: strong expressive voice and voice cloning ecosystem; docs expose streaming speech, conversational AI, professional voice cloning, voice changer, and WhatsApp-related APIs.
- Cartesia Sonic: low-latency API with byte/SSE/WebSocket TTS, voice cloning/localization endpoints, and emotion/speed controls in its API. Note: current public docs list many supported languages but do not list Arabic in the Cartesia TTS endpoint language enum, so treat Arabic support as a vendor-validation item before adoption.
- Google Cloud TTS: reliable enterprise fallback, SSML support, stable operations, but less differentiated for branded emotional Arabic.
- Azure Speech: strong enterprise controls and custom neural voice governance.
- OpenAI Realtime/audio APIs: useful for realtime voice agents, tool use, streaming, and unified multimodal interaction where available.

### Open-Source / Self-Hosted Options

Evaluate, do not blindly adopt:

- Coqui XTTS / successors for multilingual cloning.
- StyleTTS-family models for expressive control.
- Piper for lightweight deterministic local voices.
- Fish Audio / multilingual voice-cloning research models where license and production maturity fit.
- Whisper variants or faster-whisper for local/edge STT.

For Sawt's enterprise path, use commercial TTS for production quality while building a self-hosted research lane for cost control and dialect specialization.

## Memory Architecture

### Required Memory Types

- Turn state: current workflow checkpoint.
- Short-term memory: recent conversation messages.
- Episodic memory: important past interactions and completed tasks.
- Semantic user memory: preferences, language, dialect, permissions, stable facts.
- Organization memory: tenant policies, operating procedures, terminology, clients, facilities.
- Domain memory: equine concepts, accounting rules, breeding knowledge, veterinary terms.
- Tool memory: previous tool outcomes and known entity IDs.
- Translation memory: approved source-target pairs.

### Storage Model

- PostgreSQL: canonical relational state, audit, configurations, approvals.
- Redis or equivalent: ephemeral locks, hot sessions, rate limits.
- Object storage: voice notes, attachments, transcripts.
- Vector database: semantic retrieval. Qdrant, pgvector, Weaviate, or Pinecone are options. Qdrant is a dedicated vector database with filtering and payload support; pgvector is attractive if Postgres simplicity matters more than specialized scaling.
- Knowledge graph: Neo4j, Amazon Neptune, or PostgreSQL graph-style tables for entity relationships and ontology.

### Memory Governance

- Explicit memory classes: transient, retained, regulated, sensitive.
- TTL by memory type.
- User-visible memory controls.
- Tenant-level memory isolation.
- PII redaction and encryption.
- Memory provenance.
- Memory versioning.
- Memory evals: retrieval precision, harmful memory, stale facts, contradiction detection.

## AI Observability and AIOps

### What to Measure

- Model calls: provider, model, prompt hash, completion hash, latency, tokens, cost, errors.
- Prompt modules: versions, rollout group, eval pass/fail, regression deltas.
- Tools: requested tool, allowed/denied, args, validation errors, execution result, idempotency key.
- Retrieval: query, retrieved chunks, scores, source documents, answer grounding.
- Translation: language, dialect, glossary hits, ambiguity score, review outcome.
- Voice: STT confidence, WER samples, TTS provider, synthesis latency, voice persona, interruption rate.
- Workflow: steps, branches, retries, interrupts, resumes, compensations.
- Safety: prompt-injection attempts, policy denials, hallucination flags, toxic content, unsafe tool requests.
- Business: automation rate, confirmation rate, handoff rate, task completion rate, user satisfaction.

### Tooling

- OpenTelemetry for traces, metrics, and logs.
- Prometheus/Grafana or managed equivalent for infra metrics.
- LangSmith, OpenAI tracing/evals, Arize Phoenix, Braintrust, or custom eval platform for LLM traces and evals.
- Warehouse: BigQuery/Snowflake/Postgres analytics for long-term reporting.

OpenTelemetry's model of traces, metrics, and logs is a good baseline for platform observability. Sawt should add AI-specific spans on top of standard distributed tracing.

## Admin Console UX Redesign

### Current Problem

The current dashboard exposes an agent editor with provider config, master prompt, skills/MCP/sub-agents, TTS, and clarification rules. It is useful, but it conflates many platform concepts into one long form.

### Target Information Architecture

Primary navigation:

- Overview
- Conversations
- Agents
- Agent Teams
- Skills
- Tools
- Workflows
- Prompts
- Knowledge
- Memory
- Translation
- Voice
- Models
- Evaluations
- Analytics
- Approvals
- Audit
- Settings

### Key Screens

1. Agent Registry
   List agents by status, owner, domain, version, model, tool access, eval health, and traffic.

2. Agent Detail
   Tabs: Overview, Releases, Prompt Stack, Tools, Memory, Policies, Evals, Traces, Analytics.

3. Prompt Builder
   Modular prompt tree, variable inspector, preview compiled prompt, token estimate, eval status, release controls.

4. Workflow Studio
   Not a decorative node canvas first. Provide a dense execution graph with typed states, approval nodes, retry policy, and trace replay.

5. Tool Registry
   Tool schemas, permission rules, risk class, idempotency, owner, tests, usage, failure rate.

6. Translation Console
   Glossaries, terminology, translation memory, ambiguous terms, review queue, dialect settings.

7. Voice Console
   Voice personas, samples, pronunciation dictionary, SSML/emotion presets, consent/licensing, quality tests.

8. Evaluation Dashboard
   Golden test suites, model/prompt comparisons, regression alerts, dialect evals, tool-call accuracy, human review quality.

9. Trace Explorer
   Conversation replay with every prompt module, model call, retrieval, tool call, approval, translation, and TTS step.

### UX Principles

- Dense operational interface, not a marketing-style surface.
- Show status and risk everywhere.
- Make versions and releases obvious.
- Put eval health beside every deployable AI artifact.
- Provide "why did the agent do this?" from every conversation.
- Separate editing, testing, approval, and production rollout.

## Database Schema Recommendations

### Core Tables

`tenants`
- id, name, region, data_residency, retention_policy_id

`agent_definitions`
- id, tenant_id, name, domain, owner_team, status

`agent_versions`
- id, agent_id, version, prompt_stack_id, model_profile_id, tool_policy_id, memory_policy_id, release_notes, created_by

`agent_releases`
- id, agent_version_id, environment, rollout_percent, status, started_at, ended_at

`prompt_modules`
- id, type, name, body, variables, owner_team, status

`prompt_module_versions`
- id, prompt_module_id, version, body, hash, changelog, approved_by

`prompt_stacks`
- id, name, tenant_id, domain, status

`prompt_stack_modules`
- stack_id, module_version_id, order_index, condition_expr

`tool_definitions`
- id, name, domain, schema_json, output_schema_json, risk_level, idempotent, owner_team

`tool_policies`
- id, tenant_id, name, policy_json

`workflow_definitions`
- id, name, type, manifest_json, owner_team, status

`workflow_runs`
- id, tenant_id, conversation_id, workflow_definition_id, status, current_state, started_at, completed_at

`workflow_steps`
- id, workflow_run_id, step_name, status, input_json, output_json, error_json, started_at, completed_at

`approval_requests`
- id, workflow_run_id, tool_call_id, approver_policy, status, expires_at, decided_by, decided_at

`conversation_sessions`
- id, tenant_id, user_id, channel, language, dialect, status

`conversation_turns`
- id, session_id, role, content, modality, metadata_json, created_at

`memory_items`
- id, tenant_id, subject_type, subject_id, memory_type, content, embedding_id, sensitivity, expires_at, provenance_json

`knowledge_documents`
- id, tenant_id, source, title, version, checksum, metadata_json

`knowledge_chunks`
- id, document_id, chunk_text, embedding_id, metadata_json

`terminology_concepts`
- id, tenant_id, concept_key, domain, definition, status

`terminology_terms`
- id, concept_id, language, dialect, term, forbidden, preferred, notes

`translation_memory`
- id, tenant_id, source_lang, target_lang, source_text, target_text, domain, quality_score, approved_by

`voice_personas`
- id, tenant_id, name, provider, voice_id, language, dialect, consent_status, license_json

`ai_traces`
- id, run_id, span_id, parent_span_id, span_type, payload_json, started_at, ended_at

`eval_suites`
- id, tenant_id, name, target_type, status

`eval_cases`
- id, suite_id, input_json, expected_json, tags

`eval_runs`
- id, suite_id, target_version, status, metrics_json, created_at

### Migration Strategy

Move from raw idempotent schema bootstrapping to versioned migrations using Goose, Atlas, Flyway, Liquibase, or equivalent. Use migration checks in CI and environment promotion gates.

## API Architecture

### Public/External APIs

- `POST /v1/channel-events`
- `POST /v1/conversations/{id}/messages`
- `GET /v1/conversations/{id}/events`
- `POST /v1/approvals/{id}/decision`
- `GET /v1/agents`
- `POST /v1/agents`
- `POST /v1/evals/run`

### Internal APIs

- Agent Registry API
- Prompt Compilation API
- Tool Gateway API
- Memory Retrieval API
- Translation API
- Voice Synthesis API
- Trace Ingestion API
- Policy Decision API

### Event Topics

- `conversation.turn.requested`
- `conversation.turn.completed`
- `agent.run.started`
- `agent.run.step.completed`
- `tool.call.requested`
- `tool.call.completed`
- `approval.requested`
- `approval.decided`
- `translation.review.requested`
- `memory.candidate.created`
- `voice.synthesis.completed`
- `eval.regression.detected`

## Security and Governance

### Required Controls

- Tenant isolation at every table, object, vector namespace, and trace.
- Deterministic policy engine for RBAC/ABAC.
- Commit-time authorization for durable side effects.
- Idempotency keys for all writes.
- Human approval for risky financial, legal, medical, veterinary, and customer-impacting actions.
- Tool allowlists by agent version.
- Secrets stored only in secret manager, never dashboard JSON.
- Prompt injection defenses at retrieval and tool boundaries.
- PII classification, redaction, and retention.
- Full audit logs for admin edits and AI actions.
- Voice cloning consent and licensing records.
- Model/provider data residency policy.
- Canary deployment and rollback.

### Commit-Time Authorization

Before any durable effect, refresh:

- User identity.
- Tenant membership.
- Role and permissions.
- Approval status.
- Entity version or precondition.
- Tool policy.
- Idempotency key.

The approval that was valid when the agent planned a task may not still be valid when the tool commits. This distinction is critical for AI agents.

## Scalability Strategy

### Millions of Users

Move from one process to horizontally scalable services:

- Stateless channel ingress workers.
- Partitioned conversation runtime by tenant/session.
- Durable workflow queues.
- Separate LLM/speech worker pools.
- Backpressure and per-tenant quotas.
- Caching for identity, tool schemas, prompt stacks, and glossary data.
- Regional deployments for data residency and latency.
- Async voice synthesis for non-realtime channels.
- Streaming voice path for realtime channels.
- Vector indexes partitioned by tenant and document class.
- Event log for replay and analytics.

### Reliability Targets

- At-least-once message ingestion with dedup.
- Exactly-once durable side effects through idempotency.
- Workflow retry policies with compensation.
- Dead-letter queues.
- Provider fallbacks with model-quality gates.
- Circuit breakers for LLM, STT, TTS, ERP, translation.
- Graceful degradation: text fallback when voice fails, human handoff when AI confidence is low.

## Migration Roadmap

### Phase 0: Stabilize Current System

- Finish live verification with real WhatsApp, real ERP, real STT/TTS/LLM credentials.
- Add versioned database migrations.
- Add OpenTelemetry spans around LLM, STT, TTS, tools, memory, and dashboard actions.
- Add prompt hash logging.
- Add tenant IDs to all future-facing tables.
- Build golden evals for current six agents.

### Phase 1: PromptOps Foundation

- Extract current prompts from Go and DB blobs into prompt modules.
- Add prompt module versioning and compiled prompt hashes.
- Add eval suites for prompt changes.
- Add canary prompt releases.
- Update dashboard from "master prompt" to "prompt stack."

### Phase 2: Tool Registry and Policy Gateway

- Move tool definitions into a registry while generating Go types or validators.
- Add tool versioning, owners, evals, examples, and permission policies.
- Enforce commit-time authorization.
- Add approval routing beyond user self-confirmation.

### Phase 3: Durable Orchestration

- Introduce workflow run/step tables or Temporal.
- Convert pending confirmations into generalized interrupts.
- Add resumable long-running workflows.
- Support parallel tool calls where safe.
- Add retry, compensation, and dead-letter workflows.

### Phase 4: Translation and Arabic Intelligence

- Build terminology concepts and term tables.
- Add glossary and translation memory.
- Add language/dialect detection and normalization.
- Add ambiguity detection for terms like "stable."
- Add human translation review queue.
- Add Arabic dialect eval suites.

### Phase 5: Memory and Knowledge Platform

- Add vector retrieval and document ingestion.
- Separate thread memory, user memory, org memory, and domain knowledge.
- Add memory candidate extraction and approval.
- Add memory privacy controls and expiration.

### Phase 6: Voice AI Upgrade

- Add streaming STT and partial transcripts.
- Add pronunciation dictionary.
- Add voice persona registry.
- Evaluate ElevenLabs, Azure, Google, OpenAI, and Arabic-specialized vendors.
- Add SSML/emotion/prosody layer.
- Add barge-in and realtime path for supported channels.

### Phase 7: Enterprise Admin Console

- Redesign navigation around resources, versions, evals, and traces.
- Add trace replay.
- Add release approval flows.
- Add dashboards for cost, latency, quality, safety, and business outcomes.

### Phase 8: Multi-Tenant Scale

- Split services.
- Add event bus.
- Add regional deployment model.
- Add per-tenant quotas and billing analytics.
- Add disaster recovery and data residency controls.

## Risks and Trade-Offs

| Risk | Trade-off | Mitigation |
|---|---|---|
| Over-engineering too early | Platform architecture can slow product learning | Migrate in phases; preserve current Go runtime until new services prove value |
| Framework lock-in | LangGraph/Temporal/vendor choices may constrain future design | Keep manifests, state, and APIs owned by Sawt |
| Latency growth | More services and checks add round trips | Cache prompt stacks, policies, tool schemas; stream responses; use async for noncritical work |
| Cost growth | More evals, traces, and model calls cost money | Token budgets, sampling, small-model routing, batch evals |
| Arabic quality remains hard | Dialects and code-switching require data | Build real Sawt-specific eval corpus and review loop |
| Voice cloning abuse | Realistic voices create legal and safety risk | Consent, licensing, watermarking/detection where available, audit, restricted access |
| Translation review load | Human review can bottleneck | Review only ambiguity/risk cases; learn continuously from edits |
| Tool registry complexity | Dynamic tools can reduce type safety | Generate typed clients/validators from schemas and test contracts |

## Prioritized Implementation Plan

### Highest Impact, Lowest Regret

1. Add OpenTelemetry and AI trace schema.
2. Add prompt hashes and prompt module extraction.
3. Add golden eval suites for current workflows.
4. Add terminology database for equine/accounting Arabic-English terms.
5. Add dialect/language detection and normalization.
6. Add versioned migrations.
7. Add tool registry metadata without yet removing Go tool definitions.

### Medium-Term Platform Work

1. Prompt stack UI.
2. Translation memory and human review queue.
3. Durable workflow interrupts.
4. Memory service with vector retrieval.
5. Voice persona registry and pronunciation dictionary.
6. Eval dashboard.
7. Commit-time authorization.

### Long-Term Enterprise Work

1. Service decomposition.
2. Temporal or equivalent workflow engine.
3. Multi-region tenant architecture.
4. Realtime voice agents with barge-in.
5. Full agent marketplace/tool-pack model.
6. Automated model routing and cost optimizer.

## Recommended Technology Stack

### Keep

- Go for gateway, policy, tool execution, and high-reliability services.
- PostgreSQL for canonical data.
- sqlc-style typed DB access.
- HMAC-signed ERP gateway pattern.
- Typed function/tool calling.

### Add

- Versioned migrations: Goose, Atlas, or Flyway.
- OpenTelemetry for traces/metrics/logs.
- Temporal for durable workflows, or implement a smaller workflow runtime only if Temporal is too heavy.
- LangGraph-style orchestration semantics for AI graph state, interrupts, and stores if Python runtime is acceptable; otherwise mirror the concepts in Go.
- Redis for hot state, locks, and rate limiting.
- Qdrant/pgvector/Weaviate/Pinecone for vector retrieval.
- Neo4j/Neptune/Postgres graph tables for ontology if graph queries become important.
- Object storage for media and artifacts.
- Kafka/Pub/Sub/NATS/SQS for events depending on cloud choice.
- LangSmith/Braintrust/Arize Phoenix/OpenAI Evals or custom eval system.

### Voice and Translation Vendors to Evaluate

- STT: OpenAI, Deepgram, Google, Azure, Groq Whisper, self-hosted faster-whisper.
- TTS: ElevenLabs, Azure Neural Voice, Google Cloud TTS, OpenAI audio/realtime, Arabic-specialized vendors, Cartesia if Arabic support is confirmed.
- Translation: Google Cloud Translation Advanced, Microsoft Custom Translator, Amazon Translate, LLM-based semantic translation layer.

## Source Notes

The recommendations above are grounded in the current Sawt repo and these current public references:

- OpenAI API docs: Agents SDK, tools, orchestration, guardrails, evals, realtime/audio navigation: https://developers.openai.com/api/docs/guides/agents
- LangGraph persistence docs: https://docs.langchain.com/oss/python/langgraph/persistence
- LangGraph interrupts docs: https://docs.langchain.com/oss/python/langgraph/interrupts
- Temporal workflow docs: https://docs.temporal.io/workflows
- OpenTelemetry observability primer: https://opentelemetry.io/docs/concepts/observability-primer/
- Google Cloud Translation glossaries: https://docs.cloud.google.com/translate/docs/advanced/glossary
- Microsoft Custom Translator overview: https://learn.microsoft.com/en-us/azure/ai-services/translator/custom-translator/overview
- Amazon Translate overview: https://docs.aws.amazon.com/translate/latest/dg/what-is.html
- ElevenLabs speech API docs: https://elevenlabs.io/docs/api-reference/text-to-speech/convert
- Cartesia TTS API docs: https://docs.cartesia.ai/2024-06-10/api-reference/tts/bytes
- Qdrant overview: https://qdrant.tech/documentation/overview/

