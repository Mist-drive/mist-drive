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
