import type { FatalErrorProps } from '../types';
import './FatalError.css';

export default function FatalError({ message }: FatalErrorProps) {
  return (
    <div className="fatal-error">
      <div className="fatal-error-inner">
        <h1>启动失败</h1>
        <pre>{message}</pre>
        <p>请检查配置后重启应用。</p>
      </div>
    </div>
  );
}
