// Package main implements a Lcoder extension that logs turn events to a file.
// This demonstrates event subscription — a capability unavailable to shell hooks.
//
// Usage:
//   cd examples/session-logger
//   go run .
//
// Configure: copy this directory to ~/.lcoder/extensions/session-logger/
// Logs are written to ~/.lcoder/extensions/session-logger/turns.log
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ── JSON-RPC wire types ──────────────────────────────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int64     `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Events  []string `json:"events,omitempty"`
}

// ── Main ─────────────────────────────────────────────────────────────────

func main() {
	logDir := filepath.Dir(os.Args[0])
	logPath := filepath.Join(logDir, "turns.log")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			resp := response{
				JSONRPC: "2.0", ID: *req.ID,
				Result: initializeResult{
					Name:    "session-logger",
					Version: "0.1.0",
					Events:  []string{"turn_start", "turn_end", "agent_end"},
				},
			}
			data, _ := json.Marshal(resp)
			fmt.Println(string(data))

		// event/* notifications — no ID, no response needed
		case "event/turn_start", "event/turn_end", "event/agent_end":
			appendLog(logPath, req.Method, req.Params)

		default:
			// Respond to requests we don't handle (e.g. shutdown).
			if req.ID != nil {
				resp := response{JSONRPC: "2.0", ID: *req.ID, Result: struct{}{}}
				data, _ := json.Marshal(resp)
				fmt.Println(string(data))
			}
		}
	}
}

func appendLog(path, event string, params json.RawMessage) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	ts := time.Now().Format(time.RFC3339)
	fmt.Fprintf(f, "[%s] %s %s\n", ts, event, string(params))
}
