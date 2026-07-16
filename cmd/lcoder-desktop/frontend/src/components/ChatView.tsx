import { useEffect, useRef, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime';
import { GetMessages, Prompt } from '../../wailsjs/go/main/AgentService';
import ChatMessage from './ChatMessage';
import Composer from './Composer';
import { patchMessage, finalizeMessage, startTool, endTool, toUIMessages } from '../utils/eventHandlers';
import type { UIMessage, UIToolResult } from '../types';
import './ChatView.css';

export default function ChatView() {
  const [messages, setMessages] = useState<UIMessage[]>([]);
  const [busy, setBusy] = useState(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    GetMessages().then((msgs) => setMessages(toUIMessages(msgs)));

    const cleanups: (() => void)[] = [];
    cleanups.push(
      EventsOn('app:ready', (payload: { messages: UIMessage[] }) => {
        setMessages(payload.messages);
      })
    );
    cleanups.push(
      EventsOn('session:loaded', (payload: { messages: UIMessage[] }) => {
        setMessages(payload.messages);
      })
    );
    cleanups.push(
      EventsOn('message:start', (m: UIMessage) => {
        if (m.role === 'tool_result') return;
        setMessages((prev) => [...prev, m]);
      })
    );
    cleanups.push(
      EventsOn('message:delta', (payload: { id: string; delta: string; is_thinking: boolean }) => {
        setMessages((prev) => patchMessage(prev, payload.id, payload.delta, payload.is_thinking));
      })
    );
    cleanups.push(
      EventsOn('message:end', (m: UIMessage) => {
        if (m.role === 'tool_result') return;
        setMessages((prev) => finalizeMessage(prev, m));
      })
    );
    cleanups.push(
      EventsOn('tool:start', (payload: { id: string; name: string; args: Record<string, any> }) => {
        setMessages((prev) => startTool(prev, payload.id, payload.name, payload.args));
      })
    );
    cleanups.push(
      EventsOn('tool:end', (result: UIToolResult) => {
        setMessages((prev) => endTool(prev, result));
      })
    );
    cleanups.push(
      EventsOn('turn:start', () => setBusy(true))
    );
    cleanups.push(
      EventsOn('turn:end', () => setBusy(false))
    );
    cleanups.push(
      EventsOn('app:error', (payload: { message: string }) => {
        alert(`错误: ${payload.message}`);
      })
    );

    return () => cleanups.forEach((fn) => fn());
  }, []);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  const send = (text: string) => {
    Prompt(text);
  };

  return (
    <div className="chat-view">
      <div className="messages">
        {messages.map((m) => (
          <ChatMessage key={m.id} msg={m} />
        ))}
        <div ref={bottomRef} />
      </div>
      <Composer onSend={send} disabled={busy} />
    </div>
  );
}
