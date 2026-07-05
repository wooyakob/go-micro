// Package couchbasetest provides an in-memory couchbase.Cluster fake for
// unit testing the couchbase/* packages without a live Couchbase cluster.
//
// It is driven purely by the same calls production code makes: registering
// a vector index via EnsureVectorIndex and then Upserting documents that
// carry a numeric array field is enough for VectorSearch to find them by
// brute-force cosine similarity, no separate seeding API required.
package couchbasetest

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"sync"

	"go-micro.dev/v6/couchbase"
)

// Cluster is an in-memory couchbase.Cluster.
type Cluster struct {
	mu      sync.Mutex
	bucket  string
	docs    map[string]map[string][]byte
	indexes map[string]vectorIndex
}

type vectorIndex struct {
	scope, collection, field string
}

// New creates an empty in-memory cluster fake scoped to bucket.
func New(bucket string) *Cluster {
	return &Cluster{
		bucket:  bucket,
		docs:    make(map[string]map[string][]byte),
		indexes: make(map[string]vectorIndex),
	}
}

var _ couchbase.Cluster = (*Cluster)(nil)

// Bucket returns the configured bucket name.
func (c *Cluster) Bucket() string { return c.bucket }

// Close is a no-op.
func (c *Cluster) Close() error { return nil }

// EnsureScope is a no-op; the fake has no scope/collection hierarchy of its own.
func (c *Cluster) EnsureScope(string) error { return nil }

// EnsureCollection is a no-op.
func (c *Cluster) EnsureCollection(string, string) error { return nil }

// EnsurePrimaryIndex is a no-op; List always scans in-memory.
func (c *Cluster) EnsurePrimaryIndex(string, string) error { return nil }

// EnsureVectorIndex records which scope.collection/field a named index
// covers so a later VectorSearch call can find it.
func (c *Cluster) EnsureVectorIndex(scope, collectionName, index, field string, _ int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.indexes[index] = vectorIndex{scope: scope, collection: collectionName, field: field}
	return nil
}

// Collection returns an in-memory document store scoped to scope.name.
func (c *Cluster) Collection(scope, name string) couchbase.Collection {
	return &fakeCollection{c: c, key: scope + "." + name}
}

// VectorSearch performs brute-force cosine similarity search over every
// document in the collection registered for index that carries a numeric
// array under field and matches every filter key/value pair, if any.
func (c *Cluster) VectorSearch(_ context.Context, _, index, field string, embedding []float32, limit int, filter map[string]string) ([]couchbase.SearchHit, error) {
	c.mu.Lock()
	idx, ok := c.indexes[index]
	if !ok {
		c.mu.Unlock()
		return nil, nil
	}
	docs := c.docs[idx.scope+"."+idx.collection]
	type scored struct {
		id    string
		score float64
	}
	results := make([]scored, 0, len(docs))
	for id, raw := range docs {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		if !matchesFilter(doc, filter) {
			continue
		}
		vec, ok := extractVector(doc[field])
		if !ok {
			continue
		}
		results = append(results, scored{id: id, score: cosineSimilarity(embedding, vec)})
	}
	c.mu.Unlock()

	sort.Slice(results, func(i, j int) bool { return results[i].score > results[j].score })
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	hits := make([]couchbase.SearchHit, len(results))
	for i, r := range results {
		hits[i] = couchbase.SearchHit{ID: r.id, Score: r.score}
	}
	return hits, nil
}

func matchesFilter(doc map[string]any, filter map[string]string) bool {
	for k, v := range filter {
		s, ok := doc[k].(string)
		if !ok || s != v {
			return false
		}
	}
	return true
}

func extractVector(v any) ([]float32, bool) {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		return nil, false
	}
	out := make([]float32, len(raw))
	for i, e := range raw {
		f, ok := e.(float64)
		if !ok {
			return nil, false
		}
		out[i] = float32(f)
	}
	return out, true
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type fakeCollection struct {
	c   *Cluster
	key string
}

func (f *fakeCollection) Get(_ context.Context, id string) ([]byte, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	v, ok := f.c.docs[f.key][id]
	if !ok {
		return nil, couchbase.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (f *fakeCollection) Upsert(_ context.Context, id string, value []byte, _ ...couchbase.WriteOption) error {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	if f.c.docs[f.key] == nil {
		f.c.docs[f.key] = make(map[string][]byte)
	}
	f.c.docs[f.key][id] = append([]byte(nil), value...)
	return nil
}

func (f *fakeCollection) Insert(ctx context.Context, id string, value []byte, opts ...couchbase.WriteOption) error {
	f.c.mu.Lock()
	if _, exists := f.c.docs[f.key][id]; exists {
		f.c.mu.Unlock()
		return couchbase.ErrAlreadyExists
	}
	f.c.mu.Unlock()
	return f.Upsert(ctx, id, value, opts...)
}

func (f *fakeCollection) Remove(_ context.Context, id string) error {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	delete(f.c.docs[f.key], id)
	return nil
}

func (f *fakeCollection) List(_ context.Context, prefix string) ([]string, error) {
	f.c.mu.Lock()
	defer f.c.mu.Unlock()
	var ids []string
	for id := range f.c.docs[f.key] {
		if strings.HasPrefix(id, prefix) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
