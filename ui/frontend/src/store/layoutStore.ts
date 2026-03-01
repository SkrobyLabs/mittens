import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export const DRAG_MIME = 'application/x-mittens-tab'
export interface TabDragData { sourceTabId: string }

export type SplitDirection = 'horizontal' | 'vertical'

export interface Pane {
  id: string
  sessionId: string | null
}

export interface Tab {
  id: string
  label: string
  customLabel: string | null
  pinned: boolean
  panes: Pane[]
  splitDirection: SplitDirection
}

export function getTabDisplayLabel(tab: Tab, allTabs: Tab[]): string {
  if (tab.customLabel) return tab.customLabel
  if (tab.panes.length > 1) {
    const multiPaneTabs = allTabs.filter(t => t.panes.length > 1)
    const idx = multiPaneTabs.findIndex(t => t.id === tab.id)
    return `Group ${idx + 1}`
  }
  return tab.label
}

interface LayoutStore {
  tabs: Tab[]
  activeTabId: string | null
  sidebarCollapsed: boolean

  // Drag state
  isDraggingTab: boolean
  dragSourceTabId: string | null
  setDragState: (dragging: boolean, sourceTabId: string | null) => void

  toggleSidebar: () => void
  addTab: (label: string, sessionId: string) => string
  removeTab: (tabId: string) => void
  setActiveTab: (tabId: string) => void
  renameTab: (tabId: string, label: string) => void
  updateTabBaseLabel: (tabId: string, label: string) => void
  togglePinTab: (tabId: string) => void

  splitPane: (tabId: string, paneId: string, direction: SplitDirection, newSessionId: string) => void
  removePane: (tabId: string, paneId: string) => void
  assignSession: (tabId: string, paneId: string, sessionId: string) => void

  // Replace a session in all panes (for relaunch).
  replaceSession: (oldSessionId: string, newSessionId: string) => void

  reorderTab: (tabId: string, targetIndex: number) => void
  mergeTab: (sourceTabId: string, targetTabId: string) => void
  dockSession: (sourceTabId: string, targetTabId: string, direction: SplitDirection, position: 'before' | 'after') => void

  pruneStaleSessions: (liveSessionIds: Set<string>) => void
}

const genId = () => crypto.randomUUID().slice(0, 8)

