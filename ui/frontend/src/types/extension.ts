export interface ExtensionFlagMeta {
  name: string
  description: string
  argType: 'none' | 'csv' | 'enum' | 'path'
  enumValues?: string[]
  multi?: boolean
}

export interface ExtensionMeta {
  name: string
  description: string
  defaultOn: boolean
  flags: ExtensionFlagMeta[]
}

export interface CoreFlagMeta {
  name: string
  description: string
}

export interface CapsResponse {
  extensions: ExtensionMeta[]
  coreFlags: CoreFlagMeta[]
}
