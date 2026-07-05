package catalog

import (
	"context"
	"math"
	"testing"

	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase/couchbasetest"
)

// fakeEmbedder produces a deterministic, low-dimensional embedding by
// hashing each word of the input into one of a small number of buckets.
// Similar text (shared words) lands close together in cosine space without
// pulling in a real model for unit tests.
type fakeEmbedder struct{ dims int }

func (f fakeEmbedder) Dimensions() int { return f.dims }

func (f fakeEmbedder) Embed(_ context.Context, texts ...string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dims)
		for _, r := range t {
			v[int(r)%f.dims]++
		}
		var norm float64
		for _, x := range v {
			norm += float64(x) * float64(x)
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for j := range v {
				v[j] = float32(float64(v[j]) / norm)
			}
		}
		out[i] = v
	}
	return out, nil
}

func testHandler(_ context.Context, call ai.ToolCall) ai.ToolResult {
	return ai.ToolResult{ID: call.ID, Content: "ok:" + call.Name}
}

func TestRegisterToolValidation(t *testing.T) {
	c := New(couchbasetest.New("agents"), nil)
	if err := c.RegisterTool(ToolSpec{}); err == nil {
		t.Error("expected error for missing name")
	}
	if err := c.RegisterTool(ToolSpec{Name: "x"}); err == nil {
		t.Error("expected error for missing description")
	}
	if err := c.RegisterTool(ToolSpec{Name: "x", Description: "d"}); err == nil {
		t.Error("expected error for missing handler")
	}
	if err := c.RegisterTool(ToolSpec{Name: "x", Description: "d", Handler: testHandler}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSyncAndFindByName(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	c := New(cluster, fakeEmbedder{dims: 32})

	if err := c.RegisterTool(ToolSpec{
		Name: "get_weather", Description: "Look up the current weather for a city",
		Parameters: map[string]any{"city": map[string]any{"type": "string"}},
		Handler:    testHandler,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterPrompt(PromptSpec{
		Name: "concierge", Description: "Concierge system prompt", Content: "You are a helpful concierge.",
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rec, err := c.FindTool(ctx, ByName("get_weather"))
	if err != nil {
		t.Fatalf("FindTool: %v", err)
	}
	if rec.Description != "Look up the current weather for a city" {
		t.Errorf("Description = %q", rec.Description)
	}
	if rec.Version.Hash == "" {
		t.Error("expected a non-empty version hash")
	}
	if len(rec.Embedding) != 32 {
		t.Errorf("Embedding length = %d, want 32", len(rec.Embedding))
	}

	prompt, err := c.FindPrompt(ctx, ByName("concierge"))
	if err != nil {
		t.Fatalf("FindPrompt: %v", err)
	}
	if prompt.Content != "You are a helpful concierge." {
		t.Errorf("Content = %q", prompt.Content)
	}
}

func TestFindToolByNameMissing(t *testing.T) {
	ctx := context.Background()
	c := New(couchbasetest.New("agents"), nil)
	records, err := c.FindTools(ctx, ByName("nope"))
	if err != nil {
		t.Fatalf("FindTools: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected no records, got %d", len(records))
	}
}

func TestSyncSkipsReembeddingUnchangedRecords(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	calls := 0
	embedder := embedFunc(func(_ context.Context, texts ...string) ([][]float32, error) {
		calls++
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1, 0}
		}
		return out, nil
	})

	c := New(cluster, embedder)
	if err := c.RegisterTool(ToolSpec{Name: "t", Description: "d", Handler: testHandler}); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(ctx); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 embed call after first sync, got %d", calls)
	}
	if err := c.Sync(ctx); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected embed call count to stay 1 for an unchanged tool, got %d", calls)
	}

	// Changing the description should trigger a re-embed.
	if err := c.RegisterTool(ToolSpec{Name: "t", Description: "different", Handler: testHandler}); err != nil {
		t.Fatal(err)
	}
	if err := c.Sync(ctx); err != nil {
		t.Fatalf("third Sync: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected embed call count 2 after changing description, got %d", calls)
	}
}

func TestFindByQuerySemanticMatch(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	c := New(cluster, fakeEmbedder{dims: 64})

	must(t, c.RegisterTool(ToolSpec{Name: "weather", Description: "get the current weather forecast", Handler: testHandler}))
	must(t, c.RegisterTool(ToolSpec{Name: "invoice", Description: "generate a customer invoice pdf", Handler: testHandler}))
	must(t, c.Sync(ctx))

	records, err := c.FindTools(ctx, ByQuery("what is the weather forecast"), Limit(1))
	if err != nil {
		t.Fatalf("FindTools: %v", err)
	}
	if len(records) != 1 || records[0].Name != "weather" {
		t.Errorf("expected best match 'weather', got %+v", records)
	}
}

func TestByQueryWithoutEmbedderErrors(t *testing.T) {
	ctx := context.Background()
	c := New(couchbasetest.New("agents"), nil)
	if _, err := c.FindTools(ctx, ByQuery("anything")); err == nil {
		t.Error("expected an error when using ByQuery without an Embedder")
	}
}

func TestByQueryWithEmptyEmbeddingErrorsInsteadOfPanicking(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	// Returns a normal vector for Sync's tool-description embed call, but an
	// empty result for the specific query text used below, isolating the
	// find() code path from Sync's own embedding.
	embedder := embedFunc(func(_ context.Context, texts ...string) ([][]float32, error) {
		if len(texts) == 1 && texts[0] == "empty query" {
			return nil, nil
		}
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1, 0}
		}
		return out, nil
	})
	c := New(cluster, embedder)
	must(t, c.RegisterTool(ToolSpec{Name: "t", Description: "d", Handler: testHandler}))
	must(t, c.Sync(ctx))

	if _, err := c.FindTools(ctx, ByQuery("empty query")); err == nil {
		t.Error("expected an error when the embedder returns no vectors for the query")
	}
}

func TestToolsDispatchesToRegisteredHandler(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	c := New(cluster, nil)
	must(t, c.RegisterTool(ToolSpec{Name: "echo", Description: "echoes input", Handler: testHandler}))
	must(t, c.Sync(ctx))

	tools, handler, err := c.Tools(ctx)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("expected one tool named echo, got %+v", tools)
	}
	result := handler(ctx, ai.ToolCall{ID: "1", Name: "echo"})
	if result.Content != "ok:echo" {
		t.Errorf("Content = %q", result.Content)
	}

	unknown := handler(ctx, ai.ToolCall{ID: "2", Name: "missing"})
	if unknown.Content == "" {
		t.Error("expected a message for an unhandled tool call")
	}
}

func TestResolveTools(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	c := New(cluster, nil)
	must(t, c.RegisterTool(ToolSpec{Name: "a", Description: "tool a", Handler: testHandler}))
	must(t, c.RegisterTool(ToolSpec{Name: "b", Description: "tool b", Handler: testHandler}))
	must(t, c.Sync(ctx))

	tools, _, err := c.ResolveTools(ctx, []ToolRef{{Name: "a"}, {Name: "b"}, {Name: "a"}})
	if err != nil {
		t.Fatalf("ResolveTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 deduplicated tools, got %d", len(tools))
	}
}

type embedFunc func(ctx context.Context, texts ...string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts ...string) ([][]float32, error) {
	return f(ctx, texts...)
}
func (f embedFunc) Dimensions() int { return 2 }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
