import { useState } from 'react'
import type { Session } from '../../types/session'

interface SessionListProps {
  sessions: Session[]
  onSelect: (session: Session) => void
  onTerminate: (id: string) => void
  onRestart: (session: Session) => void
}

const stateColors: Record<string, string> = {
  running: '#4caf50',
  stopped: '#f44336',
  terminated: '#9e9e9e',
  orphaned: '#ff9800',
}

export function SessionList({ sessions, onSelect, onTerminate, onRestart }: SessionListProps) {
  // Track which session has the delete confirmation showing.
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)

  const handleDelete = (id: string) => {
    onTerminate(id)
    setConfirmDelete(null)
  }

  return (
    <div style={{ padding: '8px', overflow: 'auto', height: '100%' }}>
      <div style={{
        fontSize: '11px',
        color: '#666',
        textTransform: 'uppercase',
        letterSpacing: '1px',
        marginBottom: '8px',
        padding: '0 4px',
      }}>
        Sessions
      </div>
      {sessions.length === 0 && (
        <div style={{ color: '#555', fontSize: '12px', padding: '4px' }}>
          No sessions
        </div>
      )}
      {sessions.map(session => (
        <div
          key={session.id}
          onClick={() => onSelect(session)}
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            padding: '6px 8px',
            borderRadius: '4px',
            cursor: 'pointer',
            marginBottom: '2px',
            fontSize: '12px',
          }}
          onMouseEnter={(e) => { e.currentTarget.style.backgroundColor = '#1e1e3a' }}
          onMouseLeave={(e) => {
            e.currentTarget.style.backgroundColor = 'transparent'
            // Cancel confirm if mouse leaves the row.
            if (confirmDelete === session.id) setConfirmDelete(null)
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', minWidth: 0 }}>
            <span style={{
              width: '6px',
              height: '6px',
              borderRadius: '50%',
              backgroundColor: stateColors[session.state] || '#9e9e9e',
              flexShrink: 0,
            }} />
            <span style={{ color: '#ccc', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {session.name}
            </span>
          </div>
          <div style={{ display: 'flex', gap: '4px', flexShrink: 0, alignItems: 'center' }}>
            {(session.state === 'stopped' || session.state === 'orphaned') && (
              <button
                onClick={(e) => { e.stopPropagation(); onRestart(session) }}
                style={actionBtn}
                title="Restart"
              >
                <span style={{ color: '#4caf50', fontSize: '12px' }}>&#9654;</span>
              </button>
            )}
            {/* Delete: two-click pattern */}
            {confirmDelete === session.id ? (
              <>
                <button
                  onClick={(e) => { e.stopPropagation(); setConfirmDelete(null) }}
                  style={actionBtn}
                  title="Cancel"
                >
                  <span style={{ color: '#888', fontSize: '11px' }}>&#10005;</span>
                </button>
                <button
                  onClick={(e) => { e.stopPropagation(); handleDelete(session.id) }}
                  style={actionBtn}
                  title="Confirm delete"
                >
                  <span style={{ color: '#4caf50', fontSize: '11px' }}>&#10003;</span>
                </button>
              </>
            ) : (
              <button
                onClick={(e) => { e.stopPropagation(); setConfirmDelete(session.id) }}
                style={actionBtn}
                title="Delete session"
              >
                <span style={{ color: '#888', fontSize: '11px' }}>&#128465;</span>
              </button>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}

const actionBtn: React.CSSProperties = {
  background: 'none',
  border: 'none',
  cursor: 'pointer',
  padding: '0 2px',
  display: 'flex',
  alignItems: 'center',
}
