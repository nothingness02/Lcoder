package config

import "testing"

func TestExpandEnvRefs(t *testing.T) {
	t.Setenv("MY_KEY", "secret-123")
	cases := []struct {
		in   string
		want string
	}{
		{"{env:MY_KEY}", "secret-123"},
		{"Bearer {env:MY_KEY}", "Bearer secret-123"},
		{"literal", "literal"},
		{"{env:MISSING_VAR}", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := expandEnvRefs(c.in); got != c.want {
			t.Errorf("expandEnvRefs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveProviders(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "sk-moon")
	in := map[string]ProviderConn{
		"moonshot": {
			BaseURL: "https://api.moonshot.cn/v1",
			APIKey:  "{env:MOONSHOT_API_KEY}",
		},
		"myrelay": {
			BaseURL: "https://api.relay.com/v1",
			APIKey:  "{env:MOONSHOT_API_KEY}",
			Headers: map[string]string{"X-Title": "{env:MOONSHOT_API_KEY}"},
		},
	}
	out, _ := resolveProviders(in)

	if out["moonshot"].APIKey != "sk-moon" {
		t.Errorf("moonshot api_key = %q, want sk-moon", out["moonshot"].APIKey)
	}
	if out["moonshot"].BaseURL != "https://api.moonshot.cn/v1" {
		t.Errorf("moonshot base_url not preserved: %q", out["moonshot"].BaseURL)
	}
	if out["myrelay"].Headers["X-Title"] != "sk-moon" {
		t.Errorf("myrelay header not interpolated: %q", out["myrelay"].Headers["X-Title"])
	}
	// Input must not be mutated.
	if in["moonshot"].APIKey != "{env:MOONSHOT_API_KEY}" {
		t.Errorf("input was mutated: %q", in["moonshot"].APIKey)
	}
}

// 回归:resolveProviders 曾用命名字段字面量重建 ProviderConn,把
// APIKeys/Protocol/MaxConcurrent 静默置零——文档化配置路径上三个功能全灭。
func TestResolveProvidersPreservesResilienceFields(t *testing.T) {
	t.Setenv("KEY_A", "ka")
	t.Setenv("KEY_B", "kb")
	out, missing := resolveProviders(map[string]ProviderConn{
		"multi": {
			BaseURL:       "https://api.example.com/v1",
			APIKeys:       []string{"{env:KEY_A}", "{env:KEY_B}"},
			Protocol:      "anthropic",
			MaxConcurrent: 4,
		},
	})
	if len(missing) != 0 {
		t.Fatalf("unexpected missing env: %v", missing)
	}
	got := out["multi"]
	if got.Protocol != "anthropic" || got.MaxConcurrent != 4 {
		t.Fatalf("protocol/max_concurrent dropped: %+v", got)
	}
	if len(got.APIKeys) != 2 || got.APIKeys[0] != "ka" || got.APIKeys[1] != "kb" {
		t.Fatalf("api_keys not expanded: %v", got.APIKeys)
	}
}
