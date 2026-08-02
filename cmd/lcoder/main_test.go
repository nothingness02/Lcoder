package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lcoder/lcoder/pkg/config"
	"github.com/lcoder/lcoder/pkg/permissions"
)

func TestParsePermissionConfig(t *testing.T) {
	pc := config.PermissionConfig{
		Rules: map[string]map[string]string{
			"bash": {
				"*":         "ask",
				"git *":     "allow",
				"go test *": "allow",
			},
		},
	}
	rules := parsePermissionConfig(pc)
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	engine := permissions.NewEngineFromRules(rules)
	dec := engine.Decide("bash", map[string]any{"command": "git status"})
	if dec != permissions.Allow {
		t.Fatalf("expected allow for git status, got %s", dec)
	}
	dec = engine.Decide("bash", map[string]any{"command": "rm -rf /"})
	if dec != permissions.Ask {
		t.Fatalf("expected ask for rm, got %s", dec)
	}
}

func TestLoadConfigOverride(t *testing.T) {
	dir, err := os.MkdirTemp("", "lcoder-config-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "lcoder.yaml")
	if err := os.WriteFile(path, []byte("model: gpt-4o\nprovider: openai\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfgFile = path
	cfg, err := loadConfig()
	cfgFile = ""
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Model != "gpt-4o" {
		t.Fatalf("expected gpt-4o, got %s", cfg.Model)
	}
	if cfg.Provider != "openai" {
		t.Fatalf("expected openai, got %s", cfg.Provider)
	}
}

func TestValidateRunModeFlags(t *testing.T) {
	cases := []struct {
		name    string
		goal    string
		prompt  string
		wantErr bool
	}{
		{"goal only", "fix bug", "", false},
		{"prompt only", "", "list files", false},
		{"neither (TUI)", "", "", false},
		{"both", "fix bug", "list files", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunModeFlags(tc.goal, tc.prompt)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for goal=%q prompt=%q, got nil", tc.goal, tc.prompt)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for goal=%q prompt=%q: %v", tc.goal, tc.prompt, err)
			}
		})
	}
}

