# Reference Repository Skills & Practical Patterns

> **Distilled from four reference implementations** for the Sawt conversational ERP platform.
> Use this document as a quick-reference during development — each pattern includes **when to apply it**, **how to implement it**, and **pitfalls to avoid**.
>
> *Generated: 2026-06-30 • Sources: [Bulwark](https://github.com/gauravxthakur/Bulwark), [ChatbotForERP](https://github.com/M-asquerade/ChatbotForERP), [ERP-AI](https://github.com/repo-anuj/ERP-AI), [LangGraph](https://github.com/langchain-ai/langgraph)*

---

## Table of Contents

1. [LangGraph Agent Orchestration](#1-langgraph-agent-orchestration)
2. [Tool-Calling & Function Execution](#2-tool-calling--function-execution)
3. [Human-in-the-Loop for Financial Safety](#3-human-in-the-loop-for-financial-safety)
4. [Memory & Conversation State Management](#4-memory--conversation-state-management)
5. [Text-to-SQL for Ad-Hoc Analytics](#5-text-to-sql-for-ad-hoc-analytics)
6. [Entity Resolution & Fuzzy Matching](#6-entity-resolution--fuzzy-matching)
7. [Error Handling & Self-Correction](#7-error-handling--self-correction)
8. [ERP Gateway & Middleware Layer](#8-erp-gateway--middleware-layer)
9. [Multi-Tenancy & Session-Scoped Context](#9-multi-tenancy--session-scoped-context)
10. [Streaming & Real-Time UX](#10-streaming--real-time-ux)
11. [Observability & Debugging Agents](#11-observability--debugging-agents)
12. [Production Deployment Patterns](#12-production-deployment-patterns)
13. [AI-First ERP Design Philosophy](#13-ai-first-erp-design-philosophy)
14. [Plan-and-Execute for Complex Workflows](#14-plan-and-execute-for-complex-workflows)
15. [Structured Output with Pydantic](#15-structured-output-with-pydantic)

---

## 1. LangGraph Agent Orchestration

**Source:** [Bulwark](https://github.com/gauravxthakur/Bulwark) + [LangGraph](https://github.com/langchain-ai/langgraph)

### When to Use
Every time you build the Sawt agent layer — this is the orchestration backbone.

### Pattern: Supervisor + Subgraph Hierarchy

```python
from langgraph.graph import StateGraph, START, END
from typing import TypedDict, Annotated
from langgraph.graph.message import add_messages

class AgentState(TypedDict):
    messages: Annotated[list, add_messages]
    intent: str | None
    routed_agent: str | None
    summary: str

# Supervisor graph
supervisor = StateGraph(AgentState)
supervisor.add_node("classify", classify_intent)
supervisor.add_node("operations", operations_subgraph)  # compiled subgraph
supervisor.add_node("accounting", accounting_subgraph)
supervisor.add_node("administration", admin_subgraph)
supervisor.add_node("respond", generate_response)

supervisor.add_edge(START, "classify")
supervisor.add_conditional_edges("classify", route_to_agent, {
    "operations": "operations",
    "accounting": "accounting",
    "administration": "administration",
})
supervisor.add_edge("operations", "respond")
supervisor.add_edge("accounting", "respond")
supervisor.add_edge("administration", "respond")
supervisor.add_edge("respond", END)
```

### Key Rules
- **Compile subgraphs at startup** — never re-compile inside a node per request
- **Use `TypedDict` or Pydantic** for state — loose dicts cause silent bugs
- **Set recursion limits**: `graph.compile(recursion_limit=25)` to prevent runaway loops
- **Thread IDs are critical** for multi-tenant: `config = {"configurable": {"thread_id": f"wa-{jid}"}}`

### Pitfalls
- Don't use the Swarm pattern initially — it's harder to debug and trace. Start with Supervisor.
- Don't mix `.bind_tools()` and `.with_structured_output()` on the same node — they compete for the same API capability.

---

## 2. Tool-Calling & Function Execution

**Source:** [Bulwark](https://github.com/gauravxthakur/Bulwark) + [LangGraph](https://github.com/langchain-ai/langgraph) + [ERP-AI](https://github.com/repo-anuj/ERP-AI)

### When to Use
Every ERP operation exposed to the agent.

### Pattern: @tool + ToolNode + Gateway Client

```python
from langchain_core.tools import tool
from langgraph.prebuilt import ToolNode, tools_condition

@tool
def assign_horse_to_stall(horse_id: str, stall_id: str, override_occupied: bool = False) -> str:
    """Place or move a horse into a stall. Atomic — releases old stall automatically.
    
    Args:
        horse_id: The horse's Firestore document ID.
        stall_id: Target stall ID (use null to unassign).
        override_occupied: If true, evicts the current occupant (requires confirmation).
    """
    return gateway_client.call("operations.assign_horse_to_stall", {
        "horseId": horse_id,
        "stallId": stall_id,
        "overrideOccupied": override_occupied
    })

# Bind to model
model = ChatOpenAI(model="gpt-4o").bind_tools([assign_horse_to_stall, create_task, ...])

# ToolNode handles execution
tool_node = ToolNode(
    [assign_horse_to_stall, create_task, ...],
    handle_tool_errors=True  # CRITICAL: lets LLM self-correct
)

# In the graph
builder.add_node("agent", call_model)
builder.add_node("tools", tool_node)
builder.add_conditional_edges("agent", tools_condition)  # auto-routes to tools or END
builder.add_edge("tools", "agent")  # cycle back for multi-step
```

### Key Rules
- **Docstrings are everything** — the LLM reads them to decide when/how to call the tool. Be precise.
- **Always use `handle_tool_errors=True`** — without it, a tool failure crashes the graph instead of giving the LLM a chance to self-correct.
- **Never expose raw DB access** — every tool wraps a Gateway API call.
- **Use `tools_condition`** from prebuilt — it auto-routes based on whether the model made a tool call.

### Pitfalls
- Tool arguments must be simple types (str, int, float, bool, list) — not complex nested objects.
- Don't define too many tools per subgraph — LLMs degrade with >15-20 tools in context. Route first, then expose only the relevant subset.

---

## 3. Human-in-the-Loop for Financial Safety

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph) + [Bulwark](https://github.com/gauravxthakur/Bulwark)

### When to Use
Any financial transaction, soft-delete, or action exceeding a risk/amount threshold.

### Pattern: interrupt() + Command(resume=)

```python
from langgraph.types import interrupt, Command

def confirmation_node(state: AgentState):
    """Pauses the graph and asks the user for confirmation."""
    tool_call = state["pending_tool_call"]
    
    # This pauses the graph — state is checkpointed
    user_response = interrupt({
        "type": "confirmation_required",
        "action": tool_call["name"],
        "args": tool_call["args"],
        "risk_level": tool_call["risk"],
        "message_ar": f"هل تريد تنفيذ {tool_call['description']}؟",
        "message_en": f"Confirm: {tool_call['description']}?"
    })
    
    # Graph resumes here when Command(resume=...) is called
    if user_response == "yes":
        return {"confirmed": True, "confirmation_token": generate_token()}
    else:
        return {"confirmed": False, "messages": [AIMessage("تم الإلغاء.")]}

# Resuming from WhatsApp response
def handle_whatsapp_reply(thread_id, user_message):
    if user_message.lower() in ["نعم", "إيه", "yes", "أيوا"]:
        graph.invoke(Command(resume="yes"), config={"configurable": {"thread_id": thread_id}})
    else:
        graph.invoke(Command(resume="no"), config={"configurable": {"thread_id": thread_id}})
```

### Three HITL Patterns

| Pattern | Use Case | Implementation |
|---------|----------|----------------|
| **Approve/Reject** | Financial posts, soft-deletes | `interrupt()` → user says yes/no → `Command(resume=)` |
| **Review & Edit** | Data entry validation | `interrupt()` with pre-filled data → user corrects → `Command(resume=corrected_data)` |
| **Review Tool Call** | Before any ERP write | `compile(interrupt_before=["tools"])` — fixed breakpoint |

### Key Rules
- **Checkpointing is MANDATORY** — without it, the graph can't resume. Use `PostgresSaver` or `RedisSaver`.
- **Dynamic interrupts** (inside node logic) > **static interrupts** (`interrupt_before`) — they allow conditional pausing ("only pause if amount > 5000 SAR").
- Approval pauses can last **hours or days** — the checkpointer preserves state indefinitely.
- Always include an Arabic message in the interrupt value for WhatsApp.

### Pitfalls
- Don't forget to handle the "no" / rejection path — the graph must cleanly exit.
- Test resume after server restart — the checkpointer must survive restarts.

---

## 4. Memory & Conversation State Management

**Source:** [Bulwark](https://github.com/gauravxthakur/Bulwark) (Redis-backed) + [LangGraph](https://github.com/langchain-ai/langgraph)

### When to Use
Every conversation thread.

### Pattern: Checkpointer + Trim + Summarize

```python
from langgraph.checkpoint.postgres import PostgresSaver
from langchain_core.messages import trim_messages

# Production checkpointer (Neon Postgres)
checkpointer = PostgresSaver.from_conn_string(NEON_DATABASE_URL)
graph = builder.compile(checkpointer=checkpointer)

# Memory management node
def memory_node(state: AgentState):
    messages = state["messages"]
    
    # Strategy 1: Token-budget trim (keep SystemMessage + last N)
    trimmed = trim_messages(
        messages,
        max_tokens=4000,
        strategy="last",
        token_counter=len,  # or tiktoken
        include_system=True,
        allow_partial=False,
    )
    
    # Strategy 2: Rolling summary (when messages exceed threshold)
    if len(messages) > 20:
        summary_prompt = f"Summarize this conversation so far:\n{format_messages(messages[:-5])}"
        summary = llm.invoke(summary_prompt).content
        return {
            "messages": messages[-5:],  # keep last 5
            "summary": summary
        }
    
    return {"messages": trimmed}
```

### Two-Layer Persistence (Bulwark Pattern)

| Layer | Tech | Purpose | Scope |
|-------|------|---------|-------|
| **Short-term** | `PostgresSaver` (checkpointer) | Full graph state per thread_id | Thread-scoped, auto-managed |
| **Long-term** | `RedisStore` (Upstash) | User preferences, frequent entities, org context | Cross-thread, manual |
| **UI/Audit** | Neon `messages` table | Append-only message log for dashboard, search | Permanent, external |

### Key Rules
- **Always use `add_messages` reducer** on the messages field — it prevents duplication when resuming.
- **Don't use the checkpointer as a database** — it's for the LLM's working context, not for the UI.
- Thread IDs map to WhatsApp JIDs: `thread_id = f"wa-{normalized_phone}"`.
- Summarize aggressively for voice-first users — they have shorter sessions but reference earlier context.

### Pitfalls
- Token counting is model-specific — use tiktoken for OpenAI, approximate for others.
- Don't trim the SystemMessage — it contains the agent's persona and instructions.
- Redis TTL on hot cache must be longer than the longest expected conversation gap.

---

## 5. Text-to-SQL for Ad-Hoc Analytics

**Source:** [ChatbotForERP](https://github.com/M-asquerade/ChatbotForERP)

### When to Use
When users ask analytical questions that don't map to a pre-defined tool: "how much did Najm cost this quarter?", "show me overdue invoices", "what's the occupancy rate?"

### Pattern: Schema-as-Context + Fuzzy Matching + Validation

```python
@tool
def query_erp_analytics(question: str) -> str:
    """Answer ad-hoc analytical questions about ERP data.
    Uses the ERP schema to generate and validate a read-only query.
    
    Args:
        question: The user's analytical question in natural language.
    """
    # Step 1: Serialize relevant schema
    schema_context = serialize_firestore_schema(
        collections=["horses", "invoices", "journal_entries", "inventory_transactions"],
        include_field_types=True,
        include_sample_values=True  # "picklist" items for fuzzy matching
    )
    
    # Step 2: Fuzzy match entity values mentioned in the question
    matched_values = fuzzy_match_entities(question, schema_context)
    
    # Step 3: Generate query with schema-guided constraints
    query = llm.invoke(f"""
    Given this schema: {schema_context}
    And these matched values: {matched_values}
    Generate a read-only query for: {question}
    ONLY reference collections and fields that exist in the schema.
    """)
    
    # Step 4: Validate before execution
    if not validate_query(query, schema_context):
        return "I couldn't generate a valid query for that question."
    
    # Step 5: Execute (read-only, org-scoped)
    return execute_read_query(query, org_id=ctx.org_id)
```

### Key Techniques from ChatbotForERP

| Technique | What It Does | Why It Matters |
|-----------|-------------|---------------|
| **Schema serialization** | Feeds full DB structure alongside the query | LLM knows what's available |
| **Fuzzy value matching** | Links "Najm" → `horses.name: "نجم"` | Prevents hallucinated values |
| **Schema-guided decoding** | Constrains output to valid fields/collections | Prevents invalid queries |
| **Beam search + validation** | Generates N candidates, validates each | Falls back to next-best if top fails |
| **Read-only enforcement** | Only SELECT/read operations | Safety for analytics tools |

### Pitfalls
- Always org-scope queries — never let the analytics tool cross tenant boundaries.
- Cache schema serialization — don't re-serialize on every query.
- Start with pre-defined analytics tools for common questions; add Text-to-SQL gradually.

---

## 6. Entity Resolution & Fuzzy Matching

**Source:** [ChatbotForERP](https://github.com/M-asquerade/ChatbotForERP)

### When to Use
Every time the user mentions an entity by name (horse, stall, client, vendor) — before passing to the LLM for tool-calling.

### Pattern: Fuzzy Match → Resolve → Confirm if Ambiguous

```python
from rapidfuzz import fuzz, process

def resolve_entity(user_mention: str, entity_type: str, org_id: str) -> dict:
    """Resolve a user's mention to an actual ERP entity."""
    
    # Fetch candidate entities from ERP (cached)
    candidates = gateway_client.list_entities(entity_type, org_id)
    
    # Fuzzy match across name fields (Arabic + English)
    matches = []
    for candidate in candidates:
        # Match against both Arabic and English names
        score_ar = fuzz.partial_ratio(user_mention, candidate.get("nameAr", ""))
        score_en = fuzz.partial_ratio(user_mention, candidate.get("name", ""))
        best_score = max(score_ar, score_en)
        if best_score > 70:  # threshold
            matches.append({"entity": candidate, "score": best_score})
    
    matches.sort(key=lambda x: x["score"], reverse=True)
    
    if len(matches) == 0:
        return {"status": "not_found", "message": f"لم أجد {user_mention}"}
    elif len(matches) == 1 or matches[0]["score"] > 95:
        return {"status": "resolved", "entity": matches[0]["entity"]}
    else:
        # Ambiguous — return top candidates for clarification
        return {
            "status": "ambiguous",
            "candidates": matches[:3],
            "message": f"وجدت أكثر من {entity_type}. أي واحد تقصد?"
        }
```

### Key Rules
- **Never let the LLM invent entity IDs** — always resolve first via fuzzy search.
- Match against **both Arabic and English** name fields — users mix languages.
- Use a high threshold (>95) for auto-resolve; lower thresholds require confirmation.
- Cache entity lists with short TTL (30-60s) to avoid stale data.

---

## 7. Error Handling & Self-Correction

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph)

### When to Use
Every tool node and external API call.

### Pattern: RetryPolicy + Error Handler + ToolNode Self-Correction

```python
from langgraph.types import RetryPolicy, Command
from langgraph.errors import NodeError

# Layer 1: RetryPolicy for transient errors
builder.add_node("erp_tools", tool_node, retry_policy=RetryPolicy(
    max_attempts=3,
    initial_interval=1.0,
    backoff_factor=2.0,
    retry_on=[ConnectionError, TimeoutError, HTTPError]
))

# Layer 2: Error handler for persistent failures
def on_tool_failure(state, error: NodeError):
    return Command(
        update={
            "messages": [AIMessage(f"عذراً، حدث خطأ: {error.message}")],
            "error": str(error)
        },
        goto="respond"  # skip to response generation with error context
    )

builder.add_node("erp_tools", tool_node, error_handler=on_tool_failure)

# Layer 3: ToolNode self-correction (handle_tool_errors=True)
# When a tool returns an error, it's passed back to the LLM as a ToolMessage.
# The LLM can then:
#   - Fix its arguments and retry
#   - Ask the user for clarification
#   - Give up gracefully with an explanation
```

### Error Handling Hierarchy

```
Tool call fails
  └─→ ToolNode handle_tool_errors → LLM gets error as ToolMessage → self-corrects
       └─→ RetryPolicy → retries transient errors (connection, timeout)
            └─→ Error handler → graceful fallback node (Arabic error message)
                 └─→ Dead letter → audit log for investigation
```

### Pitfalls
- Always set `recursion_limit` on the compiled graph — without it, a self-correction loop can run forever.
- Don't retry on validation errors (Zod failures, permission denied) — those won't succeed on retry.
- Always include Arabic-language error messages for WhatsApp users.

---

## 8. ERP Gateway & Middleware Layer

**Source:** [Bulwark](https://github.com/gauravxthakur/Bulwark) (ERP-agnostic middleware)

### When to Use
All writes and most reads to the ERP.

### Pattern: Agentic Middleware Layer (Bulwark Architecture)

```
┌─────────────────────────────────┐
│  Agent Layer (LangGraph)        │  ← ERP-agnostic
│  Supervisor + Subgraphs + Tools │
└──────────────┬──────────────────┘
               │ @tool calls Gateway client
┌──────────────▼──────────────────┐
│  Gateway Client (platform-side) │  ← HTTP client with auth, retries
│  Wraps signed JWT + acting user │
└──────────────┬──────────────────┘
               │ HTTPS + signed requests
┌──────────────▼──────────────────┐
│  ERP Agent Gateway (ERP-side)   │  ← ERP-specific
│  AuthN → AuthZ → Zod → Execute │
│  → Audit → Response            │
└──────────────┬──────────────────┘
               │ lib/api/* (existing ERP logic)
┌──────────────▼──────────────────┐
│  Firestore / Database           │
└─────────────────────────────────┘
```

### Why This Matters (Bulwark Lesson)
Bulwark proves that the agent layer should be **completely decoupled from the ERP backend**. The agent talks to the Gateway, not to Firestore. This means:
- Switching ERPs = switching the Gateway, not the agent
- The agent never holds admin credentials
- Every operation is validated, audited, and permission-checked at the Gateway
- The tool definitions are the same regardless of the underlying ERP

### Key Rules
- **One Gateway tool = one ERP operation** — no composite "super-tools" that do multiple things.
- **Idempotency keys on every write** — WhatsApp message ID or generated UUID.
- **Audit every call** — request, actor, args, policy decision, result, latency.

---

## 9. Multi-Tenancy & Session-Scoped Context

**Source:** [ERP-AI](https://github.com/repo-anuj/ERP-AI)

### When to Use
Every request — org isolation is non-negotiable.

### Pattern: Session-Scoped Tenant Resolution

```python
class ActorIdentity:
    """Resolved from WhatsApp phone → ERP identity."""
    uid: str
    phone: str
    role: str  # super_admin|admin|manager|viewer|client
    org_id: str
    org_ids: dict[str, str]  # all orgs the user belongs to
    scopes: list[str]  # PermissionScope[]
    verified: bool

def resolve_actor(whatsapp_jid: str) -> ActorIdentity:
    """Identity resolution pipeline (ERP-AI pattern)."""
    phone = normalize_phone(whatsapp_jid)
    
    # 1. Check cache
    cached = redis.get(f"identity:{phone}")
    if cached:
        return ActorIdentity(**json.loads(cached))
    
    # 2. Query ERP
    user = gateway_client.resolve_user(phone)
    if not user:
        raise UnknownUserError(f"No ERP user for phone {phone}")
    
    # 3. Verify
    if not user.phone_verified_at:
        raise UnverifiedUserError("Phone not verified")
    
    # 4. Build identity
    identity = ActorIdentity(
        uid=user.uid,
        phone=phone,
        role=user.role,
        org_id=user.primary_org_id,
        org_ids=user.org_ids,
        scopes=get_scopes_for_role(user.role),
        verified=True,
    )
    
    # 5. Cache with TTL
    redis.setex(f"identity:{phone}", 300, identity.json())
    return identity
```

### Key Rules
- **Every Gateway call must include `org_id`** — the Gateway re-validates it.
- **Multi-org users**: if `len(org_ids) > 1`, ask which org on first message.
- **Role-based tool filtering**: only expose tools the user's role permits.
- **Cache identity with TTL** — don't hit the ERP on every message.

---

## 10. Streaming & Real-Time UX

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph) + [ERP-AI](https://github.com/repo-anuj/ERP-AI)

### When to Use
Dashboard chat interface (not WhatsApp — WhatsApp doesn't support streaming).

### Pattern: astream_events → SSE

```python
from fastapi import FastAPI
from fastapi.responses import StreamingResponse

app = FastAPI()

@app.post("/api/chat/stream")
async def stream_chat(request: ChatRequest):
    async def event_generator():
        async for event in graph.astream_events(
            {"messages": [HumanMessage(request.message)]},
            config={"configurable": {"thread_id": request.thread_id}},
            version="v2"
        ):
            if event["event"] == "on_chat_model_stream":
                content = event["data"]["chunk"].content
                if content:
                    yield f"data: {json.dumps({'token': content})}\n\n"
            
            # Track which node is active
            if event["event"] == "on_chain_start":
                node = event.get("metadata", {}).get("langgraph_node", "")
                if node:
                    yield f"data: {json.dumps({'node': node, 'status': 'started'})}\n\n"
        
        yield f"data: {json.dumps({'done': True})}\n\n"
    
    return StreamingResponse(event_generator(), media_type="text/event-stream")
```

### Key Rules
- Use `version="v2"` or `"v3"` for the typed-projection event API.
- Filter by `on_chat_model_stream` for LLM tokens; `on_chain_start/end` for node transitions.
- For WhatsApp: no streaming — send a "جاري التنفيذ…" ack, then the final reply.
- For dashboard: SSE provides real-time token display + node-status indicators.

---

## 11. Observability & Debugging Agents

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph)

### When to Use
From day one — agentic systems are opaque without tracing.

### Pattern: LangSmith Integration

```python
import os
os.environ["LANGSMITH_API_KEY"] = "..."
os.environ["LANGSMITH_PROJECT"] = "sawt-production"
os.environ["LANGSMITH_TRACING"] = "true"

# LangGraph automatically sends traces to LangSmith
# Each node transition, tool call, LLM invocation is captured

# Custom metadata for filtering
config = {
    "configurable": {"thread_id": thread_id},
    "metadata": {
        "whatsapp_jid": jid,
        "org_id": actor.org_id,
        "trace_id": whatsapp_msg_id,
    }
}
```

### What LangSmith Gives You

| Feature | Use Case |
|---------|----------|
| **Node-level traces** | See exactly which node ran, how long, what state it read/wrote |
| **Tool call inspection** | Inspect tool args, return values, errors |
| **LLM token usage** | Track cost per conversation, per agent |
| **Latency breakdown** | Identify bottlenecks (STT? LLM? Gateway?) |
| **LLM eval datasets** | Capture labeled traces → build eval suite |
| **Replay & debug** | Time-travel to any checkpoint, re-run from there |

### Pitfalls
- LangSmith is a paid service — budget for it in production.
- Redact PII from traces (voice transcripts, phone numbers) before sending.
- Set up alerts for recursion limit hits and tool error spikes.

---

## 12. Production Deployment Patterns

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph)

### Deployment Checklist

| Aspect | Dev/Prototype | Production |
|--------|--------------|------------|
| **Checkpointer** | `MemorySaver` | `PostgresSaver` (Neon) |
| **Container base** | Any | **Debian-slim** (NOT Alpine) |
| **Observability** | Console logs | LangSmith + Sentry |
| **Execution** | Synchronous | Async + background workers |
| **Safety** | None | Recursion limits, timeouts, Zod validation |
| **State isolation** | Single thread | Thread-per-user, org-scoped |

### Critical: Avoid Alpine Linux

```dockerfile
# ❌ BAD — musl libc breaks PyTorch, transformers, onnxruntime
FROM python:3.12-alpine

# ✅ GOOD — glibc compatible with all AI/ML libraries
FROM python:3.12-slim-bookworm
```

### Key Architecture Decisions

```
┌─ API Layer (Next.js / FastAPI) ─┐
│  Receives WhatsApp webhook       │
│  Validates request                │
│  Enqueues to QStash               │
└──────────────┬───────────────────┘
               │
┌──────────────▼───────────────────┐
│  Queue Worker (separate process)  │
│  Dequeues → invokes LangGraph     │
│  Uses shared PostgresSaver        │
│  Horizontal scaling               │
└──────────────────────────────────┘
```

---

## 13. AI-First ERP Design Philosophy

**Source:** [ERP-AI](https://github.com/repo-anuj/ERP-AI)

### When to Use
When designing new features or enhancing the mshalia dashboard.

### Pattern: AI Embedded in Workflows, Not Bolted On

ERP-AI teaches that AI should be a **first-class citizen** in the ERP, not a separate chatbot overlay:

| Traditional ERP | AI-First ERP |
|----------------|-------------|
| Static KPI dashboard | AI-powered trend analysis + anomaly detection |
| Manual reorder points | Predictive demand forecasting triggers reorders |
| Form-based data entry | Conversational + voice input |
| Periodic reports | Real-time AI-generated insights |
| Rule-based alerts | ML-driven anomaly detection across modules |

### Practical Applications for Sawt/mshalia

1. **Inventory**: AI predicts feed consumption patterns → auto-generates restock tasks when `stockLevel` approaches predicted demand, not just `reorderThreshold`.
2. **Finance**: Anomaly detection on `journal_entries` → flags unusual GL patterns (duplicate postings, unusual amounts, after-hours entries).
3. **Operations**: Predictive scheduling — based on historical `vet_appointments` and `incidents`, suggest preventive care schedules.
4. **Dashboard**: Embed AI trend widgets alongside existing KPIs — "cost trend for barn A this month" rendered as a chart with LLM-generated narrative.

### Key Rules
- AI features degrade gracefully — if the LLM is down, the ERP still works.
- Use **hybrid ML**: cloud LLMs for reasoning, edge/server models (TensorFlow.js) for predictions.
- Start with the conversational interface (Sawt); add dashboard AI widgets later.

---

## 14. Plan-and-Execute for Complex Workflows

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph)

### When to Use
Multi-step requests that require more than one tool call: "register a new horse, assign it to stall B-3, and schedule a vet check-up."

### Pattern: Planner → Executor → Replanner

```python
from pydantic import BaseModel, Field

class Step(BaseModel):
    tool: str = Field(description="Tool to call")
    args: dict = Field(description="Arguments for the tool")
    reason: str = Field(description="Why this step is needed")

class Plan(BaseModel):
    steps: list[Step] = Field(description="Ordered steps to execute")
    
# Planner node (expensive model)
def planner_node(state: AgentState):
    planner_llm = ChatOpenAI(model="gpt-4o").with_structured_output(Plan)
    plan = planner_llm.invoke(f"Create a plan for: {state['messages'][-1].content}")
    return {"plan": plan}

# Executor node (cheaper model, uses tools)
def executor_node(state: AgentState):
    current_step = state["plan"].steps[state["step_index"]]
    # Execute via ToolNode...
    return {"step_index": state["step_index"] + 1}

# Replanner (checks if plan needs updating based on results)
def should_replan(state: AgentState):
    if state["step_index"] >= len(state["plan"].steps):
        return "done"
    if state.get("error"):
        return "replan"
    return "execute_next"
```

### Key Rules
- **Planner uses expensive model** (GPT-4o), **executor uses cheaper model** (GPT-4o-mini) — cost optimization.
- For simple single-tool requests, **skip the planner** via conditional edge.
- Always validate the plan against the user's role/scope before executing.
- Each step should be independently rollback-able (compensating actions).

---

## 15. Structured Output with Pydantic

**Source:** [LangGraph](https://github.com/langchain-ai/langgraph) + [ERP-AI](https://github.com/repo-anuj/ERP-AI)

### When to Use
When you need the LLM to return structured data (not free text) — entity extraction, plan generation, response formatting.

### Pattern: with_structured_output()

```python
from pydantic import BaseModel, Field

class ERPResponse(BaseModel):
    """Structured response for the user."""
    action_taken: str = Field(description="What was done")
    entity_type: str = Field(description="Type of entity affected")
    entity_id: str = Field(description="ID of the affected entity")
    summary_ar: str = Field(description="Arabic summary for the user")
    summary_en: str = Field(description="English summary for the user")
    follow_up_suggestions: list[str] = Field(description="Suggested next actions")

# Use on a SEPARATE node from tool-calling
response_llm = ChatOpenAI(model="gpt-4o").with_structured_output(ERPResponse)

def response_node(state: AgentState):
    response = response_llm.invoke(
        f"Format the result of this action: {state['tool_results']}"
    )
    return {"final_response": response}
```

### Critical Rule
**NEVER use `.bind_tools()` and `.with_structured_output()` on the same LLM instance or node.** They compete for the same API capability (function calling). Use separate nodes:
- `reasoning_node` → uses `.bind_tools()` for tool selection
- `response_node` → uses `.with_structured_output()` for response formatting

---

## Quick Reference: Which Pattern for Which Scenario

| Scenario | Primary Pattern | Section |
|----------|----------------|---------|
| Building the agent graph | Supervisor + Subgraph | §1 |
| Exposing an ERP operation | @tool + ToolNode | §2 |
| Financial transaction | interrupt() + HITL | §3 |
| Conversation continuity | Checkpointer + Trim + Summarize | §4 |
| "How much did X cost?" | Text-to-SQL | §5 |
| User says "حصان نجم" | Fuzzy Entity Resolution | §6 |
| Tool call fails | RetryPolicy + Self-Correction | §7 |
| Any ERP write | Gateway Middleware | §8 |
| Multi-org user | Session-Scoped Context | §9 |
| Dashboard chat | SSE Streaming | §10 |
| "Why did it do that?" | LangSmith Traces | §11 |
| Going to production | Debian-slim + PostgresSaver | §12 |
| New dashboard feature | AI-First Design | §13 |
| "Register horse + assign stall + schedule vet" | Plan-and-Execute | §14 |
| Structured API response | Pydantic + with_structured_output | §15 |
