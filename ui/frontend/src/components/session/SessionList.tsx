import { useState, useCallback } from 'react'
import type { Session } from '../../types/session'
import type { Tab } from '../../store/layoutStore'
import { getTabDisplayLabel } from '../../store/layoutStore'
import { ContextMenu } from '../shared/ContextMenu'
import type { MenuEntry } from '../shared/ContextMenu'

interface SessionListProps {
  sessions: Session[]
  tabs: Tab[]
  onSelect: (session: Session) => void
  onTerminate: (id: string) => void
  onRestart: (session: Session) => void
  onRename: (id: string, name: string) => void
  onDuplicate: (session: Session) => void
  onEdit: (session: Session) => void
  onOpenShell: (workDir: string) => void
  onOpenMittens: (workDir: string) => void
  onRenameTab: (tabId: string, label: string) => void
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
  session: Session
}

interface GroupContextState {
  x: number
  y: number
  tab: Tab
}

type TreeNode =
  | { kind: 'group'; tab: Tab; sessions: Session[] }
  | { kind: 'session'; session: Session }

function buildTree(tabs: Tab[], sessions: Session[]): TreeNode[] {
  const sessionMap = new Map(sessions.map(s => [s.id, s]))
  const assignedIds = new Set<string>()
  const nodes: TreeNode[] = []

  for (const tab of tabs) {
    if (tab.panes.length > 1) {
      const groupSessions: Session[] = []
      for (const pane of tab.panes) {
        if (pane.sessionId) {
          const s = sessionMap.get(pane.sessionId)
          if (s) {
            groupSessions.push(s)
            assignedIds.add(s.id)
          }
        }
      }
      if (groupSessions.length > 0) {
        nodes.push({ kind: 'group', tab, sessions: groupSessions })
      }
    } else {
      // Single-pane tab — render as root-level session
      const sid = tab.panes[0]?.sessionId
      if (sid) {
        const s = sessionMap.get(sid)
        if (s) {
          nodes.push({ kind: 'session', session: s })
          assignedIds.add(s.id)
        }
      }
    }
  }

  // Ungrouped sessions (not in any tab)
  for (const s of sessions) {
    if (!assignedIds.has(s.id)) {
      nodes.push({ kind: 'session', session: s })
    }
  }

  return nodes
}

