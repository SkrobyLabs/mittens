export type SessionState = 'running' | 'stopped' | 'terminated' | 'orphaned'

export interface SessionConfig {
  workDir: string
  extensions?: string[]
  flags?: string[]
  claudeArgs?: string[]
  extraDirs?: string[]
}

export interface Session {
  id: string
  name: string
  config: SessionConfig
  state: SessionState
  pid: number
  exitCode: number
  createdAt: string
  stoppedAt?: string
}

export interface CreateSessionRequest {
  name?: string
  workDir: string
  extensions?: string[]
  flags?: string[]
  claudeArgs?: string[]
  extraDirs?: string[]
  shell?: boolean
}

export interface RelaunchRequest {
  workDir?: string
  extensions?: string[]
  flags?: string[]
  claudeArgs?: string[]
  extraDirs?: string[]
}
