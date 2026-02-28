import { FolderPicker } from '../../shared/FolderPicker'
import type { ProjectTabState } from '../../../types/wizard'

interface ProjectTabProps {
  state: ProjectTabState
  onChange: (state: ProjectTabState) => void
}

export function ProjectTab({ state, onChange }: ProjectTabProps) {
  const addExtraDir = () => {
    // Append an empty string; the FolderPicker will prompt selection.
    onChange({ ...state, extraDirs: [...state.extraDirs, ''] })
  }

  const updateExtraDir = (idx: number, path: string) => {
    const next = [...state.extraDirs]
    next[idx] = path
    onChange({ ...state, extraDirs: next })
  }

  const removeExtraDir = (idx: number) => {
    onChange({ ...state, extraDirs: state.extraDirs.filter((_, i) => i !== idx) })
  }

  return (
    <div style={containerStyle}>
      {/* Session Name */}
      <div style={fieldStyle}>
        <label style={labelStyle}>Session Name</label>
        <input
          value={state.name}
          onChange={e => onChange({ ...state, name: e.target.value })}
          placeholder="auto-generated"
          style={inputStyle}
        />
        <div style={hintStyle}>Optional. Leave blank for auto-generated name.</div>
      </div>

      {/* Work Directory */}
      <div style={fieldStyle}>
        <label style={labelStyle}>
          Work Directory <span style={{ color: '#f44336' }}>*</span>
        </label>
        <FolderPicker
          value={state.workDir}
          onChange={workDir => onChange({ ...state, workDir })}
          placeholder="Select your project directory..."
        />
      </div>

      {/* Extra Directories */}
      <div style={fieldStyle}>
        <label style={labelStyle}>Extra Directories</label>
        <div style={hintStyle}>Additional directories to mount in the container.</div>
        {state.extraDirs.map((dir, i) => (
          <div key={i} style={extraDirRow}>
            {dir ? (
              <div style={extraDirChip}>
                <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis' }}>{dir}</span>
                <button type="button" onClick={() => removeExtraDir(i)} style={removeBtnStyle}>
                  x
                </button>
              </div>
            ) : (
              <FolderPicker
                value=""
                onChange={path => updateExtraDir(i, path)}
                placeholder="Select directory..."
              />
            )}
          </div>
        ))}
        <button type="button" onClick={addExtraDir} style={addBtnStyle}>
          + Add Directory
        </button>
      </div>
    </div>
  )
}

const containerStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
}

const fieldStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '4px',
}

const labelStyle: React.CSSProperties = {
  fontSize: '12px',
  fontWeight: 600,
  color: '#e0e0e0',
  textTransform: 'uppercase',
  letterSpacing: '0.5px',
}

const hintStyle: React.CSSProperties = {
  fontSize: '11px',
  color: '#666',
}

const inputStyle: React.CSSProperties = {
  padding: '8px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  color: '#e0e0e0',
  fontSize: '13px',
  outline: 'none',
}

const extraDirRow: React.CSSProperties = {
  marginTop: '4px',
}

const extraDirChip: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  padding: '6px 10px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  fontSize: '13px',
  color: '#e0e0e0',
}

const removeBtnStyle: React.CSSProperties = {
  background: 'none',
  border: 'none',
  color: '#888',
  cursor: 'pointer',
  fontSize: '13px',
  padding: '0 4px',
  flexShrink: 0,
}

const addBtnStyle: React.CSSProperties = {
  marginTop: '4px',
  padding: '6px 12px',
  backgroundColor: 'transparent',
  border: '1px dashed #444',
  borderRadius: '4px',
  color: '#888',
  cursor: 'pointer',
  fontSize: '12px',
  alignSelf: 'flex-start',
}
