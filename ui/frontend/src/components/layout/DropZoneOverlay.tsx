import { useState, useCallback } from 'react'
import { DRAG_MIME } from '../../store/layoutStore'
import type { SplitDirection, TabDragData } from '../../store/layoutStore'

interface DropZoneOverlayProps {
  onDock: (direction: SplitDirection, position: 'before' | 'after') => void
}

type Zone = 'left' | 'right' | 'top' | 'bottom'

const ZONE_CONFIG: Record<Zone, {
  style: React.CSSProperties
  direction: SplitDirection
  position: 'before' | 'after'
  label: string
}> = {
  left: {
    style: { left: 0, top: 0, width: '20%', height: '100%' },
    direction: 'horizontal', position: 'before', label: 'Left',
  },
  right: {
    style: { right: 0, top: 0, width: '20%', height: '100%' },
    direction: 'horizontal', position: 'after', label: 'Right',
  },
  top: {
    style: { left: '20%', top: 0, width: '60%', height: '30%' },
    direction: 'vertical', position: 'before', label: 'Top',
  },
  bottom: {
    style: { left: '20%', bottom: 0, width: '60%', height: '30%' },
    direction: 'vertical', position: 'after', label: 'Bottom',
  },
}

export function DropZoneOverlay({ onDock }: DropZoneOverlayProps) {
  const [activeZone, setActiveZone] = useState<Zone | null>(null)

  const handleDragOver = useCallback((e: React.DragEvent) => {
    if (e.dataTransfer.types.includes(DRAG_MIME)) {
      e.preventDefault()
      e.dataTransfer.dropEffect = 'move'
    }
  }, [])

  const handleDrop = useCallback((e: React.DragEvent, zone: Zone) => {
    e.preventDefault()
    try {
      const raw = e.dataTransfer.getData(DRAG_MIME)
      if (!raw) return
      const _data: TabDragData = JSON.parse(raw)
      const config = ZONE_CONFIG[zone]
      onDock(config.direction, config.position)
    } catch { /* ignore */ }
    setActiveZone(null)
  }, [onDock])

  return (
    <div style={{
      position: 'absolute',
      inset: 0,
      pointerEvents: 'none',
      zIndex: 10,
    }}>
      {(Object.entries(ZONE_CONFIG) as [Zone, typeof ZONE_CONFIG[Zone]][]).map(([zone, config]) => (
        <div
          key={zone}
          style={{
            position: 'absolute',
            ...config.style,
            pointerEvents: 'auto',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            backgroundColor: activeZone === zone ? 'rgba(97, 218, 251, 0.15)' : 'transparent',
            border: activeZone === zone ? '2px dashed rgba(97, 218, 251, 0.6)' : '2px dashed transparent',
            transition: 'background-color 0.15s, border-color 0.15s',
          }}
          onDragOver={handleDragOver}
          onDragEnter={() => setActiveZone(zone)}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) {
              setActiveZone(prev => prev === zone ? null : prev)
            }
          }}
          onDrop={(e) => handleDrop(e, zone)}
        >
          {activeZone === zone && (
            <span style={{
              color: 'rgba(97, 218, 251, 0.8)',
              fontSize: '13px',
              fontWeight: 'bold',
              pointerEvents: 'none',
            }}>
              {config.label}
            </span>
          )}
        </div>
      ))}
    </div>
  )
}
