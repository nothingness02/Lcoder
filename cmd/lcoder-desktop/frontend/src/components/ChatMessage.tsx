import { useState } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import rehypeKatex from 'rehype-katex';
import type { UIMessage } from '../types';
import './ChatMessage.css';

export interface ChatMessageProps {
  msg: UIMessage;
}

export default function ChatMessage({ msg }: ChatMessageProps) {
  const [showThinking, setShowThinking] = useState(false);

  if (msg.role === 'user') {
    return (
      <div className="chat-message user">
        <div className="bubble">{msg.text}</div>
      </div>
    );
  }

  if (msg.role === 'system' || msg.role === 'notification') {
    return (
      <div className="chat-message system">
        <div className="system-banner">{msg.text}</div>
      </div>
    );
  }

  if (msg.role === 'tool_result') {
    return (
      <div className="chat-message tool">
        <pre>{msg.tool_result?.output ?? msg.text}</pre>
      </div>
    );
  }

  return (
    <div className="chat-message assistant">
      {msg.thinking && (
        <div className="thinking">
          <button
            className="thinking-toggle"
            onClick={() => setShowThinking(!showThinking)}
          >
            {showThinking ? '隐藏思考' : '显示思考'}
          </button>
          {showThinking && <pre className="thinking-content">{msg.thinking}</pre>}
        </div>
      )}
      {msg.text && (
        <div className="markdown-body">
          <ReactMarkdown
            remarkPlugins={[remarkGfm, remarkMath]}
            rehypePlugins={[rehypeKatex]}
          >
            {msg.text}
          </ReactMarkdown>
        </div>
      )}
      {msg.tool_calls?.map((tc) => (
        <div key={tc.id} className="tool-call">
          <div className="tool-call-header">
            {tc.name}({JSON.stringify(tc.arguments)})
          </div>
          {tc.result && (
            <pre className={tc.result.is_error ? 'tool-error' : ''}>
              {tc.result.output}
            </pre>
          )}
        </div>
      ))}
    </div>
  );
}
