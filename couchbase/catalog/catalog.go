// Package catalog stores and versions agent tools and prompts in Couchbase,
// mirroring the role Couchbase's agentc (agent-catalog) Python package
// plays for Python agents: tools and prompts are registered in code,
// content-hash versioned, embedded, and synced to Couchbase collections so
// they can be discovered later by name, by annotation, or by semantic
// (vector) search — from this process or any other pointed at the same
// bucket.
//
// Unlike agentc, which derives the catalog from decorated source files and
// git history at index time, this package uses explicit Go registration
// (RegisterTool/RegisterPrompt) since Go has no runtime introspection over
// doc comments or decorators. Versioning still favors git when available
// (see Version), falling back to a pure content hash otherwise, and each
// Sync call is what actually publishes the current in-process definitions
// to Couchbase.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase"
)

// Kind discriminates the two record types a Catalog stores.
type Kind string

const (
	KindTool   Kind = "tool"
	KindPrompt Kind = "prompt"
)

// DefaultScope is the Couchbase scope Sync provisions tools and prompts
// collections under, matching agentc's "agent_catalog" convention.
const DefaultScope = "agent_catalog"

const (
	toolsCollection    = "tools"
	promptsCollection  = "prompts"
	toolsVectorIndex   = "agent_catalog_tools_vector"
	promptsVectorIndex = "agent_catalog_prompts_vector"
	embeddingField     = "embedding"
)

// Embedder produces vector embeddings for text, powering semantic
// (find-by-meaning) lookups over the catalog. couchbase/model.Embedder
// implements this against Couchbase's hosted Model Service.
type Embedder interface {
	// Embed returns one embedding vector per input text, in the same order.
	Embed(ctx context.Context, texts ...string) ([][]float32, error)
	// Dimensions returns the length of the vectors Embed produces.
	Dimensions() int
}

// ToolRef is how a Prompt declares a tool it depends on. It is resolved
// against the catalog at retrieval time via Catalog.ResolveTools, the same
// way agentc prompts declare `tools:` entries.
type ToolRef struct {
	// Name looks the tool up by exact name.
	Name string `json:"name,omitempty"`
	// Query, if Name is empty, looks tools up by semantic search.
	Query string `json:"query,omitempty"`
	// Annotations further restricts matches to tools carrying these key/value pairs.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Limit caps how many tools a Query match returns (default 5).
	Limit int `json:"limit,omitempty"`
}

// ToolSpec registers a Go function as a catalog tool. Handler is never
// persisted to Couchbase — only Name, Description, Parameters and
// Annotations are — so any process that registers a tool with the same
// Name can execute catalog-discovered calls to it.
type ToolSpec struct {
	Name        string
	Description string
	// Parameters is a JSON-Schema "properties" map, matching ai.Tool.Properties.
	Parameters  map[string]any
	Annotations map[string]string
	Handler     ai.ToolHandler
}

// PromptSpec registers a versioned, catalog-managed prompt.
type PromptSpec struct {
	Name        string
	Description string
	Content     string
	// Tools are the tools this prompt depends on; resolve them with ResolveTools.
	Tools []ToolRef
	// Output is an optional JSON schema the model's answer should conform to.
	Output      map[string]any
	Annotations map[string]string
}

// Record is the persisted, versioned form of a registered tool or prompt.
type Record struct {
	Kind        Kind              `json:"kind"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  map[string]any    `json:"parameters,omitempty"`
	Content     string            `json:"content,omitempty"`
	Tools       []ToolRef         `json:"tools,omitempty"`
	Output      map[string]any    `json:"output,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Version     Version           `json:"version"`
	Embedding   []float32         `json:"embedding,omitempty"`
}

// Catalog registers, versions, and serves agent tools and prompts backed by
// Couchbase. The zero value is not usable; construct one with New.
type Catalog struct {
	cluster  couchbase.Cluster
	embedder Embedder
	scope    string

	mu      sync.RWMutex
	tools   map[string]ToolSpec
	prompts map[string]PromptSpec
}

// Option configures a Catalog.
type Option func(*Catalog)

// InScope overrides the Couchbase scope tools and prompts are stored under
// (default DefaultScope).
func InScope(name string) Option {
	return func(c *Catalog) { c.scope = name }
}

