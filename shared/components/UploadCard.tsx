import { useEffect, useState } from 'react'
import { computeGlobalEta, type UploadEntry } from '@shared/lib/upload'
import UploadProgressPanel from './UploadProgressPanel'
import { useTranslation } from '@shared/lib/i18n'

type Props = {
  entries: UploadEntry[]
  queued?: number
  done?: number
  failed?: number
  onCancelAll?: () => void
  onCancelOne?: (key: string) => void
}

export default function UploadCard({ entries, queued = 0, done = 0, failed, onCancelAll, onCancelOne }: Props) {
  const { t } = useTranslation()
  // ETA text (here and in UploadProgressPanel) is time-based, not just a
  // function of `entries` — it goes stale between progress events even
  // when nothing else changes. This re-render is scoped to UploadCard's
  // own subtree only; it used to live in the parent Files page as a
  // forced re-render of the whole component, which also rebuilt the
  // entire (potentially large) file tree twice a second during uploads.
  const [, setTick] = useState(0)
  useEffect(() => {
    if (entries.length === 0) return
    const id = setInterval(() => setTick((n) => n + 1), 500)
    return () => clearInterval(id)
  }, [entries.length])

  if (entries.length === 0 && queued === 0) return null
  const runningCount = entries.length
  const globalEta = computeGlobalEta(entries)
  return (
    <div className="card" style={{ marginBottom: '1rem' }}>
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: '.8rem', gap: '.8rem' }}>
        <h3 style={{ margin: 0 }}>{t('upload.title')}</h3>
        <div className="row" style={{ gap: '.8rem', flexShrink: 0 }}>
          <span className="muted">
            {t('upload.stats', { active: runningCount, queued, done })}
            {(failed ?? 0) > 0 ? ` · ${t('upload.failed', { count: failed })}` : ''}
            {runningCount > 0 && ` · ${t('upload.eta', { eta: globalEta })}`}
          </span>
          {(runningCount > 0 || queued > 0) && onCancelAll && (
            <button className="danger" onClick={onCancelAll}>{t('upload.cancelAll')}</button>
          )}
        </div>
      </div>
      <UploadProgressPanel entries={entries} onCancel={onCancelOne} />
    </div>
  )
}
