import { ToggleSwitch } from '../../shared/ToggleSwitch'
import { CommandPreview } from '../../shared/CommandPreview'
import type { OptionsTabState, WizardState } from '../../../types/wizard'
import type { ExtensionMeta } from '../../../types/extension'

interface OptionsTabProps {
  state: OptionsTabState
  onChange: (state: OptionsTabState) => void
  wizardState: WizardState
  extensionMetas: ExtensionMeta[]
}

const FLAG_ROWS: { key: keyof OptionsTabState; label: string; description: string }[] = [
  { key: 'dind', label: 'Docker-in-Docker', description: 'Enable DinD with --privileged' },
  { key: 'yolo', label: 'Yolo Mode', description: 'Skip all permission prompts' },
  { key: 'worktree', label: 'Worktree', description: 'Git worktree isolation per invocation' },
  { key: 'networkHost', label: 'Host Networking', description: 'Use host networking instead of bridge' },
  { key: 'noHistory', label: 'No History', description: 'Disable session persistence (ephemeral)' },
  { key: 'noBuild', label: 'No Build', description: 'Skip the Docker image build step' },
  { key: 'noResume', label: 'No Resume', description: 'Start a new session instead of continuing' },
  { key: 'shell', label: 'Shell', description: 'Start a bash shell instead of Claude' },
]

export function OptionsTab({ state, onChange, wizardState, extensionMetas }: OptionsTabProps) {
  return (
    <div style={containerStyle}>
      {/* Container Options */}
      <div>
        <div style={sectionLabel}>Container Options</div>
        <div style={flagList}>
          {FLAG_ROWS.map(row => (
            <div key={row.key} style={flagRow}>
              <ToggleSwitch
                checked={state[row.key] as boolean}
                onChange={val => onChange({ ...state, [row.key]: val })}
              />
              <div style={{ marginLeft: '10px' }}>
                <div style={flagLabel}>{row.label}</div>
                <div style={flagDesc}>{row.description}</div>
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* Claude Arguments */}
      <div>
        <div style={sectionLabel}>Claude Arguments</div>
        <input
          value={state.claudeArgs}
          onChange={e => onChange({ ...state, claudeArgs: e.target.value })}
          placeholder="--model sonnet --print"
          style={inputStyle}
        />
        <div style={hintStyle}>Passed after -- separator to Claude Code.</div>
      </div>

      {/* Command Preview */}
      <CommandPreview state={wizardState} extensionMetas={extensionMetas} />
    </div>
  )
}

const containerStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '20px',
}

const sectionLabel: React.CSSProperties = {
  fontSize: '11px',
  fontWeight: 600,
  color: '#888',
  textTransform: 'uppercase',
  letterSpacing: '0.8px',
  marginBottom: '10px',
}

const flagList: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
}

const flagRow: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  padding: '6px 0',
}

const flagLabel: React.CSSProperties = {
  fontSize: '13px',
  color: '#e0e0e0',
  fontWeight: 500,
}

const flagDesc: React.CSSProperties = {
  fontSize: '11px',
  color: '#666',
  marginTop: '1px',
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '8px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  color: '#e0e0e0',
  fontSize: '13px',
  outline: 'none',
}

const hintStyle: React.CSSProperties = {
  fontSize: '11px',
  color: '#666',
  marginTop: '4px',
}
