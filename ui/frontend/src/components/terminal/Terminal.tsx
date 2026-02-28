import { useRef } from 'react'
import { useTerminal } from '../../hooks/useTerminal'
import '@xterm/xterm/css/xterm.css'

interface TerminalProps {
  sessionId: string
}

export function TerminalView({ sessionId }: TerminalProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  useTerminal({ sessionId, containerRef })

  return (
    <div
      ref={containerRef}
      style={{
        width: '100%',
        height: '100%',
        backgroundColor: '#1a1a2e',
      }}
    />
  )
}
