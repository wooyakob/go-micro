package harness

import (
	"context"
	"testing"

	"go-micro.dev/v6/agent"
	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase/catalog"
	"go-micro.dev/v6/couchbase/couchbasetest"
	"go-micro.dev/v6/store"
)

func TestNewRequiresClusterOrConnect(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("expected an error when neither WithCluster nor WithConnect is given")
	}
}

func TestNewWithClusterProvisionsSchema(t *testing.T) {
	cluster := couchbasetest.New("agents")
	h, err := New(WithCluster(cluster))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if h.Catalog == nil || h.Tracer == nil || h.EvalStore == nil {
		t.Fatal("expected Catalog, Tracer, and EvalStore to be initialized")
	}
}

func TestSyncAndNewAgentWiresCatalogTools(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	h, err := New(WithCluster(cluster))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	called := false
	err = h.Catalog.RegisterTool(catalog.ToolSpec{
		Name: "ping", Description: "responds pong",
		Handler: func(_ context.Context, call ai.ToolCall) ai.ToolResult {
			called = true
			return ai.ToolResult{ID: call.ID, Content: "pong"}
		},
	})
	if err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}
	if err := h.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	a, err := h.NewAgent(ctx, "test-agent", agent.Address("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if a.Name() != "test-agent" {
		t.Errorf("Name() = %q", a.Name())
	}

	// Exercise the catalog tool dispatch path directly the way the agent's
	// internal tool wrapper would, without needing a live model provider.
	tools, handler, err := h.Catalog.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("expected 1 tool named ping, got %+v", tools)
	}
	result := handler(ctx, ai.ToolCall{Name: "ping"})
	if result.Content != "pong" || !called {
		t.Errorf("expected the ping handler to run and return pong, got %+v", result)
	}
}

func TestMemoryUsesHarnessEmbedder(t *testing.T) {
	cluster := couchbasetest.New("agents")
	h, err := New(WithCluster(cluster), WithEmbedder(fakeEmbedder{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m, err := h.Memory("session-1")
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	m.Add("user", "remember rockets")
	if got := m.Recall("rockets", 1); len(got) == 0 {
		t.Error("expected Recall to find the archived message via the harness embedder")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	cluster := couchbasetest.New("agents")
	h, err := New(WithCluster(cluster))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s := h.Store()
	if err := s.Write(&store.Record{Key: "k1", Value: []byte("v1")}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	recs, err := s.Read("k1")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 1 || string(recs[0].Value) != "v1" {
		t.Fatalf("unexpected records: %+v", recs)
	}
}

type fakeEmbedder struct{}

func (fakeEmbedder) Dimensions() int { return 4 }

func (fakeEmbedder) Embed(_ context.Context, texts ...string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, 4)
		for _, r := range t {
			v[int(r)%4]++
		}
		out[i] = v
	}
	return out, nil
}
