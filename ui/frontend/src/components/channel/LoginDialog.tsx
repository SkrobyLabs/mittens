import type { ChannelRequest } from '../../types/channel'
import { useChannelStore } from '../../store/channelStore'

interface LoginDialogProps {
  request: ChannelRequest | null
}

export function LoginDialog({ request }: LoginDialogProps) {
  const respond = useChannelStore(s => s.respond)

  if (!request || request.type !== 'login') return null

  const url = (request.payload?.url as string) || ''
  const provider = (request.payload?.provider as string) || 'unknown'

  const handleOpen = () => {
    if (url) {
      window.open(url, '_blank')
    }
    respond(request.id, true)
  }

  return (
    <div style={overlayStyle}>
      <div style={dialogStyle}>
        <h3 style={{ margin: '0 0 12px', color: '#e0e0e0' }}>Login Required</h3>
        <p style={{ color: '#ccc', fontSize: '13px', marginBottom: '8px' }}>
          Session <strong>{request.sessionId}</strong> needs authentication ({provider}).
        </p>
        {url && (
          <a
            href={url}
            target="_blank"
            rel="noopener noreferrer"
            style={{
              display: 'block',
              backgroundColor: '#0f0f23',
              padding: '8px 12px',
              borderRadius: '4px',
              color: '#61dafb',
              fontSize: '12px',
              marginBottom: '12px',
              wordBreak: 'break-all',
            }}
          >
            {url}
          </a>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
          <button
            onClick={() => respond(request.id, false, 'User dismissed')}
            style={denyBtnStyle}
          >
            Dismiss
          </button>
          <button onClick={handleOpen} style={approveBtnStyle}>
            Open & Approve
          </button>
        </div>
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
const denyBtnStyle: React.CSSProperties = {
  padding: '6px 16px', backgroundColor: 'transparent', border: '1px solid #444',
  borderRadius: '4px', color: '#888', cursor: 'pointer',
}
const approveBtnStyle: React.CSSProperties = {
  padding: '6px 16px', backgroundColor: '#61dafb', border: 'none',
  borderRadius: '4px', color: '#000', cursor: 'pointer', fontWeight: 'bold',
}
