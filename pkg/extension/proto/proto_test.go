package proto

import (
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	req := Request{JSONRPC: "2.0", ID: 7, Method: MethodHookToolCall,
		Params: json.RawMessage(`{"tool":"bash","params":{"command":"ls"}}`)}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 || got.Method != MethodHookToolCall {
		t.Fatalf("got %+v", got)
	}
	var p ToolCallParams
	if err := json.Unmarshal(got.Params, &p); err != nil {
		t.Fatal(err)
	}
	if p.Tool != "bash" || p.Params["command"] != "ls" {
		t.Fatalf("params %+v", p)
	}
}

func TestNotificationHasNoID(t *testing.T) {
	n := Request{JSONRPC: "2.0", Method: EventMethodPrefix + "turn_start"}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["id"]; ok {
		t.Fatal("notification must not carry id")
	}
}

func TestInitializeResultRoundTrip(t *testing.T) {
	res := InitializeResult{
		Name: "ext", Version: "0.1.0",
		Events:   []string{"turn_start"},
		Hooks:    []string{HookToolCall, HookInput},
		Commands: []CommandDecl{{Name: "review", Description: "d", Usage: "/review"}},
	}
	data, _ := json.Marshal(res)
	var got InitializeResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Hooks[1] != HookInput || got.Commands[0].Name != "review" {
		t.Fatalf("got %+v", got)
	}
}

func TestRPCErrorString(t *testing.T) {
	e := &RPCError{Code: -32601, Message: "method not found"}
	if e.Error() != "method not found" {
		t.Fatalf("got %q", e.Error())
	}
}
