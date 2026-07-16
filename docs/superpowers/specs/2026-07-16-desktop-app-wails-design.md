# Desktop App (Wails) Design

> **Goal:** Replace the Bubble Tea TUI with a local desktop application. The Go agent core stays as the backend; the UI is rewritten in TypeScript and rendered by the platform Webview via Wails.

## Context

The current `pkg/tui` is a terminal UI built on Bubble Tea. It has inherent limits: markdown/math rendering depends on terminal capabilities, complex layouts are hard, and the Webview-based desktop experience is poor. The user wants a full desktop client while keeping the existing Go agent runtime (`pkg/agent`, `pkg/session`, `pkg/tools`, `pkg/config`, etc.).

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Wails Desktop App                       │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │  TS Frontend │  │  TS Frontend │  │    TS Frontend   │  │
│  │  (Chat view) │  │ (Composer)   │  │ (Session picker) │  │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘  │
│         │                 │                   │            │
│  Wails events / bindings  │                   │            │
│         │                 │                   │            │
│  ┌──────┴─────────────────┴───────────────────┴────────┐   │
│  │              Go Backend (in-process)                │   │
│  │  ┌─────────┐ ┌──────────┐ ┌──────────┐ ┌─────────┐ │   │
│  │  │  Agent  │ │ Session  │ │  Config  │ │   MCP   │ │   │
│  │  │ Runner  │ │  Store   │ │  Loader  │ │Registry │ │   │
│  │  └────┬────┘ └────┬─────┘ └────┬─────┘ └────┬────┘ │   │
│  │       └────────────┴────────────┴────────────┘      │   │
│  │                    Events.Bus                       │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

- **Backend:** Existing Go packages wrapped by a thin Wails adapter. No sidecar, no separate HTTP server. The Wails adapter subscribes to `events.Bus` and forwards events to the frontend; frontend calls bound Go methods to send prompts, abort, switch sessions, etc.
- **Frontend:** TypeScript single-page app (recommend React for ecosystem, or Vue/Svelte if team preference). It renders chat messages with full markdown, math (KaTeX), code blocks, tool results, thinking traces, etc.
- **Build:** `wails build` produces a single binary embedding the compiled frontend. Windows and Linux are first-class targets; macOS can be added later.

## Components

### Backend (`app/desktop` or `cmd/lcoder-desktop`)

- `App` struct — Wails application context, holds references to agent, session store, config, bus.
- `AgentService` — Wails-bound methods:
  - `Prompt(text string) error`
  - `Steer(text string) error`
  - `Abort()`
  - `LoadSession(id string) error`
  - `NewSession() (string, error)`
  - `ListSessions() []SessionSummary`
  - `GetMessages() []UIMessage`
  - `SubmitPermission(id string, allow bool, scope string) error`
  - `GetConfig() UIConfig`
- `EventBridge` — subscribes to `events.Bus`, maps agent events to frontend-friendly JSON events, emits via `runtime.EventsEmit`.

### Frontend (`frontend/`)

- `App.tsx` — top-level layout: chat pane, composer, sidebar, status bar.
- `ChatMessage` — renders user/assistant/tool/system messages. Assistant messages support:
  - Markdown via `react-markdown` or `markdown-it`.
  - Math via `react-katex` / `$...$` and `$$...$$`.
  - Collapsible thinking block.
  - Code blocks with syntax highlighting.
- `Composer` — multiline input with slash-command and @file mention support.
- `SessionSidebar` — list of recent sessions, new-session button.
- `PermissionDialog` — modal for tool permission approvals.
- `StatusBar` — model, mode, cost, connection status.

## Data Flow

1. Startup
   - Wails creates backend `App`.
   - Backend loads config, initializes agent, session store, MCP registry.
   - Backend emits `app:ready` event with initial state (messages, tasks, model, mode).
2. User prompt
   - Frontend calls `AgentService.Prompt(text)`.
   - Backend enqueues prompt in agent runner.
3. Streaming response
   - Agent emits `MessageStart`, `MessageUpdate`, `ToolExecutionStart/End`, `TurnEnd`, `Error`.
   - `EventBridge` maps to frontend events (`message:start`, `message:delta`, `tool:start`, etc.).
   - Frontend appends/patches messages in React state.
4. Session switch
   - Frontend calls `AgentService.LoadSession(id)`.
   - Backend updates active session, calls `Agent.SetMessages`, emits `session:loaded` with full message list.
5. Permission
   - Backend emits `permission:request` event with request ID and tool info.
   - Frontend shows dialog; user choice calls `AgentService.SubmitPermission`.

## Error Handling

- Backend errors are emitted as `app:error` events; frontend displays a non-blocking toast.
- Wails binding errors are returned as Go errors and surfaced in the frontend as inline alerts.
- Agent crashes are caught by the runner and surfaced via `ErrorEvent`; the UI stays responsive.

## Security & Permissions

- All file system and shell execution remains in Go backend; frontend has no direct access.
- Permission requests go through the existing `permissions` package and are presented modally.
- MCP servers run in the backend; their status is exposed read-only to the frontend.

## Migration Strategy

1. **Phase 1 — Side-by-side:** Add `cmd/lcoder-desktop` without removing `cmd/lcoder` (TUI). Users can choose which binary to run. This keeps CLI/TUI usable during development.
2. **Phase 2 — Feature parity:** Replicate all TUI features in the desktop app: chat, session management, slash commands, @file mentions, task sidebar, MCP panel, provider setup wizard.
3. **Phase 3 — Replace:** Once desktop app is stable, remove `pkg/tui` and make `cmd/lcoder-desktop` the default `lcoder` binary.

## Testing

- **Backend:** Unit tests for `AgentService` and `EventBridge` using fake agent/session. Keep existing `pkg/agent` tests green.
- **Frontend:** Component tests for `ChatMessage` rendering (markdown, math, thinking, tool results).
- **Integration:** Wails provides test helpers to drive the app headlessly; add a smoke test for startup and a prompt/response round-trip.

## Risks

- **Webview differences:** Windows (WebView2) and Linux (WebKitGTK) may render CSS/JS slightly differently. Use conservative CSS and test on both.
- **Streaming performance:** Large message histories or fast tool result streams must be batched in `EventBridge` to avoid overwhelming the frontend.
- **Build complexity:** Wails requires Node toolchain for frontend builds and platform-specific C dependencies on Linux. CI must be updated.

## Recommended Stack

- **Wails v2** (Go backend + embedded Webview)
- **Frontend:** React + TypeScript + Vite (default Wails template)
- **Markdown:** `react-markdown` + `remark-gfm` + `rehype-highlight`
- **Math:** `react-katex` or `remark-math`
- **Styling:** Tailwind CSS or plain CSS modules

## Non-Goals

- Do not turn the agent into a remote/cloud service; it remains local.
- Do not rewrite the agent loop, context manager, or tool registry in TypeScript.
- Do not support mobile or browser-only deployment in this iteration.
