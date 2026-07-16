import { describe, it, expect } from 'vitest';
import { patchMessage, finalizeMessage, startTool, endTool } from './eventHandlers';
import type { UIMessage, UIToolResult } from '../types';

describe('patchMessage', () => {
  it('appends delta to existing assistant message', () => {
    const msgs: UIMessage[] = [{ id: 'a1', role: 'assistant', text: 'hello' }];
    const next = patchMessage(msgs, 'a1', ' world', false);
    expect(next[0].text).toBe('hello world');
  });

  it('creates placeholder when message missing', () => {
    const next = patchMessage([], 'a1', 'hi', false);
    expect(next).toHaveLength(1);
    expect(next[0].text).toBe('hi');
  });
});

describe('finalizeMessage', () => {
  it('replaces existing message', () => {
    const msgs: UIMessage[] = [{ id: 'a1', role: 'assistant', text: 'old' }];
    const next = finalizeMessage(msgs, { id: 'a1', role: 'assistant', text: 'new' });
    expect(next[0].text).toBe('new');
  });
});

describe('startTool', () => {
  it('appends tool call to last assistant message', () => {
    const msgs: UIMessage[] = [{ id: 'a1', role: 'assistant', text: 'working' }];
    const next = startTool(msgs, 't1', 'bash', { command: 'ls' });
    expect(next[0].tool_calls).toHaveLength(1);
    expect(next[0].tool_calls?.[0].name).toBe('bash');
  });

  it('does not duplicate existing tool call', () => {
    const msgs: UIMessage[] = [
      {
        id: 'a1',
        role: 'assistant',
        tool_calls: [{ id: 't1', name: 'bash', arguments: { command: 'ls' } }],
      },
    ];
    const next = startTool(msgs, 't1', 'bash', { command: 'ls' });
    expect(next).toBe(msgs);
  });
});

describe('endTool', () => {
  it('attaches result to matching assistant tool call', () => {
    const msgs: UIMessage[] = [
      {
        id: 'a1',
        role: 'assistant',
        tool_calls: [{ id: 't1', name: 'bash', arguments: { command: 'ls' } }],
      },
    ];
    const result: UIToolResult = { tool_call_id: 't1', name: 'bash', output: 'ok', is_error: false };
    const next = endTool(msgs, result);
    expect(next[0].tool_calls?.[0].result?.output).toBe('ok');
  });
});
