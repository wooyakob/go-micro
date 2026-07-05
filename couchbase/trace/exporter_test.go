package trace

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"go-micro.dev/v6/couchbase/couchbasetest"
)

func TestExporterAndStore(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")

	exporter, err := NewExporter(cluster)
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(ctx) }()

	tracer := tp.Tracer("test")
	var traceID string
	func() {
		ctx, root := tracer.Start(ctx, "agent.run")
		defer root.End()
		root.SetAttributes(attribute.String("agent.name", "concierge"))
		traceID = root.SpanContext().TraceID().String()

		_, child := tracer.Start(ctx, "agent.tool.call")
		child.SetAttributes(attribute.String("agent.tool.name", "get_weather"))
		child.AddEvent("started")
		child.SetStatus(codes.Ok, "")
		child.End()
	}()

	store := NewStore(cluster)
	records, err := store.Trace(ctx, traceID)
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(records))
	}
	if records[0].Name != "agent.run" {
		t.Errorf("first span = %q, want agent.run", records[0].Name)
	}
	if records[1].Name != "agent.tool.call" {
		t.Errorf("second span = %q, want agent.tool.call", records[1].Name)
	}
	if records[1].ParentSpanID != records[0].SpanID {
		t.Errorf("child span's parent = %q, want %q", records[1].ParentSpanID, records[0].SpanID)
	}
	if got := records[0].Attributes["agent.name"]; got != "concierge" {
		t.Errorf("agent.name attribute = %v", got)
	}
	if len(records[1].Events) != 1 || records[1].Events[0].Name != "started" {
		t.Errorf("expected one 'started' event, got %+v", records[1].Events)
	}

	recent, err := store.Recent(ctx, 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent span, got %d", len(recent))
	}
}
