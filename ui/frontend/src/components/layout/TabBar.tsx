import { useState, useCallback, useRef, useEffect } from 'react'
import { DRAG_MIME, useLayoutStore, getTabDisplayLabel } from '../../store/layoutStore'
import type { Tab, TabDragData } from '../../store/layoutStore'
import { ContextMenu } from '../shared/ContextMenu'
import type { MenuEntry } from '../shared/ContextMenu'

interface TabBarProps {
  tabs: Tab[]
  activeTabId: string | null
  sidebarCollapsed: boolean
  onSelect: (tabId: string) => void
  onClose: (tabId: string) => void
  onNew: () => void
  onReorder: (tabId: string, targetIndex: number) => void
  onMerge: (sourceTabId: string, targetTabId: string) => void
  onToggleSidebar: () => void
  onRenameTab: (tabId: string, label: string) => void
  onTogglePin: (tabId: string) => void
}

interface TabContextState {
  x: number
  y: number
  tab: Tab
}

function parseDragData(e: React.DragEvent): TabDragData | null {
  try {
    const raw = e.dataTransfer.getData(DRAG_MIME)
    return raw ? JSON.parse(raw) : null
  } catch { return null }
}

export function TabBar({ tabs, activeTabId, sidebarCollapsed, onSelect, onClose, onNew, onReorder, onMerge, onToggleSidebar, onRenameTab, onTogglePin }: TabBarProps) {
  const [mergeTargetId, setMergeTargetId] = useState<string | null>(null)
  const [reorderGapIndex, setReorderGapIndex] = useState<number | null>(null)
  const [editingTabId, setEditingTabId] = useState<string | null>(null)
  const [editValue, setEditValue] = useState('')
  const editInputRef = useRef<HTMLInputElement>(null)
  const [ctx, setCtx] = useState<TabContextState | null>(null)

  useEffect(() => {
    if (editingTabId && editInputRef.current) {
      editInputRef.current.focus()
      editInputRef.current.select()
    }
  }, [editingTabId])

  const commitTabRename = useCallback(() => {
    if (editingTabId && editValue.trim()) {
      onRenameTab(editingTabId, editValue.trim())
    }
    setEditingTabId(null)
  }, [editingTabId, editValue, onRenameTab])

  const handleDragStart = useCallback((e: React.DragEvent, tab: Tab) => {
    const data: TabDragData = { sourceTabId: tab.id }
    e.dataTransfer.setData(DRAG_MIME, JSON.stringify(data))
    e.dataTransfer.effectAllowed = 'move'
    useLayoutStore.getState().setDragState(true, tab.id)
  }, [])

  const handleDragEnd = useCallback(() => {
    useLayoutStore.getState().setDragState(false, null)
    setMergeTargetId(null)
    setReorderGapIndex(null)
  }, [])

  const handleTabDragOver = useCallback((e: React.DragEvent) => {
    if (e.dataTransfer.types.includes(DRAG_MIME)) {
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
    }
  }, [])

  const handleTabDrop = useCallback((e: React.DragEvent, targetTabId: string) => {
    e.preventDefault()
    const data = parseDragData(e)
    if (data && data.sourceTabId !== targetTabId) {
      onMerge(data.sourceTabId, targetTabId)
    }
    setMergeTargetId(null)
  }, [onMerge])

  const handleGapDrop = useCallback((e: React.DragEvent, gapIndex: number) => {
    e.preventDefault()
    const data = parseDragData(e)
    if (data) {
      onReorder(data.sourceTabId, gapIndex)
    }
    setReorderGapIndex(null)
  }, [onReorder])

  const handleTabContextMenu = useCallback((e: React.MouseEvent, tab: Tab) => {
    e.preventDefault()
    e.stopPropagation()
    setCtx({ x: e.clientX, y: e.clientY, tab })
  }, [])

  const tabMenuItems = (tab: Tab): MenuEntry[] => {
    const displayLabel = getTabDisplayLabel(tab, tabs)
    return [
      {
        label: 'Rename',
        onClick: () => {
          setEditingTabId(tab.id)
          setEditValue(displayLabel)
        },
      },
      {
        label: tab.pinned ? 'Unpin' : 'Pin',
        onClick: () => onTogglePin(tab.id),
      },
      { separator: true as const },
      {
        label: 'Close',
        onClick: () => onClose(tab.id),
        disabled: tab.pinned,
      },
    ]
  }

  // Build interleaved list: [gap-0, tab-0, gap-1, tab-1, ..., gap-N]
  const items: React.ReactNode[] = []

  const gapStyle = (active: boolean): React.CSSProperties => ({
    width: active ? '12px' : '4px',
    height: '100%',
    transition: 'width 0.1s, background-color 0.1s',
    backgroundColor: active ? 'rgba(97, 218, 251, 0.4)' : 'transparent',
    flexShrink: 0,
  })

  for (let i = 0; i <= tabs.length; i++) {
    // Gap before each tab (and after the last)
    items.push(
      <div
        key={`gap-${i}`}
        style={gapStyle(reorderGapIndex === i)}
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes(DRAG_MIME)) {
            e.preventDefault()
            e.dataTransfer.dropEffect = 'move'
            setReorderGapIndex(i)
          }
        }}
        onDragEnter={() => setReorderGapIndex(i)}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node)) {
            setReorderGapIndex(prev => prev === i ? null : prev)
          }
        }}
        onDrop={(e) => handleGapDrop(e, i)}
      />
    )

    // Tab
    if (i < tabs.length) {
      const tab = tabs[i]
      const isActive = tab.id === activeTabId
      const isMergeTarget = mergeTargetId === tab.id
      const displayLabel = getTabDisplayLabel(tab, tabs)

      items.push(
        <div
          key={tab.id}
          draggable={editingTabId !== tab.id}
          onDragStart={(e) => handleDragStart(e, tab)}
          onDragEnd={handleDragEnd}
          onDragOver={handleTabDragOver}
          onDragEnter={() => setMergeTargetId(tab.id)}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) {
              setMergeTargetId(prev => prev === tab.id ? null : prev)
            }
          }}
          onDrop={(e) => handleTabDrop(e, tab.id)}
          onClick={() => onSelect(tab.id)}
          onDoubleClick={() => {
            setEditingTabId(tab.id)
            setEditValue(displayLabel)
          }}
          onContextMenu={(e) => handleTabContextMenu(e, tab)}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: '4px',
            padding: '4px 12px',
            cursor: editingTabId === tab.id ? 'text' : 'grab',
            backgroundColor: isMergeTarget ? 'rgba(97, 218, 251, 0.2)' : (isActive ? '#1a1a2e' : 'transparent'),
            borderRight: '1px solid #2a2a4a',
            borderBottom: isActive ? '2px solid #61dafb' : '2px solid transparent',
            outline: isMergeTarget ? '2px solid #61dafb' : 'none',
            color: isActive ? '#e0e0e0' : '#888',
            fontSize: '12px',
            userSelect: 'none',
          }}
        >
          {tab.pinned && (
            <span style={{ fontSize: '9px', color: '#61dafb', flexShrink: 0 }} title="Pinned">
              &#128204;
            </span>
          )}
          {editingTabId === tab.id ? (
            <input
              ref={editInputRef}
              value={editValue}
              onChange={(e) => setEditValue(e.target.value)}
              onBlur={commitTabRename}
              onKeyDown={(e) => {
                if (e.key === 'Enter') commitTabRename()
                if (e.key === 'Escape') setEditingTabId(null)
              }}
              onClick={(e) => e.stopPropagation()}
              style={{
                background: '#0f0f23',
                border: '1px solid #61dafb',
                borderRadius: '3px',
                color: '#ccc',
                fontSize: '12px',
                padding: '0 4px',
                outline: 'none',
                width: '80px',
              }}
            />
          ) : (
            <span>{displayLabel}</span>
          )}
          {!tab.pinned && (
            <span
              onClick={(e) => { e.stopPropagation(); onClose(tab.id) }}
              style={{
                cursor: 'pointer',
                color: '#666',
                marginLeft: '4px',
                fontSize: '10px',
              }}
            >
              x
            </span>
          )}
        </div>
      )
    }
  }

  return (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      backgroundColor: '#0f0f23',
      borderBottom: '1px solid #2a2a4a',
      height: '32px',
      overflow: 'hidden',
    }}>
      <button
        onClick={onToggleSidebar}
        style={{
          background: 'none',
          border: 'none',
          color: '#888',
          cursor: 'pointer',
          padding: '4px 8px',
          fontSize: '12px',
          flexShrink: 0,
        }}
        title={sidebarCollapsed ? 'Show sidebar' : 'Hide sidebar'}
      >
        {sidebarCollapsed ? '\u25B6' : '\u25C0'}
      </button>
      {items}
      <button
        onClick={onNew}
        style={{
          background: 'none',
          border: 'none',
          color: '#666',
          cursor: 'pointer',
          padding: '4px 8px',
          fontSize: '14px',
        }}
        title="New session"
      >
        +
      </button>

      {ctx && (
        <ContextMenu
          x={ctx.x}
          y={ctx.y}
          items={tabMenuItems(ctx.tab)}
          onClose={() => setCtx(null)}
        />
      )}
    </div>
  )
}
