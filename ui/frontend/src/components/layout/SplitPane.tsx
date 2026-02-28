import { Allotment } from 'allotment'
import 'allotment/dist/style.css'
import { PaneContainer } from './PaneContainer'
import type { Pane, SplitDirection } from '../../store/layoutStore'
import { useSessionStore } from '../../store/sessionStore'

interface SplitPaneProps {
  panes: Pane[]
  direction: SplitDirection
  tabId: string
  onSplitH?: (paneId: string) => void
  onSplitV?: (paneId: string) => void
}

export function SplitPane({ panes, direction, tabId, onSplitH, onSplitV }: SplitPaneProps) {
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
        onSplitH={() => onSplitH?.(pane.id)}
        onSplitV={() => onSplitV?.(pane.id)}
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
            onSplitH={() => onSplitH?.(pane.id)}
            onSplitV={() => onSplitV?.(pane.id)}
          />
        </Allotment.Pane>
      ))}
    </Allotment>
  )
}
