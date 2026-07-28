// Package bridge adapts the extension runtime host to Lcoder's in-process
// seams: agent tool hooks, the compaction summarizer, the input path, the
// event bus, TUI commands, and session custom entries.
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lcoder/lcoder/pkg/agent"
	"github.com/lcoder/lcoder/pkg/compaction"
	"github.com/lcoder/lcoder/pkg/contextmgr"
	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/extension/proto"
	"github.com/lcoder/lcoder/pkg/extension/runtime"
	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/session"
)

// Bridge adapts a runtime.Host to in-process interfaces.
type Bridge struct {
	host *runtime.Host
}

func New(host *runtime.Host) *Bridge { return &Bridge{host: host} }

// Host exposes the underlying host (command dispatch, capabilities).
func (b *Bridge) Host() *runtime.Host { return b.host }

// BeforeToolCall adapts the tool_call hook chain to agent.BeforeToolCallHook.
func (b *Bridge) BeforeToolCall() agent.BeforeToolCallHook {
	return func(ctx context.Context, info agent.ToolCallInfo) (*agent.BeforeToolCallResult, error) {
		res := b.host.RunToolCallHooks(ctx, info.ToolCall.Name, info.Args)
		if res.Block {
			return &agent.BeforeToolCallResult{Block: true, Reason: res.Reason}, nil
		}
		if res.Params != nil {
			return &agent.BeforeToolCallResult{ModifiedArgs: res.Params}, nil
		}
		return nil, nil
	}
}

// AfterToolCall adapts the tool_result hook chain to agent.AfterToolCallHook.
// A rewrite collapses structured content to a single text part (the extension
// protocol is text-only); the unchanged path preserves the original content.
func (b *Bridge) AfterToolCall() agent.AfterToolCallHook {
	return func(ctx context.Context, info agent.ToolCallResultInfo) (*agent.AfterToolCallResult, error) {
		orig := resultText(info.Result)
		newText := b.host.RunToolResultHooks(ctx, info.ToolCall.Name, info.Args, orig, info.IsError)
		if newText == orig {
			return nil, nil
		}
		return &agent.AfterToolCallResult{
			Content: []models.ContentPart{models.TextContent{Text: newText}},
		}, nil
	}
}

func resultText(r models.ToolExecutionResult) string {
	var out strings.Builder
	for _, part := range r.Content {
		if t, ok := part.(models.TextContent); ok {
			out.WriteString(t.Text)
		}
	}
	return out.String()
}

// summarizerMaxInputChars caps the serialized conversation sent to the
// session_before_compact hook, mirroring summaryMaxInputChars in
// pkg/compaction/summarizer.go (the built-in summarizer's whole-input cap).
const summarizerMaxInputChars = 48000

// summarizerTruncatedSuffix marks that the serialized input was cut to fit
// summarizerMaxInputChars, mirroring the built-in summarizer.
const summarizerTruncatedSuffix = "\n...[input truncated]"

// Summarizer returns a contextmgr.SummarizeFunc that delegates to the
// session_before_compact hook when an extension declares it, falling back to
// the built-in summarizer otherwise or on hook failure.
func (b *Bridge) Summarizer(fallback contextmgr.SummarizeFunc) contextmgr.SummarizeFunc {
	return func(ctx context.Context, messages []models.AgentMessage, prior string) (string, error) {
		conversation := compaction.SerializeConversation(messages, 2000)
		if len(conversation) > summarizerMaxInputChars {
			conversation = conversation[:summarizerMaxInputChars-len(summarizerTruncatedSuffix)] + summarizerTruncatedSuffix
		}
		// The prior summary is prepended to the serialized conversation because the
		// hook protocol carries a single string. Without it a hook-provided summary
		// would silently drop everything earlier folds established.
		if p := strings.TrimSpace(prior); p != "" {
			conversation = "<previous_summary>\n" + p + "\n</previous_summary>\n\n" + conversation
		}
		// TODO: tokensBefore is 0 — SummarizeFunc cannot supply it
		if summary, ok := b.host.RunBeforeCompactHook(ctx, conversation, 0); ok && summary != "" {
			return summary, nil
		}
		return fallback(ctx, messages, prior)
	}
}

// InputHook adapts the input hook chain for the TUI/one-shot submit path.
// proceed=false means the input was blocked; reason is user-displayable.
func (b *Bridge) InputHook(ctx context.Context, text string) (newText string, proceed bool, reason string) {
	res := b.host.RunInputHook(ctx, text)
	if res.Block {
		return text, false, res.Reason
	}
	return res.Text, true, ""
}

// SubscribeEvents forwards bus events to subscribed extensions. Returns the
// unsubscribe func. Delivery is asynchronous (SubscribeAsync) so a wedged
// extension cannot stall bus.Emit for other subscribers mid-agent-loop;
// payloads are serialized with events.MarshalJSON.
func (b *Bridge) SubscribeEvents(bus *events.Bus) func() {
	return bus.SubscribeAsync(func(ctx context.Context, ev events.Event) error {
		eventType := string(ev.EventType())
		if !b.host.Subscribed(eventType) {
			return nil
		}
		data, err := events.MarshalJSON(ev)
		if err != nil {
			return nil
		}
		b.host.BroadcastEvent(eventType, json.RawMessage(data))
		return nil
	}, events.AsyncOptions{})
}

// SessionHandler builds the host-side handler for extension->host requests:
// session/append_entry, session/get_entries, host/log. custom_type must be
// namespaced "<ext-name>/", but the namespace is a convention only —
// validateCustomType merely requires a "/", and any extension can read or
// write under any prefix. Per-extension enforcement would require threading
// peer identity into the handler and is deferred.
//
// Threading model: conn.readLoop runs each inbound request on its own
// goroutine, so the returned handler is invoked concurrently across (and
// within) extensions, and the agent loop appends to the session on its own
// goroutine. session.Session serializes all of this internally via its mutex,
// so the handler needs no synchronization of its own.
func SessionHandler(sess *session.Session, logFn func(level, msg string)) runtime.Handler {
	return runtime.HandlerFunc{
		RequestFunc: func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case proto.MethodSessionAppendEntry:
				var p proto.AppendEntryParams
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, err
				}
				if err := validateCustomType(p.CustomType); err != nil {
					return nil, err
				}
				if err := sess.AppendCustomEntry(p.CustomType, p.Data); err != nil {
					return nil, err
				}
				return struct{}{}, nil
			case proto.MethodSessionGetEntries:
				var p struct {
					Prefix string `json:"prefix"`
				}
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, err
				}
				if err := validateCustomType(p.Prefix); err != nil {
					return nil, err
				}
				entries := sess.CustomEntries(p.Prefix)
				out := proto.GetEntriesResult{Entries: make([]proto.Entry, 0, len(entries))}
				for _, e := range entries {
					out.Entries = append(out.Entries, proto.Entry{CustomType: e.CustomType, Data: e.Data})
				}
				return out, nil
			}
			return nil, &proto.RPCError{Code: -32601, Message: "method not found: " + method}
		},
		NotifyFunc: func(method string, params json.RawMessage) {
			if method != proto.MethodHostLog {
				return
			}
			var p proto.HostLogParams
			if err := json.Unmarshal(params, &p); err != nil {
				return
			}
			if logFn != nil {
				logFn(p.Level, p.Message)
			}
		},
	}
}

func validateCustomType(customType string) error {
	if customType == "" || !strings.Contains(customType, "/") {
		return fmt.Errorf("custom_type must be namespaced as \"<ext-name>/<key>\", got %q", customType)
	}
	return nil
}
