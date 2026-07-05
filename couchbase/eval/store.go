package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"

	"go-micro.dev/v6/couchbase"
)

// DefaultScope is the Couchbase scope Store provisions its collection under.
const DefaultScope = "agent_evals"

const reportsCollection = "reports"

// Store persists eval Reports to Couchbase, keyed so History can list every
// past run of a suite with a prefix scan.
type Store struct {
	cluster couchbase.Cluster
	scope   string
}

// NewStore creates a Store and provisions its Couchbase schema.
func NewStore(cluster couchbase.Cluster, opts ...StoreOption) (*Store, error) {
	o := storeOptions{scope: DefaultScope}
	for _, opt := range opts {
		opt(&o)
	}
	if err := cluster.EnsureScope(o.scope); err != nil {
		return nil, err
	}
	if err := cluster.EnsureCollection(o.scope, reportsCollection); err != nil {
		return nil, err
	}
	if err := cluster.EnsurePrimaryIndex(o.scope, reportsCollection); err != nil {
		return nil, err
	}
	return &Store{cluster: cluster, scope: o.scope}, nil
}

type storeOptions struct{ scope string }

// StoreOption configures a Store.
type StoreOption func(*storeOptions)

// InScope overrides the Couchbase scope Store provisions its collection under.
func InScope(scope string) StoreOption {
	return func(o *storeOptions) { o.scope = scope }
}

// Save persists a Report under a fresh ID prefixed with its suite name.
func (s *Store) Save(ctx context.Context, r Report) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	id := r.Suite + "/" + uuid.NewString()
	if err := s.cluster.Collection(s.scope, reportsCollection).Upsert(ctx, id, raw); err != nil {
		return fmt.Errorf("eval: save report %q: %w", id, err)
	}
	return nil
}

// History returns up to limit of the most recent reports for suite,
// newest first.
func (s *Store) History(ctx context.Context, suite string, limit int) ([]Report, error) {
	col := s.cluster.Collection(s.scope, reportsCollection)
	ids, err := col.List(ctx, suite+"/")
	if err != nil {
		return nil, err
	}

	reports := make([]Report, 0, len(ids))
	for _, id := range ids {
		raw, err := col.Get(ctx, id)
		if err != nil {
			continue
		}
		var r Report
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		reports = append(reports, r)
	}

	sort.Slice(reports, func(i, j int) bool { return reports[i].RunAt.After(reports[j].RunAt) })
	if limit > 0 && len(reports) > limit {
		reports = reports[:limit]
	}
	return reports, nil
}
