import type { ChannelRequest } from '../../types/channel'
import { useChannelStore } from '../../store/channelStore'

interface AddDirDialogProps {
  request: ChannelRequest | null
}

export function AddDirDialog({ request }: AddDirDialogProps) {
  const respond = useChannelStore(s => s.respond)

  if (!request || request.type !== 'add-dir') return null

  const path = (request.payload?.path as string) || 'unknown'
  const reason = (request.payload?.reason as string) || ''

  return (
    <div style={overlayStyle}>
      <div style={dialogStyle}>
        <h3 style={{ margin: '0 0 12px', color: '#e0e0e0' }}>Directory Access Request</h3>
        <p style={{ color: '#ccc', fontSize: '13px', marginBottom: '8px' }}>
          Session <strong>{request.sessionId}</strong> requests access to:
        </p>
        <code style={{
          display: 'block',
          backgroundColor: '#0f0f23',
          padding: '8px 12px',
          borderRadius: '4px',
          color: '#61dafb',
          fontSize: '13px',
          marginBottom: '8px',
        }}>
          {path}
        </code>
        {reason && (
          <p style={{ color: '#888', fontSize: '12px', marginBottom: '12px' }}>
            Reason: {reason}
          </p>
        )}
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '8px' }}>
          <button
            onClick={() => respond(request.id, false, 'User denied')}
            style={denyBtnStyle}
          >
            Deny
          </button>
          <button
            onClick={() => respond(request.id, true)}
            style={approveBtnStyle}
          >
            Allow
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
  padding: '6px 16px', backgroundColor: 'transparent', border: '1px solid #f44336',
  borderRadius: '4px', color: '#f44336', cursor: 'pointer',
}
const approveBtnStyle: React.CSSProperties = {
  padding: '6px 16px', backgroundColor: '#4caf50', border: 'none',
  borderRadius: '4px', color: '#fff', cursor: 'pointer', fontWeight: 'bold',
}
