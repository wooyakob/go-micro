package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

type embedOptions struct {
	baseURL    string
	apiKey     string
	model      string
	dims       int
	httpClient *http.Client
}

// EmbedOption configures an Embedder.
type EmbedOption func(*embedOptions)

// WithBaseURL sets the Model Service endpoint (required).
func WithBaseURL(url string) EmbedOption { return func(o *embedOptions) { o.baseURL = url } }

// WithAPIKey sets the bearer credential — an API key, or your Capella JWT
// when the endpoint is hosted on Capella.
func WithAPIKey(key string) EmbedOption { return func(o *embedOptions) { o.apiKey = key } }

// WithEmbeddingModel sets the embedding model name sent with every request.
func WithEmbeddingModel(name string) EmbedOption { return func(o *embedOptions) { o.model = name } }

// WithDimensions declares the embedding size up front, skipping the probe
// call Dimensions would otherwise make on first use.
func WithDimensions(n int) EmbedOption { return func(o *embedOptions) { o.dims = n } }

// WithHTTPClient overrides the HTTP client used for embedding requests.
func WithHTTPClient(c *http.Client) EmbedOption { return func(o *embedOptions) { o.httpClient = c } }

// Embedder implements couchbase/catalog.Embedder and couchbase/memory.Embedder
// against a Couchbase Model Service (or any OpenAI-embeddings-API-compatible)
// endpoint.
type Embedder struct {
	opts embedOptions

	mu   sync.Mutex
	dims int
}

// NewEmbedder creates an Embedder.
func NewEmbedder(opts ...EmbedOption) *Embedder {
	o := embedOptions{httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(&o)
	}
	return &Embedder{opts: o, dims: o.dims}
}

// Dimensions returns the embedding size, probing the endpoint with a single
// short embed call the first time if it wasn't supplied via WithDimensions.
func (e *Embedder) Dimensions() int {
	e.mu.Lock()
	dims := e.dims
	e.mu.Unlock()
	if dims > 0 {
		return dims
	}
	vectors, err := e.Embed(context.Background(), "dimension probe")
	if err != nil || len(vectors) == 0 {
		return 0
	}
	return len(vectors[0])
}

// Embed returns one embedding vector per input text, in the same order.
func (e *Embedder) Embed(ctx context.Context, texts ...string) ([][]float32, error) {
	reqBody, err := json.Marshal(map[string]any{
		"model": e.opts.model,
		"input": texts,
	})
	if err != nil {
		return nil, fmt.Errorf("model: marshal embeddings request: %w", err)
	}

	apiURL := strings.TrimRight(e.opts.baseURL, "/") + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("model: create embeddings request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.opts.apiKey)

	httpResp, err := e.opts.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("model: embeddings request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model: embeddings API error (%s): %s", httpResp.Status, string(respBody))
	}

	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("model: parse embeddings response: %w", err)
	}

	vectors := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(vectors) {
			continue
		}
		vectors[d.Index] = d.Embedding
	}

	if e.dims == 0 && len(vectors) > 0 && len(vectors[0]) > 0 {
		e.mu.Lock()
		e.dims = len(vectors[0])
		e.mu.Unlock()
	}
	return vectors, nil
}
