package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentMarkdown(t *testing.T) {
	input := `---
name: worker
description: A worker agent
model: gpt-4o-mini
provider: openai
mode: code
timeout: 60
---
You are a focused implementer.
`
	agent, err := parseAgentMarkdown("worker.md", []byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if agent.Name != "worker" {
		t.Errorf("name = %q, want worker", agent.Name)
	}
	if agent.Description != "A worker agent" {
		t.Errorf("description = %q, want A worker agent", agent.Description)
	}
	if agent.Model != "gpt-4o-mini" {
		t.Errorf("model = %q", agent.Model)
	}
	if agent.Provider != "openai" {
		t.Errorf("provider = %q", agent.Provider)
	}
	if agent.Mode != "code" {
		t.Errorf("mode = %q", agent.Mode)
	}
	if agent.Timeout != 60 {
		t.Errorf("timeout = %d", agent.Timeout)
	}
	if !strings.Contains(agent.Prompt, "focused implementer") {
		t.Errorf("prompt missing body: %q", agent.Prompt)
	}
}

func TestParseAgentMarkdownCases(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Agent
		wantErr string
	}{
		{
			name:  "default mode and timeout",
			input: "---\nname: worker\n---\nbody",
			want: Agent{
				Name:    "worker",
				Mode:    defaultAgentMode,
				Timeout: defaultAgentTimeoutSec,
				Prompt:  "body",
			},
		},
		{
			name:  "crlf line endings",
			input: "---\r\nname: worker\r\n---\r\nbody\r\n",
			want: Agent{
				Name:    "worker",
				Mode:    defaultAgentMode,
				Timeout: defaultAgentTimeoutSec,
				Prompt:  "body",
			},
		},
		{
			name:  "name trimmed",
			input: "---\nname: \" worker \"\n---\nbody",
			want: Agent{
				Name:    "worker",
				Mode:    defaultAgentMode,
				Timeout: defaultAgentTimeoutSec,
				Prompt:  "body",
			},
		},
		{
			name:    "missing frontmatter",
			input:   "name: worker\n---\nbody",
			wantErr: "missing frontmatter",
		},
		{
			name:    "malformed frontmatter",
			input:   "---\nname: worker\nbody",
			wantErr: "malformed frontmatter",
		},
		{
			name:    "missing name",
			input:   "---\ndescription: no name\n---\nbody",
			wantErr: "name is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAgentMarkdown("test.md", []byte(tt.input))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.want.Name {
				t.Errorf("name = %q, want %q", got.Name, tt.want.Name)
			}
			if got.Mode != tt.want.Mode {
				t.Errorf("mode = %q, want %q", got.Mode, tt.want.Mode)
			}
			if got.Timeout != tt.want.Timeout {
				t.Errorf("timeout = %d, want %d", got.Timeout, tt.want.Timeout)
			}
			if got.Prompt != tt.want.Prompt {
				t.Errorf("prompt = %q, want %q", got.Prompt, tt.want.Prompt)
			}
		})
	}
}

func TestDiscoverAgents(t *testing.T) {
	tmp := t.TempDir()
	projectAgents := filepath.Join(tmp, ".lcoder", "agents")
	if err := os.MkdirAll(projectAgents, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "worker.md"), []byte("---\nname: worker\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}
	// Non-.md files should be ignored.
	if err := os.WriteFile(filepath.Join(projectAgents, "ignored.txt"), []byte("---\nname: ignored\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAgents(tmp)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, ok := agents["worker"]; !ok {
		t.Errorf("expected worker agent, got %v", agents)
	}
	if _, ok := agents["ignored"]; ok {
		t.Errorf("expected ignored.txt to be skipped, got %v", agents)
	}
}

func TestDiscoverAgentsMissingDir(t *testing.T) {
	// DiscoverAgents should not error when the project agent directory does
	// not exist.
	tmp := t.TempDir()
	agents, err := DiscoverAgents(tmp)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected no agents, got %v", agents)
	}
}

func TestDiscoverAgentsUserDir(t *testing.T) {
	// Redirect the home directory to a temp directory so we can exercise the
	// user-level agent directory without touching the real ~/.lcoder.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	userAgents := filepath.Join(home, ".lcoder", "agents")
	if err := os.MkdirAll(userAgents, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userAgents, "user.md"), []byte("---\nname: user\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAgents("")
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, ok := agents["user"]; !ok {
		t.Errorf("expected user agent, got %v", agents)
	}
}
