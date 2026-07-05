package trace

import "time"

// Event is a timestamped occurrence recorded within a span's lifetime
// (mirrors go.opentelemetry.io/otel/sdk/trace.Event in a JSON-friendly shape).
type Event struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Record is the persisted, queryable form of one OpenTelemetry span. Its ID
// is "<trace_id>/<span_id>", which lets Store.Trace fetch every span in a
// run with a single prefix List instead of a N1QL query — the same trick
// agentc's Log.span.session grouping achieves via a dedicated Analytics
// view, done here with a key layout instead.
type Record struct {
	ID            string         `json:"id"`
	TraceID       string         `json:"trace_id"`
	SpanID        string         `json:"span_id"`
	ParentSpanID  string         `json:"parent_span_id,omitempty"`
	Name          string         `json:"name"`
	StartTime     time.Time      `json:"start_time"`
	EndTime       time.Time      `json:"end_time"`
	Attributes    map[string]any `json:"attributes,omitempty"`
	Events        []Event        `json:"events,omitempty"`
	StatusCode    string         `json:"status_code,omitempty"`
	StatusMessage string         `json:"status_message,omitempty"`
}
