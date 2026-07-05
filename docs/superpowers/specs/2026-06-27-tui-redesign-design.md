# Lcoder TUI Redesign — Design Spec

- **Date:** 2026-06-27
- **Status:** Approved (design)
- **Goal:** Completely rewrite the Lcoder TUI (`pkg/tui`) to reach the design/interaction quality of the reference project Kocoro (`reference/Kocoro/internal/tui`), while keeping Lcoder's own brand identity and all existing functionality.

## Context

The current Lcoder TUI is a basic bubbletea app: a single `Model` composed of separate sub-models (`ToolbarModel`, `StatusBarModel`, `ChatViewport`, `InputModel`, `ToolPanelModel`, `SessionPickerModel`, `ExtensionsPanelModel`). It renders messages as Nord-palette "bubbles" with `[role]` labels, streams assistant output via a fragile string-matching rewrite (`ChatViewport.UpdateLastAssistant`), shows tool calls in a separate overlay panel, and runs inline (no alt-screen).

The reference Kocoro TUI is a single-model state-machine app with: glamour markdown (syntax-highlighted code, cached), compact inline tool rendering with friendly labels, an animated brand header, adaptive light/dark theming with accent presets, a live fuzzy slash-command menu, a shimmer spinner, ghost-text suggestions, paste stashing, CJK-aware width handling, and content-sized viewport with streaming rendered as normal markdown (zero "pop").

This rewrite ports Kocoro's design patterns and polish onto Lcoder while preserving Lcoder-specific features that Kocoro lacks.

## Decisions (from brainstorming)

- **Visual identity:** Keep the "Lcoder" name and brand; borrow Kocoro's design patterns. Not a 1:1 clone.
- **Accent / theme:** Frost cyan accent (Dark `#88C0D0`, Light `#5E81AC`), Nord-based, adaptive light/dark.
- **Startup header logo:** Kocoro-style pixel / half-block mark with a cyan diagonal gradient that "draws in" on startup, then scrolls away. No persistent top toolbar.
- **In scope (core polish):** glamour markdown, compact inline tool rendering, single-model state-machine core, live `/` fuzzy slash menu, shimmer spinner, adaptive theming, content-rebuild viewport + streaming, readline shortcuts + input history.
- **In scope (heavier, opted in):** animated brand header, ghost-text follow-up suggestions, long-paste folding, alt-screen full-screen mode.
- **Preserved Lcoder features:** modes (+ prompt auto-detect, `/mode`, `/modes`), skills (manual `skill:name` trigger + auto-detect), session picker + fork, extensions/MCP panel (HTTP tools + MCP servers), all current slash commands, cost + context-usage tracking, session persistence.
- **Out of scope:** Interactive tool-approval (y/n/a) UI — Lcoder handles permissions via `permissions.Engine` / `BeforeToolCall` hook at the agent level, and there is no approval event plumbed to the TUI. Adding it would require new agent event plumbing, which is outside this TUI rewrite.

## Architecture: Approach 1 (+ Approach 3 pragmatism)

Full single-model state-machine rewrite. Reuse the existing session-picker and extensions-panel rendering logic rather than rebuilding them.

A single top-level `Model` owns an embedded `bubbles/textarea` (composer) and `bubbles/viewport` (history), driven by an explicit `state` enum:

```
stateStartup → stateInput ↔ stateProcessing
              ↘ stateSessionPicker
              ↘ stateExtensions
```

The slash menu is an inline dropdown over the composer (`menuVisible` flag within `stateInput`), not a separate state.

`View()` = `viewport.View() + "\n" + bottomRegion()`. The viewport content is **rebuilt from a `[]block` list**; the in-flight stream is rendered as normal markdown in the tail so finishing a turn causes zero visual pop. `app.go` keeps its `Run` / `RunWithIO` signatures and enables `tea.WithAltScreen()`.

### File structure (`pkg/tui`)

New / replaced:

