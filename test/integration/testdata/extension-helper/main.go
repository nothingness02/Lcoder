// extension-helper is a test extension speaking the lcoder extension protocol
// (newline-delimited JSON-RPC 2.0 over stdio). It declares the tool_call and
// input hooks, subscribes to turn_end, and provides a ping command.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type request struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Result  any    `json:"result,omitempty"`
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil || req.ID == nil {
			continue // malformed line or notification (no reply expected)
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"name": "helper", "version": "0.0.1",
				"events":   []string{"turn_end"},
				"hooks":    []string{"tool_call", "input"},
				"commands": []map[string]string{{"name": "ping", "description": "ping"}},
			}
		case "hook/tool_call":
			var p struct {
				Tool string `json:"tool"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Tool == "danger" {
				result = map[string]any{"action": "block", "reason": "danger is blocked"}
			} else {
				result = map[string]any{"action": "allow"}
			}
		case "hook/input":
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params, &p)
			result = map[string]any{"action": "transform", "text": p.Text + "!"}
		case "command/invoke":
			result = map[string]any{"output": "pong"}
		case "shutdown":
			data, _ := json.Marshal(response{JSONRPC: "2.0", ID: *req.ID, Result: struct{}{}})
			fmt.Println(string(data))
			return
		default:
			result = struct{}{}
		}
		data, _ := json.Marshal(response{JSONRPC: "2.0", ID: *req.ID, Result: result})
		fmt.Println(string(data))
	}
}
