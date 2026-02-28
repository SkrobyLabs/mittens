import type { Session } from '../../types/session'

interface TerminalToolbarProps {
  session: Session | undefined
  onTerminate?: () => void
  onSplitH?: () => void
  onSplitV?: () => void
}

const stateColors: Record<string, string> = {
  running: '#4caf50',
  stopped: '#f44336',
  terminated: '#9e9e9e',
  orphaned: '#ff9800',
}

export function TerminalToolbar({ session, onTerminate, onSplitH, onSplitV }: TerminalToolbarProps) {
  if (!session) return null

  return (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      padding: '4px 8px',
      backgroundColor: '#16213e',
      borderBottom: '1px solid #2a2a4a',
      fontSize: '12px',
      height: '28px',
      flexShrink: 0,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        <span style={{
          width: '8px',
          height: '8px',
          borderRadius: '50%',
          backgroundColor: stateColors[session.state] || '#9e9e9e',
          display: 'inline-block',
        }} />
        <span style={{ color: '#ccc' }}>{session.name}</span>
        <span style={{ color: '#666' }}>({session.state})</span>
        <span style={{ color: '#555' }}>{session.config.workDir}</span>
      </div>
      <div style={{ display: 'flex', gap: '4px' }}>
        <button onClick={onSplitH} style={btnStyle} title="Split horizontal">
          |||
        </button>
        <button onClick={onSplitV} style={btnStyle} title="Split vertical">
          ---
        </button>
        {session.state === 'running' && (
          <button onClick={onTerminate} style={{ ...btnStyle, color: '#f44336' }} title="Terminate">
            X
          </button>
        )}
      </div>
    </div>
  )
}

const btnStyle: React.CSSProperties = {
  background: 'none',
  border: '1px solid #333',
  color: '#888',
  cursor: 'pointer',
  padding: '1px 6px',
  borderRadius: '3px',
  fontSize: '11px',
}
