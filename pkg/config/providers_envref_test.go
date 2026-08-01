package config

import "testing"

func TestResolveProvidersReportsMissingEnvRefs(t *testing.T) {
	t.Setenv("PRESENT_KEY", "sk-123")
	in := map[string]ProviderConn{
		"moonshot": {APIKey: "{env:MISSING_KEY}"},
		"openai":   {APIKey: "{env:PRESENT_KEY}"},
		"relay":    {BaseURL: "https://{env:MISSING_HOST}/v1", APIKey: "plain"},
	}
	out, missing := resolveProviders(in)

	if out["openai"].APIKey != "sk-123" {
		t.Fatalf("set var must interpolate, got %q", out["openai"].APIKey)
	}
	if out["moonshot"].APIKey != "" {
		t.Fatalf("missing var currently interpolates to empty, got %q", out["moonshot"].APIKey)
	}
	want := map[string]bool{"moonshot:MISSING_KEY": true, "relay:MISSING_HOST": true}
	for _, m := range missing {
		if !want[m] {
			t.Fatalf("unexpected missing ref %q", m)
		}
		delete(want, m)
	}
	if len(want) > 0 {
		t.Fatalf("missing refs not reported: %v", want)
	}
}
