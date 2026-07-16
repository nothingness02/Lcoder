// pkg/llm/provider/adapter_test.go
package provider

import "testing"

func TestDefaultBaseURL(t *testing.T) {
	cases := map[string]string{
		"openai":     "https://api.openai.com/v1",
		"deepseek":   "https://api.deepseek.com/v1",
		"moonshot":   "https://api.moonshot.cn/v1",
		"kimi-code":  "https://api.kimi.com/coding/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai",
		"anthropic":  "https://api.anthropic.com/v1",
	}
	for route, want := range cases {
		if got := DefaultBaseURL(route); got != want {
			t.Errorf("DefaultBaseURL(%q)=%q, want %q", route, got, want)
		}
	}
	if got := DefaultBaseURL("unknown"); got != "" {
		t.Errorf("DefaultBaseURL(unknown)=%q, want empty", got)
	}
}

func TestEventKindString(t *testing.T) {
	if KindTextDelta.String() != "text_delta" {
		t.Errorf("KindTextDelta=%q", KindTextDelta.String())
	}
}
