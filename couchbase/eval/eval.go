// Package eval provides a small evaluation harness for agents built on this
// module: define input/expected cases, run them against a target (typically
// an agent's Ask method), score each with a Check function — deterministic
// or an LLM-as-judge via Judge — and persist the results to Couchbase for
// regression history.
//
// This mirrors what agentc's example apps do by hand with the Span/KeyValue
// logging API plus Ragas as an LLM judge (see agentc's
// examples/with_langgraph/evals and examples/ragas_evaluation): a suite of
// cases, a score per case, and a durable record of every run. Here that
// pattern is a first-class, reusable type instead of bespoke pytest glue.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go-micro.dev/v6/ai"
)

// Target produces the output to evaluate for a given input, typically
// agent.Agent.Ask reduced to its answer string.
type Target func(ctx context.Context, input string) (string, error)

// CheckFunc scores a case's actual output on a 0.0-1.0 scale, returning a
// human-readable detail string explaining the score.
type CheckFunc func(ctx context.Context, c Case, output string) (score float64, detail string, err error)

// Case is a single evaluation input and how to score it.
type Case struct {
	Name     string
	Input    string
	Expected string // optional reference answer; meaning is up to Check
	Check    CheckFunc
}

// Result is one Case's outcome.
type Result struct {
	Case     string        `json:"case"`
	Score    float64       `json:"score"`
	Detail   string        `json:"detail,omitempty"`
	Output   string        `json:"output,omitempty"`
	Err      string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Report is the outcome of one Runner.Run call over a suite of cases.
type Report struct {
	Suite   string    `json:"suite"`
	Results []Result  `json:"results"`
	Mean    float64   `json:"mean"`
	RunAt   time.Time `json:"run_at"`
}

// Judge builds a CheckFunc that asks model to grade a case's output against
// rubric, the LLM-as-judge pattern agentc's example apps build on Ragas.
// The model must reply with a JSON object shaped like
// {"score": 0.0-1.0, "reason": "..."}; surrounding prose or a markdown code
// fence around the JSON is tolerated.
func Judge(model ai.Model, rubric string) CheckFunc {
	return func(ctx context.Context, c Case, output string) (float64, string, error) {
		prompt := fmt.Sprintf(
			"You are grading an AI system's output against a rubric.\n\n"+
				"Rubric: %s\n\nInput: %s\nExpected: %s\nActual output: %s\n\n"+
				"Score the actual output from 0.0 (fails the rubric) to 1.0 (fully satisfies it). "+
				"Reply with only a JSON object: {\"score\": <number>, \"reason\": \"<short reason>\"}",
			rubric, c.Input, c.Expected, output,
		)
		resp, err := model.Generate(ctx, &ai.Request{Prompt: prompt})
		if err != nil {
			return 0, "", fmt.Errorf("eval: judge model call: %w", err)
		}
		text := resp.Reply
		if resp.Answer != "" {
			text = resp.Answer
		}
		var verdict struct {
			Score  float64 `json:"score"`
			Reason string  `json:"reason"`
		}
		if err := json.Unmarshal([]byte(extractJSON(text)), &verdict); err != nil {
			return 0, "", fmt.Errorf("eval: parse judge response %q: %w", text, err)
		}
		return verdict.Score, verdict.Reason, nil
	}
}

// extractJSON returns the substring between the first '{' and the last '}'
// in s, tolerating a model wrapping its JSON reply in prose or a markdown
// code fence.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start == -1 || end == -1 || end < start {
		return s
	}
	return s[start : end+1]
}
