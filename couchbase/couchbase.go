// Package couchbase provides the storage substrate for a Couchbase-backed
// agent harness: a thin, interface-first wrapper around the Couchbase Go SDK
// (gocb) that the couchbase/catalog, couchbase/trace, couchbase/memory,
// couchbase/eval and couchbase/model packages build on.
//
// Every higher-level package in this tree depends only on the Cluster and
// Collection interfaces defined here, never on gocb types directly. That
// keeps them testable with in-memory fakes and keeps the Couchbase Go SDK
// as an implementation detail confined to this file.
package couchbase

import (
	"context"
	"errors"
	"fmt"

	gocb "github.com/couchbase/gocb/v2"
	"github.com/couchbase/gocb/v2/search"
	"github.com/couchbase/gocb/v2/vector"
)

// Cluster is a Couchbase cluster scoped to a single bucket. It exposes just
// enough surface for the couchbase/* packages: document access via
// Collection, FTS vector search via VectorSearch, and idempotent schema
// bootstrap via the Ensure* methods.
type Cluster interface {
	// Collection returns a handle to a scope.collection, creating neither.
	Collection(scope, name string) Collection
	// VectorSearch runs a KNN vector search against a scope-level FTS index
	// and returns the matching document IDs ordered by descending score.
	// filter, if non-empty, restricts matches to documents whose fields
	// exactly match every key/value pair (evaluated by the search service
	// itself, before the KNN cut, e.g. to scope a shared collection to one
	// tenant without an extra round trip).
	VectorSearch(ctx context.Context, scope, index, field string, embedding []float32, limit int, filter map[string]string) ([]SearchHit, error)
	// EnsureScope creates the scope if it does not already exist.
	EnsureScope(scope string) error
	// EnsureCollection creates the collection if it does not already exist.
	EnsureCollection(scope, name string) error
	// EnsurePrimaryIndex creates a primary GSI index on the collection if
	// one does not already exist.
	EnsurePrimaryIndex(scope, collection string) error
	// EnsureVectorIndex creates (or updates) a scope-level FTS index that
	// supports KNN vector search over field on scope.collection.
	EnsureVectorIndex(scope, collection, index, field string, dims int) error
	// Bucket returns the name of the bucket this Cluster is scoped to.
	Bucket() string
	// Close releases the underlying connection.
	Close() error
}

// SearchHit is a single result from a vector search.
type SearchHit struct {
	ID    string
	Score float64
}

// Collection stores JSON documents under string keys within one
// scope.collection. Values are passed through as raw JSON so callers control
// their own (de)serialization, matching the []byte-oriented go-micro
// store.Record convention.
type Collection interface {
	// Get fetches a document. Returns ErrNotFound if it does not exist.
	Get(ctx context.Context, id string) ([]byte, error)
	// Upsert creates or replaces a document.
	Upsert(ctx context.Context, id string, value []byte, opts ...WriteOption) error
	// Insert creates a document. Returns ErrAlreadyExists if it already exists.
	Insert(ctx context.Context, id string, value []byte, opts ...WriteOption) error
	// Remove deletes a document. Removing a missing document is not an error.
	Remove(ctx context.Context, id string) error
	// List returns the IDs of every document whose key starts with prefix.
	List(ctx context.Context, prefix string) ([]string, error)
}

// Connect opens a connection to a Couchbase cluster and waits for the
// configured bucket to become ready.
func Connect(opts ...Option) (Cluster, error) {
	options := newOptions(opts...)
	if options.ConnectionString == "" {
		return nil, errors.New("couchbase: ConnectionString is required")
	}
	if options.Bucket == "" {
		return nil, errors.New("couchbase: Bucket is required")
	}

	clusterOpts := gocb.ClusterOptions{
		Authenticator: gocb.PasswordAuthenticator{
			Username: options.Username,
			Password: options.Password,
		},
	}
	if options.WanProfile {
		if err := clusterOpts.ApplyProfile(gocb.ClusterConfigProfileWanDevelopment); err != nil {
			return nil, fmt.Errorf("couchbase: apply wan profile: %w", err)
		}
	}

	cl, err := gocb.Connect(options.ConnectionString, clusterOpts)
	if err != nil {
		return nil, fmt.Errorf("couchbase: connect: %w", err)
	}

	bucket := cl.Bucket(options.Bucket)
	if err := bucket.WaitUntilReady(options.ConnectTimeout, nil); err != nil {
		_ = cl.Close(nil)
		return nil, fmt.Errorf("couchbase: bucket %q not ready: %w", options.Bucket, err)
	}

	return &cluster{cl: cl, bucket: bucket, opts: options}, nil
}

