interface SessionControlsProps {
  onNewSession: () => void
}

export function SessionControls({ onNewSession }: SessionControlsProps) {
  return (
    <div style={{
      padding: '8px',
      borderTop: '1px solid #2a2a4a',
    }}>
      <button
        onClick={onNewSession}
        style={{
          width: '100%',
          padding: '8px',
          backgroundColor: '#16213e',
          border: '1px solid #2a2a4a',
          borderRadius: '4px',
          color: '#61dafb',
          cursor: 'pointer',
          fontSize: '12px',
        }}
      >
        + New Session
      </button>
    </div>
  )
}
