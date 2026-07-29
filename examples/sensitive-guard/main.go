// Package main implements a real Lcoder extension that blocks write/edit
// access to sensitive files (.env, SSH keys, credentials).
//
// Communication: JSON-RPC 2.0 over stdin/stdout (newline-delimited).
// The extension is spawned by Lcoder as a child process and declared via
// extension.yaml. On initialize it advertises the tool_call hook; for
// every incoming hook/tool_call it checks the path argument against a
// configured blocklist and either allows or blocks the call.
//
// Usage:
//   cd examples/sensitive-guard
//   go run .                           # start the extension (stdin/stdout)
//
// Configure in ~/.lcoder/config.yaml:
//   extensions:
//     dirs: ["examples/sensitive-guard"]
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── JSON-RPC wire types ──────────────────────────────────────────────────

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── Protocol constants ───────────────────────────────────────────────────

const (
	methodInitialize   = "initialize"
	methodShutdown     = "shutdown"
	methodHookToolCall = "hook/tool_call"
	protocolVersion    = 1
)

type initializeParams struct {
	ProtocolVersion int    `json:"protocol_version"`
	Host            string `json:"host"`
}

type initializeResult struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Hooks   []string `json:"hooks,omitempty"`
}

type toolCallParams struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
}

type toolCallResult struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// ── Main ─────────────────────────────────────────────────────────────────

func main() {
	patterns := loadPatterns()
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
		if req.ID == nil {
			continue // notifications: ignore
		}
		handleRequest(req, patterns)
	}
}

func handleRequest(req request, patterns []string) {
	var result any
	var rpcErr *rpcError

	switch req.Method {
	case methodInitialize:
		result = initializeResult{
			Name:    "sensitive-guard",
			Version: "0.1.0",
			Hooks:   []string{"tool_call"},
		}

	case methodShutdown:
		result = struct{}{}

	case methodHookToolCall:
		result = handleToolCall(req.Params, patterns)

	default:
		rpcErr = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}

	resp := response{JSONRPC: "2.0", ID: *req.ID, Result: result, Error: rpcErr}
	data, _ := json.Marshal(resp)
	fmt.Println(string(data))
}

func handleToolCall(paramsRaw json.RawMessage, patterns []string) toolCallResult {
	var p toolCallParams
	if err := json.Unmarshal(paramsRaw, &p); err != nil {
		return toolCallResult{Action: "allow"}
	}

	// Only check write and edit tools.
	if p.Tool != "write" && p.Tool != "edit" {
		return toolCallResult{Action: "allow"}
	}

	path, _ := p.Params["path"].(string)
	if path == "" {
		return toolCallResult{Action: "allow"}
	}

	basename := strings.ToLower(filepath.Base(path))
	for _, pat := range patterns {
		if basename == pat || strings.HasPrefix(basename, pat+".") || strings.HasPrefix(basename, pat+"-") || strings.HasPrefix(basename, pat+"_") {
			return toolCallResult{
				Action: "block",
				Reason: fmt.Sprintf("sensitive-guard: %q matches protected pattern %q", basename, pat),
			}
		}
	}

	return toolCallResult{Action: "allow"}
}

func loadPatterns() []string {
	raw := os.Getenv("BLOCK_PATTERNS")
	if raw == "" {
		return []string{".env", "id_rsa", "id_ed25519", "id_ecdsa", "credentials"}
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
