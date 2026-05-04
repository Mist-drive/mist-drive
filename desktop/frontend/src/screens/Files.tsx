import { useEffect, useRef, useState } from 'react'
import { useConfirm } from '../components/ConfirmDialog'
import { useTranslation } from '@shared/lib/i18n'
import { fmt } from '@shared/lib/format'
import { startLoading, endLoading } from '@shared/lib/loading'
import { TreeNode, buildTree, sortedChildren } from '@shared/lib/tree'
import {
  CancelUpload,
  CancelUploads,
  CreateFolder,
  DeleteFile,
  DeleteFolder,
  DownloadFile,
  DownloadFolder,
  GetSettings,
  ListFiles,
  PickFile,
  PickFolderForUpload,
  PreviewFile,
  RecomputeUsage,
  RenameFile,
  UploadFolderPicked,
  UploadPicked,
} from '../../wailsjs/go/main/App'
import ReplaceDialog, { type ConflictEntry } from '../components/ReplaceDialog'
import PreviewContent, { type PreviewResult } from '@shared/components/PreviewContent'
import StorageStats from '@shared/components/StorageStats'
import UploadCard from '@shared/components/UploadCard'
import { type UploadEntry } from '@shared/lib/upload'
import { apiclient } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { is401, notifySessionExpired } from '../session'


type Props = { onQuotaChange?: () => void; user: apiclient.PublicUser }

