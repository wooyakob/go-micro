# Couchbase Agent Harness

An agent whose entire harness — tools, prompts, traces, memory, and the LLM
itself — is backed by [Couchbase](https://www.couchbase.com), via the
`couchbase/*` packages under the repo root:

| Package | Role |
|---------|------|
| [`couchbase`](../../couchbase/) | Cluster connection, document access, FTS vector search, schema bootstrap |
| [`couchbase/catalog`](../../couchbase/catalog/) | Tools and prompts, registered in code, content-hash versioned, embedded, and synced to Couchbase for name/annotation/semantic lookup — the Go counterpart to Couchbase's Python `agentc` (agent-catalog) package |
| [`couchbase/trace`](../../couchbase/trace/) | Exports every span go-micro's agent package already emits (`agent.run`, `agent.tool.call`, `agent.model.call`) into a Couchbase collection |
| [`couchbase/memory`](../../couchbase/memory/) | `agent.Memory` / `agent.MemoryRecall` backed by Couchbase: a bounded active conversation plus long-term, embedding-searchable recall |
| [`couchbase/eval`](../../couchbase/eval/) | Run scored evaluation suites (deterministic or LLM-as-judge) against an agent and keep a regression history in Couchbase |
| [`couchbase/model`](../../couchbase/model/) | `ai.Model` and embeddings client for Couchbase's hosted Model Service (Capella AI Services), an OpenAI-compatible endpoint |
| [`couchbase/harness`](../../couchbase/harness/) | Wires all of the above into one `agent.Agent` |

## What this example does

1. Connects to a Couchbase cluster and constructs a `harness.Harness`.
2. Registers a `get_weather` tool and a `concierge` prompt on the harness's
   `Catalog`.
3. Calls `Sync` — the Catalog provisions its Couchbase schema (scopes,
   collections, GSI primary indexes, an FTS vector index) and publishes both
   the tool and the prompt, computing embeddings for semantic search.
4. Builds an `agent.Agent` from the harness — its tool comes from the
   catalog, its memory and state persist in Couchbase, its spans export to
   Couchbase, and its model calls go to the Couchbase Model Service.
5. Asks the agent about the weather and prints the reply.

## Run

You need a reachable Couchbase cluster (Capella or self-managed, Server
7.6+ with the Search service enabled for vector search) and a Model Service
endpoint:

```bash
export COUCHBASE_CONNSTR=couchbases://cb.xxxxx.cloud.couchbase.com
export COUCHBASE_USERNAME=agent-harness
export COUCHBASE_PASSWORD=...
export COUCHBASE_BUCKET=agents
export MODEL_SERVICE_URL=https://xxxxx.model-service.cloud.couchbase.com/v1
export MODEL_SERVICE_KEY=...   # API key, or your Capella JWT
go run .
```

Without these set, the example prints what's missing and exits — there's no
mock-model fallback here since the whole point is exercising the real
Couchbase-backed storage layer.

## Where things end up in Couchbase

Inside your bucket (scope names are the package defaults; every one is
overridable):

```
agent_catalog.tools / .prompts     ← catalog.Catalog (versioned tool & prompt records)
agent_activity.spans               ← trace.Exporter  (every OpenTelemetry span)
agent_memory.sessions / .items     ← memory.Memory   (active buffer / long-term recall)
agent_evals.reports                ← eval.Store      (scored run history)
agent_state.state                  ← harness.Store   (agent.WithStore / checkpoints)
```

Query any of them directly with SQL++ once you want dashboards or ad-hoc
debugging beyond what `couchbase/trace.Store` and `couchbase/eval.Store`
already provide.

## Extending this

- Add more `catalog.ToolSpec`/`catalog.PromptSpec` registrations, call
  `Sync` again — unchanged tools/prompts are not re-embedded.
- Point `harness.WithEmbedder` at any `couchbase/model.Embedder`-shaped
  implementation to swap embedding providers (just re-run `Sync`: the FTS
  vector index is keyed to the current embedding dimension).
- Wrap `h.EvalRunner()` around a suite of `eval.Case`s to track regressions
  across model or prompt changes — see `couchbase/eval`.
