import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, act, waitFor } from '@testing-library/react';
import StatusBar from './StatusBar';
import type { UIConfig } from '../types';

vi.mock('../../wailsjs/runtime', () => ({
  EventsOn: vi.fn(),
}));
vi.mock('../../wailsjs/go/main/AgentService', () => ({
  GetConfig: vi.fn(),
}));

import { EventsOn } from '../../wailsjs/runtime';
import { GetConfig } from '../../wailsjs/go/main/AgentService';

const config: UIConfig = {
  provider: 'openai',
  model: 'gpt-5',
  mode: 'code',
  cwd: 'D:/work/project',
};

function getCallback(name: string): (payload: any) => void {
  const call = vi.mocked(EventsOn).mock.calls.find((c) => c[0] === name);
  if (!call) throw new Error(`${name} handler not registered`);
  return call[1] as unknown as (payload: any) => void;
}

describe('StatusBar', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(EventsOn).mockReturnValue(() => {});
    vi.mocked(GetConfig).mockResolvedValue(config);
  });

  it('renders provider/model, mode and cwd from GetConfig', async () => {
    render(<StatusBar />);
    await waitFor(() => expect(screen.getByText('openai/gpt-5')).toBeTruthy());
    expect(screen.getByText('code')).toBeTruthy();
    expect(screen.getByText('D:/work/project')).toBeTruthy();
  });

  it('updates config on app:ready', async () => {
    render(<StatusBar />);
    await waitFor(() => screen.getByText('openai/gpt-5'));
    act(() => getCallback('app:ready')({ config: { ...config, model: 'gpt-5-mini' } }));
    expect(screen.getByText('openai/gpt-5-mini')).toBeTruthy();
  });

  it('shows compacting indicator only while compacting', async () => {
    render(<StatusBar />);
    await waitFor(() => screen.getByText('openai/gpt-5'));
    expect(screen.queryByText('压缩中…')).toBeNull();
    act(() => getCallback('status:compacting')({ compacting: true }));
    expect(screen.getByText('压缩中…')).toBeTruthy();
    act(() => getCallback('status:compacting')({ compacting: false }));
    expect(screen.queryByText('压缩中…')).toBeNull();
  });
});
