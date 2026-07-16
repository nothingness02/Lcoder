import { useEffect, useState } from 'react';
import { EventsOn } from '../wailsjs/runtime';
import { GetConfig, LoadSession } from '../wailsjs/go/main/AgentService';
import ChatView from './components/ChatView';
import SessionSidebar from './components/SessionSidebar';
import PermissionDialog from './components/PermissionDialog';
import StatusBar from './components/StatusBar';
import FatalError from './components/FatalError';
import type { UIConfig } from './types';
import './App.css';

function App() {
  const [currentSessionId, setCurrentSessionId] = useState<string | undefined>();
  const [fatal, setFatal] = useState<string | null>(null);

  useEffect(() => {
    GetConfig().then((cfg) => setCurrentSessionId(cfg.session_id));

    const cleanups: (() => void)[] = [];
    cleanups.push(
      EventsOn('app:ready', (payload: { session_id: string }) => {
        setCurrentSessionId(payload.session_id);
      })
    );
    cleanups.push(
      EventsOn('app:fatal', (payload: { message: string }) => {
        setFatal(payload.message);
      })
    );
    return () => cleanups.forEach((fn) => fn());
  }, []);

  const handleSwitch = (id: string) => {
    setCurrentSessionId(id);
    LoadSession(id);
  };

  if (fatal) {
    return <FatalError message={fatal} />;
  }

  return (
    <div className="app">
      <SessionSidebar currentSessionId={currentSessionId} onSwitch={handleSwitch} />
      <div className="main">
        <ChatView />
        <StatusBar />
      </div>
      <PermissionDialog />
    </div>
  );
}

export default App;
