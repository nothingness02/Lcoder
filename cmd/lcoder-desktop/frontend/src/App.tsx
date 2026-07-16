import { useState } from 'react';
import { LoadSession } from '../wailsjs/go/main/AgentService';
import ChatView from './components/ChatView';
import SessionSidebar from './components/SessionSidebar';
import PermissionDialog from './components/PermissionDialog';
import StatusBar from './components/StatusBar';
import './App.css';

function App() {
  const [currentSessionId, setCurrentSessionId] = useState<string | undefined>();

  const handleSwitch = (id: string) => {
    setCurrentSessionId(id);
    LoadSession(id);
  };

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
