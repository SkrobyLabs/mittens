import { create } from 'zustand'

export type SplitDirection = 'horizontal' | 'vertical'

export interface Pane {
  id: string
  sessionId: string | null
}

export interface Tab {
  id: string
  label: string
  panes: Pane[]
  splitDirection: SplitDirection
}

interface LayoutStore {
  tabs: Tab[]
  activeTabId: string | null

  addTab: (label: string, sessionId: string) => string
  removeTab: (tabId: string) => void
  setActiveTab: (tabId: string) => void
  renameTab: (tabId: string, label: string) => void

  splitPane: (tabId: string, paneId: string, direction: SplitDirection, newSessionId: string) => void
  removePane: (tabId: string, paneId: string) => void
  assignSession: (tabId: string, paneId: string, sessionId: string) => void

  // Replace a session in all panes (for relaunch).
  replaceSession: (oldSessionId: string, newSessionId: string) => void
}

let nextId = 1
const genId = () => String(nextId++)

export const useLayoutStore = create<LayoutStore>((set, get) => ({
  tabs: [],
  activeTabId: null,

  addTab: (label, sessionId) => {
    const tabId = genId()
    const paneId = genId()
    const tab: Tab = {
      id: tabId,
      label,
      panes: [{ id: paneId, sessionId }],
      splitDirection: 'horizontal',
    }
    set({ tabs: [...get().tabs, tab], activeTabId: tabId })
    return tabId
  },

  removeTab: (tabId) => {
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
        t.id === tabId ? { ...t, label } : t
      ),
    })
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
}))
