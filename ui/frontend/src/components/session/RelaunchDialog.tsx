import { useState } from 'react'
import type { Session, RelaunchRequest } from '../../types/session'

interface RelaunchDialogProps {
  open: boolean
  session: Session | null
  onClose: () => void
  onRelaunch: (id: string, req: RelaunchRequest) => void
}

export function RelaunchDialog({ open, session, onClose, onRelaunch }: RelaunchDialogProps) {
  const [extensions, setExtensions] = useState('')
  const [flags, setFlags] = useState('')
  const [extraDirs, setExtraDirs] = useState('')

  if (!open || !session) return null

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const req: RelaunchRequest = {}
    if (extensions) req.extensions = extensions.split(',').map(s => s.trim()).filter(Boolean)
    if (flags) req.flags = flags.split(' ').filter(Boolean)
    if (extraDirs) req.extraDirs = extraDirs.split(',').map(s => s.trim()).filter(Boolean)
    onRelaunch(session.id, req)
    onClose()
  }

  return (
    <div style={overlayStyle}>
      <div style={dialogStyle}>
        <h3 style={{ margin: '0 0 16px', color: '#e0e0e0' }}>
          Relaunch: {session.name}
        </h3>
        <p style={{ color: '#888', fontSize: '12px', marginBottom: '12px' }}>
          The session will be terminated and restarted with --continue to preserve the conversation.
        </p>
        <form onSubmit={handleSubmit}>
          <div style={fieldStyle}>
            <label style={labelStyle}>Extensions (comma-separated)</label>
            <input
              value={extensions}
              onChange={e => setExtensions(e.target.value)}
              placeholder={session.config.extensions?.join(', ') || 'none'}
              style={inputStyle}
            />
          </div>
          <div style={fieldStyle}>
            <label style={labelStyle}>Additional Flags</label>
            <input
              value={flags}
              onChange={e => setFlags(e.target.value)}
              placeholder={session.config.flags?.join(' ') || 'none'}
              style={inputStyle}
            />
          </div>
          <div style={fieldStyle}>
            <label style={labelStyle}>Extra Dirs (comma-separated)</label>
            <input
              value={extraDirs}
              onChange={e => setExtraDirs(e.target.value)}
              placeholder={session.config.extraDirs?.join(', ') || 'none'}
              style={inputStyle}
            />
          </div>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px', marginTop: '16px' }}>
            <button type="button" onClick={onClose} style={cancelBtnStyle}>Cancel</button>
            <button type="submit" style={submitBtnStyle}>Relaunch</button>
          </div>
        </form>
      </div>
    </div>
  )
}

const overlayStyle: React.CSSProperties = {
  position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
  backgroundColor: 'rgba(0,0,0,0.6)',
  display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
}
const dialogStyle: React.CSSProperties = {
  backgroundColor: '#1a1a2e', border: '1px solid #2a2a4a', borderRadius: '8px',
  padding: '24px', width: '420px',
}
const fieldStyle: React.CSSProperties = { marginBottom: '12px' }
const labelStyle: React.CSSProperties = { display: 'block', fontSize: '12px', color: '#888', marginBottom: '4px' }
const inputStyle: React.CSSProperties = {
  width: '100%', padding: '8px', backgroundColor: '#0f0f23',
  border: '1px solid #333', borderRadius: '4px', color: '#e0e0e0', fontSize: '13px', outline: 'none',
}
const cancelBtnStyle: React.CSSProperties = {
  padding: '6px 16px', backgroundColor: 'transparent', border: '1px solid #444',
  borderRadius: '4px', color: '#888', cursor: 'pointer',
}
const submitBtnStyle: React.CSSProperties = {
  padding: '6px 16px', backgroundColor: '#ff9800', border: 'none',
  borderRadius: '4px', color: '#000', cursor: 'pointer', fontWeight: 'bold',
}
