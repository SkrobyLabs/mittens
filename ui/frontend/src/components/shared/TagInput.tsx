import { useState } from 'react'

interface TagInputProps {
  values: string[]
  onChange: (values: string[]) => void
  placeholder?: string
}

export function TagInput({ values, onChange, placeholder }: TagInputProps) {
  const [input, setInput] = useState('')

  const addValue = (raw: string) => {
    const val = raw.trim()
    if (val && !values.includes(val)) {
      onChange([...values, val])
    }
    setInput('')
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      addValue(input)
    }
    if (e.key === 'Backspace' && !input && values.length > 0) {
      onChange(values.slice(0, -1))
    }
  }

  const removeValue = (idx: number) => {
    onChange(values.filter((_, i) => i !== idx))
  }

  return (
    <div style={containerStyle}>
      {values.map((v, i) => (
        <span key={i} style={chipStyle}>
          {v}
          <button
            type="button"
            onClick={() => removeValue(i)}
            style={chipRemoveStyle}
          >
            x
          </button>
        </span>
      ))}
      <input
        value={input}
        onChange={e => setInput(e.target.value)}
        onKeyDown={handleKeyDown}
        onBlur={() => input && addValue(input)}
        placeholder={values.length === 0 ? placeholder : ''}
        style={inputStyle}
      />
    </div>
  )
}

const containerStyle: React.CSSProperties = {
  display: 'flex',
  flexWrap: 'wrap',
  gap: '4px',
  padding: '4px 6px',
  backgroundColor: '#0f0f23',
  border: '1px solid #333',
  borderRadius: '4px',
  minHeight: '32px',
  alignItems: 'center',
}

const chipStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  gap: '4px',
  padding: '2px 8px',
  backgroundColor: '#2a2a4a',
  borderRadius: '3px',
  fontSize: '12px',
  color: '#e0e0e0',
}

const chipRemoveStyle: React.CSSProperties = {
  background: 'none',
  border: 'none',
  color: '#888',
  cursor: 'pointer',
  padding: '0 2px',
  fontSize: '11px',
  lineHeight: 1,
}

const inputStyle: React.CSSProperties = {
  flex: 1,
  minWidth: '80px',
  border: 'none',
  outline: 'none',
  backgroundColor: 'transparent',
  color: '#e0e0e0',
  fontSize: '13px',
  padding: '2px 0',
}
