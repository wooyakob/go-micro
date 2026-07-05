package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newFakeEmbeddingsServer(t *testing.T, dims int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{}
		// Return results in reverse order to prove Embed reassembles by index.
		for i := len(req.Input) - 1; i >= 0; i-- {
			v := make([]float32, dims)
			for j := range v {
				v[j] = float32(i + j)
			}
			resp.Data = append(resp.Data, item{Embedding: v, Index: i})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestEmbedReturnsVectorsInInputOrder(t *testing.T) {
	server := newFakeEmbeddingsServer(t, 3)
	defer server.Close()

	e := NewEmbedder(WithBaseURL(server.URL), WithAPIKey("test-key"), WithEmbeddingModel("test-embed"))
	vectors, err := e.Embed(context.Background(), "a", "b", "c")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vectors))
	}
	if vectors[0][0] != 0 || vectors[2][0] != 2 {
		t.Errorf("vectors not reassembled by index: %+v", vectors)
	}
}

func TestDimensionsProbesWhenUnset(t *testing.T) {
	server := newFakeEmbeddingsServer(t, 7)
	defer server.Close()

	e := NewEmbedder(WithBaseURL(server.URL), WithAPIKey("test-key"))
	if got := e.Dimensions(); got != 7 {
		t.Errorf("Dimensions() = %d, want 7", got)
	}
	// Second call should reuse the cached value without another request
	// (the test server would fail on an empty input otherwise).
	if got := e.Dimensions(); got != 7 {
		t.Errorf("Dimensions() (cached) = %d, want 7", got)
	}
}

func TestDimensionsExplicitSkipsProbe(t *testing.T) {
	e := NewEmbedder(WithBaseURL("http://unused.invalid"), WithDimensions(1536))
	if got := e.Dimensions(); got != 1536 {
		t.Errorf("Dimensions() = %d, want 1536", got)
	}
}

func TestEmbedSurfacesAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer server.Close()

	e := NewEmbedder(WithBaseURL(server.URL), WithAPIKey("bad"))
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Error("expected an error for a 401 response")
	}
}
