import { TerminalView } from '../terminal/Terminal'
import { TerminalToolbar } from '../terminal/TerminalToolbar'
import { useSessionStore } from '../../store/sessionStore'

interface PaneContainerProps {
  sessionId: string | null
  onTerminate?: () => void
  onSplitH?: () => void
  onSplitV?: () => void
}

export function PaneContainer({ sessionId, onTerminate, onSplitH, onSplitV }: PaneContainerProps) {
  const session = useSessionStore(s => s.sessions.find(sess => sess.id === sessionId))

  if (!sessionId) {
    return (
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        height: '100%',
        color: '#555',
      }}>
        No session assigned
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', width: '100%' }}>
      <TerminalToolbar
        session={session}
        onTerminate={onTerminate}
        onSplitH={onSplitH}
        onSplitV={onSplitV}
      />
      <div style={{ flex: 1, minHeight: 0 }}>
        <TerminalView sessionId={sessionId} />
      </div>
    </div>
  )
}
