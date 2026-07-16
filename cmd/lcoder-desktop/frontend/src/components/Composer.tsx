import { useState, useRef, KeyboardEvent, FormEvent } from 'react';
import './Composer.css';

export interface ComposerProps {
  onSend: (text: string) => void;
  disabled?: boolean;
}

export default function Composer({ onSend, disabled }: ComposerProps) {
  const [text, setText] = useState('');
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const submit = () => {
    const trimmed = text.trim();
    if (!trimmed || disabled) return;
    onSend(trimmed);
    setText('');
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto';
    }
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  const handleInput = (e: FormEvent<HTMLTextAreaElement>) => {
    const el = e.currentTarget;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  };

  return (
    <div className="composer">
      <textarea
        ref={textareaRef}
        className="composer-input"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={handleKeyDown}
        onInput={handleInput}
        placeholder={disabled ? '等待中...' : '输入消息，Shift+Enter 换行'}
        disabled={disabled}
        rows={1}
      />
      <button
        className="composer-send"
        onClick={submit}
        disabled={disabled || !text.trim()}
      >
        发送
      </button>
    </div>
  );
}
