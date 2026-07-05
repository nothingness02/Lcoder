# TUI Permission Confirmation Redesign

## Status

Approved for implementation.

## Problem

The current TUI shows a full-screen centered modal when a tool requires permission approval (`stateConfirm`). This hides the conversation history, interrupting the user's flow and preventing them from reviewing the log before deciding.

## Goals

- Keep the conversation log fully visible while a permission prompt is active.
- Allow the user to scroll the log while the prompt is waiting.
- Provide a clear, keyboard-driven selection UI similar to Claude Code / OpenCode.
- Remain backward-compatible with the existing blocking confirmation model.

## Non-Goals

- "Always allow" / rule persistence is out of scope for this change.
- Touch or mouse-driven selection is not required.

## Design

### Layout

When `stateConfirm` is active, the main view continues to render the normal `viewport + bottomRegion` layout. The bottom region, however, is replaced by a permission confirmation strip instead of the input box and status line.

```
+------------------------------------------+
| Conversation log (still visible/scrollable)|
| ...                                      |
+------------------------------------------+
| Permission request: bash rm -rf /tmp/old |
| [Allow] [Deny]      ← → Enter Esc        |
+------------------------------------------+
```

### Confirmation Strip

The strip is rendered by `confirmPanel`. It shows:

1. Tool name and truncated arguments.
2. Two selectable options: `Allow` and `Deny`.
3. A hint line: `← → select · Enter confirm · Esc cancel`.

The selected option is highlighted with the existing error/accent color; the unselected option has a neutral border.

### Keyboard Interaction

| Key | Action |
| --- | --- |
| `←` / `→` | Move selection between `Allow` and `Deny`. |
| `Enter` | Confirm the selected option. |
| `Esc` | Cancel and treat as `Deny`. |
| `PgUp` / `PgDn` / `↑` / `↓` | Scroll the conversation viewport. |

The tool-call goroutine remains blocked until the user confirms or cancels, preserving the existing safety model.

## Implementation Plan

### Files to Change

1. `pkg/tui/view.go`
   - Remove the `stateConfirm` branch that returns `confirmView()`.
   - Update `bottomRegion()` to render the confirmation strip when `m.state == stateConfirm`.

2. `pkg/tui/confirm.go`
   - Extend `confirmPanel` to track a `selected` index (`0 = Allow`, `1 = Deny`).
   - Replace the centered modal `View()` with a bottom-strip renderer.
   - Add navigation helpers: `nextOption()`, `prevOption()`.

3. `pkg/tui/keys.go`
   - Update `handleConfirmKey` to handle arrow keys, Enter, Esc, and viewport-scroll keys.

4. `pkg/tui/confirm_test.go`
   - Update existing assertions to expect a non-modal confirmation strip.
   - Add tests for left/right selection, Enter confirmation, and log scrolling while confirming.

### Testing Checklist

- `TestConfirmPanelShowsAndHides` passes with the new layout.
- A new test verifies that `←` / `→` cycles selection.
- A new test verifies that `Enter` returns the selected decision.
- A new test verifies that `PgUp` / `PgDn` update the viewport offset while in confirm state.
- `go test ./pkg/tui` and `go vet ./pkg/tui` pass.

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Bottom strip may be too narrow on very small terminals. | Truncate arguments aggressively and keep the strip to 2 lines. If `height < 6`, fall back to a simpler one-line prompt. |
| Users expect `y`/`n` shortcuts from the old modal. | Keep `y` mapped to `Allow` and `n` mapped to `Deny` for the first iteration, documented as legacy shortcuts. |
| Multiple simultaneous permission requests. | Keep the existing single-pending-confirmation behavior; the second request waits on the same channel. |

## Future Work

- Add an `Always allow` option that registers a permission rule via the permissions engine.
- Support number shortcuts (`1`, `2`) for faster selection.
- Make the strip dismissible with a queue when non-blocking approvals are desired.
