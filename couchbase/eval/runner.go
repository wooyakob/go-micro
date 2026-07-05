package eval

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// Runner executes a suite of Cases against a Target and scores each result.
type Runner struct {
	tracer oteltrace.Tracer
	store  *Store
}

// Option configures a Runner.
type Option func(*Runner)

// WithTracer emits an "eval.case" span per case, tagged with eval.suite,
// eval.case and eval.score. Pass the same TracerProvider's tracer an agent
// uses (see couchbase/trace) to land eval runs in the same Couchbase trace
// store as agent runs.
func WithTracer(t oteltrace.Tracer) Option {
	return func(r *Runner) { r.tracer = t }
}

// WithStore persists every Run's Report to Couchbase for regression history.
func WithStore(s *Store) Option {
	return func(r *Runner) { r.store = s }
}

// NewRunner creates a Runner.
func NewRunner(opts ...Option) *Runner {
	r := &Runner{}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run executes every case in cases against target, scores each, and returns
// a Report. If the Runner was built WithStore, the report is also
// persisted.
func (r *Runner) Run(ctx context.Context, suite string, target Target, cases []Case) (*Report, error) {
	report := &Report{Suite: suite, RunAt: time.Now().UTC(), Results: make([]Result, 0, len(cases))}

	var total float64
	for _, c := range cases {
		result := r.runCase(ctx, suite, target, c)
		report.Results = append(report.Results, result)
		total += result.Score
	}
	if len(cases) > 0 {
		report.Mean = total / float64(len(cases))
	}

	if r.store != nil {
		if err := r.store.Save(ctx, *report); err != nil {
			return report, fmt.Errorf("eval: save report: %w", err)
		}
	}
	return report, nil
}

func (r *Runner) runCase(ctx context.Context, suite string, target Target, c Case) Result {
	start := time.Now()

	var span oteltrace.Span
	if r.tracer != nil {
		ctx, span = r.tracer.Start(ctx, "eval.case", oteltrace.WithAttributes(
			attribute.String("eval.suite", suite),
			attribute.String("eval.case", c.Name),
		))
		defer span.End()
	}

	output, err := target(ctx, c.Input)
	if err != nil {
		if span != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		return Result{Case: c.Name, Output: output, Err: err.Error(), Duration: time.Since(start)}
	}

	var score float64
	var detail string
	if c.Check != nil {
		score, detail, err = c.Check(ctx, c, output)
		if err != nil {
			if span != nil {
				span.SetStatus(codes.Error, err.Error())
			}
			return Result{Case: c.Name, Output: output, Err: err.Error(), Duration: time.Since(start)}
		}
	}

	if span != nil {
		span.SetAttributes(attribute.Float64("eval.score", score))
		if detail != "" {
			span.SetAttributes(attribute.String("eval.detail", detail))
		}
	}
	return Result{Case: c.Name, Score: score, Detail: detail, Output: output, Duration: time.Since(start)}
}
