import { useState, useCallback } from 'react'
import type { Session } from '../../types/session'
import { ContextMenu } from '../shared/ContextMenu'
import type { MenuEntry } from '../shared/ContextMenu'

interface TerminalToolbarProps {
  session: Session | undefined
  onTerminate?: () => void
  onRename?: (id: string, name: string) => void
  onDuplicate?: (session: Session) => void
  onEdit?: (session: Session) => void
  onOpenShell?: (workDir: string) => void
  onOpenMittens?: (workDir: string) => void
}

const stateColors: Record<string, string> = {
  running: '#4caf50',
  stopped: '#f44336',
  terminated: '#9e9e9e',
  orphaned: '#ff9800',
}

interface ContextState {
  x: number
  y: number
}

export function TerminalToolbar({
  session, onTerminate, onRename, onDuplicate, onEdit, onOpenShell, onOpenMittens,
}: TerminalToolbarProps) {
  const [ctx, setCtx] = useState<ContextState | null>(null)
  const [editing, setEditing] = useState(false)
  const [editValue, setEditValue] = useState('')

  const handleContextMenu = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setCtx({ x: e.clientX, y: e.clientY })
  }, [])

  const startRename = useCallback(() => {
    if (!session) return
    setEditing(true)
    setEditValue(session.name)
  }, [session])

  const commitRename = useCallback(() => {
    if (session && editValue.trim() && onRename) {
      onRename(session.id, editValue.trim())
    }
    setEditing(false)
  }, [session, editValue, onRename])

  if (!session) return null

  const menuItems: MenuEntry[] = [
    { label: 'Rename', onClick: startRename },
    { label: 'Duplicate', onClick: () => onDuplicate?.(session), disabled: !onDuplicate },
    { label: 'Edit', onClick: () => onEdit?.(session), disabled: !onEdit },
    { separator: true as const },
    {
      label: 'Open here',
      onClick: () => {},
      submenu: [
        { label: 'Mittens', onClick: () => onOpenMittens?.(session.config.workDir) },
        { label: 'Shell', onClick: () => onOpenShell?.(session.config.workDir) },
      ],
    },
    { separator: true as const },
    { label: 'Delete', onClick: () => onTerminate?.(), danger: true },
  ]

  return (
    <div
      onContextMenu={handleContextMenu}
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '4px 8px',
        backgroundColor: '#16213e',
        borderBottom: '1px solid #2a2a4a',
        fontSize: '12px',
        height: '28px',
        flexShrink: 0,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
        <span style={{
          width: '8px',
          height: '8px',
          borderRadius: '50%',
          backgroundColor: stateColors[session.state] || '#9e9e9e',
          display: 'inline-block',
        }} />
        {editing ? (
          <input
            autoFocus
            value={editValue}
            onChange={(e) => setEditValue(e.target.value)}
            onBlur={commitRename}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitRename()
              if (e.key === 'Escape') setEditing(false)
            }}
            style={{
              background: '#0f0f23',
              border: '1px solid #61dafb',
              borderRadius: '3px',
              color: '#ccc',
              fontSize: '12px',
              padding: '1px 4px',
              outline: 'none',
            }}
          />
        ) : (
          <span style={{ color: '#ccc' }}>{session.name}</span>
        )}
        <span style={{ color: '#666' }}>({session.state})</span>
        <span style={{ color: '#555' }}>{session.config.workDir}</span>
      </div>
      <div style={{ display: 'flex', gap: '4px' }}>
        {session.state === 'running' && (
          <button onClick={onTerminate} style={{ ...btnStyle, color: '#f44336' }} title="Terminate">
            X
          </button>
        )}
      </div>

      {ctx && (
        <ContextMenu
          x={ctx.x}
          y={ctx.y}
          items={menuItems}
          onClose={() => setCtx(null)}
        />
      )}
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
