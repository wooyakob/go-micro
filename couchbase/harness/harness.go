// Package harness composes couchbase, couchbase/catalog, couchbase/trace,
// couchbase/memory, and couchbase/eval into a single entry point for
// building go-micro agents that run entirely on Couchbase: tools and
// prompts versioned in the Catalog, conversation state and long-term
// recall in Memory, every run's spans in the trace Store, and evaluation
// history in the eval Store — one Cluster, one bucket.
//
// It is the Go counterpart to what a Python agent gets by combining
// agentc's Catalog with its own LLM/framework glue: this package plays
// the connecting-tissue role agentc's own top-level `agentc` package plays
// (re-exporting Catalog, Span, tool decorators), but wired directly into
// go-micro's existing agent.Agent instead of a separate framework.
//
//	h, _ := harness.New(harness.WithConnect(
//	    couchbase.ConnectionString(dsn), couchbase.Credentials(user, pass), couchbase.Bucket("agents"),
//	), harness.WithEmbedder(model.NewEmbedder(
//	    model.WithBaseURL(modelServiceURL), model.WithAPIKey(jwt), model.WithEmbeddingModel("embed-v1"),
//	)))
//	h.Catalog.RegisterTool(catalog.ToolSpec{Name: "get_weather", ...})
//	h.Sync(ctx)
//	a, _ := h.NewAgent(ctx, "concierge",
//	    agent.Provider("couchbase"), agent.APIKey(jwt), agent.BaseURL(modelServiceURL), agent.Model("chat-v1"),
//	    agent.Prompt("You are a helpful concierge."),
//	)
//	a.Ask(ctx, "what's the weather in Austin?")
package harness

import (
	"context"
	"errors"
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go-micro.dev/v6/agent"
	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase"
	"go-micro.dev/v6/couchbase/catalog"
	"go-micro.dev/v6/couchbase/eval"
	"go-micro.dev/v6/couchbase/memory"
	"go-micro.dev/v6/couchbase/trace"
	"go-micro.dev/v6/store"
)

// Embedder produces vector embeddings for text. Satisfied by
// couchbase/model.Embedder; used to power the Catalog's semantic search and
// Memory's long-term recall.
type Embedder interface {
	Embed(ctx context.Context, texts ...string) ([][]float32, error)
	Dimensions() int
}

// Harness is the storage substrate for one or more agents: a Couchbase
// Cluster plus a Catalog, a TracerProvider, and an eval Store built on top
// of it.
type Harness struct {
	Cluster   couchbase.Cluster
	Catalog   *catalog.Catalog
	Tracer    *sdktrace.TracerProvider
	EvalStore *eval.Store

	embedder Embedder
}

type options struct {
	cluster     couchbase.Cluster
	connectOpts []couchbase.Option
	embedder    Embedder
	catalogOpts []catalog.Option
	traceOpts   []trace.Option
}

// Option configures a Harness.
type Option func(*options)

// WithCluster reuses an already-connected Cluster instead of dialing a new
// one, e.g. to share a connection across several harnesses or inject a test
// fake (couchbase/couchbasetest.Cluster).
func WithCluster(c couchbase.Cluster) Option {
	return func(o *options) { o.cluster = c }
}

// WithConnect dials a new Cluster with the given options. Ignored if
// WithCluster is also passed.
func WithConnect(opts ...couchbase.Option) Option {
	return func(o *options) { o.connectOpts = opts }
}

// WithEmbedder enables semantic search in the Catalog and long-term Recall
// in every Memory this Harness creates.
func WithEmbedder(e Embedder) Option {
	return func(o *options) { o.embedder = e }
}

// WithCatalogOptions passes options through to catalog.New.
func WithCatalogOptions(opts ...catalog.Option) Option {
	return func(o *options) { o.catalogOpts = opts }
}

// WithTraceOptions passes options through to trace.NewExporter.
func WithTraceOptions(opts ...trace.Option) Option {
	return func(o *options) { o.traceOpts = opts }
}

