import { TerminalView } from '../terminal/Terminal'
import { TerminalToolbar } from '../terminal/TerminalToolbar'
import { useSessionStore } from '../../store/sessionStore'
import type { Session } from '../../types/session'

interface PaneContainerProps {
  sessionId: string | null
  onTerminate?: () => void
  onRename?: (id: string, name: string) => void
  onDuplicate?: (session: Session) => void
  onEdit?: (session: Session) => void
  onOpenShell?: (workDir: string) => void
  onOpenMittens?: (workDir: string) => void
}

export function PaneContainer({ sessionId, onTerminate, onRename, onDuplicate, onEdit, onOpenShell, onOpenMittens }: PaneContainerProps) {
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
        onRename={onRename}
        onDuplicate={onDuplicate}
        onEdit={onEdit}
        onOpenShell={onOpenShell}
        onOpenMittens={onOpenMittens}
      />
      <div style={{ flex: 1, minHeight: 0, paddingLeft: 6 }}>
        <TerminalView sessionId={sessionId} />
      </div>
    </div>
  )
}
