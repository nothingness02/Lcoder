// cmd/cache-eval/main.go
//
// Evaluate how well the current context-management system gets cache hits
// when the same prefix (system prompt + memory + project docs) is reused
// across turns. Uses the Kimi / Moonshot OpenAI-compatible endpoint by
// default and writes the result as Markdown.
//
// Run:
//
//	go run ./cmd/cache-eval -provider moonshot -model kimi-code -o cache_report.md
//
// The program loads ~/.lcoder/config.yaml + credentials.yaml. If the provider
// key is not configured, it falls back to the MOONSHOT_API_KEY / KIMI_API_KEY
// environment variable.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lcoder/lcoder/pkg/agentsetup"
	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/llm"
	"github.com/lcoder/lcoder/pkg/llm/catalog"
	"github.com/lcoder/lcoder/pkg/llm/engine"
	"github.com/lcoder/lcoder/pkg/llm/provider"
	"github.com/lcoder/lcoder/pkg/models"
)

func main() {
	providerName := flag.String("provider", "moonshot", "provider name (moonshot, openai, deepseek, anthropic, ...)")
	modelID := flag.String("model", "", "model id (defaults to config model or env/model-specific fallback)")
	apiKeyFlag := flag.String("api-key", "", "API key (default: config or env)")
	baseURLFlag := flag.String("base-url", "", "provider base URL (default: config or env)")
	turns := flag.Int("turns", 3, "number of turns to evaluate")
	output := flag.String("o", "", "output markdown file (default stdout)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to load config: %v\n", err)
		cfg = config.DefaultConfig()
	}

	if *modelID == "" {
		if cfg.Provider == *providerName && cfg.Model != "" {
			*modelID = cfg.Model
		} else if *providerName == "anthropic" {
			*modelID = envOr("ANTHROPIC_MODEL", "kimi-code")
		} else {
			*modelID = "kimi-code"
		}
	}

	apiKey := *apiKeyFlag
	if apiKey == "" {
		apiKey = providerAPIKey(cfg, *providerName)
	}
	if apiKey == "" {
		fatal("no API key found for provider %q (set it in ~/.lcoder/config.yaml providers.%s.api_key, -api-key, or the provider-specific env var)", *providerName, *providerName)
	}

	baseURL := *baseURLFlag
	if baseURL == "" {
		baseURL = providerBaseURL(cfg, *providerName)
	}

	route := providerRoute(cfg, *providerName)

	cat := catalog.New(catalog.Options{})
	eng := engine.New(cat)
	eng.RegisterProvider(*providerName, provider.Conn{APIKey: apiKey, Route: route, BaseURL: baseURL})
	client := llm.NewClient(eng)

	mgr := buildContextManager()

	questions := []string{
		"What is the capital of France?",
		"Now multiply 13 by 17.",
		"Summarize the project conventions above in one sentence.",
		"What is 2^10?",
		"Translate 'hello' to Chinese.",
	}
	if *turns > len(questions) {
		*turns = len(questions)
	}

	var results []turnResult
	for i := 0; i < *turns; i++ {
		mgr.AppendRecent(models.NewAgentMessage(models.RoleUser, models.TextContent{Text: questions[i]}))

		req, err := mgr.BuildTurnRequest(models.ModelRef{Provider: *providerName, ID: *modelID}, nil)
		if err != nil {
			fatal("build turn %d: %v", i+1, err)
		}

		usage, assistantText, err := runTurn(context.Background(), client, req)
		if err != nil {
			fatal("turn %d: %v", i+1, err)
		}

		results = append(results, turnResult{
			Turn:      i + 1,
			Question:  questions[i],
			Usage:     usage,
			Assistant: assistantText,
		})

		if usage != nil {
			mgr.AppendRecent(models.NewAgentMessage(models.RoleAssistant, models.TextContent{Text: assistantText}))
		}
	}

	md := renderMarkdown(*providerName, *modelID, mgr, results)
	if *output != "" {
		if err := os.WriteFile(*output, []byte(md), 0644); err != nil {
			fatal("write output: %v", err)
		}
		fmt.Printf("Wrote report to %s\n", *output)
	} else {
		fmt.Println(md)
	}
}

