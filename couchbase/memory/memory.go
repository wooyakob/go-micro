// Package memory implements go-micro's agent.Memory and agent.MemoryRecall
// interfaces on top of Couchbase, so an agent's conversation state and
// long-term recall live in the same cluster as its catalog and traces.
//
// It is two-tiered, mirroring agent.NewCompactingMemory's own active/archive
// split: a bounded "session" document holds the active conversation buffer
// (what agent.Memory.Messages returns), while every message is additionally
// appended to a shared, embedding-indexed "items" collection that
// agent.MemoryRecall.Recall searches by meaning — the Couchbase analog of
// agentc's embedding-backed retrieval, keyed so one agent's recall can never
// surface another's history.
package memory

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"

	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase"
)

// DefaultScope is the Couchbase scope Memory provisions its collections
// under.
const DefaultScope = "agent_memory"

const (
	sessionsCollection = "sessions"
	itemsCollection    = "items"
	itemsVectorIndex   = "agent_memory_items_vector"
	embeddingField     = "embedding"
	ownerField         = "owner"
)

// Embedder produces vector embeddings for text, powering Recall. If not
// supplied, Recall always returns nil — the session buffer still works.
type Embedder interface {
	Embed(ctx context.Context, texts ...string) ([][]float32, error)
	Dimensions() int
}

type options struct {
	scope    string
	limit    int
	embedder Embedder
}

// Option configures a Memory.
type Option func(*options)

// InScope overrides the Couchbase scope Memory provisions collections under.
func InScope(scope string) Option { return func(o *options) { o.scope = scope } }

// Limit caps how many messages the active session buffer retains, oldest
// dropped first (default 50). It does not bound the long-term item archive
// Recall searches.
func Limit(n int) Option { return func(o *options) { o.limit = n } }

// WithEmbedder enables Recall by giving Memory a way to embed both stored
// messages and recall queries.
func WithEmbedder(e Embedder) Option { return func(o *options) { o.embedder = e } }

// Memory is a Couchbase-backed agent.Memory and agent.MemoryRecall
// implementation, scoped to one conversation via key (typically an agent
// name or session ID).
type Memory struct {
	cluster  couchbase.Cluster
	key      string
	scope    string
	limit    int
	embedder Embedder

	mu   sync.Mutex
	hist *ai.History
}

// New creates Memory scoped to key and provisions its Couchbase schema.
func New(cluster couchbase.Cluster, key string, opts ...Option) (*Memory, error) {
	o := options{scope: DefaultScope, limit: 50}
	for _, opt := range opts {
		opt(&o)
	}

	m := &Memory{cluster: cluster, key: key, scope: o.scope, limit: o.limit, embedder: o.embedder, hist: ai.NewHistory(o.limit)}
	if err := m.ensureSchema(); err != nil {
		return nil, err
	}
	m.load(context.Background())
	return m, nil
}

func (m *Memory) ensureSchema() error {
	if err := m.cluster.EnsureScope(m.scope); err != nil {
		return err
	}
	if err := m.cluster.EnsureCollection(m.scope, sessionsCollection); err != nil {
		return err
	}
	if err := m.cluster.EnsurePrimaryIndex(m.scope, sessionsCollection); err != nil {
		return err
	}
	if err := m.cluster.EnsureCollection(m.scope, itemsCollection); err != nil {
		return err
	}
	if err := m.cluster.EnsurePrimaryIndex(m.scope, itemsCollection); err != nil {
		return err
	}
	if m.embedder != nil {
		if err := m.cluster.EnsureVectorIndex(m.scope, itemsCollection, itemsVectorIndex, embeddingField, m.embedder.Dimensions()); err != nil {
			return err
		}
	}
	return nil
}

// Add appends a message to the active conversation and, when an Embedder is
// configured, archives it for later semantic Recall.
//
// agent.Memory has no context parameter (it predates context propagation,
// matching store.Store's own Write signature), so persistence here uses
// context.Background().
func (m *Memory) Add(role, content string) {
	ctx := context.Background()

	m.mu.Lock()
	m.hist.Add(role, content)
	msgs := append([]ai.Message(nil), m.hist.Messages()...)
	m.mu.Unlock()

	m.saveSession(ctx, msgs)
	m.archive(ctx, role, content)
}

// Messages returns the active, bounded conversation buffer, oldest first.
func (m *Memory) Messages() []ai.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hist.Messages()
}

// Clear resets the active conversation buffer. The long-term archive Recall
// searches is left intact.
func (m *Memory) Clear() {
	m.mu.Lock()
	m.hist.Reset()
	m.mu.Unlock()
	_ = m.cluster.Collection(m.scope, sessionsCollection).Remove(context.Background(), m.key)
}

// Recall returns up to limit archived messages semantically similar to
// query, scoped to this Memory's key so one agent never sees another's
// history even though items share a collection. It returns nil when no
// Embedder was configured.
func (m *Memory) Recall(query string, limit int) []ai.Message {
	if m.embedder == nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	ctx := context.Background()

	vectors, err := m.embedder.Embed(ctx, query)
	if err != nil || len(vectors) == 0 {
		return nil
	}
	hits, err := m.cluster.VectorSearch(ctx, m.scope, itemsVectorIndex, embeddingField, vectors[0], limit, map[string]string{ownerField: m.key})
	if err != nil {
		return nil
	}

	col := m.cluster.Collection(m.scope, itemsCollection)
	out := make([]ai.Message, 0, len(hits))
	for _, h := range hits {
		raw, err := col.Get(ctx, h.ID)
		if err != nil {
			continue
		}
		var item memoryItem
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		out = append(out, ai.Message{Role: item.Role, Content: item.Content})
	}
	return out
}

type session struct {
	Messages []ai.Message `json:"messages"`
}

type memoryItem struct {
	Owner     string    `json:"owner"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Embedding []float32 `json:"embedding,omitempty"`
}

func (m *Memory) load(ctx context.Context) {
	raw, err := m.cluster.Collection(m.scope, sessionsCollection).Get(ctx, m.key)
	if err != nil {
		return
	}
	var s session
	if err := json.Unmarshal(raw, &s); err != nil {
		return
	}
	m.mu.Lock()
	for _, msg := range s.Messages {
		m.hist.Add(msg.Role, stringContent(msg.Content))
	}
	m.mu.Unlock()
}

func (m *Memory) saveSession(ctx context.Context, msgs []ai.Message) {
	raw, err := json.Marshal(session{Messages: msgs})
	if err != nil {
		return
	}
	_ = m.cluster.Collection(m.scope, sessionsCollection).Upsert(ctx, m.key, raw)
}

func (m *Memory) archive(ctx context.Context, role, content string) {
	if m.embedder == nil {
		return
	}
	vectors, err := m.embedder.Embed(ctx, content)
	if err != nil || len(vectors) == 0 {
		return
	}
	item := memoryItem{Owner: m.key, Role: role, Content: content, Embedding: vectors[0]}
	raw, err := json.Marshal(item)
	if err != nil {
		return
	}
	_ = m.cluster.Collection(m.scope, itemsCollection).Upsert(ctx, m.key+"/"+uuid.NewString(), raw)
}

func stringContent(v any) string {
	s, _ := v.(string)
	return s
}