// New connects to Couchbase (unless WithCluster supplies an existing
// connection) and provisions the catalog, trace, and eval schemas.
func New(opts ...Option) (*Harness, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	cluster := o.cluster
	if cluster == nil {
		if len(o.connectOpts) == 0 {
			return nil, errors.New("harness: WithCluster or WithConnect is required")
		}
		c, err := couchbase.Connect(o.connectOpts...)
		if err != nil {
			return nil, fmt.Errorf("harness: connect: %w", err)
		}
		cluster = c
	}

	var catalogEmbedder catalog.Embedder
	if o.embedder != nil {
		catalogEmbedder = o.embedder
	}
	cat := catalog.New(cluster, catalogEmbedder, o.catalogOpts...)

	exporter, err := trace.NewExporter(cluster, o.traceOpts...)
	if err != nil {
		return nil, fmt.Errorf("harness: trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))

	evalStore, err := eval.NewStore(cluster)
	if err != nil {
		return nil, fmt.Errorf("harness: eval store: %w", err)
	}

	return &Harness{Cluster: cluster, Catalog: cat, Tracer: tp, EvalStore: evalStore, embedder: o.embedder}, nil
}

// Sync publishes every tool and prompt registered on h.Catalog to
// Couchbase. Call it once at startup after registering them.
func (h *Harness) Sync(ctx context.Context) error {
	return h.Catalog.Sync(ctx)
}

// Memory returns Couchbase-backed conversation memory scoped to key
// (typically an agent name or session ID). Semantic Recall is enabled
// automatically when the Harness was built WithEmbedder.
func (h *Harness) Memory(key string, opts ...memory.Option) (*memory.Memory, error) {
	if h.embedder != nil {
		opts = append([]memory.Option{memory.WithEmbedder(h.embedder)}, opts...)
	}
	return memory.New(h.Cluster, key, opts...)
}

// Store adapts the Harness's Cluster into a go-micro store.Store, for
// agent.WithStore or flow.StoreCheckpoint.
func (h *Harness) Store(opts ...store.Option) store.Store {
	return NewStore(h.Cluster, opts...)
}

// EvalRunner returns an eval.Runner that persists every Report to the
// Harness's EvalStore.
func (h *Harness) EvalRunner(opts ...eval.Option) *eval.Runner {
	return eval.NewRunner(append([]eval.Option{eval.WithStore(h.EvalStore)}, opts...)...)
}

// NewAgent builds a go-micro agent.Agent wired to this Harness: its tools
// come from the Catalog (see catalog.Catalog.Tools — register tools and
// call Sync before calling NewAgent), its conversation memory persists
// through Memory, its state through Store, and its spans through Tracer.
// Pass agent.Provider/agent.Model/agent.APIKey/agent.BaseURL in opts to
// select the LLM — use agent.Provider("couchbase") to route through
// couchbase/model at the Couchbase Model Service. Options in opts override
// the Harness's defaults, applied last.
func (h *Harness) NewAgent(ctx context.Context, name string, opts ...agent.Option) (agent.Agent, error) {
	tools, handler, err := h.Catalog.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("harness: resolve catalog tools: %w", err)
	}

	mem, err := h.Memory(name)
	if err != nil {
		return nil, fmt.Errorf("harness: create memory for agent %q: %w", name, err)
	}

	base := []agent.Option{
		agent.Name(name),
		agent.WithMemory(mem),
		agent.WithStore(h.Store()),
		agent.TraceProvider(h.Tracer),
	}
	for _, t := range tools {
		base = append(base, agent.WithTool(t.Name, t.Description, t.Properties, dispatchTool(handler, t.Name)))
	}

	return agent.New(append(base, opts...)...), nil
}

func dispatchTool(handler ai.ToolHandler, name string) agent.ToolFunc {
	return func(ctx context.Context, input map[string]any) (string, error) {
		result := handler(ctx, ai.ToolCall{Name: name, Input: input})
		if result.Refused != "" {
			return "", fmt.Errorf("tool %q refused: %s", name, result.Refused)
		}
		return result.Content, nil
	}
}