type cluster struct {
	cl     *gocb.Cluster
	bucket *gocb.Bucket
	opts   Options
}

func (c *cluster) Bucket() string { return c.opts.Bucket }

func (c *cluster) Close() error {
	return c.cl.Close(nil)
}

func (c *cluster) Collection(scope, name string) Collection {
	s := c.bucket.Scope(scope)
	return &collection{col: s.Collection(name), scope: s, name: name}
}

func (c *cluster) VectorSearch(ctx context.Context, scope, index, field string, embedding []float32, limit int, filter map[string]string) ([]SearchHit, error) {
	if limit <= 0 {
		limit = 10
	}
	q := vector.NewQuery(field, embedding).NumCandidates(uint32(limit)) //nolint:gosec // limit is bounded by callers
	if len(filter) > 0 {
		q = q.Prefilter(matchAllFields(filter))
	}
	req := gocb.SearchRequest{VectorSearch: vector.NewSearch([]*vector.Query{q}, nil)}
	res, err := c.bucket.Scope(scope).Search(index, req, &gocb.SearchOptions{
		Context: ctx,
		Limit:   uint32(limit), //nolint:gosec // limit is bounded by callers
	})
	if err != nil {
		return nil, fmt.Errorf("couchbase: vector search: %w", err)
	}
	defer res.Close()

	var hits []SearchHit
	for res.Next() {
		row := res.Row()
		hits = append(hits, SearchHit{ID: row.ID, Score: row.Score})
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("couchbase: vector search: %w", err)
	}
	return hits, nil
}

func (c *cluster) EnsureScope(scope string) error {
	err := c.bucket.CollectionsV2().CreateScope(scope, nil)
	if err != nil && !errors.Is(err, gocb.ErrScopeExists) {
		return fmt.Errorf("couchbase: create scope %q: %w", scope, err)
	}
	return nil
}

func (c *cluster) EnsureCollection(scope, name string) error {
	err := c.bucket.CollectionsV2().CreateCollection(scope, name, &gocb.CreateCollectionSettings{}, nil)
	if err != nil && !errors.Is(err, gocb.ErrCollectionExists) {
		return fmt.Errorf("couchbase: create collection %q.%q: %w", scope, name, err)
	}
	return nil
}

func (c *cluster) EnsurePrimaryIndex(scope, collectionName string) error {
	qm := c.bucket.Scope(scope).Collection(collectionName).QueryIndexes()
	err := qm.CreatePrimaryIndex(&gocb.CreatePrimaryQueryIndexOptions{IgnoreIfExists: true})
	if err != nil && !errors.Is(err, gocb.ErrIndexExists) {
		return fmt.Errorf("couchbase: create primary index on %q.%q: %w", scope, collectionName, err)
	}
	return nil
}

