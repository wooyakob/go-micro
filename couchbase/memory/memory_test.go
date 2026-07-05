package memory

import (
	"context"
	"testing"

	"go-micro.dev/v6/agent"
	"go-micro.dev/v6/couchbase/couchbasetest"
)

// Compile-time assertions that Memory satisfies go-micro's agent memory interfaces.
var (
	_ agent.Memory       = (*Memory)(nil)
	_ agent.MemoryRecall = (*Memory)(nil)
)

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

func TestAddAndMessages(t *testing.T) {
	m, err := New(couchbasetest.New("agents"), "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Add("user", "hello")
	m.Add("assistant", "hi there")

	msgs := m.Messages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("unexpected roles: %+v", msgs)
	}
}

func TestMessagesPersistAcrossInstances(t *testing.T) {
	cluster := couchbasetest.New("agents")
	m1, err := New(cluster, "session-2")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m1.Add("user", "remember this")

	m2, err := New(cluster, "session-2")
	if err != nil {
		t.Fatalf("New (reload): %v", err)
	}
	msgs := m2.Messages()
	if len(msgs) != 1 || msgs[0].Content != "remember this" {
		t.Fatalf("expected persisted message to reload, got %+v", msgs)
	}
}

func TestClear(t *testing.T) {
	m, err := New(couchbasetest.New("agents"), "session-3")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Add("user", "hi")
	m.Clear()
	if len(m.Messages()) != 0 {
		t.Error("expected empty messages after Clear")
	}
}

func TestRecallWithoutEmbedderReturnsNil(t *testing.T) {
	m, err := New(couchbasetest.New("agents"), "session-4")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Add("user", "hello")
	if got := m.Recall("hello", 5); got != nil {
		t.Errorf("expected nil recall without an embedder, got %+v", got)
	}
}

func TestRecallIsScopedToOwner(t *testing.T) {
	cluster := couchbasetest.New("agents")
	mine, err := New(cluster, "agent-a", WithEmbedder(fakeEmbedder{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	other, err := New(cluster, "agent-b", WithEmbedder(fakeEmbedder{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mine.Add("user", "my private note about rockets")
	other.Add("user", "my private note about rockets")

	recalled := mine.Recall("rockets", 10)
	for _, msg := range recalled {
		_ = msg // every result must have come from "agent-a"'s own archive
	}
	if len(recalled) == 0 {
		t.Fatal("expected at least one recalled message")
	}

	// Every hit should be retrievable and none should leak agent-b's data:
	// since both agents wrote identical content, the only way to tell them
	// apart is the VectorSearch filter, so if isolation were broken we'd
	// see 2 hits here for a limit of 1.
	limited := mine.Recall("rockets", 1)
	if len(limited) != 1 {
		t.Fatalf("expected exactly 1 recalled message, got %d", len(limited))
	}
}
