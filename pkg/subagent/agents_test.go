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

func TestDiscoverAgents(t *testing.T) {
	tmp := t.TempDir()
	projectAgents := filepath.Join(tmp, ".lcoder", "agents")
	if err := os.MkdirAll(projectAgents, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "worker.md"), []byte("---\nname: worker\n---\nbody"), 0644); err != nil {
		t.Fatal(err)
	}

	agents, err := DiscoverAgents(tmp)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, ok := agents["worker"]; !ok {
		t.Errorf("expected worker agent, got %v", agents)
	}
}