// New creates a Catalog. embedder may be nil, in which case Sync skips
// vector index creation and FindTools/FindPrompts can only match by name or
// annotation, not by semantic query.
func New(cluster couchbase.Cluster, embedder Embedder, opts ...Option) *Catalog {
	c := &Catalog{
		cluster:  cluster,
		embedder: embedder,
		scope:    DefaultScope,
		tools:    make(map[string]ToolSpec),
		prompts:  make(map[string]PromptSpec),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// RegisterTool adds (or replaces) a tool definition. It takes effect in
// Couchbase the next time Sync runs.
func (c *Catalog) RegisterTool(spec ToolSpec) error {
	if spec.Name == "" {
		return errors.New("catalog: tool name is required")
	}
	if spec.Description == "" {
		return fmt.Errorf("catalog: tool %q: description is required", spec.Name)
	}
	if spec.Handler == nil {
		return fmt.Errorf("catalog: tool %q: handler is required", spec.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tools[spec.Name] = spec
	return nil
}

// RegisterPrompt adds (or replaces) a prompt definition. It takes effect in
// Couchbase the next time Sync runs.
func (c *Catalog) RegisterPrompt(spec PromptSpec) error {
	if spec.Name == "" {
		return errors.New("catalog: prompt name is required")
	}
	if spec.Description == "" {
		return fmt.Errorf("catalog: prompt %q: description is required", spec.Name)
	}
	if spec.Content == "" {
		return fmt.Errorf("catalog: prompt %q: content is required", spec.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prompts[spec.Name] = spec
	return nil
}

// Sync provisions the catalog's Couchbase schema (scope, collections, GSI
// primary indexes and, if an Embedder is configured, FTS vector indexes)
// and publishes every registered tool and prompt. It is safe to call
// repeatedly — schema provisioning is idempotent and only tools/prompts
// whose content actually changed are re-embedded, mirroring agentc's
// index-then-publish loop without needing a separate offline step.
func (c *Catalog) Sync(ctx context.Context) error {
	c.mu.RLock()
	tools := make([]ToolSpec, 0, len(c.tools))
	for _, t := range c.tools {
		tools = append(tools, t)
	}
	prompts := make([]PromptSpec, 0, len(c.prompts))
	for _, p := range c.prompts {
		prompts = append(prompts, p)
	}
	c.mu.RUnlock()

	if err := c.ensureSchema(ctx); err != nil {
		return err
	}

	toolRecords := make([]Record, len(tools))
	for i, t := range tools {
		toolRecords[i] = Record{
			Kind: KindTool, Name: t.Name, Description: t.Description,
			Parameters: t.Parameters, Annotations: t.Annotations,
			Version: newVersion(hashTool(t)),
		}
	}
	promptRecords := make([]Record, len(prompts))
	for i, p := range prompts {
		promptRecords[i] = Record{
			Kind: KindPrompt, Name: p.Name, Description: p.Description,
			Content: p.Content, Tools: p.Tools, Output: p.Output, Annotations: p.Annotations,
			Version: newVersion(hashPrompt(p)),
		}
	}

	if err := c.syncRecords(ctx, toolsCollection, toolRecords); err != nil {
		return fmt.Errorf("catalog: sync tools: %w", err)
	}
	if err := c.syncRecords(ctx, promptsCollection, promptRecords); err != nil {
		return fmt.Errorf("catalog: sync prompts: %w", err)
	}
	return nil
}

func (c *Catalog) ensureSchema(_ context.Context) error {
	if err := c.cluster.EnsureScope(c.scope); err != nil {
		return err
	}
	for _, coll := range []string{toolsCollection, promptsCollection} {
		if err := c.cluster.EnsureCollection(c.scope, coll); err != nil {
			return err
		}
		if err := c.cluster.EnsurePrimaryIndex(c.scope, coll); err != nil {
			return err
		}
	}
	if c.embedder == nil {
		return nil
	}
	dims := c.embedder.Dimensions()
	if err := c.cluster.EnsureVectorIndex(c.scope, toolsCollection, toolsVectorIndex, embeddingField, dims); err != nil {
		return err
	}
	if err := c.cluster.EnsureVectorIndex(c.scope, promptsCollection, promptsVectorIndex, embeddingField, dims); err != nil {
		return err
	}
	return nil
}

// syncRecords upserts records into collectionName, re-embedding only those
// whose Version.Hash changed since the last sync (or that are new).
func (c *Catalog) syncRecords(ctx context.Context, collectionName string, records []Record) error {
	col := c.cluster.Collection(c.scope, collectionName)

	var toEmbed []int
	for i, r := range records {
		existing, err := fetchRecord(ctx, col, r.Name)
		if err == nil && existing.Version.Hash == r.Version.Hash {
			records[i].Embedding = existing.Embedding
			continue
		}
		toEmbed = append(toEmbed, i)
	}

	if c.embedder != nil && len(toEmbed) > 0 {
		texts := make([]string, len(toEmbed))
		for i, idx := range toEmbed {
			texts[i] = records[idx].Description
		}
		vectors, err := c.embedder.Embed(ctx, texts...)
		if err != nil {
			return fmt.Errorf("embed: %w", err)
		}
		if len(vectors) != len(toEmbed) {
			return fmt.Errorf("embedder returned %d vectors for %d inputs", len(vectors), len(toEmbed))
		}
		for i, idx := range toEmbed {
			records[idx].Embedding = vectors[i]
		}
	}

	for _, r := range records {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := col.Upsert(ctx, r.Name, raw); err != nil {
			return fmt.Errorf("upsert %q: %w", r.Name, err)
		}
	}
	return nil
}

func fetchRecord(ctx context.Context, col couchbase.Collection, name string) (Record, error) {
	raw, err := col.Get(ctx, name)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return Record{}, err
	}
	return r, nil
}
