import { ExtensionCard } from '../../shared/ExtensionCard'
import type { ExtensionMeta } from '../../../types/extension'
import type { ExtensionToggle } from '../../../types/wizard'

interface ExtensionsTabProps {
  metas: ExtensionMeta[]
  toggles: ExtensionToggle[]
  onChange: (toggles: ExtensionToggle[]) => void
  loading: boolean
}

const CATEGORIES: Record<string, string[]> = {
  'Security & Network': ['firewall', 'ssh', 'gh'],
  'Cloud Providers': ['aws', 'azure', 'gcp', 'kubectl'],
  'Runtimes': ['go', 'dotnet'],
  'Integrations': ['mcp'],
}

export function ExtensionsTab({ metas, toggles, onChange, loading }: ExtensionsTabProps) {
  if (loading) {
    return <div style={{ color: '#888', fontSize: '13px' }}>Loading extensions...</div>
  }

  const handleChange = (name: string, toggle: ExtensionToggle) => {
    onChange(toggles.map(t => (t.name === name ? toggle : t)))
  }

  // Group extensions into categories, with uncategorized at the end.
  const categorized = Object.entries(CATEGORIES)
    .map(([category, names]) => ({
      category,
      extensions: names
        .map(n => ({ meta: metas.find(m => m.name === n), toggle: toggles.find(t => t.name === n) }))
        .filter((e): e is { meta: ExtensionMeta; toggle: ExtensionToggle } => !!e.meta && !!e.toggle),
    }))
    .filter(g => g.extensions.length > 0)

  const categorizedNames = new Set(Object.values(CATEGORIES).flat())
  const uncategorized = metas
    .filter(m => !categorizedNames.has(m.name))
    .map(m => ({ meta: m, toggle: toggles.find(t => t.name === m.name) }))
    .filter((e): e is { meta: ExtensionMeta; toggle: ExtensionToggle } => !!e.toggle)

  return (
    <div style={containerStyle}>
      {categorized.map(({ category, extensions }) => (
        <div key={category}>
          <div style={categoryStyle}>{category}</div>
          <div style={gridStyle}>
            {extensions.map(({ meta, toggle }) => (
              <ExtensionCard
                key={meta.name}
                meta={meta}
                toggle={toggle}
                onChange={t => handleChange(meta.name, t)}
              />
            ))}
          </div>
        </div>
      ))}
      {uncategorized.length > 0 && (
        <div>
          <div style={categoryStyle}>Other</div>
          <div style={gridStyle}>
            {uncategorized.map(({ meta, toggle }) => (
              <ExtensionCard
                key={meta.name}
                meta={meta}
                toggle={toggle}
                onChange={t => handleChange(meta.name, t)}
              />
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

const containerStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '16px',
}

const categoryStyle: React.CSSProperties = {
  fontSize: '11px',
  fontWeight: 600,
  color: '#888',
  textTransform: 'uppercase',
  letterSpacing: '0.8px',
  marginBottom: '8px',
}

const gridStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: '8px',
}
