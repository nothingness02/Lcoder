import { useEffect, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime';
import { GetConfig } from '../../wailsjs/go/main/AgentService';
import type { UIConfig } from '../types';
import './StatusBar.css';

export default function StatusBar() {
  const [config, setConfig] = useState<UIConfig | null>(null);
  const [compacting, setCompacting] = useState(false);

  useEffect(() => {
    GetConfig().then(setConfig);
    return EventsOn('app:ready', (payload: { config: UIConfig }) => {
      setConfig(payload.config);
    });
  }, []);

  useEffect(() => {
    return EventsOn('status:compacting', (payload: { compacting: boolean }) => {
      setCompacting(payload.compacting);
    });
  }, []);

  return (
    <div className="status-bar">
      <span>{config?.provider}/{config?.model}</span>
      <span>{config?.mode}</span>
      <span className="cwd" title={config?.cwd}>{config?.cwd}</span>
      {compacting && <span className="compacting">压缩中…</span>}
    </div>
  );
}
