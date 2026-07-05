package trace

import (
	"context"
	"encoding/json"
	"sort"

	"go-micro.dev/v6/couchbase"
)

// Store reads spans back out of Couchbase. Point it at the same
// scope/collection an Exporter writes to (they default to the same place).
type Store struct {
	cluster    couchbase.Cluster
	scope      string
	collection string
}

// NewStore creates a Store. It does not provision any schema — use
// NewExporter (or Sync it yourself) first.
func NewStore(cluster couchbase.Cluster, opts ...Option) *Store {
	o := newOptions(opts...)
	return &Store{cluster: cluster, scope: o.scope, collection: o.collection}
}

// Trace returns every span belonging to traceID (one agent run, including
// its tool calls and model calls), ordered by start time.
func (s *Store) Trace(ctx context.Context, traceID string) ([]Record, error) {
	col := s.cluster.Collection(s.scope, s.collection)
	ids, err := col.List(ctx, traceID+"/")
	if err != nil {
		return nil, err
	}
	records := s.fetchAll(ctx, col, ids)
	sort.Slice(records, func(i, j int) bool { return records[i].StartTime.Before(records[j].StartTime) })
	return records, nil
}

// Recent returns up to limit of the most recently started spans across all
// traces. It scans every span in the collection to sort them, so it is
// meant for operator/debugging use at modest data volumes — for
// high-throughput analytics over trace history, query the collection
// directly with SQL++.
func (s *Store) Recent(ctx context.Context, limit int) ([]Record, error) {
	col := s.cluster.Collection(s.scope, s.collection)
	ids, err := col.List(ctx, "")
	if err != nil {
		return nil, err
	}
	records := s.fetchAll(ctx, col, ids)
	sort.Slice(records, func(i, j int) bool { return records[i].StartTime.After(records[j].StartTime) })
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *Store) fetchAll(ctx context.Context, col couchbase.Collection, ids []string) []Record {
	records := make([]Record, 0, len(ids))
	for _, id := range ids {
		raw, err := col.Get(ctx, id)
		if err != nil {
			continue
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		records = append(records, r)
	}
	return records
}
