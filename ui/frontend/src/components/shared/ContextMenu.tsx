import { useEffect, useRef } from 'react'

export interface MenuItem {
  label: string
  onClick: () => void
  disabled?: boolean
  danger?: boolean
  submenu?: MenuItem[]
}

export interface Separator {
  separator: true
}

export type MenuEntry = MenuItem | Separator

export function isSeparator(entry: MenuEntry): entry is Separator {
  return 'separator' in entry
}

interface ContextMenuProps {
  x: number
  y: number
  items: MenuEntry[]
  onClose: () => void
}

export function ContextMenu({ x, y, items, onClose }: ContextMenuProps) {
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const handle = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        onClose()
      }
    }
    document.addEventListener('mousedown', handle)
    return () => document.removeEventListener('mousedown', handle)
  }, [onClose])

  useEffect(() => {
    const handle = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handle)
    return () => document.removeEventListener('keydown', handle)
  }, [onClose])

  // Clamp position so menu stays within viewport.
  const style: React.CSSProperties = {
    position: 'fixed',
    left: x,
    top: y,
    zIndex: 9999,
    backgroundColor: '#1a1a2e',
    border: '1px solid #2a2a4a',
    borderRadius: '6px',
    padding: '4px 0',
    minWidth: '160px',
    boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
  }

  return (
    <div ref={ref} style={style}>
      {items.map((entry, i) => {
        if (isSeparator(entry)) {
          return <div key={i} style={{ height: '1px', backgroundColor: '#2a2a4a', margin: '4px 0' }} />
        }
        return <MenuItemRow key={i} item={entry} onClose={onClose} />
      })}
    </div>
  )
}

function MenuItemRow({ item, onClose }: { item: MenuItem; onClose: () => void }) {
  const ref = useRef<HTMLDivElement>(null)

  if (item.submenu && item.submenu.length > 0) {
    return <SubmenuRow item={item} onClose={onClose} />
  }

  return (
    <div
      ref={ref}
      onClick={() => {
        if (item.disabled) return
        item.onClick()
        onClose()
      }}
      style={{
        padding: '6px 12px',
        fontSize: '12px',
        color: item.disabled ? '#555' : item.danger ? '#f44336' : '#ccc',
        cursor: item.disabled ? 'default' : 'pointer',
        display: 'flex',
        alignItems: 'center',
      }}
      onMouseEnter={(e) => {
        if (!item.disabled) e.currentTarget.style.backgroundColor = '#2a2a4a'
      }}
      onMouseLeave={(e) => {
        e.currentTarget.style.backgroundColor = 'transparent'
      }}
    >
      {item.label}
    </div>
  )
}

function SubmenuRow({ item, onClose }: { item: MenuItem; onClose: () => void }) {
  const rowRef = useRef<HTMLDivElement>(null)
  const subRef = useRef<HTMLDivElement>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const showSub = () => {
    if (timerRef.current) clearTimeout(timerRef.current)
    if (subRef.current) subRef.current.style.display = 'block'
  }

  const hideSub = () => {
    timerRef.current = setTimeout(() => {
      if (subRef.current) subRef.current.style.display = 'none'
    }, 150)
  }

  return (
    <div
      ref={rowRef}
      style={{ position: 'relative' }}
      onMouseEnter={showSub}
      onMouseLeave={hideSub}
    >
      <div
        style={{
          padding: '6px 12px',
          fontSize: '12px',
          color: '#ccc',
          cursor: 'pointer',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
        }}
        onMouseEnter={(e) => { e.currentTarget.style.backgroundColor = '#2a2a4a' }}
        onMouseLeave={(e) => { e.currentTarget.style.backgroundColor = 'transparent' }}
      >
        <span>{item.label}</span>
        <span style={{ color: '#666', fontSize: '10px', marginLeft: '12px' }}>&rsaquo;</span>
      </div>
      <div
        ref={subRef}
        style={{
          display: 'none',
          position: 'absolute',
          left: '100%',
          top: 0,
          backgroundColor: '#1a1a2e',
          border: '1px solid #2a2a4a',
          borderRadius: '6px',
          padding: '4px 0',
          minWidth: '140px',
          boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
        }}
        onMouseEnter={showSub}
        onMouseLeave={hideSub}
      >
        {item.submenu!.map((sub, i) => (
          <div
            key={i}
            onClick={() => {
              if (sub.disabled) return
              sub.onClick()
              onClose()
            }}
            style={{
              padding: '6px 12px',
              fontSize: '12px',
              color: sub.disabled ? '#555' : '#ccc',
              cursor: sub.disabled ? 'default' : 'pointer',
            }}
            onMouseEnter={(e) => {
              if (!sub.disabled) e.currentTarget.style.backgroundColor = '#2a2a4a'
            }}
            onMouseLeave={(e) => { e.currentTarget.style.backgroundColor = 'transparent' }}
          >
            {sub.label}
          </div>
        ))}
      </div>
    </div>
  )
}
