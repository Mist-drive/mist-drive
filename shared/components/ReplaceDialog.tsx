import { useEffect, useRef, useState } from 'react'

export type ConflictEntry = {
  key: string
  existingSize: number
  incomingSize: number
}

type Props = {
  conflicts: ConflictEntry[]
  onConfirm: () => void
  onDiff: () => void
  onCancel: () => void
}

const pathStyle: React.CSSProperties = {
  direction: 'rtl',
  textOverflow: 'ellipsis',
  overflow: 'hidden',
  whiteSpace: 'nowrap',
  display: 'block',
  unicodeBidi: 'plaintext',
}

export default function ReplaceDialog({ conflicts, onConfirm, onDiff, onCancel }: Props) {
  const [expanded, setExpanded] = useState(false)
  const confirmBtn = useRef<HTMLButtonElement>(null)
  const multi = conflicts.length > 1
  const diffCount = conflicts.filter(c => c.incomingSize !== c.existingSize).length

  useEffect(() => {
    confirmBtn.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onCancel()
      else if (e.key === 'Enter') onConfirm()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div
      className="modal-backdrop"
      onClick={(e) => { if (e.target === e.currentTarget) onCancel() }}
    >
      <div className="modal" role="dialog" aria-modal="true">
        <h3 className="modal-title">Replace existing {multi ? 'files' : 'file'}?</h3>
        <div className="modal-message">
          {!multi ? (
            <span style={pathStyle}>{conflicts[0].key}</span>
          ) : (
            <>
              <button
                type="button"
                className="ghost"
                onClick={() => setExpanded(e => !e)}
                style={{ padding: 0, background: 'none', border: 'none', display: 'flex', alignItems: 'center', gap: '.4rem', fontSize: '0.9rem', color: 'var(--text-secondary)', marginBottom: expanded ? '.6rem' : 0 }}
              >
                <span>{expanded ? '▾' : '▸'}</span>
                {conflicts.length} files will be replaced
              </button>
              {expanded && (
                <ul style={{ maxHeight: '10rem', overflowY: 'auto', listStyle: 'none', margin: 0, padding: 0 }}>
                  {conflicts.map(c => (
                    <li key={c.key} style={{ padding: '.2rem 0' }}>
                      <span className="muted" style={{ ...pathStyle, fontSize: '0.85rem' }}>{c.key}</span>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
        </div>
        <div className="modal-actions">
          <button className="ghost" onClick={onCancel}>Cancel</button>
          <button
            onClick={onDiff}
            disabled={diffCount === 0}
            title={diffCount === 0 ? 'All conflicting files are identical (same size)' : `Upload only the ${diffCount} file${diffCount > 1 ? 's' : ''} with a different size`}
          >
            Diff ({diffCount})
          </button>
          <button ref={confirmBtn} onClick={onConfirm}>Replace ({conflicts.length})</button>
        </div>
      </div>
    </div>
  )
}
