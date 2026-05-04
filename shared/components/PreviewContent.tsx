import { useTranslation } from '@shared/lib/i18n'

export type PreviewResult = {
  type: 'image' | 'text' | 'binary'
  content?: string
}

type Props = {
  filename: string
  result: PreviewResult | null
  loading: boolean
  onClose: () => void
}

export default function PreviewContent({ filename, result, loading, onClose }: Props) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '.75rem 1rem',
        borderBottom: '1px solid var(--border)',
        flexShrink: 0,
      }}>
        <span
          title={filename}
          style={{
            fontWeight: 500,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            minWidth: 0,
            flex: 1,
          }}
        >
          {filename}
        </span>
        <button
          onClick={onClose}
          title={t('preview.close')}
          style={{
            flexShrink: 0,
            marginLeft: '.75rem',
            padding: '.2rem .5rem',
            lineHeight: 1,
            fontSize: '1rem',
            background: 'transparent',
            border: 'none',
            color: 'var(--text-secondary)',
            cursor: 'pointer',
            borderRadius: '6px',
          }}
        >
          ✕
        </button>
      </div>
      <div style={{
        flex: 1,
        overflow: 'auto',
        padding: '1rem',
        display: 'flex',
        alignItems: loading || !result || result.type !== 'text' ? 'center' : 'flex-start',
        justifyContent: loading || !result || result.type !== 'text' ? 'center' : 'flex-start',
      }}>
        {loading ? (
          <span className="muted">{t('preview.loading')}</span>
        ) : !result ? null : result.type === 'image' ? (
          <img
            src={result.content}
            alt={filename}
            style={{ maxWidth: '100%', maxHeight: '100%', objectFit: 'contain', borderRadius: '4px' }}
          />
        ) : result.type === 'text' ? (
          <pre style={{
            margin: 0,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-all',
            fontSize: '0.82rem',
            lineHeight: 1.6,
            width: '100%',
            color: 'var(--text-secondary)',
            fontFamily: 'monospace',
          }}>
            {result.content}
          </pre>
        ) : (
          <div style={{ textAlign: 'center', color: 'var(--text-secondary)' }}>
            <div style={{ fontSize: '2.5rem', marginBottom: '.5rem' }}>📦</div>
            <span className="muted" style={{ fontSize: '0.9rem' }}>{t('preview.binary')}</span>
          </div>
        )}
      </div>
    </div>
  )
}
