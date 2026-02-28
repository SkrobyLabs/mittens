import { ToggleSwitch } from './ToggleSwitch'
import { TagInput } from './TagInput'
import type { ExtensionMeta } from '../../types/extension'
import type { ExtensionToggle } from '../../types/wizard'

interface ExtensionCardProps {
  meta: ExtensionMeta
  toggle: ExtensionToggle
  onChange: (toggle: ExtensionToggle) => void
}

export function ExtensionCard({ meta, toggle, onChange }: ExtensionCardProps) {
  const primaryFlag = meta.flags.find(
    f => !f.name.startsWith('--no-') && !f.name.endsWith('-all')
  )
  const allFlag = meta.flags.find(f => f.name.endsWith('-all'))
  const hasConfig = primaryFlag && primaryFlag.argType !== 'none'

  const handleToggle = (enabled: boolean) => {
    onChange({ ...toggle, enabled })
  }

  const handleAllMode = (allMode: boolean) => {
    onChange({ ...toggle, allMode, values: allMode ? [] : toggle.values })
  }

  const handleValues = (values: string[]) => {
    onChange({ ...toggle, values })
  }

  const handleEnumSelect = (value: string) => {
    if (primaryFlag?.multi) {
      const next = toggle.values.includes(value)
        ? toggle.values.filter(v => v !== value)
        : [...toggle.values, value]
      onChange({ ...toggle, values: next })
    } else {
      onChange({ ...toggle, values: [value] })
    }
  }

  return (
    <div style={cardStyle}>
      <div style={headerRow}>
        <ToggleSwitch checked={toggle.enabled} onChange={handleToggle} />
        <div style={{ flex: 1, marginLeft: '10px' }}>
          <div style={nameStyle}>{meta.name}</div>
          <div style={descStyle}>{meta.description}</div>
        </div>
      </div>

      {toggle.enabled && hasConfig && (
        <div style={configArea}>
          {/* All mode radio for extensions with -all flags */}
          {allFlag && (
            <div style={radioGroup}>
              <label style={radioLabel}>
                <input
                  type="radio"
                  name={`${meta.name}-mode`}
                  checked={!toggle.allMode}
                  onChange={() => handleAllMode(false)}
                />
                <span>Select specific</span>
              </label>
              <label style={radioLabel}>
                <input
                  type="radio"
                  name={`${meta.name}-mode`}
                  checked={toggle.allMode}
                  onChange={() => handleAllMode(true)}
                />
                <span>All</span>
              </label>
            </div>
          )}

          {/* CSV input */}
          {!toggle.allMode && primaryFlag?.argType === 'csv' && (
            <TagInput
              values={toggle.values}
              onChange={handleValues}
              placeholder={`Add ${meta.name} values...`}
            />
          )}

          {/* Enum: dropdown or multi-select chips */}
          {!toggle.allMode && primaryFlag?.argType === 'enum' && primaryFlag.enumValues && (
            primaryFlag.multi ? (
              <div style={chipGroup}>
                {primaryFlag.enumValues.map(v => (
                  <button
                    key={v}
                    type="button"
                    style={{
                      ...chipBtn,
                      backgroundColor: toggle.values.includes(v) ? '#61dafb' : '#2a2a4a',
                      color: toggle.values.includes(v) ? '#000' : '#e0e0e0',
                    }}
                    onClick={() => handleEnumSelect(v)}
                  >
                    {v}
                  </button>
                ))}
              </div>
            ) : (
              <select
                value={toggle.values[0] || ''}
                onChange={e => handleEnumSelect(e.target.value)}
                style={selectStyle}
              >
                <option value="">Default</option>
                {primaryFlag.enumValues.map(v => (
                  <option key={v} value={v}>{v}</option>
                ))}
              </select>
            )
          )}

          {/* Path input */}
          {!toggle.allMode && primaryFlag?.argType === 'path' && (
            <input
              type="text"
              value={toggle.values[0] || ''}
              onChange={e => handleValues([e.target.value])}
              placeholder="Enter path..."
              style={textInputStyle}
            />
          )}
        </div>
      )}
    </div>
  )
}

const cardStyle: React.CSSProperties = {
  border: '1px solid #2a2a4a',
  borderRadius: '6px',
  padding: '10px 12px',
  backgroundColor: '#12122a',
}

const headerRow: React.CSSProperties = {
  display: 'flex',
  alignItems: 'flex-start',
}

const nameStyle: React.CSSProperties = {
  fontSize: '13px',
  fontWeight: 600,
  color: '#e0e0e0',
}

const descStyle: React.CSSProperties = {
  fontSize: '11px',
  color: '#888',
  marginTop: '2px',
}

const configArea: React.CSSProperties = {
  marginTop: '10px',
  paddingTop: '10px',
  borderTop: '1px solid #2a2a4a',
}

const radioGroup: React.CSSProperties = {
  display: 'flex',
  gap: '16px',
  marginBottom: '8px',
}

const radioLabel: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  fontSize: '12px',
  color: '#e0e0e0',
  cursor: 'pointer',
}

const chipGroup: React.CSSProperties = {
  display: 'flex',
  flexWrap: 'wrap',
  gap: '6px',
}

const chipBtn: React.CSSProperties = {
  padding: '4px 10px',
  borderRadius: '3px',
  border: '1px solid #333',
  cursor: 'pointer',
  fontSize: '12px',
  fontWeight: 500,
}

const selectStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 8px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  color: '#e0e0e0',
  fontSize: '13px',
  outline: 'none',
}

const textInputStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 8px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  color: '#e0e0e0',
  fontSize: '13px',
  outline: 'none',
}