// Phase-2 file browser. Mirrors the web UI's tree view but all the
// actual HTTP / multipart work happens in Go via Wails bindings — the
// JWT never leaves the Go side.
export default function Files({ onQuotaChange, user }: Props) {
  const { t } = useTranslation()
  const [files, setFiles] = useState<apiclient.ObjectInfo[]>([])
  const [processing, setProcessing] = useState<string[]>([])
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [err, setErr] = useState<string | null>(null)
  const [renameErr, setRenameErr] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null) // status line during uploads/downloads
  const [editingPath, setEditingPath] = useState<string | null>(null)
  const [editingValue, setEditingValue] = useState('')
  const [query, setQuery] = useState('')
  const [newFolder, setNewFolder] = useState<string | null>(null)
  const [replaceConflicts, setReplaceConflicts] = useState<ConflictEntry[]>([])
  const [syncRoots, setSyncRoots] = useState<Set<string>>(new Set())
  const [previewKey, setPreviewKey] = useState<string | null>(null)
  const [previewResult, setPreviewResult] = useState<PreviewResult | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const [uploadActive, setUploadActive] = useState<Record<string, UploadEntry>>({})
  const [uploadDone, setUploadDone] = useState(0)
  const [, setTick] = useState(0)
  const [downloadingKeys, setDownloadingKeys] = useState<Set<string>>(new Set())
  const uploadTotalRef = useRef(0)
  const cancelledKeysRef = useRef<Set<string>>(new Set())
  const replaceResolve = useRef<((choice: 'replace' | 'diff' | 'cancel') => void) | null>(null)
  const confirm = useConfirm()

  const refresh = async () => {
    startLoading()
    try {
      const resp = await ListFiles()
      setFiles(resp.objects || [])
      setProcessing(resp.processing || [])
    }
    catch (e: any) {
      if (is401(e)) { notifySessionExpired(); return }
      setErr(String(e?.message ?? e))
    }
    finally { endLoading() }
  }
  useEffect(() => {
    refresh()
    GetSettings().then(s => {
      setSyncRoots(new Set(s.folders.map(f => f.remotePrefix.replace(/\/$/, ''))))
    }).catch(() => {})
    const unsubChange = EventsOn('files-changed', () => { refresh(); onQuotaChange?.() })
    const unsubErr = EventsOn('rename-error', (message: string, path: string) => {
      setRenameErr(t('files.renameError', { path, message }))
      refresh()
    })
    const unsubProgress = EventsOn('upload-progress', (ev: { key: string; loaded: number; total: number; done: boolean }) => {
      if (cancelledKeysRef.current.has(ev.key)) return
      if (ev.done) {
        setUploadDone(n => n + 1)
        setUploadActive(a => { const { [ev.key]: _, ...rest } = a; return rest })
        return
      }
      setUploadActive((a) => {
        const existing = a[ev.key]
        return {
          ...a,
          [ev.key]: {
            key: ev.key,
            loaded: ev.loaded,
            total: ev.total,
            startedAt: existing?.startedAt ?? Date.now(),
            pct: Math.round((ev.loaded / ev.total) * 100),
          },
        }
      })
    })
    return () => { unsubChange(); unsubErr(); unsubProgress() }
  }, [])

  useEffect(() => {
    if (Object.keys(uploadActive).length === 0) return
    const t = setInterval(() => setTick((n) => n + 1), 500)
    return () => clearInterval(t)
  }, [uploadActive])

  const toggle = (p: string) => setExpanded((e) => ({ ...e, [p]: !e[p] }))

  const withBusy = async <T,>(label: string, fn: () => Promise<T>): Promise<T | null> => {
    setBusy(label); setErr(null); startLoading()
    try { return await fn() }
    catch (e: any) {
      if (is401(e)) { notifySessionExpired(); return null }
      setErr(String(e?.message ?? e)); return null
    }
    finally { setBusy(null); endLoading() }
  }

  const onUpload = async () => {
    const picked = await withBusy(t('status.picking'), () => PickFile(''))
    if (!picked?.key) return
    const existing = files.find(f => f.key === picked.key)
    if (existing) {
      const conflict: ConflictEntry = { key: picked.key, existingSize: existing.size, incomingSize: picked.size }
      const choice = await new Promise<'replace' | 'diff' | 'cancel'>(resolve => {
        replaceResolve.current = resolve
        setReplaceConflicts([conflict])
      })
      if (choice === 'cancel') return
      if (choice === 'diff' && conflict.existingSize === conflict.incomingSize) return
    }
    cancelledKeysRef.current.clear(); uploadTotalRef.current = 1; setUploadDone(0)
    await withBusy(t('status.uploading'), () => UploadPicked(picked.key))
    setUploadActive({}); uploadTotalRef.current = 0; setUploadDone(0)
    await refresh()
    onQuotaChange?.()
  }
  const onUploadFolder = async () => {
    const picked = await withBusy(t('status.pickingFolder'), () => PickFolderForUpload(''))
    if (!picked || picked.length === 0) return
    const existingMap = new Map(files.map(f => [f.key, f.size]))
    const conflicts: ConflictEntry[] = picked
      .filter(p => existingMap.has(p.key))
      .map(p => ({ key: p.key, existingSize: existingMap.get(p.key)!, incomingSize: p.size }))
    let skipKeys: string[] = []
    if (conflicts.length > 0) {
      const choice = await new Promise<'replace' | 'diff' | 'cancel'>(resolve => {
        replaceResolve.current = resolve
        setReplaceConflicts(conflicts)
      })
      if (choice === 'cancel') return
      if (choice === 'diff') {
        skipKeys = conflicts.filter(c => c.existingSize === c.incomingSize).map(c => c.key)
      }
    }
    cancelledKeysRef.current.clear(); uploadTotalRef.current = picked.length - skipKeys.length; setUploadDone(0)
    await withBusy(t('status.uploadingFolder'), () => UploadFolderPicked(skipKeys))
    setUploadActive({}); uploadTotalRef.current = 0; setUploadDone(0)
    await refresh()
    onQuotaChange?.()
  }

  const onDownload = async (key: string) => {
    setDownloadingKeys(s => new Set(s).add(key))
    try {
      const dest = await withBusy(t('status.downloading'), () => DownloadFile(key))
      if (dest) setBusy(t('status.savedTo', { path: dest }))
      setTimeout(() => setBusy(null), 2500)
    } finally {
      setDownloadingKeys(s => { const n = new Set(s); n.delete(key); return n })
    }
  }
  const onDelete = async (key: string) => {
    const ok = await confirm({ title: t('files.deleteTitle'), message: t('files.deleteConfirm', { key }), confirmText: t('files.delete'), danger: true })
    if (!ok) return
    await withBusy(t('status.deleting'), () => DeleteFile(key))
    await refresh()
    onQuotaChange?.()
  }
  const onDeleteFolder = async (path: string) => {
    const ok = await confirm({ title: t('files.deleteFolderTitle'), message: t('files.deleteFolderConfirm', { path }), confirmText: t('files.delete'), danger: true })
    if (!ok) return
    await withBusy(t('status.deletingFolder'), () => DeleteFolder(path))
    await refresh()
    onQuotaChange?.()
  }
  const onDownloadFolder = async (path: string) => {
    setDownloadingKeys(s => new Set(s).add(path))
    try {
      const dest = await withBusy(t('status.downloadingFolder'), () => DownloadFolder(path + '/'))
      if (dest) setBusy(t('status.savedTo', { path: dest }))
      setTimeout(() => setBusy(null), 2500)
    } finally {
      setDownloadingKeys(s => { const n = new Set(s); n.delete(path); return n })
    }
  }
  const onMkdir = async () => {
    if (!newFolder?.trim()) return
    await withBusy(t('status.creatingFolder'), () => CreateFolder(newFolder.trim()))
    setNewFolder(null)
    await refresh()
  }

  const onCancelOne = (key: string) => {
    cancelledKeysRef.current.add(key)
    setUploadActive(a => { const { [key]: _, ...rest } = a; return rest })
    CancelUpload(key)
  }

  const onCancelAll = () => {
    setUploadActive(a => {
      for (const key of Object.keys(a)) cancelledKeysRef.current.add(key)
      return {}
    })
    CancelUploads()
  }

  const onRefresh = async () => {
    await withBusy(t('status.refreshing'), () => RecomputeUsage())
    onQuotaChange?.()
  }

  const isProcessing = (path: string) =>
    processing.some(p => path === p || path.startsWith(p + '/'))

  const isSyncRoot = (path: string) => syncRoots.has(path)

  const onPreview = async (key: string) => {
    setPreviewKey(key)
    setPreviewResult(null)
    setPreviewLoading(true)
    try {
      const r = await PreviewFile(key)
      setPreviewResult(r as PreviewResult)
    } catch {
      setPreviewResult({ type: 'binary' })
    } finally {
      setPreviewLoading(false)
    }
  }

  const onCommitRename = async (oldPath: string) => {
    const newName = editingValue.trim()
    setEditingPath(null)
    setEditingValue('')
    if (!newName) return
    const oldName = oldPath.split('/').pop() ?? oldPath
    if (newName === oldName) return
    try {
      await RenameFile(oldPath, newName)
      await refresh()
    } catch (e: any) {
      setErr(String(e?.message ?? e))
    }
  }

  // Client-side search: case-insensitive substring match on the full
  // key. The whole listing is already in memory so there's no reason
  // to round-trip the API — mirrors the web UI's behavior.
  const folderSet = new Set<string>()
  for (const f of files) {
    const parts = f.key.split('/').filter(Boolean)
    for (let i = 1; i < parts.length; i++) folderSet.add(parts.slice(0, i).join('/'))
  }
  const totalFiles = files.filter(f => !f.key.endsWith('/.keep')).length
  const totalFolders = folderSet.size

  const q = query.trim().toLowerCase()
  const filteredFiles = q
    ? files.filter((f) => f.key.toLowerCase().includes(q))
    : files
  // When a search is active, auto-expand every ancestor folder of a
  // match so hits are visible without manual clicking.
  const effectiveExpanded = q
    ? (() => {
        const open: Record<string, boolean> = { ...expanded }
        for (const f of filteredFiles) {
          const parts = f.key.split('/').filter(Boolean)
          for (let i = 1; i < parts.length; i++) {
            open[parts.slice(0, i).join('/')] = true
          }
        }
        return open
      })()
    : expanded

  return (
    <>
    <div>
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem', gap: '.5rem' }}>
        <div style={{ flex: 1 }}>
          {err && <p className="error" style={{ margin: 0 }}>{err}</p>}
          {renameErr && <p className="error" style={{ margin: 0 }}>{renameErr} <button className="ghost" style={{ padding: '.1rem .4rem', fontSize: '0.8rem' }} onClick={() => setRenameErr(null)}>✕</button></p>}
          {busy && <p className="muted" style={{ margin: 0, fontSize: '0.9rem' }}>{busy}</p>}
        </div>
        <div className="row" style={{ gap: '.5rem', flexShrink: 0 }}>
          <button className="ghost" onClick={() => setNewFolder('')} disabled={!!busy}>{t('files.newFolder')}</button>
          <button className="ghost" onClick={onUpload} disabled={!!busy}>{t('files.uploadFile')}</button>
          <button className="ghost" onClick={onUploadFolder} disabled={!!busy}>{t('files.uploadFolder')}</button>
        </div>
      </div>
      <UploadCard
        entries={Object.values(uploadActive)}
        queued={Math.max(0, uploadTotalRef.current - Object.keys(uploadActive).length - uploadDone)}
        done={uploadDone}
        onCancelAll={onCancelAll}
        onCancelOne={onCancelOne}
      />
      <div className="row" style={{ marginBottom: '.6rem' }}>
        <input
          type="search"
          placeholder={t('files.searchPlaceholder')}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          style={{ flex: 1 }}
        />
        {query && (
          <span className="muted" style={{ flexShrink: 0 }}>
            {filteredFiles.length} / {files.length}
          </span>
        )}
      </div>
      {newFolder !== null && (
        <div className="row" style={{ marginBottom: '.6rem', gap: '.5rem' }}>
          <input
            autoFocus
            type="text"
            placeholder={t('files.newFolderPlaceholder')}
            value={newFolder}
            onChange={(e) => setNewFolder(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') onMkdir()
              if (e.key === 'Escape') setNewFolder(null)
            }}
            style={{ flex: 1 }}
          />
          <button onClick={onMkdir} disabled={!newFolder.trim()}>{t('files.create')}</button>
          <button className="ghost" onClick={() => setNewFolder(null)}>{t('files.cancel')}</button>
        </div>
      )}
      <table>
        <thead>
          <tr>
            <th>
              {t('files.name')}
              <button
                className="ghost"
                title={t('files.collapseAll')}
                onClick={() => setExpanded({})}
                style={{ padding: '.1rem .4rem', marginLeft: '.5rem', fontSize: '0.8rem', lineHeight: 1 }}
              >⊟</button>
            </th>
            <th>{t('files.size')}</th>
            <th>{t('files.modified')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, isSyncRoot, onPreview, downloadingKeys, t)}
        </tbody>
      </table>
      <StorageStats
        usedBytes={user.usedBytes}
        quotaBytes={user.quotaBytes}
        totalFiles={totalFiles}
        totalFolders={totalFolders}
        onRefresh={onRefresh}
        refreshing={busy === t('status.refreshing')}
      />
    </div>
    {replaceConflicts.length > 0 && (
      <ReplaceDialog
        conflicts={replaceConflicts}
        onConfirm={() => { setReplaceConflicts([]); replaceResolve.current?.('replace') }}
        onDiff={() => { setReplaceConflicts([]); replaceResolve.current?.('diff') }}
        onCancel={() => { setReplaceConflicts([]); replaceResolve.current?.('cancel') }}
      />
    )}
    {previewKey && (
      <div
        className="modal-backdrop"
        onClick={(e) => { if (e.target === e.currentTarget) { setPreviewKey(null); setPreviewResult(null) } }}
      >
        <div className="modal" style={{ width: '560px', maxWidth: '92vw', maxHeight: '80vh', padding: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
          <PreviewContent
            filename={previewKey.split('/').pop() ?? previewKey}
            result={previewResult}
            loading={previewLoading}
            onClose={() => { setPreviewKey(null); setPreviewResult(null) }}
          />
        </div>
      </div>
    )}
    </>
  )
}

