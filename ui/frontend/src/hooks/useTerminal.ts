import { useEffect, useRef, useCallback } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import type { WSMessage } from '../types/protocol'
import { useSessionStore } from '../store/sessionStore'

interface UseTerminalOptions {
  sessionId: string
  containerRef: React.RefObject<HTMLDivElement | null>
}

export function useTerminal({ sessionId, containerRef }: UseTerminalOptions) {
  const termRef = useRef<Terminal | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const updateSessionState = useSessionStore(s => s.updateSessionState)

  const sendResize = useCallback(() => {
    const term = termRef.current
    const ws = wsRef.current
    if (!term || !ws || ws.readyState !== WebSocket.OPEN) return

    const msg = JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols })
    ws.send(msg)
  }, [])

  useEffect(() => {
    const container = containerRef.current
    if (!container || !sessionId) return

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'JetBrains Mono', 'Fira Code', 'Cascadia Code', Menlo, monospace",
      theme: {
        background: '#1a1a2e',
        foreground: '#e0e0e0',
        cursor: '#61dafb',
        selectionBackground: '#3d3d6b',
      },
      allowProposedApi: true,
    })

    const fitAddon = new FitAddon()
    const linksAddon = new WebLinksAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(linksAddon)
    term.open(container)

    termRef.current = term
    fitRef.current = fitAddon

    // Fit terminal to container.
    requestAnimationFrame(() => {
      fitAddon.fit()
    })

    // WebSocket connection.
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${protocol}//${window.location.host}/ws/sessions/${sessionId}`)
    ws.binaryType = 'arraybuffer'
    wsRef.current = ws

    ws.onopen = () => {
      sendResize()
    }

    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) {
        // Binary frame: terminal output.
        term.write(new Uint8Array(event.data))
      } else {
        // Text frame: control message.
        try {
          const msg: WSMessage = JSON.parse(event.data)
          switch (msg.type) {
            case 'state':
              if (msg.state) {
                updateSessionState(sessionId, msg.state as any)
              }
              break
            case 'exit':
              updateSessionState(sessionId, 'stopped', msg.code)
              break
          }
        } catch {
          // Ignore parse errors.
        }
      }
    }

    ws.onclose = () => {
      term.write('\r\n\x1b[90m[Connection closed]\x1b[0m\r\n')
    }

    // Terminal input → WebSocket.
    const inputDisposable = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        const msg = JSON.stringify({ type: 'input', data: btoa(data) })
        ws.send(msg)
      }
    })

    // Resize observer.
    const ro = new ResizeObserver(() => {
      fitAddon.fit()
      sendResize()
    })
    ro.observe(container)

    return () => {
      inputDisposable.dispose()
      ro.disconnect()
      ws.close()
      term.dispose()
      termRef.current = null
      wsRef.current = null
      fitRef.current = null
    }
  }, [sessionId, containerRef, sendResize, updateSessionState])

  return { termRef, wsRef }
}
