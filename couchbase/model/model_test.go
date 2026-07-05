package model

import (
	"testing"

	"go-micro.dev/v6/ai"
)

func TestProviderStringIsCouchbase(t *testing.T) {
	p := NewProvider(ai.WithBaseURL("https://example.capella.couchbase.com/model-service/v1"), ai.WithAPIKey("jwt"))
	if got := p.String(); got != "couchbase" {
		t.Errorf("String() = %q, want %q", got, "couchbase")
	}
}

func TestProviderRegisteredUnderCouchbaseName(t *testing.T) {
	m := ai.New("couchbase", ai.WithBaseURL("https://example.com"), ai.WithAPIKey("k"))
	if m == nil {
		t.Fatal("expected ai.New(\"couchbase\", ...) to return a registered provider")
	}
	if m.String() != "couchbase" {
		t.Errorf("String() = %q, want %q", m.String(), "couchbase")
	}
}

func TestProviderPassesThroughOptions(t *testing.T) {
	p := NewProvider(ai.WithModel("my-hosted-model"), ai.WithBaseURL("https://example.com"))
	if p.Options().Model != "my-hosted-model" {
		t.Errorf("Model = %q", p.Options().Model)
	}
	if p.Options().BaseURL != "https://example.com" {
		t.Errorf("BaseURL = %q", p.Options().BaseURL)
	}
}
