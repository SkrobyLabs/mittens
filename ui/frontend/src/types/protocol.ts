export type WSMessageType = 'input' | 'resize' | 'scrollback' | 'state' | 'exit'

export interface WSMessage {
  type: WSMessageType
  data?: string
  rows?: number
  cols?: number
  state?: string
  code?: number
}

export interface InputMessage {
  type: 'input'
  data: string // base64
}

export interface ResizeMessage {
  type: 'resize'
  rows: number
  cols: number
}