// EnsureVectorIndex creates a scope-level FTS index configured for KNN
// vector search over a single dense-vector field. If the index already
// exists it is left untouched; changing the embedding dimension for an
// existing index requires dropping and recreating it.
func (c *cluster) EnsureVectorIndex(scope, collectionName, index, field string, dims int) error {
	sim := c.bucket.Scope(scope).SearchIndexes()

	if _, err := sim.GetIndex(index, nil); err == nil {
		return nil
	}

	def := gocb.SearchIndex{
		Name:       index,
		Type:       "fulltext-index",
		SourceType: "gocbcore",
		SourceName: c.opts.Bucket,
		Params: map[string]any{
			"doc_config": map[string]any{
				"mode": "scope.collection.type_field",
			},
			"mapping": map[string]any{
				"default_mapping": map[string]any{"enabled": false},
				"index_dynamic":   false,
				"store_dynamic":   false,
				"types": map[string]any{
					// dynamic:true here (scoped to just this collection's
					// type mapping, not the disabled default_mapping) lets
					// every other field - e.g. an "owner" field used to
					// scope VectorSearch's filter to one tenant - be
					// matched on without listing it explicitly, while the
					// vector field below still gets its dedicated mapping.
					scope + "." + collectionName: map[string]any{
						"enabled": true,
						"dynamic": true,
						"properties": map[string]any{
							field: map[string]any{
								"enabled": true,
								"dynamic": false,
								"fields": []map[string]any{
									{
										"name":                       field,
										"type":                       "vector",
										"dims":                       dims,
										"similarity":                 "dot_product",
										"index":                      true,
										"vector_index_optimized_for": "recall",
									},
								},
							},
						},
					},
				},
			},
			"store": map[string]any{"indexType": "scorch"},
		},
	}

	if err := sim.UpsertIndex(def, nil); err != nil {
		return fmt.Errorf("couchbase: create vector index %q: %w", index, err)
	}
	return nil
}

// matchAllFields builds a search.Query that requires an exact match on
// every field/value pair, for use as a vector.Query.Prefilter.
func matchAllFields(filter map[string]string) search.Query {
	queries := make([]search.Query, 0, len(filter))
	for field, value := range filter {
		queries = append(queries, search.NewMatchQuery(value).Field(field))
	}
	if len(queries) == 1 {
		return queries[0]
	}
	return search.NewConjunctionQuery(queries...)
}

type collection struct {
	col   *gocb.Collection
	scope *gocb.Scope
	name  string
}

func (c *collection) Get(ctx context.Context, id string) ([]byte, error) {
	res, err := c.col.Get(id, &gocb.GetOptions{Context: ctx})
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("couchbase: get %q: %w", id, err)
	}
	var raw []byte
	if err := res.Content(&raw); err != nil {
		return nil, fmt.Errorf("couchbase: decode %q: %w", id, err)
	}
	return raw, nil
}

func (c *collection) Upsert(ctx context.Context, id string, value []byte, opts ...WriteOption) error {
	options := newWriteOptions(opts...)
	_, err := c.col.Upsert(id, value, &gocb.UpsertOptions{Context: ctx, Expiry: options.Expiry})
	if err != nil {
		return fmt.Errorf("couchbase: upsert %q: %w", id, err)
	}
	return nil
}

func (c *collection) Insert(ctx context.Context, id string, value []byte, opts ...WriteOption) error {
	options := newWriteOptions(opts...)
	_, err := c.col.Insert(id, value, &gocb.InsertOptions{Context: ctx, Expiry: options.Expiry})
	if err != nil {
		if errors.Is(err, gocb.ErrDocumentExists) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("couchbase: insert %q: %w", id, err)
	}
	return nil
}

func (c *collection) Remove(ctx context.Context, id string) error {
	_, err := c.col.Remove(id, &gocb.RemoveOptions{Context: ctx})
	if err != nil && !errors.Is(err, gocb.ErrDocumentNotFound) {
		return fmt.Errorf("couchbase: remove %q: %w", id, err)
	}
	return nil
}

func (c *collection) List(ctx context.Context, prefix string) ([]string, error) {
	statement := fmt.Sprintf(
		"SELECT META(`%s`).id AS id FROM `%s` WHERE META(`%s`).id LIKE $prefix",
		c.name, c.name, c.name,
	)
	res, err := c.scope.Query(statement, &gocb.QueryOptions{
		Context:         ctx,
		NamedParameters: map[string]any{"prefix": prefix + "%"},
	})
	if err != nil {
		return nil, fmt.Errorf("couchbase: list %q*: %w", prefix, err)
	}
	defer res.Close()

	var ids []string
	for res.Next() {
		var row struct {
			ID string `json:"id"`
		}
		if err := res.Row(&row); err != nil {
			return nil, fmt.Errorf("couchbase: list %q*: %w", prefix, err)
		}
		ids = append(ids, row.ID)
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("couchbase: list %q*: %w", prefix, err)
	}
	return ids, nil
}
