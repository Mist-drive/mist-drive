import { fmt } from '@shared/lib/format'
import { useTranslation } from '@shared/lib/i18n'

type Props = {
  usedBytes: number
  quotaBytes: number
  totalFiles: number
  totalFolders: number
  onRefresh: () => void
  refreshing?: boolean
}

export default function StorageStats({ usedBytes, quotaBytes, totalFiles, totalFolders, onRefresh, refreshing = false }: Props) {
  const { t } = useTranslation()
  return (
    <div className="muted" style={{ textAlign: 'center', marginTop: '1rem', fontSize: '0.85rem' }}>
      {fmt(usedBytes)} / {fmt(quotaBytes)} {t('storage.used')}
      {' '}({t('storage.files', { count: totalFiles })},{' '}
      {t('storage.folders', { count: totalFolders })})
      {' · '}
      <a
        href="#"
        onClick={(e) => { e.preventDefault(); if (!refreshing) onRefresh() }}
        style={{ color: 'var(--accent-blue)', opacity: refreshing ? 0.5 : 1, cursor: refreshing ? 'default' : 'pointer' }}
      >
        {refreshing ? t('storage.refreshing') : t('storage.refresh')}
      </a>
    </div>
  )
}
