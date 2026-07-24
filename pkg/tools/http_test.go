package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/models"
)

func TestHTTPExecutable(t *testing.T) {
	var received map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"content":[{"type":"text","text":"deployed"}],"details":{"version":"v1"},"terminate":false}`)
	}))
	defer ts.Close()

	exec := NewHTTPExecutable(HTTPConfig{
		Name:        "deploy",
		Endpoint:    ts.URL,
		Description: "Deploy service",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service": map[string]any{"type": "string"},
			},
		},
	})

	result, err := exec.Execute(context.Background(), "call_1", map[string]any{"service": "api"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if received["name"] != "deploy" {
		t.Fatalf("expected deploy, got %v", received["name"])
	}
	if result.Content[0].(models.TextContent).Text != "deployed" {
		t.Fatalf("unexpected result text: %v", result.Content)
	}
	if result.Details["version"] != "v1" {
		t.Fatalf("unexpected details: %v", result.Details)
	}
}

func TestHTTPExecutableError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"service missing"}`)
	}))
	defer ts.Close()

	exec := NewHTTPExecutable(HTTPConfig{Name: "deploy", Endpoint: ts.URL})
	result, err := exec.Execute(context.Background(), "call_1", map[string]any{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	if result.Content[0].(models.TextContent).Text != "service missing" {
		t.Fatalf("unexpected error text: %v", result.Content)
	}
	// The registry must propagate the flag to the model-facing tool_result.
	reg := NewRegistry(".")
	reg.Register("deploy", NewHTTPExecutable(HTTPConfig{Name: "deploy", Endpoint: ts.URL}))
	if _, isError := reg.Execute(context.Background(), "call_2", "deploy", map[string]any{}); !isError {
		t.Fatal("registry should propagate result.IsError")
	}
}

func TestHTTPExecutableTruncatesOversizedResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxHTTPResponseBytes*2)))
	}))
	defer ts.Close()

	exec := NewHTTPExecutable(HTTPConfig{Name: "big", Endpoint: ts.URL})
	result, err := exec.Execute(context.Background(), "call_1", nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Text(), "[truncated: response body exceeded") {
		t.Fatal("expected truncation notice")
	}
	if result.Details["truncated"] != true {
		t.Fatalf("truncated detail = %v, want true", result.Details["truncated"])
	}
	if result.IsError {
		t.Fatal("truncated 2xx response should not be flagged as an error")
	}
}
