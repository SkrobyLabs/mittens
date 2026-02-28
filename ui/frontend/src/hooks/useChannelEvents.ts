import { useEffect } from 'react'
import { useChannelStore } from '../store/channelStore'
import type { ChannelRequest } from '../types/channel'

/** Connects to the channel SSE endpoint and dispatches events to the store. */
export function useChannelEvents() {
  const addRequest = useChannelStore(s => s.addRequest)

  useEffect(() => {
    const es = new EventSource('/api/v1/channel/events')

    es.onmessage = (event) => {
      try {
        const req: ChannelRequest = JSON.parse(event.data)
        addRequest(req)
      } catch {
        // Ignore parse errors.
      }
    }

    es.onerror = () => {
      // EventSource will auto-reconnect.
    }

    return () => es.close()
  }, [addRequest])
}
