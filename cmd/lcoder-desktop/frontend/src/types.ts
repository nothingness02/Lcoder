export interface UIToolResult {
  tool_call_id: string;
  name: string;
  output: string;
  is_error: boolean;
}

export interface UIToolCall {
  id: string;
  name: string;
  arguments: Record<string, any>;
  result?: UIToolResult;
}

export interface UIMessage {
  id: string;
  role: 'user' | 'assistant' | 'tool_result' | 'system' | 'notification';
  text?: string;
  thinking?: string;
  tool_calls?: UIToolCall[];
  tool_result?: UIToolResult;
  timestamp?: number;
}

export interface SessionSummary {
  id: string;
  created_at: number;
}

export interface UIConfig {
  provider: string;
  model: string;
  mode: string;
  cwd: string;
  session_id: string;
}

export interface PermissionRequest {
  id: string;
  tool_name: string;
  args: Record<string, any>;
}

export interface FatalErrorProps {
  message: string;
}
