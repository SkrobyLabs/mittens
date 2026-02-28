import { useState, useEffect, useCallback } from 'react'
import { ProjectTab } from './tabs/ProjectTab'
import { ExtensionsTab } from './tabs/ExtensionsTab'
import { OptionsTab } from './tabs/OptionsTab'
import type { CreateSessionRequest } from '../../types/session'
import type { ExtensionMeta, CapsResponse } from '../../types/extension'
import type { WizardState, WizardTab, ExtensionToggle } from '../../types/wizard'

interface CreateSessionWizardProps {
  open: boolean
  onClose: () => void
  onCreate: (req: CreateSessionRequest) => void
}

interface Preset {
  name: string
  workDir?: string
  extensions?: ExtensionToggle[]
  options?: WizardState['options']
  extraDirs?: string[]
  claudeArgs?: string
}

const TABS: { id: WizardTab; label: string }[] = [
  { id: 'project', label: 'Project' },
  { id: 'extensions', label: 'Extensions' },
  { id: 'options', label: 'Options' },
]

const INITIAL_OPTIONS: WizardState['options'] = {
  dind: false,
  yolo: false,
  worktree: false,
  networkHost: false,
  noHistory: false,
  noBuild: false,
  shell: false,
  noResume: false,
  claudeArgs: '',
}

function makeDefaultState(metas: ExtensionMeta[]): WizardState {
  return {
    project: { name: '', workDir: '', extraDirs: [] },
    extensions: metas.map(ext => ({
      name: ext.name,
      enabled: ext.defaultOn,
      allMode: false,
      values: [],
    })),
    options: { ...INITIAL_OPTIONS },
  }
}

