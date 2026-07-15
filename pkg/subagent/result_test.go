package subagent

import (
	"strings"
	"testing"

	"github.com/lcoder/lcoder/pkg/events"
	"github.com/lcoder/lcoder/pkg/models"
)

func TestParseEventLine(t *testing.T) {
	line := `{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}]}]}`
	ev, err := ParseEventLine([]byte(line))
	if err != nil {
		t.Fatalf("ParseEventLine(...) error = %v, want nil", err)
	}
	end, ok := ev.(events.AgentEndEvent)
	if !ok {
		t.Fatalf("ParseEventLine(...) type = %T, want AgentEndEvent", ev)
	}
	if end.Reason != events.EndReasonCompleted {
		t.Errorf("Reason = %q, want %q", end.Reason, events.EndReasonCompleted)
	}
	if len(end.Messages) != 1 {
		t.Fatalf("Messages length = %d, want 1", len(end.Messages))
	}
	if end.Messages[0].Role != models.RoleAssistant {
		t.Errorf("Messages[0].Role = %q, want %q", end.Messages[0].Role, models.RoleAssistant)
	}
	if got := end.Messages[0].Text(); got != "hello" {
		t.Errorf("Messages[0].Text() = %q, want %q", got, "hello")
	}
}

func TestParseEventLine_Cases(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantErr  bool
		wantType any
	}{
		{
			name:     "turn_start",
			line:     `{"type":"turn_start","turn":1}`,
			wantType: events.TurnStartEvent{Base: events.Base{Turn: 1}},
		},
		{
			name:    "malformed JSON",
			line:    `{"type":"turn_start",turn:1}`,
			wantErr: true,
		},
		{
			name:    "unknown event type",
			line:    `{"type":"unknown"}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEventLine([]byte(tt.line))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseEventLine(...) error = nil, want non-nil")
				}
				if tt.name == "unknown event type" && !strings.Contains(err.Error(), "unknown event type") {
					t.Errorf("error = %q, want to contain %q", err.Error(), "unknown event type")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEventLine(...) error = %v, want nil", err)
			}
			switch want := tt.wantType.(type) {
			case events.TurnStartEvent:
				gotEvt, ok := got.(events.TurnStartEvent)
				if !ok {
					t.Fatalf("ParseEventLine(...) type = %T, want TurnStartEvent", got)
				}
				if gotEvt.Turn != want.Turn {
					t.Errorf("Turn = %d, want %d", gotEvt.Turn, want.Turn)
				}
			default:
				t.Fatalf("unexpected wantType %T", want)
			}
		})
	}
}

func TestExtractFinalAnswer(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{
			name: "happy path",
			output: `{"type":"turn_start","turn":1}
{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"final"}]}]}
`,
			want: "final",
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
		{
			name:   "whitespace only",
			output: "   \n\n  ",
			want:   "",
		},
		{
			name:   "agent end with no assistant",
			output: `{"type":"agent_end","reason":"completed","messages":[]}`,
			want:   "",
		},
		{
			name: "multiple agent_end events last assistant wins",
			output: `{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"first"}]}]}
{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"last"}]}]}`,
			want: "last",
		},
		{
			name: "malformed line in stream",
			output: `{"type":"agent_end","reason":"completed","messages":[{"role":"assistant","content":[{"type":"text","text":"final"}]}]}
this is not json`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractFinalAnswer([]byte(tt.output))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExtractFinalAnswer(...) error = nil, want non-nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ExtractFinalAnswer(...) error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("ExtractFinalAnswer(...) = %q, want %q", got, tt.want)
			}
		})
	}
}
