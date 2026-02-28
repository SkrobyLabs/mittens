import type { Tab } from '../../store/layoutStore'

interface TabBarProps {
  tabs: Tab[]
  activeTabId: string | null
  onSelect: (tabId: string) => void
  onClose: (tabId: string) => void
  onNew: () => void
}

export function TabBar({ tabs, activeTabId, onSelect, onClose, onNew }: TabBarProps) {
  return (
    <div style={{
      display: 'flex',
      alignItems: 'center',
      backgroundColor: '#0f0f23',
      borderBottom: '1px solid #2a2a4a',
      height: '32px',
      overflow: 'hidden',
    }}>
      {tabs.map(tab => (
        <div
          key={tab.id}
          onClick={() => onSelect(tab.id)}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: '4px',
            padding: '4px 12px',
            cursor: 'pointer',
            backgroundColor: tab.id === activeTabId ? '#1a1a2e' : 'transparent',
            borderRight: '1px solid #2a2a4a',
            borderBottom: tab.id === activeTabId ? '2px solid #61dafb' : '2px solid transparent',
            color: tab.id === activeTabId ? '#e0e0e0' : '#888',
            fontSize: '12px',
            userSelect: 'none',
          }}
        >
          <span>{tab.label}</span>
          <span
            onClick={(e) => { e.stopPropagation(); onClose(tab.id) }}
            style={{
              cursor: 'pointer',
              color: '#666',
              marginLeft: '4px',
              fontSize: '10px',
            }}
          >
            x
          </span>
        </div>
      ))}
      <button
        onClick={onNew}
        style={{
          background: 'none',
          border: 'none',
          color: '#666',
          cursor: 'pointer',
          padding: '4px 8px',
          fontSize: '14px',
        }}
        title="New session"
      >
        +
      </button>
    </div>
  )
}
