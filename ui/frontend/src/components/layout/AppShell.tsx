import { useState, useCallback, useEffect, useRef } from 'react'
import { Allotment } from 'allotment'
import 'allotment/dist/style.css'
import { TabBar } from './TabBar'
import { SplitPane } from './SplitPane'
import { DropZoneOverlay } from './DropZoneOverlay'
import { SessionList } from '../session/SessionList'
import { SessionControls } from '../session/SessionControls'
import { CreateSessionWizard } from '../session/CreateSessionWizard'
import { RelaunchDialog } from '../session/RelaunchDialog'
import { AddDirDialog } from '../channel/AddDirDialog'
import { LoginDialog } from '../channel/LoginDialog'
import { useSessionAPI } from '../../hooks/useSessionAPI'
import { useChannelEvents } from '../../hooks/useChannelEvents'
import { useLayoutStore } from '../../store/layoutStore'
import { useSessionStore } from '../../store/sessionStore'
import { useChannelStore } from '../../store/channelStore'
import type { Session, CreateSessionRequest, RelaunchRequest } from '../../types/session'

export function AppShell() {
  const { sessions, createSession, terminateSession, relaunchSession } = useSessionAPI()
  const renameSession = useSessionStore(s => s.renameSession)
  const duplicateSession = useSessionStore(s => s.duplicateSession)
  useChannelEvents()

  const { tabs, activeTabId, sidebarCollapsed, toggleSidebar, addTab, removeTab: rawRemoveTab, setActiveTab, renameTab, updateTabBaseLabel, togglePinTab, reorderTab, mergeTab, dockSession, isDraggingTab, dragSourceTabId, pruneStaleSessions } = useLayoutStore()
  const channelRequests = useChannelStore(s => s.requests)

  // Prune layout panes referencing sessions that no longer exist.
  const hasPruned = useRef(false)
  useEffect(() => {
    if (sessions.length > 0 && !hasPruned.current) {
      hasPruned.current = true
      pruneStaleSessions(new Set(sessions.map(s => s.id)))
    }
  }, [sessions, pruneStaleSessions])

  const [createMode, setCreateMode] = useState<'closed' | 'session' | 'shell'>('closed')
  const [wizardWorkDir, setWizardWorkDir] = useState<string | undefined>(undefined)
  const [relaunchTarget, setRelaunchTarget] = useState<Session | null>(null)
  const [createError, setCreateError] = useState<string | null>(null)

  const activeTab = tabs.find(t => t.id === activeTabId) || null

  // Close tab — sessions persist in tmux; user kills explicitly via sidebar.
  const handleCloseTab = useCallback((tabId: string) => {
    rawRemoveTab(tabId)
  }, [rawRemoveTab])

  const handleCreate = useCallback(async (req: CreateSessionRequest) => {
    setCreateError(null)
    try {
      const session = await createSession(req)
      addTab(session.name, session.id)
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Failed to create session'
      setCreateError(msg)
      throw e
    }
  }, [createSession, addTab])

  const handleNewShell = useCallback(async () => {
    const workDir = localStorage.getItem('mittens:lastFolder') || ''
    if (!workDir) {
      // No last folder — fall back to wizard for directory selection.
      setCreateMode('shell')
      return
    }
    try {
      const session = await createSession({ workDir, shell: true })
      addTab(session.name, session.id)
    } catch (e) {
      console.error('Failed to create shell:', e)
    }
  }, [createSession, addTab])

  const handleSelectSession = useCallback((session: Session) => {
    // Find existing tab for this session.
    const existingTab = tabs.find(t => t.panes.some(p => p.sessionId === session.id))
    if (existingTab) {
      setActiveTab(existingTab.id)
    } else {
      addTab(session.name, session.id)
    }
  }, [tabs, setActiveTab, addTab])

  const handleRelaunch = useCallback(async (id: string, req: RelaunchRequest) => {
    try {
      const newSession = await relaunchSession(id, req)
      // Layout store will handle replacing the session in panes.
      useLayoutStore.getState().replaceSession(id, newSession.id)
    } catch (e) {
      console.error('Failed to relaunch session:', e)
    }
  }, [relaunchSession])

  const handleRestart = useCallback(async (session: Session) => {
    try {
      const newSession = await relaunchSession(session.id, {})
      useLayoutStore.getState().replaceSession(session.id, newSession.id)
      // Open a tab for the restarted session.
      const existingTab = tabs.find(t => t.panes.some(p => p.sessionId === newSession.id))
      if (!existingTab) {
        addTab(newSession.name, newSession.id)
      }
    } catch (e) {
      console.error('Failed to restart session:', e)
    }
  }, [relaunchSession, tabs, addTab])

  // Context menu actions.
  const handleRename = useCallback(async (id: string, name: string) => {
    try {
      const updated = await renameSession(id, name)
      // Update the base label for single-pane tabs (don't override customLabel).
      const tab = tabs.find(t => t.panes.some(p => p.sessionId === id))
      if (tab && tab.panes.length === 1) {
        updateTabBaseLabel(tab.id, updated.name)
      }
    } catch (e) {
      console.error('Failed to rename session:', e)
    }
  }, [renameSession, tabs, updateTabBaseLabel])

  const handleDuplicate = useCallback(async (session: Session) => {
    try {
      const newSession = await duplicateSession(session)
      addTab(newSession.name, newSession.id)
    } catch (e) {
      console.error('Failed to duplicate session:', e)
    }
  }, [duplicateSession, addTab])

  const handleEdit = useCallback((session: Session) => {
    setRelaunchTarget(session)
  }, [])

  const handleOpenShell = useCallback(async (workDir: string) => {
    try {
      const session = await createSession({ workDir, shell: true })
      addTab(session.name, session.id)
    } catch (e) {
      console.error('Failed to open shell:', e)
    }
  }, [createSession, addTab])

  const handleOpenMittens = useCallback((workDir: string) => {
    setWizardWorkDir(workDir)
    setCreateMode('session')
  }, [])

  const handleWizardClose = useCallback(() => {
    setCreateMode('closed')
    setWizardWorkDir(undefined)
    setCreateError(null)
  }, [])

  // Get the first pending channel request for dialog display.
  const addDirRequest = channelRequests.find(r => r.type === 'add-dir') || null
  const loginRequest = channelRequests.find(r => r.type === 'login') || null

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100vh', width: '100vw' }}>
      {/* Tab Bar */}
      <TabBar
        tabs={tabs}
        activeTabId={activeTabId}
        sidebarCollapsed={sidebarCollapsed}
        onSelect={setActiveTab}
        onClose={handleCloseTab}
        onNew={() => setCreateMode('session')}
        onReorder={reorderTab}
        onMerge={mergeTab}
        onToggleSidebar={toggleSidebar}
        onRenameTab={renameTab}
        onTogglePin={togglePinTab}
      />

      {/* Main content: sidebar + terminal area */}
      <div style={{ flex: 1, minHeight: 0 }}>
        <Allotment>
          {/* Sidebar */}
          {!sidebarCollapsed && (
          <Allotment.Pane minSize={150} preferredSize={200} maxSize={350}>
            <div style={{
              height: '100%',
              backgroundColor: '#12122a',
              display: 'flex',
              flexDirection: 'column',
              borderRight: '1px solid #2a2a4a',
            }}>
              <div style={{ flex: 1, overflow: 'auto' }}>
                <SessionList
                  sessions={sessions}
                  tabs={tabs}
                  onSelect={handleSelectSession}
                  onTerminate={terminateSession}
                  onRestart={handleRestart}
                  onRename={handleRename}
                  onDuplicate={handleDuplicate}
                  onEdit={handleEdit}
                  onOpenShell={handleOpenShell}
                  onOpenMittens={handleOpenMittens}
                  onRenameTab={renameTab}
                />
              </div>
              <SessionControls
                onNewSession={() => setCreateMode('session')}
                onNewShell={handleNewShell}
              />
            </div>
          </Allotment.Pane>
          )}

          {/* Terminal area */}
          <Allotment.Pane>
            <div style={{ position: 'relative', height: '100%' }}>
              {activeTab ? (
                <SplitPane
                  panes={activeTab.panes}
                  direction={activeTab.splitDirection}
                  tabId={activeTab.id}
                  onRename={handleRename}
                  onDuplicate={handleDuplicate}
                  onEdit={handleEdit}
                  onOpenShell={handleOpenShell}
                  onOpenMittens={handleOpenMittens}
                />
              ) : (
                <div style={{
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  justifyContent: 'center',
                  height: '100%',
                  color: '#555',
                  gap: '12px',
                }}>
                  <div style={{ fontSize: '24px' }}>mittens-ui</div>
                  <div style={{ fontSize: '13px' }}>
                    Create a new session to get started
                  </div>
                  <button
                    onClick={() => setCreateMode('session')}
                    style={{
                      padding: '8px 20px',
                      backgroundColor: '#61dafb',
                      border: 'none',
                      borderRadius: '4px',
                      color: '#000',
                      cursor: 'pointer',
                      fontWeight: 'bold',
                      fontSize: '13px',
                    }}
                  >
                    New Session
                  </button>
                </div>
              )}
              {isDraggingTab && activeTab && dragSourceTabId !== activeTab.id && (
                <DropZoneOverlay
                  onDock={(direction, position) => {
                    if (dragSourceTabId) {
                      dockSession(dragSourceTabId, activeTab.id, direction, position)
                    }
                  }}
                />
              )}
            </div>
          </Allotment.Pane>
        </Allotment>
      </div>

      {/* Dialogs */}
      <CreateSessionWizard
        open={createMode !== 'closed'}
        onClose={handleWizardClose}
        onCreate={handleCreate}
        initialShell={createMode === 'shell'}
        initialWorkDir={wizardWorkDir}
        error={createError}
      />
      <RelaunchDialog
        open={!!relaunchTarget}
        session={relaunchTarget}
        onClose={() => setRelaunchTarget(null)}
        onRelaunch={handleRelaunch}
      />
      <AddDirDialog request={addDirRequest} />
      <LoginDialog request={loginRequest} />
    </div>
  )
}
