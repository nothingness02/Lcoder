import { describe, it, expect } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import ChatMessage from './ChatMessage';
import type { UIMessage } from '../types';

describe('ChatMessage', () => {
  it('renders user text', () => {
    const msg: UIMessage = { id: '1', role: 'user', text: 'hello' };
    render(<ChatMessage msg={msg} />);
    expect(screen.getByText('hello')).toBeInTheDocument();
  });

  it('collapses thinking by default', () => {
    const msg: UIMessage = {
      id: '2',
      role: 'assistant',
      text: 'answer',
      thinking: 'reasoning steps',
    };
    render(<ChatMessage msg={msg} />);
    expect(screen.queryByText('reasoning steps')).not.toBeInTheDocument();
    fireEvent.click(screen.getByText('显示思考'));
    expect(screen.getByText('reasoning steps')).toBeInTheDocument();
  });

  it('renders markdown heading', () => {
    const msg: UIMessage = {
      id: '3',
      role: 'assistant',
      text: '# Title',
    };
    render(<ChatMessage msg={msg} />);
    expect(screen.getByRole('heading', { name: 'Title' })).toBeInTheDocument();
  });

  it('renders system message as banner', () => {
    const msg: UIMessage = { id: '4', role: 'system', text: 'system prompt' };
    render(<ChatMessage msg={msg} />);
    expect(screen.getByText('system prompt')).toBeInTheDocument();
  });

  it('renders notification message as banner', () => {
    const msg: UIMessage = { id: '5', role: 'notification', text: 'notification' };
    render(<ChatMessage msg={msg} />);
    expect(screen.getByText('notification')).toBeInTheDocument();
  });
});
