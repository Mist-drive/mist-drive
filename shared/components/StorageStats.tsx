import { fmt } from '@shared/lib/format'

type Props = {
  usedBytes: number
  quotaBytes: number
  totalFiles: number
  totalFolders: number
  onRefresh: () => void
  refreshing?: boolean
}

export default function StorageStats({ usedBytes, quotaBytes, totalFiles, totalFolders, onRefresh, refreshing = false }: Props) {
  return (
    <div className="muted" style={{ textAlign: 'center', marginTop: '1rem', fontSize: '0.85rem' }}>
      {fmt(usedBytes)} / {fmt(quotaBytes)} used
      {' '}({totalFiles} file{totalFiles === 1 ? '' : 's'},{' '}
      {totalFolders} folder{totalFolders === 1 ? '' : 's'})
      {' · '}
      <a
        href="#"
        onClick={(e) => { e.preventDefault(); if (!refreshing) onRefresh() }}
        style={{ color: 'var(--accent-blue)', opacity: refreshing ? 0.5 : 1, cursor: refreshing ? 'default' : 'pointer' }}
      >
        {refreshing ? 'refreshing…' : 'refresh'}
      </a>
    </div>
  )
}
