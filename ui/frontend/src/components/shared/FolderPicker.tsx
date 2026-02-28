import { useState, useEffect, useCallback } from 'react'

const LAST_FOLDER_KEY = 'mittens:lastFolder'

interface FolderPickerProps {
  value: string
  onChange: (path: string) => void
  placeholder?: string
}

interface BrowseEntry {
  name: string
  path: string
  isDir: boolean
}

interface BrowseResponse {
  path: string
  parent: string
  entries: BrowseEntry[]
}

export function FolderPicker({ value, onChange, placeholder }: FolderPickerProps) {
  const [open, setOpen] = useState(false)
  const [browsePath, setBrowsePath] = useState('')
  const [entries, setEntries] = useState<BrowseEntry[]>([])
  const [parent, setParent] = useState('')
  const [loading, setLoading] = useState(false)

  const browse = useCallback(async (path: string) => {
    setLoading(true)
    try {
      const params = path ? `?path=${encodeURIComponent(path)}` : ''
      const res = await fetch(`/api/v1/fs/browse${params}`)
      if (!res.ok) return
      const data: BrowseResponse = await res.json()
      setBrowsePath(data.path)
      setEntries(data.entries || [])
      setParent(data.parent)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (open) {
      // Start from: current value → last used folder → home (empty triggers server default)
      const startPath = value || localStorage.getItem(LAST_FOLDER_KEY) || ''
      browse(startPath)
    }
  }, [open, value, browse])

  const handleSelect = () => {
    onChange(browsePath)
    localStorage.setItem(LAST_FOLDER_KEY, browsePath)
    setOpen(false)
  }

  if (!open) {
    return (
      <div style={closedStyle} onClick={() => setOpen(true)}>
        <span style={{ flex: 1, color: value ? '#e0e0e0' : '#555' }}>
          {value || placeholder || 'Select a folder...'}
        </span>
        <span style={{ color: '#888', fontSize: '12px' }}>Browse</span>
      </div>
    )
  }

  // Split path into breadcrumb segments.
  const segments = browsePath.split('/').filter(Boolean)

  return (
    <div style={browserStyle}>
      {/* Breadcrumbs */}
      <div style={breadcrumbBar}>
        <span
          style={crumbStyle}
          onClick={() => browse('/')}
        >
          /
        </span>
        {segments.map((seg, i) => {
          const path = '/' + segments.slice(0, i + 1).join('/')
          const isLast = i === segments.length - 1
          return (
            <span key={path}>
              <span
                style={{
                  ...crumbStyle,
                  color: isLast ? '#e0e0e0' : '#61dafb',
                  cursor: isLast ? 'default' : 'pointer',
                }}
                onClick={() => !isLast && browse(path)}
              >
                {seg}
              </span>
              {!isLast && <span style={{ color: '#555' }}>/</span>}
            </span>
          )
        })}
      </div>

      {/* Directory listing */}
      <div style={listStyle}>
        {parent && (
          <div
            style={entryStyle}
            onClick={() => browse(parent)}
          >
            <span style={{ color: '#888' }}>..</span>
          </div>
        )}
        {loading && <div style={{ color: '#555', padding: '8px', fontSize: '12px' }}>Loading...</div>}
        {!loading && entries.length === 0 && (
          <div style={{ color: '#555', padding: '8px', fontSize: '12px' }}>No subdirectories</div>
        )}
        {!loading && entries.map(e => (
          <div
            key={e.path}
            style={entryStyle}
            onClick={() => browse(e.path)}
          >
            <span style={{ color: '#61dafb', marginRight: '6px' }}>&#128193;</span>
            {e.name}
          </div>
        ))}
      </div>

      {/* Actions */}
      <div style={actionsStyle}>
        <button type="button" onClick={() => setOpen(false)} style={cancelBtnStyle}>
          Cancel
        </button>
        <button type="button" onClick={handleSelect} style={selectBtnStyle}>
          Select
        </button>
      </div>
    </div>
  )
}

const closedStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  padding: '8px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  cursor: 'pointer',
  fontSize: '13px',
}

const browserStyle: React.CSSProperties = {
  border: '1px solid #2a2a4a',
  borderRadius: '4px',
  overflow: 'hidden',
  backgroundColor: '#0f0f23',
}

const breadcrumbBar: React.CSSProperties = {
  display: 'flex',
  flexWrap: 'wrap',
  gap: '2px',
  padding: '6px 10px',
  backgroundColor: '#1a1a2e',
  borderBottom: '1px solid #2a2a4a',
  fontSize: '12px',
}

const crumbStyle: React.CSSProperties = {
  color: '#61dafb',
  cursor: 'pointer',
  padding: '0 2px',
}

const listStyle: React.CSSProperties = {
  maxHeight: '180px',
  overflowY: 'auto',
}

const entryStyle: React.CSSProperties = {
  padding: '5px 10px',
  fontSize: '13px',
  color: '#e0e0e0',
  cursor: 'pointer',
  borderBottom: '1px solid #1a1a2e',
}

const actionsStyle: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'flex-end',
  gap: '8px',
  padding: '8px 10px',
  borderTop: '1px solid #2a2a4a',
  backgroundColor: '#1a1a2e',
}

const cancelBtnStyle: React.CSSProperties = {
  padding: '4px 12px',
  backgroundColor: 'transparent',
  border: '1px solid #444',
  borderRadius: '3px',
  color: '#888',
  cursor: 'pointer',
  fontSize: '12px',
}

const selectBtnStyle: React.CSSProperties = {
  padding: '4px 12px',
  backgroundColor: '#61dafb',
  border: 'none',
  borderRadius: '3px',
  color: '#000',
  cursor: 'pointer',
  fontWeight: 'bold',
  fontSize: '12px',
}
