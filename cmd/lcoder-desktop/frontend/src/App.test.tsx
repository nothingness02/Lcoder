import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, act, waitFor } from '@testing-library/react';

vi.mock('../wailsjs/runtime', () => ({
  EventsOn: vi.fn(),
}));

vi.mock('../wailsjs/go/main/AgentService', () => ({
  LoadSession: vi.fn(),
  ListSessions: vi.fn(),
  NewSession: vi.fn(),
  GetMessages: vi.fn(),
  GetConfig: vi.fn(),
  Prompt: vi.fn(),
  Steer: vi.fn(),
  Abort: vi.fn(),
  SubmitPermission: vi.fn(),
}));

import App from './App';
import { EventsOn } from '../wailsjs/runtime';
import { ListSessions, GetMessages, GetConfig } from '../wailsjs/go/main/AgentService';

type EventCallback = (payload: any) => void;

function getCallback(name: string): EventCallback {
  const call = vi.mocked(EventsOn).mock.calls.find((c) => c[0] === name);
  if (!call) throw new Error(`${name} handler not registered`);
  return call[1] as unknown as EventCallback;
}

describe('App fatal error', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(EventsOn).mockReturnValue(() => {});
    vi.mocked(ListSessions).mockResolvedValue([]);
    vi.mocked(GetMessages).mockResolvedValue([]);
    vi.mocked(GetConfig).mockResolvedValue({
      provider: 'p',
      model: 'm',
      mode: 'code',
      cwd: '/',
      session_id: 'init-id',
    });
  });

  it('renders fatal error overlay on app:fatal', () => {
    render(<App />);
    act(() => getCallback('app:fatal')({ message: 'load config failed' }));
    expect(screen.getByText('启动失败')).toBeTruthy();
    expect(screen.getByText('load config failed')).toBeTruthy();
  });
});

describe('App session highlight', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(EventsOn).mockReturnValue(() => {});
    vi.mocked(ListSessions).mockResolvedValue([{ id: 'init-id', created_at: 1 }]);
    vi.mocked(GetMessages).mockResolvedValue([]);
    vi.mocked(GetConfig).mockResolvedValue({
      provider: 'p',
      model: 'm',
      mode: 'code',
      cwd: '/',
      session_id: 'init-id',
    });
  });

  it('highlights the initial session from GetConfig', async () => {
    render(<App />);
    const item = await waitFor(() => screen.getByText('init-id'));
    expect(item.className).toBe('active');
  });
});
