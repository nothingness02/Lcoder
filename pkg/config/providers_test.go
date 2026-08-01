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
			Route:   "openai",
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
	if out["myrelay"].Route != "openai" {
		t.Errorf("myrelay route = %q, want openai", out["myrelay"].Route)
	}
	if out["myrelay"].Headers["X-Title"] != "sk-moon" {
		t.Errorf("myrelay header not interpolated: %q", out["myrelay"].Headers["X-Title"])
	}
	// Input must not be mutated.
	if in["moonshot"].APIKey != "{env:MOONSHOT_API_KEY}" {
		t.Errorf("input was mutated: %q", in["moonshot"].APIKey)
	}
}
