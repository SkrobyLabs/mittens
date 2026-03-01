import { Allotment } from 'allotment'
import 'allotment/dist/style.css'
import { PaneContainer } from './PaneContainer'
import type { Pane, SplitDirection } from '../../store/layoutStore'
import { useSessionStore } from '../../store/sessionStore'
import type { Session } from '../../types/session'

interface SplitPaneProps {
  panes: Pane[]
  direction: SplitDirection
  tabId: string
  onRename?: (id: string, name: string) => void
  onDuplicate?: (session: Session) => void
  onEdit?: (session: Session) => void
  onOpenShell?: (workDir: string) => void
  onOpenMittens?: (workDir: string) => void
}

export function SplitPane({ panes, direction, tabId, onRename, onDuplicate, onEdit, onOpenShell, onOpenMittens }: SplitPaneProps) {
  const terminateSession = useSessionStore(s => s.terminateSession)

  if (panes.length === 0) {
    return (
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        height: '100%',
        color: '#555',
      }}>
        Empty tab
      </div>
    )
  }

  if (panes.length === 1) {
    const pane = panes[0]
    return (
      <PaneContainer
        sessionId={pane.sessionId}
        onTerminate={() => pane.sessionId && terminateSession(pane.sessionId)}
        onRename={onRename}
        onDuplicate={onDuplicate}
        onEdit={onEdit}
        onOpenShell={onOpenShell}
        onOpenMittens={onOpenMittens}
      />
    )
  }

  return (
    <Allotment vertical={direction === 'vertical'}>
      {panes.map(pane => (
        <Allotment.Pane key={pane.id} minSize={100}>
          <PaneContainer
            sessionId={pane.sessionId}
            onTerminate={() => pane.sessionId && terminateSession(pane.sessionId)}
            onRename={onRename}
            onDuplicate={onDuplicate}
            onEdit={onEdit}
            onOpenShell={onOpenShell}
            onOpenMittens={onOpenMittens}
          />
        </Allotment.Pane>
      ))}
    </Allotment>
  )
}
