import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, act } from '@testing-library/react';
import PermissionDialog from './PermissionDialog';
import type { PermissionRequest } from '../types';

vi.mock('../../wailsjs/runtime', () => ({
  EventsOn: vi.fn(),
}));
vi.mock('../../wailsjs/go/main/AgentService', () => ({
  SubmitPermission: vi.fn(),
}));

import { EventsOn } from '../../wailsjs/runtime';
import { SubmitPermission } from '../../wailsjs/go/main/AgentService';

type EventCallback = (payload: PermissionRequest) => void;

function getPermissionCallback(): EventCallback {
  const calls = vi.mocked(EventsOn).mock.calls;
  const call = calls.find((c) => c[0] === 'permission:request');
  if (!call) throw new Error('permission:request handler not registered');
  return call[1] as unknown as EventCallback;
}

const sample: PermissionRequest = {
  id: 'req-1',
  tool_name: 'bash',
  args: { command: 'rm -rf /tmp/x' },
};

describe('PermissionDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders nothing before a request arrives', () => {
    vi.mocked(EventsOn).mockReturnValue(() => {});
    const { container } = render(<PermissionDialog />);
    expect(container.firstChild).toBeNull();
  });

  it('shows tool name and args on permission:request', () => {
    vi.mocked(EventsOn).mockReturnValue(() => {});
    render(<PermissionDialog />);
    act(() => getPermissionCallback()(sample));
    expect(screen.getByText('bash')).toBeTruthy();
    expect(screen.getByText(/rm -rf/)).toBeTruthy();
  });

  it('submits allow-once and closes', () => {
    vi.mocked(EventsOn).mockReturnValue(() => {});
    render(<PermissionDialog />);
    act(() => getPermissionCallback()(sample));
    fireEvent.click(screen.getByText('允许一次'));
    expect(SubmitPermission).toHaveBeenCalledWith('req-1', true, 'once');
    expect(screen.queryByText('权限请求')).toBeNull();
  });

  it('submits deny and closes', () => {
    vi.mocked(EventsOn).mockReturnValue(() => {});
    render(<PermissionDialog />);
    act(() => getPermissionCallback()(sample));
    fireEvent.click(screen.getByText('拒绝'));
    expect(SubmitPermission).toHaveBeenCalledWith('req-1', false, 'deny');
  });

  it('submits allow-project', () => {
    vi.mocked(EventsOn).mockReturnValue(() => {});
    render(<PermissionDialog />);
    act(() => getPermissionCallback()(sample));
    fireEvent.click(screen.getByText('允许本项目'));
    expect(SubmitPermission).toHaveBeenCalledWith('req-1', true, 'project');
  });

  it('queues multiple requests and processes them in order', () => {
    vi.mocked(EventsOn).mockReturnValue(() => {});
    render(<PermissionDialog />);
    act(() => getPermissionCallback()(sample));
    act(() => getPermissionCallback()({ id: 'req-2', tool_name: 'read_file', args: { path: 'x' } }));
    expect(screen.getByText('bash')).toBeTruthy();
    fireEvent.click(screen.getByText('允许一次'));
    expect(SubmitPermission).toHaveBeenCalledWith('req-1', true, 'once');
    expect(screen.getByText('read_file')).toBeTruthy();
    fireEvent.click(screen.getByText('拒绝'));
    expect(SubmitPermission).toHaveBeenCalledWith('req-2', false, 'deny');
    expect(screen.queryByText('权限请求')).toBeNull();
  });
});
