package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase"
)

type findOptions struct {
	name        string
	query       string
	annotations map[string]string
	limit       int
}

// FindOption narrows a catalog lookup.
type FindOption func(*findOptions)

// ByName matches a tool or prompt by its exact name, short-circuiting
// semantic search.
func ByName(name string) FindOption {
	return func(o *findOptions) { o.name = name }
}

// ByQuery matches tools or prompts by semantic similarity to query. Requires
// the Catalog to have been constructed with an Embedder.
func ByQuery(query string) FindOption {
	return func(o *findOptions) { o.query = query }
}

// WithAnnotations restricts matches to records carrying every given
// key/value pair.
func WithAnnotations(annotations map[string]string) FindOption {
	return func(o *findOptions) { o.annotations = annotations }
}

// Limit caps the number of records a Find call returns (default 5).
func Limit(n int) FindOption {
	return func(o *findOptions) { o.limit = n }
}

func newFindOptions(opts ...FindOption) findOptions {
	o := findOptions{limit: 5}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// FindTool returns the single best-matching tool, or an error if none match.
func (c *Catalog) FindTool(ctx context.Context, opts ...FindOption) (*Record, error) {
	records, err := c.FindTools(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("catalog: no matching tool")
	}
	return &records[0], nil
}

// FindTools returns every tool matching opts, ranked by semantic similarity
// when ByQuery is used, alphabetically otherwise.
func (c *Catalog) FindTools(ctx context.Context, opts ...FindOption) ([]Record, error) {
	return c.find(ctx, toolsCollection, toolsVectorIndex, opts...)
}

// FindPrompt returns the single best-matching prompt, or an error if none match.
func (c *Catalog) FindPrompt(ctx context.Context, opts ...FindOption) (*Record, error) {
	records, err := c.FindPrompts(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("catalog: no matching prompt")
	}
	return &records[0], nil
}

// FindPrompts returns every prompt matching opts, ranked by semantic
// similarity when ByQuery is used, alphabetically otherwise.
func (c *Catalog) FindPrompts(ctx context.Context, opts ...FindOption) ([]Record, error) {
	return c.find(ctx, promptsCollection, promptsVectorIndex, opts...)
}

func (c *Catalog) find(ctx context.Context, collectionName, index string, opts ...FindOption) ([]Record, error) {
	o := newFindOptions(opts...)
	col := c.cluster.Collection(c.scope, collectionName)

	if o.name != "" {
		r, err := fetchRecord(ctx, col, o.name)
		if err != nil {
			if errors.Is(err, couchbase.ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
		if !matchesAnnotations(r.Annotations, o.annotations) {
			return nil, nil
		}
		return []Record{r}, nil
	}

	var ids []string
	scores := map[string]float64{}
	if o.query != "" {
		if c.embedder == nil {
			return nil, errors.New("catalog: ByQuery requires a Catalog constructed with an Embedder")
		}
		vectors, err := c.embedder.Embed(ctx, o.query)
		if err != nil {
			return nil, fmt.Errorf("catalog: embed query: %w", err)
		}
		if len(vectors) == 0 {
			return nil, errors.New("catalog: embed query returned no vectors")
		}
		overfetch := o.limit * 4
		if overfetch < o.limit {
			overfetch = o.limit
		}
		hits, err := c.cluster.VectorSearch(ctx, c.scope, index, embeddingField, vectors[0], overfetch, nil)
		if err != nil {
			return nil, fmt.Errorf("catalog: vector search: %w", err)
		}
		for _, h := range hits {
			ids = append(ids, h.ID)
			scores[h.ID] = h.Score
		}
	} else {
		var err error
		ids, err = col.List(ctx, "")
		if err != nil {
			return nil, err
		}
	}

	records := make([]Record, 0, len(ids))
	for _, id := range ids {
		r, err := fetchRecord(ctx, col, id)
		if err != nil {
			continue
		}
		if !matchesAnnotations(r.Annotations, o.annotations) {
			continue
		}
		records = append(records, r)
	}

	if o.query != "" {
		sort.Slice(records, func(i, j int) bool { return scores[records[i].Name] > scores[records[j].Name] })
	} else {
		sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	}
	if o.limit > 0 && len(records) > o.limit {
		records = records[:o.limit]
	}
	return records, nil
}

func matchesAnnotations(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// Tools resolves matching catalog tools into ai.Tool definitions plus a
// single ai.ToolHandler that dispatches each call, by name, to the Go
// function this process registered for it via RegisterTool. Pass no
// FindOption to return every tool this process both knows about and has a
// catalog record for. Records with no local Handler (registered by some
// other process) are skipped, since there is nothing here to execute them.
func (c *Catalog) Tools(ctx context.Context, opts ...FindOption) ([]ai.Tool, ai.ToolHandler, error) {
	records, err := c.FindTools(ctx, opts...)
	if err != nil {
		return nil, nil, err
	}
	tools, handler := c.toolsFromRecords(records)
	return tools, handler, nil
}

// ResolveTools resolves the tools a prompt declares it depends on (see
// PromptSpec.Tools) into the same ai.Tool/ai.ToolHandler shape as Tools.
func (c *Catalog) ResolveTools(ctx context.Context, refs []ToolRef) ([]ai.Tool, ai.ToolHandler, error) {
	seen := map[string]bool{}
	var records []Record
	for _, ref := range refs {
		var opts []FindOption
		if ref.Name != "" {
			opts = append(opts, ByName(ref.Name))
		}
		if ref.Query != "" {
			opts = append(opts, ByQuery(ref.Query))
		}
		if len(ref.Annotations) > 0 {
			opts = append(opts, WithAnnotations(ref.Annotations))
		}
		if ref.Limit > 0 {
			opts = append(opts, Limit(ref.Limit))
		}
		found, err := c.FindTools(ctx, opts...)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range found {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			records = append(records, r)
		}
	}
	tools, handler := c.toolsFromRecords(records)
	return tools, handler, nil
}

func (c *Catalog) toolsFromRecords(records []Record) ([]ai.Tool, ai.ToolHandler) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tools := make([]ai.Tool, 0, len(records))
	handlers := make(map[string]ai.ToolHandler, len(records))
	for _, r := range records {
		spec, ok := c.tools[r.Name]
		if !ok || spec.Handler == nil {
			continue
		}
		tools = append(tools, ai.Tool{Name: r.Name, Description: r.Description, Properties: r.Parameters})
		handlers[r.Name] = spec.Handler
	}

	handler := func(ctx context.Context, call ai.ToolCall) ai.ToolResult {
		h, ok := handlers[call.Name]
		if !ok {
			return ai.ToolResult{ID: call.ID, Content: fmt.Sprintf("no local handler registered for tool %q", call.Name)}
		}
		return h(ctx, call)
	}
	return tools, handler
}
