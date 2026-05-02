import { fmt } from '@shared/lib/format'
import { etaFor, type UploadEntry } from '@shared/lib/upload'

type Props = {
  entries: UploadEntry[]
  onCancel?: (key: string) => void
}

export default function UploadProgressPanel({ entries, onCancel }: Props) {
  if (entries.length === 0) return null
  return (
    <>
      {entries.map((up) => (
        <div key={up.key} style={{ marginBottom: '.7rem' }}>
          <div className="row" style={{ justifyContent: 'space-between', gap: '.8rem', minWidth: 0 }}>
            <span
              title={up.key}
              style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
            >
              {up.key}
            </span>
            <span className="muted" style={{ flexShrink: 0, whiteSpace: 'nowrap' }}>
              {fmt(up.loaded)} / {fmt(up.total)} · {up.pct}% · {etaFor(up.loaded, up.total, up.startedAt)}
            </span>
            {onCancel && (
              <button
                className="ghost"
                title="Cancel this upload"
                onClick={() => onCancel(up.key)}
                style={{ flexShrink: 0, padding: '.25rem .55rem', fontSize: '0.8rem' }}
              >✕</button>
            )}
          </div>
          <div className="progress"><div style={{ width: `${up.pct}%` }} /></div>
        </div>
      ))}
    </>
  )
}
