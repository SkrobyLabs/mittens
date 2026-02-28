import { useState } from 'react'
import type { WizardState } from '../../types/wizard'
import type { ExtensionMeta } from '../../types/extension'

interface CommandPreviewProps {
  state: WizardState
  extensionMetas: ExtensionMeta[]
}

export function CommandPreview({ state, extensionMetas }: CommandPreviewProps) {
  const [copied, setCopied] = useState(false)
  const cmd = buildCommand(state, extensionMetas)

  const handleCopy = () => {
    navigator.clipboard.writeText(cmd).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }

  return (
    <div style={containerStyle}>
      <div style={headerStyle}>
        <span style={{ fontSize: '11px', color: '#888' }}>Command Preview</span>
        <button type="button" onClick={handleCopy} style={copyBtnStyle}>
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre style={preStyle}>{cmd}</pre>
    </div>
  )
}

function buildCommand(state: WizardState, metas: ExtensionMeta[]): string {
  const parts = ['mittens']

  // Extension flags.
  for (const ext of state.extensions) {
    if (!ext.enabled) continue
    const meta = metas.find(m => m.name === ext.name)
    if (!meta) continue

    // Check if this is a default-on extension being disabled.
    if (meta.defaultOn && !ext.enabled) {
      const noFlag = meta.flags.find(f => f.name.startsWith('--no-'))
      if (noFlag) parts.push(noFlag.name)
      continue
    }

    if (ext.allMode) {
      const allFlag = meta.flags.find(f => f.name.endsWith('-all'))
      if (allFlag) {
        parts.push(allFlag.name)
        continue
      }
    }

    // Primary flag (first flag that isn't --no- or -all).
    const primaryFlag = meta.flags.find(
      f => !f.name.startsWith('--no-') && !f.name.endsWith('-all')
    )
    if (!primaryFlag) continue

    parts.push(primaryFlag.name)
    if (ext.values.length > 0 && primaryFlag.argType !== 'none') {
      parts.push(ext.values.join(','))
    }
  }

  // Core flags.
  const opts = state.options
  if (opts.dind) parts.push('--dind')
  if (opts.worktree) parts.push('--worktree')
  if (opts.yolo) parts.push('--yolo')
  if (opts.networkHost) parts.push('--network-host')
  if (opts.noHistory) parts.push('--no-history')
  if (opts.noBuild) parts.push('--no-build')
  if (opts.noResume) parts.push('--no-resume')
  if (opts.shell) parts.push('--shell')

  // Extra dirs.
  for (const dir of state.project.extraDirs) {
    parts.push('--dir', dir)
  }

  // Claude args.
  const claudeArgs = opts.claudeArgs.trim()
  if (claudeArgs) {
    parts.push('--')
    parts.push(...claudeArgs.split(/\s+/).filter(Boolean))
  }

  return parts.join(' ')
}

const containerStyle: React.CSSProperties = {
  border: '1px solid #2a2a4a',
  borderRadius: '4px',
  overflow: 'hidden',
}

const headerStyle: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  alignItems: 'center',
  padding: '6px 10px',
  backgroundColor: '#1a1a2e',
  borderBottom: '1px solid #2a2a4a',
}

const copyBtnStyle: React.CSSProperties = {
  background: 'none',
  border: '1px solid #444',
  borderRadius: '3px',
  color: '#888',
  fontSize: '11px',
  padding: '2px 8px',
  cursor: 'pointer',
}

const preStyle: React.CSSProperties = {
  margin: 0,
  padding: '10px',
  backgroundColor: '#0f0f23',
  color: '#e0e0e0',
  fontSize: '12px',
  fontFamily: 'monospace',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
  lineHeight: 1.5,
}