| File | Purpose |
|---|---|
| `model.go` | `Model` struct, `Init/Update/View`, state enum, layout pass |
| `events.go` | maps `events.Event` → block updates / streaming state |
| `keys.go` | key handling per state, readline shortcuts |
| `view.go` | `bottomRegion()`, `buildViewportContent()` |
| `theme.go` | adaptive Nord palette + frost accent, semantic style accessors |
| `markdown.go` | glamour compact renderer + per-`(width,dark)` renderer cache + `(text,width)` render cache |
| `block.go` | conversation block model + per-role rendering |
| `toolformat.go` | friendly labels, key-arg extraction, compact + expanded tool result, turn summary |
| `header.go` | startup header box (tips / model / cwd / version) |
| `logo.go` | pixel/half-block Lcoder mark + draw-in animation + gradient |
| `spinner.go` | braille glyph + rotating phrases + shimmer/wave text |
| `statusline.go` | full-width separator bar, state-specific captions |
| `width.go` | CJK-aware width helpers (`displayWidth`, `truncateCells…`) |
| `input.go` | composer config, auto-grow height |
| `menu.go` | slash-command fuzzy menu (`sahilm/fuzzy`) + highlight render |
| `commands.go` | slash command registry + dispatch (merges current `slash_commands.go` + `mode_commands.go`) |
| `suggestion.go` | ghost-text follow-up (async cmd, gated) |
| `paste.go` | large-paste stashing |
| `history.go` | input history navigation |
| `messages.go` | tea.Msg types, `AgentRunner/SessionWriter/SessionStore` interfaces, cmds (kept + extended) |

Reused / restyled (Approach 3): `sessionpicker.go`, `extensionspanel.go`.

Removed (folded into the above): `toolpanel.go`, `chat.go`, `statusbar.go`, `styles.go`, `renderer.go`, `status.go`, `slash_commands.go`, `mode_commands.go`.

### Dependencies

No new heavy dependencies. Already present (direct or indirect): `glamour` v1.0.0, `chroma` (syntax highlighting), `sahilm/fuzzy` (fuzzy matching), `mattn/go-runewidth` (CJK width), `lipgloss`, `bubbles`, `bubbletea`.

## Component designs

### Theme system (`theme.go`)

Replace the fixed Nord structs with `lipgloss.AdaptiveColor{Light,Dark}` semantic tokens: `colorDim`, `colorSecondary`, `colorFaint`, `colorSuccess`, `colorError`, `colorWarn`, `colorAccent` (frost cyan), `colorInfo`, `colorSelect`. Background light/dark is detected once at startup before bubbletea grabs stdin (Kocoro `warmBackgroundColor` pattern). The existing `themeStyle` ("dark"/"light") param remains an override. Styles are exposed as accessor funcs (`styleDim()`, `styleAccent()`, …) so glamour and lipgloss share one palette.

### Conversation rendering & streaming (`block.go`, `events.go`, `markdown.go`)

`block` has `kind` (user/assistant/tool/system), `raw`, and a cached `rendered`. Rendering rules:

- **user** → full-width tinted bar `› text` (subtle background)
- **assistant** → glamour markdown (compact style, syntax-highlighted code), optional thinking prefix, trailing ` · N tokens · $cost` in dim
- **tool** → compact one-liner (see Tool rendering)
- **system** → dim / italic

Streaming flow:

1. `MessageStart(assistant)` → `streamLive=""`, streaming on.
2. `MessageUpdate` → append delta to `streamLive`, bounded to ~32 KiB cut at a line boundary; mark viewport dirty.
3. `MessageEnd` → commit a final assistant block (with usage), clear `streamLive`.
4. `buildViewportContent` = rendered committed blocks + `streamLive` rendered as markdown tail.

Markdown is cached by `(text, width)` so scroll re-renders are cheap; the glamour renderer is cached by `(width, dark)`.

### Tool rendering (`toolformat.go`)

- **Friendly labels** map covering Lcoder's tool set + MCP/HTTP tools, with a sensible fallback (`bash→"Running a command"`, `file_read→"Reading a file"`, etc.).
- **Key-arg extraction:** command / path / pattern / query / url.
- **Compact:** `⏵ Running a command: go test  ✓  1.2s` (green ✓ / red ✗) + short result summary.
- **Expanded (Ctrl+O toggle):** header + args + result body with head/tail windowing (8 head / 4 tail lines, `… +N lines`).
- **Turn summary:** `⏵ 3 tools used  ✓2 ✗1`.

Replaces the separate tool panel. `/tools` toggles the expanded (Ctrl+O) view. Live tool status also appears in the status line during processing.

### Input, slash menu, paste, history, suggestions

