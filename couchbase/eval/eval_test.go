package eval

import (
	"context"
	"errors"
	"testing"

	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase/couchbasetest"
)

func exactMatch(_ context.Context, c Case, output string) (float64, string, error) {
	if output == c.Expected {
		return 1, "exact match", nil
	}
	return 0, "mismatch", nil
}

func TestRunnerScoresCases(t *testing.T) {
	target := func(_ context.Context, input string) (string, error) {
		return input + "!", nil
	}
	cases := []Case{
		{Name: "greet", Input: "hi", Expected: "hi!", Check: exactMatch},
		{Name: "wrong", Input: "bye", Expected: "nope", Check: exactMatch},
	}

	r := NewRunner()
	report, err := r.Run(context.Background(), "greetings", target, cases)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(report.Results))
	}
	if report.Results[0].Score != 1 {
		t.Errorf("case 0 score = %v, want 1", report.Results[0].Score)
	}
	if report.Results[1].Score != 0 {
		t.Errorf("case 1 score = %v, want 0", report.Results[1].Score)
	}
	if report.Mean != 0.5 {
		t.Errorf("Mean = %v, want 0.5", report.Mean)
	}
}

func TestRunnerRecordsTargetError(t *testing.T) {
	target := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("boom")
	}
	r := NewRunner()
	report, err := r.Run(context.Background(), "failing", target, []Case{{Name: "x", Input: "in"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Results[0].Err == "" {
		t.Error("expected the target error to be recorded")
	}
	if report.Results[0].Score != 0 {
		t.Errorf("expected zero score on error, got %v", report.Results[0].Score)
	}
}

func TestRunnerPersistsAndHistory(t *testing.T) {
	ctx := context.Background()
	cluster := couchbasetest.New("agents")
	store, err := NewStore(cluster)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	target := func(_ context.Context, input string) (string, error) { return input, nil }
	r := NewRunner(WithStore(store))

	for i := 0; i < 3; i++ {
		if _, err := r.Run(ctx, "regression", target, []Case{{Name: "c", Input: "x", Expected: "x", Check: exactMatch}}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	history, err := store.History(ctx, "regression", 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries (limit), got %d", len(history))
	}
	for _, h := range history {
		if h.Mean != 1 {
			t.Errorf("expected Mean 1 for a matching case, got %v", h.Mean)
		}
	}
}

type fakeJudgeModel struct{ reply string }

func (m fakeJudgeModel) Init(...ai.Option) error { return nil }
func (m fakeJudgeModel) Options() ai.Options     { return ai.Options{} }
func (m fakeJudgeModel) String() string          { return "fake" }
func (m fakeJudgeModel) Stream(context.Context, *ai.Request, ...ai.GenerateOption) (ai.Stream, error) {
	return nil, ai.ErrStreamingUnsupported
}

func (m fakeJudgeModel) Generate(context.Context, *ai.Request, ...ai.GenerateOption) (*ai.Response, error) {
	return &ai.Response{Reply: m.reply}, nil
}

func TestJudgeParsesModelVerdict(t *testing.T) {
	model := fakeJudgeModel{reply: "Sure, here you go:\n```json\n{\"score\": 0.75, \"reason\": \"mostly right\"}\n```"}
	check := Judge(model, "Answer must be polite and correct")

	score, detail, err := check(context.Background(), Case{Input: "hi", Expected: "hello"}, "hello there")
	if err != nil {
		t.Fatalf("Judge check: %v", err)
	}
	if score != 0.75 {
		t.Errorf("score = %v, want 0.75", score)
	}
	if detail != "mostly right" {
		t.Errorf("detail = %q", detail)
	}
}