function renderTree(
  node: TreeNode,
  depth: number,
  expanded: Record<string, boolean>,
  toggle: (p: string) => void,
  onDownload: (k: string) => void,
  onDelete: (k: string) => void,
  onDeleteFolder: (p: string) => void,
  onDownloadFolder: (p: string) => void,
  isProcessing: (path: string) => boolean,
  editingPath: string | null,
  editingValue: string,
  setEditingPath: (p: string | null) => void,
  setEditingValue: (v: string) => void,
  onCommitRename: (oldPath: string) => void,
  isSyncRoot: (path: string) => boolean,
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
      const open = !!expanded[c.path]

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

      const syncRoot = isSyncRoot(c.path)
      const disabledStyle = { opacity: 0.35, cursor: 'not-allowed' as const, pointerEvents: 'none' as const }
      const actionsTd = proc ? (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end' }}>
          <span className="muted" style={{ fontSize: '0.85rem' }}>{t('status.renamingInline')}</span>
        </div>
      ) : (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="ghost" title={syncRoot ? t('files.syncRootRename') : t('files.rename')} style={{ ...iconBtn, ...(syncRoot ? disabledStyle : {}) }}
            onClick={(e) => { e.stopPropagation(); if (!syncRoot) { setEditingPath(c.path); setEditingValue(c.name) } }}>✏️</button>
          <button className="ghost" title={t('files.downloadZip')} style={iconBtn}
            disabled={downloadingKeys.has(c.path)}
            onClick={(e) => { e.stopPropagation(); onDownloadFolder(c.path) }}>{downloadingKeys.has(c.path) ? '⏳' : '⬇'}</button>
          <button className="danger" title={syncRoot ? t('files.syncRootDelete') : t('files.delete')} style={{ ...iconBtn, ...(syncRoot ? disabledStyle : {}) }}
            onClick={(e) => { e.stopPropagation(); if (!syncRoot) onDeleteFolder(c.path) }}>✕</button>
        </div>
      )

      rows.push(
        <tr key={`d:${c.path}`}>
          <td style={{ ...indent, cursor: editing ? 'default' : 'pointer' }} onClick={() => !editing && toggle(c.path)}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}>
              {open ? '▾' : '▸'}
            </span>
            <span style={{ display: 'inline-block', width: '1.4rem' }}>{proc ? '⏳' : (open ? '📂' : '📁')}</span>
            {nameCell}
          </td>
          <td className="muted">{fmt(c.size)}</td>
          <td className="muted">—</td>
          <td>{actionsTd}</td>
        </tr>,
      )
      if (open) {
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, isSyncRoot, onPreview, downloadingKeys, t))
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
          <span className="muted" style={{ fontSize: '0.85rem' }}>{t('status.renamingInline')}</span>
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