export function CreateSessionWizard({ open, onClose, onCreate }: CreateSessionWizardProps) {
  const [tab, setTab] = useState<WizardTab>('project')
  const [extensionMetas, setExtensionMetas] = useState<ExtensionMeta[]>([])
  const [capsLoading, setCapsLoading] = useState(false)
  const [state, setState] = useState<WizardState>({
    project: { name: '', workDir: '', extraDirs: [] },
    extensions: [],
    options: { ...INITIAL_OPTIONS },
  })

  // Preset state.
  const [presets, setPresets] = useState<Preset[]>([])
  const [showSavePreset, setShowSavePreset] = useState(false)
  const [presetName, setPresetName] = useState('')

  // Fetch capabilities + presets on open.
  useEffect(() => {
    if (!open) return
    setCapsLoading(true)
    Promise.all([
      fetch('/api/v1/config/extensions').then(r => r.json()).catch(() => ({ extensions: [], coreFlags: [] })),
      fetch('/api/v1/presets').then(r => r.json()).catch(() => []),
    ]).then(([caps, savedPresets]: [CapsResponse, Preset[]]) => {
      const metas = caps.extensions || []
      setExtensionMetas(metas)
      setPresets(savedPresets || [])
      setState(prev => {
        // Only reset extensions if they haven't been initialized yet.
        if (prev.extensions.length > 0) return prev
        return makeDefaultState(metas)
      })
    }).finally(() => setCapsLoading(false))
  }, [open])

  const loadPreset = useCallback((preset: Preset) => {
    setState(prev => ({
      project: {
        name: '',
        workDir: preset.workDir || '',
        extraDirs: preset.extraDirs || [],
      },
      extensions: preset.extensions
        ? prev.extensions.map(t => {
            const saved = preset.extensions!.find(e => e.name === t.name)
            return saved ? { ...t, ...saved } : t
          })
        : prev.extensions,
      options: preset.options || prev.options,
    }))
  }, [])

  const savePreset = useCallback(async (name: string) => {
    const preset: Preset = {
      name,
      workDir: state.project.workDir || undefined,
      extensions: state.extensions.filter(e => e.enabled !== extensionMetas.find(m => m.name === e.name)?.defaultOn || e.values.length > 0 || e.allMode),
      options: state.options,
      extraDirs: state.project.extraDirs.filter(Boolean).length > 0 ? state.project.extraDirs.filter(Boolean) : undefined,
    }
    const res = await fetch('/api/v1/presets', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(preset),
    })
    if (res.ok) {
      const saved = await res.json()
      setPresets(prev => {
        const idx = prev.findIndex(p => p.name === saved.name)
        if (idx >= 0) {
          const next = [...prev]
          next[idx] = saved
          return next
        }
        return [...prev, saved]
      })
    }
    setShowSavePreset(false)
    setPresetName('')
  }, [state, extensionMetas])

  const deletePreset = useCallback(async (name: string) => {
    await fetch(`/api/v1/presets/${encodeURIComponent(name)}`, { method: 'DELETE' })
    setPresets(prev => prev.filter(p => p.name !== name))
  }, [])

  const handleCreate = useCallback(() => {
    if (!state.project.workDir) return

    const flags: string[] = []

    for (const ext of state.extensions) {
      const meta = extensionMetas.find(m => m.name === ext.name)
      if (!meta) continue

      if (meta.defaultOn && !ext.enabled) {
        const noFlag = meta.flags.find(f => f.name.startsWith('--no-'))
        if (noFlag) flags.push(noFlag.name)
        continue
      }

      if (!ext.enabled) continue
      if (meta.defaultOn && !ext.allMode && ext.values.length === 0) continue

      if (ext.allMode) {
        const allFlag = meta.flags.find(f => f.name.endsWith('-all'))
        if (allFlag) {
          flags.push(allFlag.name)
          continue
        }
      }

      const primaryFlag = meta.flags.find(
        f => !f.name.startsWith('--no-') && !f.name.endsWith('-all')
      )
      if (!primaryFlag) continue

      flags.push(primaryFlag.name)
      if (ext.values.length > 0 && primaryFlag.argType !== 'none') {
        flags.push(ext.values.join(','))
      }
    }

    const opts = state.options
    if (opts.dind) flags.push('--dind')
    if (opts.worktree) flags.push('--worktree')
    if (opts.yolo) flags.push('--yolo')
    if (opts.networkHost) flags.push('--network-host')
    if (opts.noHistory) flags.push('--no-history')
    if (opts.noBuild) flags.push('--no-build')
    if (opts.noResume) flags.push('--no-resume')
    if (opts.shell) flags.push('--shell')

    const claudeArgs = opts.claudeArgs.trim()
      ? opts.claudeArgs.trim().split(/\s+/).filter(Boolean)
      : undefined

    const req: CreateSessionRequest = {
      name: state.project.name || undefined,
      workDir: state.project.workDir,
      flags: flags.length > 0 ? flags : undefined,
      claudeArgs,
      extraDirs: state.project.extraDirs.filter(Boolean).length > 0
        ? state.project.extraDirs.filter(Boolean)
        : undefined,
    }

    onCreate(req)
    setState(makeDefaultState(extensionMetas))
    setTab('project')
    onClose()
  }, [state, extensionMetas, onCreate, onClose])

  if (!open) return null

  const tabIdx = TABS.findIndex(t => t.id === tab)
  const isFirst = tabIdx === 0
  const isLast = tabIdx === TABS.length - 1
  const canCreate = !!state.project.workDir

  return (
    <div style={overlayStyle} onClick={onClose}>
      <div style={panelStyle} onClick={e => e.stopPropagation()}>
        {/* Header */}
        <div style={headerStyle}>
          <h3 style={{ margin: 0, color: '#e0e0e0', fontSize: '16px' }}>New Session</h3>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            {/* Preset selector */}
            {presets.length > 0 && (
              <select
                onChange={e => {
                  const p = presets.find(p => p.name === e.target.value)
                  if (p) loadPreset(p)
                  e.target.value = ''
                }}
                defaultValue=""
                style={presetSelectStyle}
              >
                <option value="" disabled>Load preset...</option>
                {presets.map(p => (
                  <option key={p.name} value={p.name}>{p.name}</option>
                ))}
              </select>
            )}
            <button type="button" onClick={onClose} style={closeBtnStyle}>x</button>
          </div>
        </div>

        <div style={bodyStyle}>
          {/* Tab sidebar */}
          <div style={sidebarStyle}>
            {TABS.map((t, i) => (
              <button
                key={t.id}
                type="button"
                onClick={() => setTab(t.id)}
                style={{
                  ...tabBtnStyle,
                  backgroundColor: tab === t.id ? '#1a1a2e' : 'transparent',
                  color: tab === t.id ? '#61dafb' : '#888',
                  borderLeft: tab === t.id ? '2px solid #61dafb' : '2px solid transparent',
                }}
              >
                <span style={tabNumStyle}>{i + 1}</span>
                {t.label}
              </button>
            ))}

            {/* Preset management in sidebar */}
            <div style={{ marginTop: 'auto', padding: '8px 14px', borderTop: '1px solid #2a2a4a' }}>
              {showSavePreset ? (
                <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                  <input
                    value={presetName}
                    onChange={e => setPresetName(e.target.value)}
                    onKeyDown={e => e.key === 'Enter' && presetName && savePreset(presetName)}
                    placeholder="Preset name"
                    autoFocus
                    style={presetInputStyle}
                  />
                  <div style={{ display: 'flex', gap: '4px' }}>
                    <button
                      type="button"
                      onClick={() => presetName && savePreset(presetName)}
                      style={presetSaveBtnStyle}
                    >
                      Save
                    </button>
                    <button
                      type="button"
                      onClick={() => { setShowSavePreset(false); setPresetName('') }}
                      style={presetCancelBtnStyle}
                    >
                      Cancel
                    </button>
                  </div>
                </div>
              ) : (
                <button
                  type="button"
                  onClick={() => setShowSavePreset(true)}
                  style={savePresetBtnStyle}
                >
                  Save Preset
                </button>
              )}
              {/* List saved presets with delete */}
              {presets.length > 0 && !showSavePreset && (
                <div style={{ marginTop: '8px', display: 'flex', flexDirection: 'column', gap: '2px' }}>
                  {presets.map(p => (
                    <div key={p.name} style={presetItemStyle}>
                      <span
                        style={{ flex: 1, cursor: 'pointer', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
                        onClick={() => loadPreset(p)}
                        title={`Load "${p.name}"`}
                      >
                        {p.name}
                      </span>
                      <button
                        type="button"
                        onClick={() => deletePreset(p.name)}
                        style={presetDeleteBtnStyle}
                        title="Delete preset"
                      >
                        x
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          {/* Tab content */}
          <div style={contentStyle}>
            {tab === 'project' && (
              <ProjectTab
                state={state.project}
                onChange={project => setState(s => ({ ...s, project }))}
              />
            )}
            {tab === 'extensions' && (
              <ExtensionsTab
                metas={extensionMetas}
                toggles={state.extensions}
                onChange={extensions => setState(s => ({ ...s, extensions }))}
                loading={capsLoading}
              />
            )}
            {tab === 'options' && (
              <OptionsTab
                state={state.options}
                onChange={options => setState(s => ({ ...s, options }))}
                wizardState={state}
                extensionMetas={extensionMetas}
              />
            )}
          </div>
        </div>

        {/* Footer */}
        <div style={footerStyle}>
          <button
            type="button"
            onClick={() => !isFirst && setTab(TABS[tabIdx - 1].id)}
            disabled={isFirst}
            style={{
              ...navBtnStyle,
              opacity: isFirst ? 0.3 : 1,
              cursor: isFirst ? 'default' : 'pointer',
            }}
          >
            Back
          </button>
          <div style={{ flex: 1 }} />
          {isLast ? (
            <button
              type="button"
              onClick={handleCreate}
              disabled={!canCreate}
              style={{
                ...createBtnStyle,
                opacity: canCreate ? 1 : 0.4,
                cursor: canCreate ? 'pointer' : 'default',
              }}
            >
              Create
            </button>
          ) : (
            <button
              type="button"
              onClick={() => setTab(TABS[tabIdx + 1].id)}
              style={nextBtnStyle}
            >
              Next
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

const overlayStyle: React.CSSProperties = {
  position: 'fixed',
  top: 0,
  left: 0,
  right: 0,
  bottom: 0,
  backgroundColor: 'rgba(0,0,0,0.6)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  zIndex: 1000,
}

const panelStyle: React.CSSProperties = {
  backgroundColor: '#1a1a2e',
  border: '1px solid #2a2a4a',
  borderRadius: '10px',
  width: '660px',
  maxHeight: '85vh',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
}

const headerStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: '16px 20px',
  borderBottom: '1px solid #2a2a4a',
}

const closeBtnStyle: React.CSSProperties = {
  background: 'none',
  border: 'none',
  color: '#888',
  fontSize: '18px',
  cursor: 'pointer',
  padding: '0 4px',
}

const bodyStyle: React.CSSProperties = {
  display: 'flex',
  flex: 1,
  minHeight: 0,
  overflow: 'hidden',
}

const sidebarStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  width: '160px',
  borderRight: '1px solid #2a2a4a',
  backgroundColor: '#12122a',
  padding: '8px 0',
  flexShrink: 0,
}

const tabBtnStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  padding: '10px 14px',
  border: 'none',
  cursor: 'pointer',
  fontSize: '13px',
  textAlign: 'left',
  width: '100%',
}

const tabNumStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: '18px',
  height: '18px',
  borderRadius: '50%',
  backgroundColor: '#2a2a4a',
  fontSize: '10px',
  color: '#888',
  flexShrink: 0,
}

const contentStyle: React.CSSProperties = {
  flex: 1,
  padding: '20px',
  overflowY: 'auto',
  minHeight: '380px',
}

const footerStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  padding: '12px 20px',
  borderTop: '1px solid #2a2a4a',
}

const navBtnStyle: React.CSSProperties = {
  padding: '6px 16px',
  backgroundColor: 'transparent',
  border: '1px solid #444',
  borderRadius: '4px',
  color: '#888',
  fontSize: '13px',
  cursor: 'pointer',
}

const nextBtnStyle: React.CSSProperties = {
  padding: '6px 16px',
  backgroundColor: '#2a2a4a',
  border: 'none',
  borderRadius: '4px',
  color: '#e0e0e0',
  fontSize: '13px',
  cursor: 'pointer',
}

const createBtnStyle: React.CSSProperties = {
  padding: '6px 20px',
  backgroundColor: '#61dafb',
  border: 'none',
  borderRadius: '4px',
  color: '#000',
  fontWeight: 'bold',
  fontSize: '13px',
  cursor: 'pointer',
}

const presetSelectStyle: React.CSSProperties = {
  padding: '4px 8px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  color: '#e0e0e0',
  fontSize: '12px',
  outline: 'none',
}

const savePresetBtnStyle: React.CSSProperties = {
  width: '100%',
  padding: '5px 8px',
  backgroundColor: 'transparent',
  border: '1px dashed #444',
  borderRadius: '4px',
  color: '#888',
  cursor: 'pointer',
  fontSize: '11px',
}

const presetInputStyle: React.CSSProperties = {
  padding: '4px 6px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '3px',
  color: '#e0e0e0',
  fontSize: '12px',
  outline: 'none',
}

const presetSaveBtnStyle: React.CSSProperties = {
  flex: 1,
  padding: '3px 6px',
  backgroundColor: '#61dafb',
  border: 'none',
  borderRadius: '3px',
  color: '#000',
  fontSize: '11px',
  cursor: 'pointer',
  fontWeight: 'bold',
}

const presetCancelBtnStyle: React.CSSProperties = {
  flex: 1,
  padding: '3px 6px',
  backgroundColor: 'transparent',
  border: '1px solid #444',
  borderRadius: '3px',
  color: '#888',
  fontSize: '11px',
  cursor: 'pointer',
}

const presetItemStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '4px',
  padding: '3px 0',
  fontSize: '11px',
  color: '#61dafb',
}

const presetDeleteBtnStyle: React.CSSProperties = {
  background: 'none',
  border: 'none',
  color: '#555',
  cursor: 'pointer',
  fontSize: '10px',
  padding: '0 2px',
  flexShrink: 0,
}
