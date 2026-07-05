# Command Panel Below Composer — Design

Date: 2026-06-28
Status: Implemented

## Context

The Lcoder TUI previously routed all slash-command feedback (`/help`, `/modes`,
`/status`, mode-switch confirmations, `/skill` info, unknown-command errors) into
the main conversation viewport as dim "system" blocks. This mixed transient
command chatter with the agent transcript, cluttering history.

Goal: the main viewport shows **only** agent-run content (user bar, assistant
text, tool calls) plus run notices (`error`, `interrupted`, tool summaries).
Command feedback moves to an ephemeral panel rendered just above the input box,
matching Claude Code's behavior.

## Requirements

- Main viewport keeps: user bar, assistant messages, tool calls/results, and run
  notices (`error`, `interrupted`, tool-result summaries).
- Command feedback moves out of the viewport into a panel above the composer.
- `/modes`, `/mode` (no arg), `/skill` -> interactive **selection box**
  (`up/down` to move, `Enter` to execute).
- `/help`, `/status`, unknown/unhandled command -> read-only **text box**.
- Panel lifecycle is **ephemeral**: any keystroke other than the panel's own
  navigation dismisses it (same feel as the `/`-autocomplete dropdown).
- `/mode <name>` switches **silently** — the status-line mode label is the
  feedback; no viewport block, no panel.

## Architecture

### New component: `cmdPanel` (`pkg/tui/cmdpanel.go`)

```go
type cmdPanelKind int   // cmdPanelText | cmdPanelSelect
type cmdPanelAction int // actionNone | actionSwitchMode | actionTriggerSkill

type cmdPanelItem struct { label, desc, value string }

type cmdPanel struct {
    visible  bool
    kind     cmdPanelKind
    title    string
    text     string         // text kind
    items    []cmdPanelItem // select kind
    selected int
    action   cmdPanelAction
}
```

- `moveUp` / `moveDown` clamp `selected` within range.
- `renderCmdPanel(p, width)` draws a rounded-border box (reuses the slash-menu
  style). Select kind renders `›`-marked rows `label  desc`; text kind renders
  the wrapped text. Lines truncate to the inner width.

### Model (`pkg/tui/model.go`)

Single new field `cmdPanel cmdPanel` on `Model`. The panel is an overlay within
`stateInput` (a struct flag, like the existing slash menu), not a new `uiState`.

### Rendering (`pkg/tui/view.go`)

`bottomRegion` renders the panel between the slash-menu slot and the input when
`m.cmdPanel.visible` (mutually exclusive with the typing menu). `bottomHeight`
measures by rendering, so layout adjusts automatically; `updateSizes()` is called
on panel open/close so the viewport shrinks and a tall mode/skill list never
overflows.

### Dispatch (`pkg/tui/keys.go` `dispatchSlash`)

| Command           | Action                                              |
|-------------------|-----------------------------------------------------|
| `help`            | `showTextPanel("help", formatCommandHelp())`        |
| `status`          | `showTextPanel("status", statusText())`             |
| `modes`           | `openModePanel()` (select, `actionSwitchMode`)      |
| `mode` (no arg)   | `switchMode("")` -> `openModePanel()`               |
| `mode <name>`     | `switchMode(name)` — silent                         |
| `skill`           | `openSkillPanel()` (select, `actionTriggerSkill`)   |
| unknown/unhandled | `showTextPanel(name, error)`                        |
| new/sessions/fork/extensions/tools/retry/quit | unchanged (real actions/overlays) |

Run notices (`onAgentDone` error & tool-summary, `interrupted`) remain in the
viewport via `addSystem`.

### Key handling (`pkg/tui/keys.go` `handleInputKey`)

Before the slash-menu block, when `cmdPanel.visible`:

- **Select kind:** `Up`/`Down` move; `Enter` -> `execCmdPanel()` (runs action,
  closes); `Esc` closes; any other key closes and falls through to composing.
- **Text kind:** `Esc`/`Enter` close; any other key closes and falls through.

`execCmdPanel` dispatches on `action`: `actionSwitchMode` -> `switchMode(value)`;
`actionTriggerSkill` -> `handleSkillTrigger(value, "")`.

## Testing

`pkg/tui/cmdpanel_test.go`:

- `/help` populates a text panel, appends no system block.
- `/modes` builds a select panel from `ModeManager.List()`; `Enter` switches the
  agent mode and closes the panel.
- `/status` text panel closes on `Esc`.
- Typing a rune dismisses the panel.
- `/skill` builds a select panel; `Enter` returns a prompt command (skill run).

`fakeAgent` in `model_test.go` gained a `mode` field and `WithMode` so it
satisfies `ModeSwitcher` for the switch test.

## Out of scope

No changes to the agent, events bus, block rendering, or the `/`-autocomplete
dropdown. No spec for scrolling long text panels (current content fits).