export function SessionList({
  sessions, tabs, onSelect, onTerminate, onRestart,
  onRename, onDuplicate, onEdit, onOpenShell, onOpenMittens, onRenameTab,
}: SessionListProps) {
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [ctx, setCtx] = useState<ContextState | null>(null)
  const [groupCtx, setGroupCtx] = useState<GroupContextState | null>(null)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editValue, setEditValue] = useState('')
  const [editingGroupId, setEditingGroupId] = useState<string | null>(null)
  const [editGroupValue, setEditGroupValue] = useState('')
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set())

  const toggleGroup = useCallback((tabId: string) => {
    setCollapsedGroups(prev => {
      const next = new Set(prev)
      if (next.has(tabId)) next.delete(tabId)
      else next.add(tabId)
      return next
    })
  }, [])

  const handleContextMenu = useCallback((e: React.MouseEvent, session: Session) => {
    e.preventDefault()
    setCtx({ x: e.clientX, y: e.clientY, session })
  }, [])

  const startRename = useCallback((session: Session) => {
    setEditingId(session.id)
    setEditValue(session.name)
  }, [])

  const commitRename = useCallback(() => {
    if (editingId && editValue.trim()) {
      onRename(editingId, editValue.trim())
    }
    setEditingId(null)
  }, [editingId, editValue, onRename])

  const handleGroupContextMenu = useCallback((e: React.MouseEvent, tab: Tab) => {
    e.preventDefault()
    e.stopPropagation()
    setGroupCtx({ x: e.clientX, y: e.clientY, tab })
  }, [])

  const startGroupRename = useCallback((tab: Tab) => {
    setEditingGroupId(tab.id)
    setEditGroupValue(getTabDisplayLabel(tab, tabs))
  }, [tabs])

  const commitGroupRename = useCallback(() => {
    if (editingGroupId && editGroupValue.trim()) {
      onRenameTab(editingGroupId, editGroupValue.trim())
    }
    setEditingGroupId(null)
  }, [editingGroupId, editGroupValue, onRenameTab])

  const groupMenuItems = (tab: Tab): MenuEntry[] => [
    { label: 'Rename', onClick: () => startGroupRename(tab) },
  ]

  const handleDelete = (id: string) => {
    onTerminate(id)
    setConfirmDelete(null)
  }

  const menuItems = (session: Session): MenuEntry[] => [
    { label: 'Rename', onClick: () => startRename(session) },
    { label: 'Duplicate', onClick: () => onDuplicate(session) },
    { label: 'Edit', onClick: () => onEdit(session) },
    { separator: true as const },
    {
      label: 'Open here',
      onClick: () => {},
      submenu: [
        { label: 'Mittens', onClick: () => onOpenMittens(session.config.workDir) },
        { label: 'Shell', onClick: () => onOpenShell(session.config.workDir) },
      ],
    },
  ]

  const renderSessionItem = (session: Session, indent: boolean) => (
    <div
      key={session.id}
      onClick={() => onSelect(session)}
      onContextMenu={(e) => handleContextMenu(e, session)}
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '6px 8px',
        paddingLeft: indent ? '24px' : '8px',
        borderRadius: '4px',
        cursor: 'pointer',
        marginBottom: '2px',
        fontSize: '12px',
      }}
      onMouseEnter={(e) => { e.currentTarget.style.backgroundColor = '#1e1e3a' }}
      onMouseLeave={(e) => {
        e.currentTarget.style.backgroundColor = 'transparent'
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
        {editingId === session.id ? (
          <input
            autoFocus
            value={editValue}
            onChange={(e) => setEditValue(e.target.value)}
            onBlur={commitRename}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitRename()
              if (e.key === 'Escape') setEditingId(null)
            }}
            onClick={(e) => e.stopPropagation()}
            style={{
              background: '#0f0f23',
              border: '1px solid #61dafb',
              borderRadius: '3px',
              color: '#ccc',
              fontSize: '12px',
              padding: '1px 4px',
              outline: 'none',
              width: '100%',
            }}
          />
        ) : (
          <span style={{ color: '#ccc', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {session.name}
          </span>
        )}
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
  )

  const tree = buildTree(tabs, sessions)

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
      {tree.map(node => {
        if (node.kind === 'session') {
          return renderSessionItem(node.session, false)
        }
        const collapsed = collapsedGroups.has(node.tab.id)
        const label = getTabDisplayLabel(node.tab, tabs)
        const isEditingGroup = editingGroupId === node.tab.id
        return (
          <div key={`group-${node.tab.id}`}>
            <div
              onClick={() => toggleGroup(node.tab.id)}
              onContextMenu={(e) => handleGroupContextMenu(e, node.tab)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
                padding: '5px 8px',
                cursor: 'pointer',
                fontSize: '12px',
                color: '#999',
                userSelect: 'none',
                borderRadius: '4px',
                marginBottom: '2px',
              }}
              onMouseEnter={(e) => { e.currentTarget.style.backgroundColor = '#1e1e3a' }}
              onMouseLeave={(e) => { e.currentTarget.style.backgroundColor = 'transparent' }}
            >
              <span style={{ fontSize: '8px', width: '10px', textAlign: 'center' }}>
                {collapsed ? '\u25B6' : '\u25BC'}
              </span>
              {isEditingGroup ? (
                <input
                  autoFocus
                  value={editGroupValue}
                  onChange={(e) => setEditGroupValue(e.target.value)}
                  onBlur={commitGroupRename}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') commitGroupRename()
                    if (e.key === 'Escape') setEditingGroupId(null)
                  }}
                  onClick={(e) => e.stopPropagation()}
                  style={{
                    background: '#0f0f23',
                    border: '1px solid #61dafb',
                    borderRadius: '3px',
                    color: '#ccc',
                    fontSize: '12px',
                    padding: '1px 4px',
                    outline: 'none',
                    width: '100%',
                  }}
                />
              ) : (
                <>
                  <span style={{ fontWeight: 500, color: '#bbb' }}>{label}</span>
                  <span style={{ color: '#666', fontSize: '11px' }}>({node.sessions.length})</span>
                </>
              )}
            </div>
            {!collapsed && node.sessions.map(s => renderSessionItem(s, true))}
          </div>
        )
      })}

      {ctx && (
        <ContextMenu
          x={ctx.x}
          y={ctx.y}
          items={menuItems(ctx.session)}
          onClose={() => setCtx(null)}
        />
      )}
      {groupCtx && (
        <ContextMenu
          x={groupCtx.x}
          y={groupCtx.y}
          items={groupMenuItems(groupCtx.tab)}
          onClose={() => setGroupCtx(null)}
        />
      )}
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
