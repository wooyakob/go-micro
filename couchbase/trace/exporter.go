// Package trace stores OpenTelemetry spans in Couchbase, giving agent runs
// built on go-micro's existing instrumentation (agent/otel.go's
// agent.run / agent.model.call / agent.tool.call spans) a durable,
// queryable home — the same role agentc's activity auditor plays for
// Python agents, without needing a parallel span/log API: agent.go already
// emits the spans, this package just gives them a Couchbase-backed
// destination via the standard OpenTelemetry SDK exporter interface.
//
// Wire it up with:
//
//	exporter, _ := trace.NewExporter(cluster)
//	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter))
//	a := agent.New(..., agent.TraceProvider(tp))
package trace

import (
	"context"
	"encoding/json"
	"fmt"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go-micro.dev/v6/couchbase"
)

// Exporter implements go.opentelemetry.io/otel/sdk/trace.SpanExporter,
// persisting every finished span as a Record document in Couchbase.
type Exporter struct {
	cluster    couchbase.Cluster
	scope      string
	collection string
}

var _ sdktrace.SpanExporter = (*Exporter)(nil)

// NewExporter creates a Couchbase-backed span exporter and provisions its
// collection (scope, collection, primary index).
func NewExporter(cluster couchbase.Cluster, opts ...Option) (*Exporter, error) {
	o := newOptions(opts...)
	if err := cluster.EnsureScope(o.scope); err != nil {
		return nil, err
	}
	if err := cluster.EnsureCollection(o.scope, o.collection); err != nil {
		return nil, err
	}
	if err := cluster.EnsurePrimaryIndex(o.scope, o.collection); err != nil {
		return nil, err
	}
	return &Exporter{cluster: cluster, scope: o.scope, collection: o.collection}, nil
}

// ExportSpans persists a batch of finished spans.
func (e *Exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	col := e.cluster.Collection(e.scope, e.collection)
	for _, s := range spans {
		rec := recordFromSpan(s)
		raw, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("trace: marshal span %s: %w", rec.SpanID, err)
		}
		if err := col.Upsert(ctx, rec.ID, raw); err != nil {
			return fmt.Errorf("trace: export span %s: %w", rec.SpanID, err)
		}
	}
	return nil
}

// Shutdown is a no-op; Exporter holds no resources of its own beyond the
// shared Cluster, which callers own and close themselves.
func (e *Exporter) Shutdown(context.Context) error { return nil }

func recordFromSpan(s sdktrace.ReadOnlySpan) Record {
	sc := s.SpanContext()
	traceID, spanID := sc.TraceID().String(), sc.SpanID().String()

	var parentSpanID string
	if p := s.Parent(); p.IsValid() {
		parentSpanID = p.SpanID().String()
	}

	attrs := make(map[string]any, len(s.Attributes()))
	for _, kv := range s.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsInterface()
	}

	events := make([]Event, 0, len(s.Events()))
	for _, ev := range s.Events() {
		eventAttrs := make(map[string]any, len(ev.Attributes))
		for _, kv := range ev.Attributes {
			eventAttrs[string(kv.Key)] = kv.Value.AsInterface()
		}
		events = append(events, Event{Name: ev.Name, Time: ev.Time, Attributes: eventAttrs})
	}

	status := s.Status()
	return Record{
		ID:            traceID + "/" + spanID,
		TraceID:       traceID,
		SpanID:        spanID,
		ParentSpanID:  parentSpanID,
		Name:          s.Name(),
		StartTime:     s.StartTime(),
		EndTime:       s.EndTime(),
		Attributes:    attrs,
		Events:        events,
		StatusCode:    status.Code.String(),
		StatusMessage: status.Description,
	}
}
