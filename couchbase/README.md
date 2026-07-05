# Couchbase Agent Harness

A Couchbase-backed harness for building go-micro agents: tools, prompts,
traces, memory, and evals, all constructed in Go and stored in one
Couchbase bucket, with the LLM and its embeddings served by Couchbase's
hosted Model Service (Capella AI Services).

This is the Go counterpart to Couchbase's Python
[`agentc`](https://github.com/couchbaselabs/agent-catalog) (agent-catalog)
package — versioned tool/prompt storage plus run auditing in Couchbase —
extended to also cover memory, evals, and the model layer, and wired
directly into go-micro's existing `agent.Agent` instead of a separate
framework. See "Differences from agentc" below for exactly what's the same
and what's deliberately simplified for a first Go implementation.

## Packages

| Package | Role |
|---------|------|
| [`couchbase`](.) | Cluster connection (gocb), document access, FTS vector search, idempotent schema bootstrap. Every other package depends only on this package's `Cluster`/`Collection` interfaces, never on gocb types directly. |
| [`couchbase/catalog`](./catalog/) | Register tools and prompts in code (`RegisterTool`/`RegisterPrompt`), version them by content hash (plus git commit when available), `Sync` them to Couchbase, and find them again by name, annotation, or semantic (vector) search. |
| [`couchbase/trace`](./trace/) | A Couchbase-backed OpenTelemetry `SpanExporter` — wire it into `agent.TraceProvider` and every span go-micro's agent package already emits (`agent.run`, `agent.tool.call`, `agent.model.call`, ...) lands in Couchbase, queryable by trace ID. |
| [`couchbase/memory`](./memory/) | `agent.Memory` / `agent.MemoryRecall` backed by Couchbase: a bounded active conversation buffer plus a long-term, embedding-searchable archive, isolated per owner even in a shared collection. |
| [`couchbase/eval`](./eval/) | Score a suite of cases against a target (typically `agent.Agent.Ask`) — deterministic checks or an LLM-as-judge via `eval.Judge` — and keep a regression history in Couchbase. |
| [`couchbase/model`](./model/) | `ai.Model` and an embeddings client for Couchbase's hosted Model Service, an OpenAI-compatible endpoint. Chat completions delegate to the existing `ai/openai` provider; only the embeddings client is new. |
| [`couchbase/harness`](./harness/) | Composes all of the above into one `harness.Harness` and a `NewAgent` that builds a fully wired `agent.Agent`. |
| [`couchbase/couchbasetest`](./couchbasetest/) | An in-memory `couchbase.Cluster` fake (brute-force vector search included) for unit testing — no live cluster needed. Every package above is tested against it. |

## Quick start

```go
import (
    "go-micro.dev/v6/agent"
    "go-micro.dev/v6/couchbase"
    "go-micro.dev/v6/couchbase/catalog"
    "go-micro.dev/v6/couchbase/harness"
    "go-micro.dev/v6/couchbase/model"
)

embedder := model.NewEmbedder(
    model.WithBaseURL(modelServiceURL),
    model.WithAPIKey(capellaJWT),
    model.WithEmbeddingModel("embed-v1"),
)

h, err := harness.New(
    harness.WithConnect(
        couchbase.ConnectionString("couchbases://cb.xxxxx.cloud.couchbase.com"),
        couchbase.Credentials("agent-harness", password),
        couchbase.Bucket("agents"),
        couchbase.WanDevelopmentProfile(),
    ),
    harness.WithEmbedder(embedder),
)

h.Catalog.RegisterTool(catalog.ToolSpec{
    Name: "get_weather", Description: "Look up the current weather for a city",
    Handler: handleWeather,
})
h.Sync(ctx) // provisions Couchbase schema, embeds, publishes

a, err := h.NewAgent(ctx, "concierge",
    agent.Provider("couchbase"), agent.BaseURL(modelServiceURL), agent.APIKey(capellaJWT),
    agent.Prompt("You are a helpful concierge."),
)
resp, err := a.Ask(ctx, "what's the weather in Austin?")
```

A complete, runnable version of this is in
[`examples/couchbase-agent-harness`](../examples/couchbase-agent-harness/).

## Design

- **Interface-first, gocb confined to one file.** `couchbase.Cluster` and
  `couchbase.Collection` are the only types every other package depends on;
  the Couchbase Go SDK (`gocb`) only appears in `couchbase/couchbase.go`.
  That's what makes `couchbase/couchbasetest`'s in-memory fake possible, and
  it's why every package's tests run without a live cluster.
- **Reuse over reinvention.** `couchbase/memory` implements the existing
  `agent.Memory`/`agent.MemoryRecall` interfaces rather than a new memory
  API; `couchbase/trace` implements the standard OpenTelemetry
  `sdktrace.SpanExporter` rather than a parallel span/log system, so it
  captures spans go-micro's `agent/otel.go` already emits with zero changes
  to the agent package; `couchbase/model`'s chat completions delegate to
  `ai/openai`, since Couchbase's Model Service is wire-compatible with it.
- **Everything is a `store.Store`, too.** `couchbase/harness.NewStore`
  adapts a `Cluster` into a go-micro `store.Store`, so `agent.WithStore` and
  `flow.StoreCheckpoint` also persist through Couchbase — an agent's plan
  steps, checkpointed runs, and default memory can all live in the same
  bucket as its catalog and traces.

## Differences from agentc

Go has no runtime introspection over doc comments or decorators, and this
is a first pass at a large feature area, so a few things are deliberately
simpler than Couchbase's Python `agentc`:

- **Registration is explicit**, not derived from decorated source files at
  index time: call `RegisterTool`/`RegisterPrompt` in code, then `Sync`.
- **Versioning is content-hash based** (SHA-256 over a tool/prompt's
  fields), with git commit/dirty metadata attached best-effort when the
  process happens to be running inside a git checkout — there is no
  git-commit-driven, offline `index` step separate from `publish`; `Sync`
  does both at once. Only the *current* version of each tool/prompt is kept
  queryable in Couchbase (full version history is visible in git and in
  each `Sync`'s trace spans), not agentc's arbitrary-historical-snapshot
  catalog.
- **No SQL++/HTTP/semantic-search tool kinds.** Tools are Go functions
  (`ai.ToolHandler`); there's no equivalent to agentc's `.sqlpp`/OpenAPI/
  vector-search-as-a-tool YAML descriptors.
- **No Analytics service views.** agentc provisions Analytics
  scopes/views/UDFs (`Sessions`, `Exchanges`, `ToolInvocations`) for
  ad-hoc SQL++ querying over trace history. `couchbase/trace.Store`
  provides the same lookups (by trace ID, most recent) directly against the
  raw span collection instead; for large-scale analytics, query the
  collection with SQL++ yourself.
- **One embedding dimension per vector index at a time.** agentc suffixes
  its vector field name with the dimension (`embedding_384`) so multiple
  embedding-model generations can coexist in one FTS index. This package
  uses a single `embedding` field; switching embedding models means
  recreating the vector index.

## Requirements

- Couchbase Server 7.6+ or Capella, with the Search service enabled for
  vector search (`catalog`, `memory`'s recall, and `harness` all need it;
  everything else works without it).
- A Couchbase Model Service (Capella AI Services) endpoint, or any other
  OpenAI-chat-completions/embeddings-API-compatible endpoint, for
  `couchbase/model`.
