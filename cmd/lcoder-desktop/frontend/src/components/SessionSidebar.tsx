import { useEffect, useState } from 'react';
import { ListSessions, NewSession } from '../../wailsjs/go/main/AgentService';
import type { SessionSummary } from '../types';
import './SessionSidebar.css';

export interface SessionSidebarProps {
  currentSessionId?: string;
  onSwitch?: (id: string) => void;
}

export default function SessionSidebar({ currentSessionId, onSwitch }: SessionSidebarProps) {
  const [sessions, setSessions] = useState<SessionSummary[]>([]);

  useEffect(() => {
    ListSessions().then(setSessions);
  }, [currentSessionId]);

  const create = async () => {
    const id = await NewSession();
    if (onSwitch) onSwitch(id);
    setSessions(await ListSessions());
  };

  return (
    <div className="session-sidebar">
      <div className="sidebar-header">
        <span>会话</span>
        <button onClick={create}>+ 新建</button>
      </div>
      <ul>
        {sessions.map((s) => (
          <li
            key={s.id}
            className={s.id === currentSessionId ? 'active' : ''}
            onClick={() => onSwitch && onSwitch(s.id)}
          >
            {s.id}
          </li>
        ))}
      </ul>
    </div>
  );
}
