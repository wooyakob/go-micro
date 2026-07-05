// Couchbase Agent Harness — tools, prompts, traces, memory, and evals, all
// backed by Couchbase.
//
// This example builds a small "concierge" agent whose entire harness lives
// in one Couchbase bucket:
//
//   - a get_weather tool and a concierge prompt, versioned in the Catalog
//     (couchbase/catalog) and semantically searchable once embedded;
//   - conversation memory (couchbase/memory), so the agent picks up where
//     it left off across restarts, with long-term semantic recall;
//   - every run's spans exported to Couchbase (couchbase/trace) through
//     the exact agent.run / agent.tool.call instrumentation go-micro's
//     agent package already emits;
//   - the LLM and its embeddings served by Couchbase's hosted Model
//     Service (couchbase/model), an OpenAI-compatible endpoint.
//
// Run (needs a reachable Couchbase cluster and Model Service endpoint):
//
//	export COUCHBASE_CONNSTR=couchbases://cb.xxxxx.cloud.couchbase.com
//	export COUCHBASE_USERNAME=agent-harness
//	export COUCHBASE_PASSWORD=...
//	export COUCHBASE_BUCKET=agents
//	export MODEL_SERVICE_URL=https://xxxxx.model-service.cloud.couchbase.com/v1
//	export MODEL_SERVICE_KEY=...   # API key or Capella JWT
//	go run .
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go-micro.dev/v6/agent"
	"go-micro.dev/v6/ai"
	"go-micro.dev/v6/couchbase"
	"go-micro.dev/v6/couchbase/catalog"
	"go-micro.dev/v6/couchbase/eval"
	"go-micro.dev/v6/couchbase/harness"
	"go-micro.dev/v6/couchbase/model"
)

func main() {
	cfg, ok := loadConfig()
	if !ok {
		return
	}

	embedder := model.NewEmbedder(
		model.WithBaseURL(cfg.modelServiceURL),
		model.WithAPIKey(cfg.modelServiceKey),
		model.WithEmbeddingModel(cfg.embeddingModel),
	)

	h, err := harness.New(
		harness.WithConnect(
			couchbase.ConnectionString(cfg.connStr),
			couchbase.Credentials(cfg.username, cfg.password),
			couchbase.Bucket(cfg.bucket),
			couchbase.WanDevelopmentProfile(),
		),
		harness.WithEmbedder(embedder),
	)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	if err := h.Catalog.RegisterTool(catalog.ToolSpec{
		Name:        "get_weather",
		Description: "Look up the current weather forecast for a city",
		Parameters: map[string]any{
			"city": map[string]any{"type": "string", "description": "City name"},
		},
		Handler: handleWeather,
	}); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	if err := h.Catalog.RegisterPrompt(catalog.PromptSpec{
		Name:        "concierge",
		Description: "System prompt for the hotel concierge agent",
		Content:     "You are a friendly hotel concierge. Use the get_weather tool when guests ask about the weather.",
		Tools:       []catalog.ToolRef{{Name: "get_weather"}},
	}); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	if err := h.Sync(ctx); err != nil {
		fmt.Println("error syncing catalog:", err)
		os.Exit(1)
	}
	fmt.Println("Catalog synced: get_weather tool and concierge prompt are now versioned in Couchbase.")

	prompt, err := h.Catalog.FindPrompt(ctx, catalog.ByName("concierge"))
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	a, err := h.NewAgent(ctx, "concierge",
		agent.Provider("couchbase"),
		agent.BaseURL(cfg.modelServiceURL),
		agent.APIKey(cfg.modelServiceKey),
		agent.Model(cfg.chatModel),
		agent.Prompt(prompt.Content),
		agent.Address("127.0.0.1:0"),
	)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}

	resp, err := a.Ask(ctx, "What's the weather like in Austin today?")
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("\n--- concierge reply ---")
	fmt.Println(resp.Reply)
	fmt.Println("\nEvery span from this run is now queryable in Couchbase — see couchbase/trace.Store.")

	runEval(ctx, h, a)
}

// runEval scores the concierge's replies against a tiny suite and persists
// the report to Couchbase (agent_evals.reports), so repeating this after a
// prompt or model change gives a regression history via
// h.EvalStore.History.
func runEval(ctx context.Context, h *harness.Harness, a agent.Agent) {
	runner := h.EvalRunner()
	target := func(ctx context.Context, input string) (string, error) {
		resp, err := a.Ask(ctx, input)
		if err != nil {
			return "", err
		}
		return resp.Reply, nil
	}

	report, err := runner.Run(ctx, "concierge-weather", target, []eval.Case{
		{
			Name:  "mentions-weather",
			Input: "What's the weather in Austin?",
			Check: func(_ context.Context, _ eval.Case, output string) (float64, string, error) {
				if strings.Contains(strings.ToLower(output), "austin") {
					return 1, "mentions the requested city", nil
				}
				return 0, "did not mention the requested city", nil
			},
		},
	})
	if err != nil {
		fmt.Println("eval error:", err)
		return
	}
	fmt.Printf("\n--- eval report (mean score %.2f, persisted to Couchbase) ---\n", report.Mean)
	for _, r := range report.Results {
		fmt.Printf("  %s: score=%.2f (%s)\n", r.Case, r.Score, r.Detail)
	}
}

func handleWeather(_ context.Context, call ai.ToolCall) ai.ToolResult {
	var args struct {
		City string `json:"city"`
	}
	_ = call.Scan(&args)
	return ai.ToolResult{ID: call.ID, Content: fmt.Sprintf("%s: 72F and sunny", args.City)}
}

type config struct {
	connStr, username, password, bucket string
	modelServiceURL, modelServiceKey    string
	chatModel, embeddingModel           string
}

func loadConfig() (config, bool) {
	cfg := config{
		connStr:         os.Getenv("COUCHBASE_CONNSTR"),
		username:        os.Getenv("COUCHBASE_USERNAME"),
		password:        os.Getenv("COUCHBASE_PASSWORD"),
		bucket:          os.Getenv("COUCHBASE_BUCKET"),
		modelServiceURL: os.Getenv("MODEL_SERVICE_URL"),
		modelServiceKey: os.Getenv("MODEL_SERVICE_KEY"),
		chatModel:       envOr("MODEL_SERVICE_CHAT_MODEL", "chat-v1"),
		embeddingModel:  envOr("MODEL_SERVICE_EMBED_MODEL", "embed-v1"),
	}
	if cfg.connStr == "" || cfg.bucket == "" || cfg.modelServiceURL == "" || cfg.modelServiceKey == "" {
		fmt.Println("Missing configuration. Set at least:")
		fmt.Println("  COUCHBASE_CONNSTR, COUCHBASE_USERNAME, COUCHBASE_PASSWORD, COUCHBASE_BUCKET")
		fmt.Println("  MODEL_SERVICE_URL, MODEL_SERVICE_KEY")
		fmt.Println("then: go run .")
		return cfg, false
	}
	return cfg, true
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
