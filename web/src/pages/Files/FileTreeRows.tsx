import { TreeNode, sortedChildren } from '@shared/lib/tree'
import { fmt } from '@shared/lib/format'
import { useTranslation } from '@shared/lib/i18n'

export type DragHandlers = {
  dragOverFolder: string | null
  onDragEnterFolder: (path: string) => void
  onFolderDrop: (e: React.DragEvent, path: string) => void
}

// Pure render function (no hooks, everything comes in as params) so it
// can recurse over the tree without re-subscribing to component state at
// every depth.
export function renderTree(
  node: TreeNode,
  depth: number,
  expanded: Record<string, boolean>,
  toggle: (p: string) => void,
  onDownload: (k: string) => void,
  onDelete: (k: string) => void,
  onDeleteFolder: (p: string) => void,
  onDownloadFolder: (p: string) => void,
  dnd: DragHandlers,
  isProcessing: (path: string) => boolean,
  editingPath: string | null,
  editingValue: string,
  setEditingPath: (p: string | null) => void,
  setEditingValue: (v: string) => void,
  onCommitRename: (oldPath: string) => void,
  onPreview: (k: string) => void,
  downloadingKeys: Set<string>,
  t: ReturnType<typeof useTranslation>['t'],
): JSX.Element[] {
  const rows: JSX.Element[] = []
  for (const c of sortedChildren(node)) {
    const indent = { paddingLeft: `${depth * 1.2 + 0.4}rem` }
    const iconBtn = { padding: '.3rem .55rem', fontSize: '0.95rem', lineHeight: 1 }
    const proc = isProcessing(c.path)
    const editing = editingPath === c.path

    if (c.isDir) {
      const isOpen = !!expanded[c.path]

      const nameCell = editing ? (
        <input
          autoFocus
          type="text"
          value={editingValue}
          onChange={(e) => setEditingValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onCommitRename(c.path)
            if (e.key === 'Escape') { setEditingPath(null); setEditingValue('') }
          }}
          onBlur={() => onCommitRename(c.path)}
          style={{ fontSize: 'inherit', padding: '.1rem .3rem', width: '12rem' }}
          onClick={(e) => e.stopPropagation()}
        />
      ) : (
        <strong>{c.name}</strong>
      )

      const actionsTd = proc ? (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end' }}>
          <span className="muted" style={{ fontSize: '0.85rem' }}>{t('status.processingInline')}</span>
        </div>
      ) : (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="ghost" title={t('files.rename')} style={iconBtn}
            onClick={(e) => { e.stopPropagation(); setEditingPath(c.path); setEditingValue(c.name) }}>✏️</button>
          <button className="ghost" title={t('files.downloadZip')} style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDownloadFolder(c.path) }}>⬇</button>
          <button className="danger" title={t('files.delete')} style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDeleteFolder(c.path) }}>✕</button>
        </div>
      )

      rows.push(
        <tr
          key={`d:${c.path}`}
          onDragEnter={(e) => { e.stopPropagation(); dnd.onDragEnterFolder(c.path) }}
          onDragOver={(e) => { e.preventDefault(); e.stopPropagation() }}
          onDrop={(e) => { e.stopPropagation(); dnd.onFolderDrop(e, c.path) }}
          style={dnd.dragOverFolder === c.path ? { backgroundColor: 'rgba(59, 130, 246, 0.15)' } : undefined}
        >
          <td style={{ ...indent, cursor: editing ? 'default' : 'pointer' }} onClick={() => !editing && toggle(c.path)}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}>
              {isOpen ? '▾' : '▸'}
            </span>
            <span style={{ display: 'inline-block', width: '1.4rem' }}>{proc ? '⏳' : (isOpen ? '📂' : '📁')}</span>
            {nameCell}
          </td>
          <td className="muted">{fmt(c.size)}</td>
          <td className="muted">—</td>
          <td>{actionsTd}</td>
        </tr>,
      )
      if (isOpen) {
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, dnd, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, onPreview, downloadingKeys, t))
      }
    } else {
      const nameCell = editing ? (
        <input
          autoFocus
          type="text"
          value={editingValue}
          onChange={(e) => setEditingValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') onCommitRename(c.path)
            if (e.key === 'Escape') { setEditingPath(null); setEditingValue('') }
          }}
          onBlur={() => onCommitRename(c.path)}
          style={{ fontSize: 'inherit', padding: '.1rem .3rem', width: '12rem' }}
        />
      ) : c.name

      const actionsTd = proc ? (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end' }}>
          <span className="muted" style={{ fontSize: '0.85rem' }}>{t('status.processingInline')}</span>
        </div>
      ) : (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="ghost" title={t('files.rename')} style={iconBtn}
            onClick={(e) => { e.stopPropagation(); setEditingPath(c.path); setEditingValue(c.name) }}>✏️</button>
          <button className="ghost" title={t('files.download')} style={iconBtn}
            disabled={downloadingKeys.has(c.path)}
            onClick={(e) => { e.stopPropagation(); onDownload(c.path) }}>{downloadingKeys.has(c.path) ? '⏳' : '⬇'}</button>
          <button className="danger" title={t('files.delete')} style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDelete(c.path) }}>✕</button>
        </div>
      )

      rows.push(
        <tr key={`f:${c.path}`} style={{ cursor: editing ? 'default' : 'pointer' }} onClick={() => !editing && onPreview(c.path)}>
          <td style={indent}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}></span>
            <span style={{ display: 'inline-block', width: '1.4rem' }}>{proc ? '⏳' : '📄'}</span>
            {nameCell}
          </td>
          <td>{fmt(c.size)}</td>
          <td className="muted">
            {c.lastModified ? new Date(c.lastModified).toLocaleString() : ''}
          </td>
          <td>{actionsTd}</td>
        </tr>,
      )
    }
  }
  return rows
}
