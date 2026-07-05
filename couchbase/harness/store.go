package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"go-micro.dev/v6/couchbase"
	"go-micro.dev/v6/store"
)

const (
	defaultStoreScope = "agent_state"
	defaultStoreTable = "state"
)

// NewStore adapts a couchbase.Cluster into a go-micro store.Store, so
// agent.WithStore and flow.StoreCheckpoint can persist agent state —
// conversation buffers, checkpointed runs, plan steps — in the same
// Couchbase cluster as the rest of the harness instead of a separate
// backend. Table maps to a Couchbase collection (created lazily on first
// use) within a scope named by Database (default "agent_state").
func NewStore(cluster couchbase.Cluster, opts ...store.Option) store.Store {
	var options store.Options
	for _, o := range opts {
		o(&options)
	}
	if options.Database == "" {
		options.Database = defaultStoreScope
	}
	if options.Table == "" {
		options.Table = defaultStoreTable
	}
	return &cbStore{cluster: cluster, opts: options}
}

type wireRecord struct {
	Metadata map[string]any `json:"metadata,omitempty"`
	Value    []byte         `json:"value"`
}

type cbStore struct {
	cluster couchbase.Cluster
	opts    store.Options
	ensured sync.Map
}

func (s *cbStore) Init(opts ...store.Option) error {
	for _, o := range opts {
		o(&s.opts)
	}
	return nil
}

func (s *cbStore) Options() store.Options { return s.opts }
func (s *cbStore) String() string         { return "couchbase" }
func (s *cbStore) Close() error           { return nil }

func (s *cbStore) resolve(database, table string) (string, string) {
	if database == "" {
		database = s.opts.Database
	}
	if table == "" {
		table = s.opts.Table
	}
	return database, table
}

func (s *cbStore) ensure(scope, collection string) error {
	key := scope + "." + collection
	if _, ok := s.ensured.Load(key); ok {
		return nil
	}
	if err := s.cluster.EnsureScope(scope); err != nil {
		return err
	}
	if err := s.cluster.EnsureCollection(scope, collection); err != nil {
		return err
	}
	if err := s.cluster.EnsurePrimaryIndex(scope, collection); err != nil {
		return err
	}
	s.ensured.Store(key, struct{}{})
	return nil
}

func (s *cbStore) Write(r *store.Record, opts ...store.WriteOption) error {
	var wo store.WriteOptions
	for _, o := range opts {
		o(&wo)
	}
	scope, table := s.resolve(wo.Database, wo.Table)
	if err := s.ensure(scope, table); err != nil {
		return err
	}

	raw, err := json.Marshal(wireRecord{Metadata: r.Metadata, Value: r.Value})
	if err != nil {
		return err
	}

	var writeOpts []couchbase.WriteOption
	switch {
	case wo.TTL > 0:
		writeOpts = append(writeOpts, couchbase.WithExpiry(wo.TTL))
	case !wo.Expiry.IsZero():
		if d := time.Until(wo.Expiry); d > 0 {
			writeOpts = append(writeOpts, couchbase.WithExpiry(d))
		}
	case r.Expiry > 0:
		writeOpts = append(writeOpts, couchbase.WithExpiry(r.Expiry))
	}

	return s.cluster.Collection(scope, table).Upsert(context.Background(), r.Key, raw, writeOpts...)
}

func (s *cbStore) Read(key string, opts ...store.ReadOption) ([]*store.Record, error) {
	var ro store.ReadOptions
	for _, o := range opts {
		o(&ro)
	}
	scope, table := s.resolve(ro.Database, ro.Table)
	if err := s.ensure(scope, table); err != nil {
		return nil, err
	}
	col := s.cluster.Collection(scope, table)
	ctx := context.Background()

	if !ro.Prefix && !ro.Suffix {
		raw, err := col.Get(ctx, key)
		if err != nil {
			if errors.Is(err, couchbase.ErrNotFound) {
				return nil, store.ErrNotFound
			}
			return nil, err
		}
		rec, err := decodeRecord(key, raw)
		if err != nil {
			return nil, err
		}
		return []*store.Record{rec}, nil
	}

	prefix := ""
	if ro.Prefix {
		prefix = key
	}
	ids, err := col.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if ro.Suffix {
		ids = filterSuffix(ids, key)
	}
	ids = paginate(ids, ro.Offset, ro.Limit)

	records := make([]*store.Record, 0, len(ids))
	for _, id := range ids {
		raw, err := col.Get(ctx, id)
		if err != nil {
			continue
		}
		rec, err := decodeRecord(id, raw)
		if err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

func (s *cbStore) Delete(key string, opts ...store.DeleteOption) error {
	var do store.DeleteOptions
	for _, o := range opts {
		o(&do)
	}
	scope, table := s.resolve(do.Database, do.Table)
	if err := s.ensure(scope, table); err != nil {
		return err
	}
	return s.cluster.Collection(scope, table).Remove(context.Background(), key)
}

func (s *cbStore) List(opts ...store.ListOption) ([]string, error) {
	var lo store.ListOptions
	for _, o := range opts {
		o(&lo)
	}
	scope, table := s.resolve(lo.Database, lo.Table)
	if err := s.ensure(scope, table); err != nil {
		return nil, err
	}
	ids, err := s.cluster.Collection(scope, table).List(context.Background(), lo.Prefix)
	if err != nil {
		return nil, err
	}
	if lo.Suffix != "" {
		ids = filterSuffix(ids, lo.Suffix)
	}
	return paginate(ids, lo.Offset, lo.Limit), nil
}

func decodeRecord(key string, raw []byte) (*store.Record, error) {
	var wr wireRecord
	if err := json.Unmarshal(raw, &wr); err != nil {
		return nil, err
	}
	return &store.Record{Key: key, Value: wr.Value, Metadata: wr.Metadata}, nil
}

func filterSuffix(ids []string, suffix string) []string {
	filtered := ids[:0]
	for _, id := range ids {
		if strings.HasSuffix(id, suffix) {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

func paginate(ids []string, offset, limit uint) []string {
	if int(offset) >= len(ids) {
		return nil
	}
	ids = ids[offset:]
	if limit > 0 && uint(len(ids)) > limit {
		ids = ids[:limit]
	}
	return ids
}
