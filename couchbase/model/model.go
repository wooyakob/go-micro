// Package model implements go-micro's ai.Model interface, and a matching
// embeddings client, against Couchbase's hosted Model Service (Capella AI
// Services) — an OpenAI-chat-completions-compatible endpoint for both LLM
// and embedding calls, the same integration point agentc uses for its own
// embedding model (see agentc_core/learned/embedding.py, which does
// `openai.OpenAI(base_url=embedding_model_url, api_key=embedding_model_auth)`).
//
// Chat completions delegate entirely to the existing ai/openai provider —
// the wire format is identical, only the endpoint and its credential
// differ — so Provider's own code is limited to reporting itself under the
// "couchbase" name for tracing and provider registration. Embed is the new
// piece: go-micro has no existing embeddings client.
package model

import (
	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/ai/openai"
)

func init() {
	ai.Register("couchbase", func(opts ...ai.Option) ai.Model {
		return NewProvider(opts...)
	})
}

// Provider is an ai.Model backed by a Couchbase Model Service endpoint.
// Configure it with ai.WithBaseURL(your Model Service URL) and
// ai.WithAPIKey(your Capella JWT or API key) — unlike ai/openai, no
// default BaseURL is assumed, since a Capella Model Service endpoint is
// unique per organization; omitting it falls back to ai/openai's own
// default of api.openai.com, which is only useful for testing against a
// real OpenAI account.
type Provider struct {
	*openai.Provider
}

// NewProvider creates a Provider.
func NewProvider(opts ...ai.Option) *Provider {
	return &Provider{Provider: openai.NewProvider(opts...)}
}

// String returns "couchbase", so tracing and logs correctly attribute calls
// to the Couchbase Model Service rather than the underlying OpenAI-wire-format client.
func (p *Provider) String() string { return "couchbase" }
