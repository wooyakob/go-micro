package harness

import (
	"errors"
	"testing"

	"go-micro.dev/v6/couchbase/couchbasetest"
	"go-micro.dev/v6/store"
)

func TestReadPropagatesNonNotFoundErrors(t *testing.T) {
	cluster := couchbasetest.New("agents")
	s := NewStore(cluster)

	if err := s.Write(&store.Record{Key: "k1", Value: []byte("v1")}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Write(&store.Record{Key: "k2", Value: []byte("v2")}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	boom := errors.New("boom: transient network failure")
	cluster.InjectGetError(defaultStoreScope, defaultStoreTable, "k1", boom)

	if _, err := s.Read("k", store.ReadPrefix()); !errors.Is(err, boom) {
		t.Fatalf("Read(prefix) error = %v, want %v", err, boom)
	}
}
