interface SessionControlsProps {
  onNewSession: () => void
  onNewShell: () => void
}

export function SessionControls({ onNewSession, onNewShell }: SessionControlsProps) {
  return (
    <div style={{
      padding: '8px',
      borderTop: '1px solid #2a2a4a',
      display: 'flex',
      gap: '4px',
    }}>
      <button
        onClick={onNewSession}
        style={{
          flex: 1,
          padding: '8px',
          backgroundColor: '#16213e',
          border: '1px solid #2a2a4a',
          borderRadius: '4px',
          color: '#61dafb',
          cursor: 'pointer',
          fontSize: '12px',
        }}
      >
        + Session
      </button>
      <button
        onClick={onNewShell}
        style={{
          flex: 1,
          padding: '8px',
          backgroundColor: '#16213e',
          border: '1px solid #2a2a4a',
          borderRadius: '4px',
          color: '#4caf50',
          cursor: 'pointer',
          fontSize: '12px',
        }}
      >
        + Shell
      </button>
    </div>
  )
}
