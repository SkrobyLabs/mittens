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

    // Intercept mouse wheel: prevent xterm.js default (which converts
    // wheel to up/down arrow keys in alt-screen) and instead send SGR
    // mouse wheel escape sequences directly to tmux via the WebSocket.
    term.attachCustomWheelEventHandler((e: WheelEvent) => {
      e.preventDefault()
      const ws = wsRef.current
      if (ws && ws.readyState === WebSocket.OPEN) {
        // SGR mouse: \e[<button;col;rowM  — 64=wheel up, 65=wheel down
        const button = e.deltaY < 0 ? 64 : 65
        const lines = Math.max(1, Math.ceil(Math.abs(e.deltaY) / 40))
        for (let i = 0; i < lines; i++) {
          const seq = `\x1b[<${button};1;1M`
          ws.send(JSON.stringify({ type: 'input', data: btoa(seq) }))
        }
      }
      return false
    })

    // Fit terminal to container.
    requestAnimationFrame(() => {
      fitAddon.fit()
    })

    // Reconnection state.
    let disposed = false
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let fibPrev = 0
    let fibCurr = 1
    let sessionExited = false

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/ws/sessions/${sessionId}`

    function connect() {
      if (disposed) return

      const ws = new WebSocket(wsUrl)
      ws.binaryType = 'arraybuffer'
      wsRef.current = ws

      ws.onopen = () => {
        // Reset backoff on successful connection.
        fibPrev = 0
        fibCurr = 1
        sendResize()
      }

      ws.onmessage = (event) => {
        if (event.data instanceof ArrayBuffer) {
          term.write(new Uint8Array(event.data))
        } else {
          try {
            const msg: WSMessage = JSON.parse(event.data)
            switch (msg.type) {
              case 'state':
                if (msg.state) {
                  updateSessionState(sessionId, msg.state as any)
                }
                break
              case 'exit':
                sessionExited = true
                updateSessionState(sessionId, 'stopped', msg.code)
                break
            }
          } catch {
            // Ignore parse errors.
          }
        }
      }

      ws.onclose = () => {
        if (disposed || sessionExited) return
        scheduleReconnect()
      }
    }

    function scheduleReconnect() {
      // Fibonacci backoff: 1, 1, 2, 3, 5, 8, 10, 10, 10...
      const delay = Math.min(fibCurr, 10)
      const next = fibPrev + fibCurr
      fibPrev = fibCurr
      fibCurr = next

      term.write(`\r\n\x1b[90m[Disconnected — reconnecting in ${delay}s]\x1b[0m\r\n`)
      reconnectTimer = setTimeout(() => {
        if (disposed) return
        term.write('\x1b[90m[Reconnecting…]\x1b[0m\r\n')
        connect()
      }, delay * 1000)
    }

    connect()

    // Terminal input → WebSocket.
    const inputDisposable = term.onData((data) => {
      const ws = wsRef.current
      if (ws && ws.readyState === WebSocket.OPEN) {
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
      disposed = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      inputDisposable.dispose()
      ro.disconnect()
      wsRef.current?.close()
      term.dispose()
      termRef.current = null
      wsRef.current = null
      fitRef.current = null
    }
  }, [sessionId, containerRef, sendResize, updateSessionState])

  return { termRef, wsRef }
}
