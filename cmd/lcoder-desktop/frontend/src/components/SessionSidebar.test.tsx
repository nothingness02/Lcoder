import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import SessionSidebar from './SessionSidebar';

vi.mock('../../wailsjs/go/main/AgentService', () => ({
  ListSessions: vi.fn(),
  NewSession: vi.fn(),
}));

import { ListSessions, NewSession } from '../../wailsjs/go/main/AgentService';

describe('SessionSidebar', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders sessions from ListSessions', async () => {
    vi.mocked(ListSessions).mockResolvedValue([
      { id: 's1', created_at: 1 },
      { id: 's2', created_at: 2 },
    ]);
    render(<SessionSidebar />);
    await waitFor(() => expect(screen.getByText('s1')).toBeTruthy());
    expect(screen.getByText('s2')).toBeTruthy();
  });

  it('marks current session active and calls onSwitch on click', async () => {
    vi.mocked(ListSessions).mockResolvedValue([{ id: 's1', created_at: 1 }]);
    const onSwitch = vi.fn();
    render(<SessionSidebar currentSessionId="s1" onSwitch={onSwitch} />);
    const item = await waitFor(() => screen.getByText('s1'));
    expect(item.className).toBe('active');
    fireEvent.click(item);
    expect(onSwitch).toHaveBeenCalledWith('s1');
  });

  it('creates a new session via NewSession and switches to it', async () => {
    vi.mocked(ListSessions).mockResolvedValue([]);
    vi.mocked(NewSession).mockResolvedValue('new-id');
    const onSwitch = vi.fn();
    render(<SessionSidebar onSwitch={onSwitch} />);
    fireEvent.click(screen.getByText('+ 新建'));
    await waitFor(() => expect(onSwitch).toHaveBeenCalledWith('new-id'));
  });
});
