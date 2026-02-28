interface ToggleSwitchProps {
  checked: boolean
  onChange: (checked: boolean) => void
  disabled?: boolean
}

export function ToggleSwitch({ checked, onChange, disabled }: ToggleSwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => !disabled && onChange(!checked)}
      style={{
        ...trackStyle,
        backgroundColor: checked ? '#61dafb' : '#333',
        opacity: disabled ? 0.5 : 1,
        cursor: disabled ? 'default' : 'pointer',
      }}
    >
      <span
        style={{
          ...thumbStyle,
          transform: checked ? 'translateX(16px)' : 'translateX(2px)',
        }}
      />
    </button>
  )
}

const trackStyle: React.CSSProperties = {
  position: 'relative',
  display: 'inline-flex',
  alignItems: 'center',
  width: '36px',
  height: '20px',
  borderRadius: '10px',
  border: 'none',
  padding: 0,
  transition: 'background-color 0.15s',
  flexShrink: 0,
}

const thumbStyle: React.CSSProperties = {
  display: 'block',
  width: '16px',
  height: '16px',
  borderRadius: '50%',
  backgroundColor: '#fff',
  transition: 'transform 0.15s',
  boxShadow: '0 1px 2px rgba(0,0,0,0.3)',
}