func providerAPIKey(cfg config.Config, name string) string {
	if conn, ok := cfg.Providers[name]; ok {
		if conn.APIKey != "" {
			return conn.APIKey
		}
	}
	envs := []string{strings.ToUpper(name) + "_API_KEY"}
	switch name {
	case "anthropic":
		envs = append(envs, "ANTHROPIC_AUTH_TOKEN")
	case "openai":
		// Kimi Code is exposed through an OpenAI-compatible endpoint but the key is
		// often present in the ANTHROPIC_* env vars used by Claude Code.
		envs = append(envs, "OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	case "moonshot":
		envs = append(envs, "KIMI_API_KEY", "MOONSHOT_API_KEY")
	default:
		envs = append(envs, "KIMI_API_KEY", "MOONSHOT_API_KEY")
	}
	for _, env := range envs {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

func providerBaseURL(cfg config.Config, name string) string {
	if conn, ok := cfg.Providers[name]; ok {
		if conn.BaseURL != "" {
			return conn.BaseURL
		}
	}
	switch name {
	case "anthropic":
		return os.Getenv("ANTHROPIC_BASE_URL")
	case "openai":
		if v := os.Getenv("OPENAI_BASE_URL"); v != "" {
			return v
		}
		v := os.Getenv("ANTHROPIC_BASE_URL")
		// Claude Code's Kimi Code setting exposes https://api.kimi.com/coding/,
		// but the OpenAI-compatible completions live under .../coding/v1.
		if strings.HasSuffix(v, "/coding/") {
			v += "v1"
		}
		return v
	}
	return ""
}

func providerRoute(cfg config.Config, name string) string {
	if conn, ok := cfg.Providers[name]; ok {
		if conn.Route != "" {
			return conn.Route
		}
	}
	if name == "anthropic" {
		return "anthropic"
	}
	return name
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func buildContextManager() *contextmgr.Manager {
	mgr := contextmgr.NewManager(contextmgr.TokenBudget{
		MaxTotal:      128000,
		TargetTotal:   120000,
		ReserveOutput: 8192,
		MaxOutput:     4096,
	},
		contextmgr.WithCacheHintPolicy(contextmgr.CachePolicyDefault),
		contextmgr.WithWindowPolicy(contextmgr.NewKeepRecentInBudget(5)),
	)

	sysText := agentsetup.BuildSystemPrompt()
	if sysText == "" {
		sysText = "You are Lcoder, an expert software engineering agent."
	}
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockSystem, "system", contextmgr.StabilityStatic, 100,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: sysText})))

	projectDocs := "Project conventions:\n- Use Go modules and keep functions focused.\n- Prefer parallel tool calls when operations are independent.\n- Write tests for every behavior change.\n- Never commit secrets."
	mgr.SetBlock(contextmgr.NewBlock(contextmgr.BlockProjectDocs, "project_docs", contextmgr.StabilityStable, 80,
		models.NewAgentMessage(models.RoleSystem, models.TextContent{Text: projectDocs})))

	return mgr
}

func runTurn(ctx context.Context, client *llm.Client, req models.TurnRequest) (*models.LLMUsage, string, error) {
	stream, err := client.StreamTurn(ctx, req)
	if err != nil {
		return nil, "", err
	}
	var text strings.Builder
	var usage *models.LLMUsage
	for ev := range stream {
		switch ev.Kind {
		case provider.KindError:
			return nil, "", ev.Err
		case provider.KindTextDelta:
			text.WriteString(ev.Delta)
		case provider.KindDone:
			if ev.Usage != nil {
				usage = ev.Usage
			}
		}
	}
	return usage, strings.TrimSpace(text.String()), nil
}

type turnResult struct {
	Turn      int
	Question  string
	Usage     *models.LLMUsage
	Assistant string
}

func renderMarkdown(provider, model string, mgr *contextmgr.Manager, results []turnResult) string {
	sysBlock, _ := mgr.GetBlock(contextmgr.BlockSystem, "system")
	docBlock, _ := mgr.GetBlock(contextmgr.BlockProjectDocs, "project_docs")

	var b strings.Builder
	fmt.Fprint(&b, "# Context Manager Cache Evaluation\n\n")
	fmt.Fprintf(&b, "- **Provider**: %s\n", provider)
	fmt.Fprintf(&b, "- **Model**: %s\n", model)
	fmt.Fprintf(&b, "- **System prompt chars**: %d\n", len(sysBlock.Text()))
	fmt.Fprintf(&b, "- **Project docs chars**: %d\n", len(docBlock.Text()))
	fmt.Fprintf(&b, "- **Turns evaluated**: %d\n\n", len(results))

	b.WriteString("## Per-turn usage\n\n")
	b.WriteString("| Turn | Prompt tokens | Completion tokens | Cache read/hit | Cache write/miss | Hit rate | Cost (USD) |\n")
	b.WriteString("|------|--------------:|------------------:|---------------:|-----------------:|---------:|-----------:|\n")

	var totalPrompt, totalCacheRead, totalCacheWrite int
	for _, r := range results {
		u := r.Usage
		if u == nil {
			fmt.Fprintf(&b, "| %d | N/A | N/A | N/A | N/A | N/A | N/A |\n", r.Turn)
			continue
		}
		hitRate := 0.0
		if u.PromptTokens > 0 {
			hitRate = float64(u.CacheReadTokens) * 100.0 / float64(u.PromptTokens)
		}
		fmt.Fprintf(&b,
			"| %d | %d | %d | %d | %d | %.1f%% | $%.6f |\n",
			r.Turn,
			u.PromptTokens,
			u.CompletionTokens,
			u.CacheReadTokens,
			u.CacheWriteTokens,
			hitRate,
			u.TotalCost,
		)
		totalPrompt += u.PromptTokens
		totalCacheRead += u.CacheReadTokens
		totalCacheWrite += u.CacheWriteTokens
	}

	b.WriteString("\n## Summary\n\n")
	if totalPrompt > 0 {
		overall := float64(totalCacheRead) * 100.0 / float64(totalPrompt)
		fmt.Fprintf(&b, "- **Overall cache hit rate**: %.1f%%\n", overall)
		fmt.Fprintf(&b, "- **Total cache read/hit tokens**: %d\n", totalCacheRead)
		fmt.Fprintf(&b, "- **Total cache write/miss tokens**: %d\n", totalCacheWrite)
	} else {
		b.WriteString("- No usage data returned by the provider.\n")
	}

	b.WriteString("\n## Raw assistant responses\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "### Turn %d\n\n", r.Turn)
		fmt.Fprintf(&b, "**User**: %s\n\n", r.Question)
		if r.Assistant == "" {
			b.WriteString("**Assistant**: *(no text)*\n\n")
		} else {
			fmt.Fprintf(&b, "**Assistant**: %s\n\n", r.Assistant)
		}
	}

	return b.String()
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
