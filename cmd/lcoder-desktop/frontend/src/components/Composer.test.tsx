import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import Composer from './Composer';

describe('Composer', () => {
  it('sends on Enter', () => {
    const onSend = vi.fn();
    render(<Composer onSend={onSend} />);
    const input = screen.getByRole('textbox');
    fireEvent.change(input, { target: { value: 'hello' } });
    fireEvent.keyDown(input, { key: 'Enter', code: 'Enter' });
    expect(onSend).toHaveBeenCalledWith('hello');
  });

  it('does not send on Shift+Enter', () => {
    const onSend = vi.fn();
    render(<Composer onSend={onSend} />);
    const input = screen.getByRole('textbox');
    fireEvent.change(input, { target: { value: 'line1' } });
    fireEvent.keyDown(input, { key: 'Enter', shiftKey: true, code: 'Enter' });
    expect(onSend).not.toHaveBeenCalled();
  });
});