- **Composer (`input.go`):** rounded border (accent when focused, dim during processing), `› ` prompt, auto-grow 1–6 lines, readline shortcuts (Ctrl+K/U/W/A/E), Ctrl+C two-stage exit, esc-to-interrupt during processing (`agent.Abort()`).
- **Follow-up while processing:** typing + Enter during `stateProcessing` injects via `agent.Steer(...)` and shows a user block (the agent already supports `Steer`/`FollowUp`).
- **Slash menu (`menu.go`):** typing `/` opens an inline dropdown; exact-prefix then fuzzy (`sahilm/fuzzy`) matches with highlighted characters; `?` opens the full palette; immediate commands (`/help`, `/status`, …) execute on Enter.
- **Paste folding (`paste.go`):** pastes > 1000 runes → `[Pasted #N (X chars)]` placeholder, expanded on submit.
- **History (`history.go`):** Up/Down recall when the composer is single-line; double-esc clears.
- **Suggestions (`suggestion.go`):** after a completed turn, asynchronously generate a single follow-up suggestion; render as ghost text `↳ … Tab` under the composer; gated by completed-turn count. Source: a lightweight call via the existing agent/llm client. **The exact source is confirmed during planning;** if no cheap source is available, the feature degrades to off (no ghost text), never blocking the UI.

### Header, spinner, status line, state machine

- **Startup header (`header.go` + `logo.go`):** rounded accent box; left = pixel/half-block Lcoder mark drawing in with a cyan diagonal gradient over the first frames; right = tips / model / cwd / version. Plays in `stateStartup`; any key or the first agent event advances to `stateInput`; it scrolls away as content grows (no persistent toolbar).
- **Spinner (`spinner.go`):** braille glyph (`⠋⠙⠹…` @100 ms) + phrase rotation (@5 s) + shimmer sweep in the accent gradient. Driven by a `tea.Tick` cmd active only in `stateProcessing`.
- **Status line (`statusline.go`):** full-width `─` separator with left/right captions. Input state: `▌ mode · model` left, `? for commands` right, plus `ctx N/Max` and cost. Processing state: glyph + tool label left, `esc to interrupt · elapsed` right.

## Integration (preserve all Lcoder features)

The `events.Bus` subscription, `AgentRunner / SessionWriter / SessionStore` interfaces, `ModeSwitcher`, `modeManager`, `skills`, `mcpRegistry`, and `httpTools` wiring are unchanged. Preserved behaviors:

- Mode auto-detect from prompt + `/mode`, `/modes`.
- Skill manual trigger (`skill:name`) + auto-detect.
- Slash commands: `/help`, `/sessions` (+`resume`/`continue`), `/fork`, `/new` (+`clear`), `/retry`, `/status`, `/tools` (toggles expanded tool view), `/extensions`, `/mode`, `/modes`, `/skill`, `/quit`.
- Cost + context-usage tracking (moved into the status line).
- Session persistence (`persistSession`).

## Testing

Follow the repo's existing table-test style; keep `model_test.go` green (adapting assertions to the new structure). New unit tests mirroring Kocoro's coverage:

- `theme` — token resolution, light/dark.
- `markdown` — renders without error, cache hit behavior.
- `toolformat` — labels, key-arg extraction, compact + expanded formatting, turn summary.
- `menu` — fuzzy match ordering + highlight spans.
- `paste` — stash/expand round-trip.
- `width` — CJK truncation widths.
- `statusline` — left/right composition fills width.
- `block` — per-role rendering.
- `events` — event→block mapping (start/update/end, tool start/end).
- `logo` — frame line/column bounds across draw-in frames.

TDD where practical, per the global engineering guidelines.

## Success criteria

1. The TUI builds and `go test ./...` passes.
2. Startup shows the animated frost-cyan Lcoder header, which scrolls away.
3. Assistant output renders as syntax-highlighted markdown; streaming has no visual pop on completion.
4. Tool calls render as compact one-liners with friendly labels and ✓/✗ + elapsed; Ctrl+O expands.
5. Typing `/` shows a live fuzzy menu; all preserved slash commands work.
6. Modes, skills (manual + auto), session fork, and the extensions/MCP panel all function as before.
7. Alt-screen mode, paste folding, input history, readline shortcuts, esc-to-interrupt, and ghost-text suggestions (or graceful no-op) all work.
