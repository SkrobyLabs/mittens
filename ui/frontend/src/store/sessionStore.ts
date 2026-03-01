import { create } from 'zustand'
import type { Session, CreateSessionRequest, RelaunchRequest } from '../types/session'

const API = '/api/v1'

interface SessionStore {
  sessions: Session[]
  loading: boolean
  error: string | null

  fetchSessions: () => Promise<void>
  createSession: (req: CreateSessionRequest) => Promise<Session>
  terminateSession: (id: string) => Promise<void>
  relaunchSession: (id: string, req: RelaunchRequest) => Promise<Session>
  renameSession: (id: string, name: string) => Promise<Session>
  duplicateSession: (session: Session) => Promise<Session>
  updateSessionState: (id: string, state: Session['state'], exitCode?: number) => void
}

export const useSessionStore = create<SessionStore>((set, get) => ({
  sessions: [],
  loading: false,
  error: null,

  fetchSessions: async () => {
    set({ loading: true, error: null })
    try {
      const res = await fetch(`${API}/sessions`)
      const data = await res.json()
      set({ sessions: data, loading: false })
    } catch (e) {
      set({ error: (e as Error).message, loading: false })
    }
  },

  createSession: async (req) => {
    const res = await fetch(`${API}/sessions`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
    if (!res.ok) {
      const err = await res.json()
      throw new Error(err.error || 'Failed to create session')
    }
    const session: Session = await res.json()
    const existing = get().sessions
    if (existing.some(s => s.id === session.id)) {
      set({ sessions: existing.map(s => s.id === session.id ? session : s) })
    } else {
      set({ sessions: [...existing, session] })
    }
    return session
  },

  terminateSession: async (id) => {
    await fetch(`${API}/sessions/${id}`, { method: 'DELETE' })
    set({ sessions: get().sessions.filter(s => s.id !== id) })
    // Remove from layout so the tab/pane doesn't linger.
    const { useLayoutStore } = await import('./layoutStore')
    useLayoutStore.getState().removeSession(id)
  },

  relaunchSession: async (id, req) => {
    const res = await fetch(`${API}/sessions/${id}/relaunch`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    })
    if (!res.ok) {
      const err = await res.json()
      throw new Error(err.error || 'Failed to relaunch session')
    }
    const newSession: Session = await res.json()
    set({
      sessions: [
        ...get().sessions.filter(s => s.id !== id),
        newSession,
      ],
    })
    return newSession
  },

  renameSession: async (id, name) => {
    const res = await fetch(`${API}/sessions/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    })
    if (!res.ok) {
      const err = await res.json()
      throw new Error(err.error || 'Failed to rename session')
    }
    const session: Session = await res.json()
    set({ sessions: get().sessions.map(s => s.id === id ? session : s) })
    return session
  },

  duplicateSession: async (session) => {
    const req: CreateSessionRequest = {
      name: session.name + ' (copy)',
      workDir: session.config.workDir,
      extensions: session.config.extensions,
      flags: session.config.flags,
      claudeArgs: session.config.claudeArgs,
      extraDirs: session.config.extraDirs,
    }
    return get().createSession(req)
  },

  updateSessionState: (id, state, exitCode) => {
    set({
      sessions: get().sessions.map(s =>
        s.id === id ? { ...s, state, ...(exitCode !== undefined ? { exitCode } : {}) } : s
      ),
    })
  },
}))
