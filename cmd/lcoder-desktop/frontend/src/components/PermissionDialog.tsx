import { useEffect, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime';
import { SubmitPermission } from '../../wailsjs/go/main/AgentService';
import type { PermissionRequest } from '../types';
import './PermissionDialog.css';

export default function PermissionDialog() {
  const [requests, setRequests] = useState<PermissionRequest[]>([]);

  useEffect(() => {
    return EventsOn('permission:request', (payload: PermissionRequest) => {
      setRequests((prev) => [...prev, payload]);
    });
  }, []);

  const request = requests[0] ?? null;

  const respond = (allow: boolean, scope: string) => {
    if (!request) return;
    SubmitPermission(request.id, allow, scope);
    setRequests((prev) => prev.slice(1));
  };

  if (!request) return null;

  return (
    <div className="permission-overlay">
      <div className="permission-dialog">
        <h3>权限请求</h3>
        <p>
          <strong>{request.tool_name}</strong>
        </p>
        <pre>{JSON.stringify(request.args, null, 2)}</pre>
        <div className="permission-actions">
          <button className="deny" onClick={() => respond(false, 'deny')}>
            拒绝
          </button>
          <button onClick={() => respond(true, 'once')}>允许一次</button>
          <button onClick={() => respond(true, 'project')}>允许本项目</button>
        </div>
      </div>
    </div>
  );
}
