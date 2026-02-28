import { create } from 'zustand'
import type { ChannelRequest } from '../types/channel'

const API = '/api/v1'

interface ChannelStore {
  requests: ChannelRequest[]

  addRequest: (req: ChannelRequest) => void
  removeRequest: (id: string) => void
  respond: (requestId: string, approved: boolean, reason?: string) => Promise<void>
}

export const useChannelStore = create<ChannelStore>((set, get) => ({
  requests: [],

  addRequest: (req) => {
    set({ requests: [...get().requests, req] })
  },

  removeRequest: (id) => {
    set({ requests: get().requests.filter(r => r.id !== id) })
  },

  respond: async (requestId, approved, reason) => {
    await fetch(`${API}/channel/${requestId}/respond`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ approved, reason }),
    })
    set({ requests: get().requests.filter(r => r.id !== requestId) })
  },
}))
