import { useEffect } from 'react'
import { useSessionStore } from '../store/sessionStore'

/** Fetches sessions on mount and returns the store. */
export function useSessionAPI() {
  const store = useSessionStore()

  useEffect(() => {
    store.fetchSessions()
    // Poll for updates every 5 seconds.
    const interval = setInterval(() => {
      store.fetchSessions()
    }, 5000)
    return () => clearInterval(interval)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return store
}