export const useLayoutStore = create<LayoutStore>()(
  persist(
    (set, get) => ({
  tabs: [],
  activeTabId: null,
  sidebarCollapsed: false,

  isDraggingTab: false,
  dragSourceTabId: null,
  setDragState: (dragging, sourceTabId) => set({ isDraggingTab: dragging, dragSourceTabId: sourceTabId }),

  toggleSidebar: () => set({ sidebarCollapsed: !get().sidebarCollapsed }),

  addTab: (label, sessionId) => {
    const tabId = genId()
    const paneId = genId()
    const tab: Tab = {
      id: tabId,
      label,
      customLabel: null,
      pinned: false,
      panes: [{ id: paneId, sessionId }],
      splitDirection: 'horizontal',
    }
    set({ tabs: [...get().tabs, tab], activeTabId: tabId })
    return tabId
  },

  removeTab: (tabId) => {
    const target = get().tabs.find(t => t.id === tabId)
    if (target?.pinned) return
    const tabs = get().tabs.filter(t => t.id !== tabId)
    const activeTabId = get().activeTabId === tabId
      ? (tabs.length > 0 ? tabs[tabs.length - 1].id : null)
      : get().activeTabId
    set({ tabs, activeTabId })
  },

  setActiveTab: (tabId) => set({ activeTabId: tabId }),

  renameTab: (tabId, label) => {
    set({
      tabs: get().tabs.map(t =>
        t.id === tabId ? { ...t, customLabel: label } : t
      ),
    })
  },

  updateTabBaseLabel: (tabId, label) => {
    set({
      tabs: get().tabs.map(t =>
        t.id === tabId ? { ...t, label } : t
      ),
    })
  },

  togglePinTab: (tabId) => {
    const updated = get().tabs.map(t =>
      t.id === tabId ? { ...t, pinned: !t.pinned } : t
    )
    // Sort: pinned tabs always come first, preserve relative order within each group.
    const pinned = updated.filter(t => t.pinned)
    const unpinned = updated.filter(t => !t.pinned)
    set({ tabs: [...pinned, ...unpinned] })
  },

  splitPane: (tabId, _paneId, direction, newSessionId) => {
    set({
      tabs: get().tabs.map(t => {
        if (t.id !== tabId) return t
        const newPane: Pane = { id: genId(), sessionId: newSessionId }
        return {
          ...t,
          splitDirection: direction,
          panes: [...t.panes, newPane],
        }
      }),
    })
  },

  removePane: (tabId, paneId) => {
    set({
      tabs: get().tabs.map(t => {
        if (t.id !== tabId) return t
        return { ...t, panes: t.panes.filter(p => p.id !== paneId) }
      }),
    })
  },

  assignSession: (tabId, paneId, sessionId) => {
    set({
      tabs: get().tabs.map(t => {
        if (t.id !== tabId) return t
        return {
          ...t,
          panes: t.panes.map(p =>
            p.id === paneId ? { ...p, sessionId } : p
          ),
        }
      }),
    })
  },

  replaceSession: (oldSessionId, newSessionId) => {
    set({
      tabs: get().tabs.map(t => ({
        ...t,
        panes: t.panes.map(p =>
          p.sessionId === oldSessionId ? { ...p, sessionId: newSessionId } : p
        ),
      })),
    })
  },

  reorderTab: (tabId, targetIndex) => {
    const tabs = [...get().tabs]
    const srcIdx = tabs.findIndex(t => t.id === tabId)
    if (srcIdx === -1) return
    const [tab] = tabs.splice(srcIdx, 1)
    // Adjust target if source was before it
    let insertIdx = targetIndex > srcIdx ? targetIndex - 1 : targetIndex
    // Enforce pinned-first boundary: pinned tabs can't move past unpinned and vice versa.
    const pinnedCount = tabs.filter(t => t.pinned).length + (tab.pinned ? 1 : 0)
    if (tab.pinned) {
      insertIdx = Math.min(insertIdx, pinnedCount - 1)
    } else {
      insertIdx = Math.max(insertIdx, tabs.filter(t => t.pinned).length)
    }
    tabs.splice(insertIdx, 0, tab)
    set({ tabs })
  },

  mergeTab: (sourceTabId, targetTabId) => {
    if (sourceTabId === targetTabId) return
    const tabs = get().tabs
    const src = tabs.find(t => t.id === sourceTabId)
    const tgt = tabs.find(t => t.id === targetTabId)
    if (!src || !tgt) return
    const merged: Tab = { ...tgt, panes: [...tgt.panes, ...src.panes] }
    const newTabs = tabs.filter(t => t.id !== sourceTabId).map(t => t.id === targetTabId ? merged : t)
    const activeTabId = get().activeTabId === sourceTabId ? targetTabId : get().activeTabId
    set({ tabs: newTabs, activeTabId })
  },

  dockSession: (sourceTabId, targetTabId, direction, position) => {
    if (sourceTabId === targetTabId) return
    const tabs = get().tabs
    const src = tabs.find(t => t.id === sourceTabId)
    const tgt = tabs.find(t => t.id === targetTabId)
    if (!src || !tgt || src.panes.length === 0) return
    const [movedPane, ...remaining] = src.panes
    const newPanes = position === 'before'
      ? [movedPane, ...tgt.panes]
      : [...tgt.panes, movedPane]
    const updatedTarget: Tab = { ...tgt, panes: newPanes, splitDirection: direction }
    let newTabs: Tab[]
    if (remaining.length === 0) {
      newTabs = tabs.filter(t => t.id !== sourceTabId).map(t => t.id === targetTabId ? updatedTarget : t)
    } else {
      newTabs = tabs.map(t => {
        if (t.id === sourceTabId) return { ...t, panes: remaining }
        if (t.id === targetTabId) return updatedTarget
        return t
      })
    }
    const activeTabId = get().activeTabId === sourceTabId && remaining.length === 0
      ? targetTabId : get().activeTabId
    set({ tabs: newTabs, activeTabId })
  },

  pruneStaleSessions: (liveSessionIds) => {
    const tabs = get().tabs
      .map(t => ({
        ...t,
        panes: t.panes.filter(p => p.sessionId === null || liveSessionIds.has(p.sessionId)),
      }))
      .filter(t => t.panes.length > 0)
    const activeTabId = tabs.find(t => t.id === get().activeTabId)
      ? get().activeTabId
      : (tabs.length > 0 ? tabs[tabs.length - 1].id : null)
    set({ tabs, activeTabId })
  },
}),
    {
      name: 'mittens:layout',
      partialize: (state) => ({
        tabs: state.tabs,
        activeTabId: state.activeTabId,
        sidebarCollapsed: state.sidebarCollapsed,
      }),
    }
  )
)
