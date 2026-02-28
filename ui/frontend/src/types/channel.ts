export interface ChannelRequest {
  id: string
  sessionId: string
  type: string // 'add-dir', 'login', etc.
  payload: Record<string, unknown>
}

export interface ChannelResponse {
  id: string
  approved: boolean
  reason?: string
}
