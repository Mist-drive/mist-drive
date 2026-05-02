import { computeGlobalEta, type UploadEntry } from '@shared/lib/upload'
import UploadProgressPanel from './UploadProgressPanel'

type Props = {
  entries: UploadEntry[]
  queued?: number
  done?: number
  failed?: number
  onCancelAll?: () => void
  onCancelOne?: (key: string) => void
}

export default function UploadCard({ entries, queued = 0, done = 0, failed, onCancelAll, onCancelOne }: Props) {
  if (entries.length === 0 && queued === 0) return null
  const runningCount = entries.length
  const globalEta = computeGlobalEta(entries)
  return (
    <div className="card" style={{ marginBottom: '1rem' }}>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: '.8rem', gap: '.8rem' }}>
        <h3 style={{ margin: 0 }}>Uploads</h3>
        <div className="row" style={{ gap: '.8rem', flexShrink: 0 }}>
          <span className="muted">
            {runningCount} active · {queued} queued · {done} done
            {(failed ?? 0) > 0 ? ` · ${failed} failed` : ''}
            {runningCount > 0 && ` · ETA ${globalEta}`}
          </span>
          {(runningCount > 0 || queued > 0) && onCancelAll && (
            <button className="danger" onClick={onCancelAll}>Cancel all</button>
          )}
        </div>
      </div>
      <UploadProgressPanel entries={entries} onCancel={onCancelOne} />
    </div>
  )
}
