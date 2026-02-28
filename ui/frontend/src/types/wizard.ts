export interface ProjectTabState {
  name: string
  workDir: string
  extraDirs: string[]
}

export interface ExtensionToggle {
  name: string
  enabled: boolean
  allMode: boolean
  values: string[]
}

export interface OptionsTabState {
  dind: boolean
  yolo: boolean
  worktree: boolean
  networkHost: boolean
  noHistory: boolean
  noBuild: boolean
  shell: boolean
  noResume: boolean
  claudeArgs: string
}

export interface WizardState {
  project: ProjectTabState
  extensions: ExtensionToggle[]
  options: OptionsTabState
}

export type WizardTab = 'project' | 'extensions' | 'options'
