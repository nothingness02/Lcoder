import type { UIMessage, UIToolCall, UIToolResult } from '../types';
import type { desktop } from '../../wailsjs/go/models';

export function patchMessage(
  messages: UIMessage[],
  id: string,
  delta: string,
  isThinking: boolean
): UIMessage[] {
  const idx = messages.findIndex((m) => m.id === id);
  if (idx === -1) {
    return [
      ...messages,
      {
        id,
        role: 'assistant',
        text: isThinking ? '' : delta,
        thinking: isThinking ? delta : '',
      },
    ];
  }

  const next = [...messages];
  const m = { ...next[idx] };
  if (isThinking) {
    m.thinking = (m.thinking ?? '') + delta;
  } else {
    m.text = (m.text ?? '') + delta;
  }
  next[idx] = m;
  return next;
}

export function finalizeMessage(messages: UIMessage[], finalized: UIMessage): UIMessage[] {
  const idx = messages.findIndex((m) => m.id === finalized.id);
  if (idx === -1) {
    return [...messages, finalized];
  }
  const next = [...messages];
  next[idx] = finalized;
  return next;
}

export function startTool(
  messages: UIMessage[],
  id: string,
  name: string,
  args: Record<string, any>
): UIMessage[] {
  const lastIdx = messages.length - 1;
  if (lastIdx >= 0 && messages[lastIdx].role === 'assistant') {
    const last = messages[lastIdx];
    const existing = (last.tool_calls ?? []).find((tc) => tc.id === id);
    if (existing) {
      return messages;
    }
    const next = [...messages];
    const m = { ...next[lastIdx] };
    m.tool_calls = [...(m.tool_calls ?? []), { id, name, arguments: args }];
    next[lastIdx] = m;
    return next;
  }
  return [
    ...messages,
    {
      id: `assistant-tool-${id}`,
      role: 'assistant',
      tool_calls: [{ id, name, arguments: args }],
    },
  ];
}

export function endTool(messages: UIMessage[], result: UIToolResult): UIMessage[] {
  return messages.map((m) => {
    if (m.role === 'assistant' && m.tool_calls) {
      const calls = m.tool_calls.map((tc: UIToolCall) =>
        tc.id === result.tool_call_id ? { ...tc, result } : tc
      );
      return { ...m, tool_calls: calls };
    }
    if (m.role === 'tool_result' && m.tool_result?.tool_call_id === result.tool_call_id) {
      return { ...m, tool_result: result };
    }
    return m;
  });
}

export function toUIMessage(m: desktop.UIMessage): UIMessage {
  const role = (['user', 'assistant', 'tool_result', 'system', 'notification'] as const).includes(
    m.role as UIMessage['role']
  )
    ? (m.role as UIMessage['role'])
    : 'system';
  return {
    id: m.id,
    role,
    text: m.text,
    thinking: m.thinking,
    tool_calls: m.tool_calls as UIToolCall[] | undefined,
    tool_result: m.tool_result as UIToolResult | undefined,
    timestamp: m.timestamp,
  };
}

export function toUIMessages(msgs: desktop.UIMessage[]): UIMessage[] {
  return msgs.map(toUIMessage);
}
