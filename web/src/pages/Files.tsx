import { useEffect, useRef, useState } from 'react'
import { api, getUser, ObjectInfo, setSession, getToken, PublicUser, onEvent, PreviewResult } from '../lib/api'
import { uploadFile } from '../lib/uploader'
import { useConfirm } from '../components/ConfirmDialog'
import ReplaceDialog, { type ConflictEntry } from '../components/ReplaceDialog'
import PreviewContent from '@shared/components/PreviewContent'
import StorageStats from '@shared/components/StorageStats'
import UploadCard from '@shared/components/UploadCard'
import { fmt } from '@shared/lib/format'
import { type UploadEntry } from '@shared/lib/upload'
import { TreeNode, buildTree, sortedChildren } from '@shared/lib/tree'
import { useTranslation } from '@shared/lib/i18n'

// webkitdirectory isn't in the default React HTMLInputAttributes type
declare module 'react' {
  interface InputHTMLAttributes<T> {
    webkitdirectory?: string
    directory?: string
  }
}


const FILE_CONCURRENCY = 4


async function readAllEntries(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  const all: FileSystemEntry[] = []
  while (true) {
    const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej))
    if (!batch.length) break
    all.push(...batch)
  }
  return all
}

type DragHandlers = {
  dragOverFolder: string | null
  onDragEnterFolder: (path: string) => void
  onFolderDrop: (e: React.DragEvent, path: string) => void
}

export default function Files() {
  const { t } = useTranslation()
  const [files, setFiles] = useState<ObjectInfo[]>([])
  const [processing, setProcessing] = useState<string[]>([])
  const [me, setMe] = useState<PublicUser | null>(getUser())
  const [busy, setBusy] = useState<string | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [editingPath, setEditingPath] = useState<string | null>(null)
  const [editingValue, setEditingValue] = useState('')
  const [renameErr, setRenameErr] = useState<string | null>(null)
  const [active, setActive] = useState<Record<string, UploadEntry>>({})
  const [queued, setQueued] = useState(0)
  const [done, setDone] = useState(0)
  const [failed, setFailed] = useState(0)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [query, setQuery] = useState('')
  const [newFolder, setNewFolder] = useState<string | null>(null)
  const [replaceConflicts, setReplaceConflicts] = useState<ConflictEntry[]>([])
  const [isDragging, setIsDragging] = useState(false)
  const [dragOverFolder, setDragOverFolder] = useState<string | null>(null)
  const [previewKey, setPreviewKey] = useState<string | null>(null)
  const [previewResult, setPreviewResult] = useState<PreviewResult | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)
  const replaceResolve = useRef<((choice: 'replace' | 'diff' | 'cancel') => void) | null>(null)
  const confirm = useConfirm()
  const controllers = useRef<Map<string, AbortController>>(new Map())
  const cancelAll = useRef(false)
  const quotaHit = useRef(false)
  const [, setTick] = useState(0)
  const dragCounter = useRef(0)
  const expandTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    if (Object.keys(active).length === 0) return
    const t = setInterval(() => setTick((n) => n + 1), 500)
    return () => clearInterval(t)
  }, [active])

  useEffect(() => {
    const busy = Object.keys(active).length > 0 || queued > 0
    if (!busy) return
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
      return ''
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [active, queued])

  const uploading = Object.keys(active).length > 0 || queued > 0
  const isBusy = !!busy || uploading
  const statusText = busy ?? (uploading ? t('status.uploading') : null)

  const withBusy = async <T,>(label: string, fn: () => Promise<T>): Promise<T | null> => {
    setBusy(label); setErr(null)
    try { return await fn() }
    catch (e: any) { setErr(e.message); return null }
    finally { setBusy(null) }
  }

  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)

  const toggle = (path: string) =>
    setExpanded(e => ({ ...e, [path]: !e[path] }))

  const refresh = async () => {
    try {
      const [listResp, u] = await Promise.all([api.listFiles(), api.me()])
      setFiles(listResp.objects)
      setProcessing(listResp.processing)
      setMe(u)
      const tok = getToken()
      if (tok) setSession(tok, u)
    } catch (e: any) { setErr(e.message) }
  }
  useEffect(() => { refresh() }, [])

  useEffect(() => {
    return onEvent((e) => {
      if (e.type === 'rename-error') {
        setRenameErr(t('files.renameError', { path: e.path, message: e.message }))
        refresh()
      } else {
        refresh()
      }
    })
  }, [])

  const onRefresh = async () => {
    await withBusy(t('status.refreshing'), async () => { await api.recomputeUsage(); await refresh() })
  }

  const runOne = async (key: string, file: File) => {
    const ctrl = new AbortController()
    controllers.current.set(key, ctrl)
    const startedAt = Date.now()
    setActive(a => ({ ...a, [key]: { key, pct: 0, loaded: 0, total: file.size, startedAt } }))
    setQueued(q => q - 1)
    try {
      await uploadFile(key, file, (loaded, total) => {
        setActive(a => ({
          ...a,
          [key]: { key, pct: Math.round((loaded / total) * 100), loaded, total, startedAt },
        }))
      }, ctrl.signal)
      setDone(d => d + 1)
    } catch (err: any) {
      if (err?.name === 'AbortError') {
        // user-initiated cancel
      } else {
        console.error('upload failed:', key, err)
        setFailed(f => f + 1)
        if (typeof err?.message === 'string' && err.message.startsWith('413')) {
          quotaHit.current = true
          for (const c of controllers.current.values()) c.abort()
          setErr(t('files.quotaExceeded'))
        }
      }
    } finally {
      controllers.current.delete(key)
      setActive(a => { const { [key]: _, ...rest } = a; return rest })
    }
  }

  const runUploadJobs = async (jobs: { key: string; file: File }[], label = t('status.uploading')) => {
    if (jobs.length === 0) return
    setQueued(q => q + jobs.length)

    const existingMap = new Map(files.map(f => [f.key, f.size]))
    const conflicts: ConflictEntry[] = jobs
      .filter(j => existingMap.has(j.key))
      .map(j => ({ key: j.key, existingSize: existingMap.get(j.key)!, incomingSize: j.file.size }))
    if (conflicts.length > 0) {
      const choice = await new Promise<'replace' | 'diff' | 'cancel'>(resolve => {
        replaceResolve.current = resolve
        setReplaceConflicts(conflicts)
      })
      if (choice === 'cancel') {
        setQueued(q => q - jobs.length)
        return
      }
      if (choice === 'diff') {
        const skipKeys = new Set(conflicts.filter(c => c.existingSize === c.incomingSize).map(c => c.key))
        jobs = jobs.filter(j => !skipKeys.has(j.key))
        setQueued(q => q - (skipKeys.size))
      }
    }

    cancelAll.current = false
    quotaHit.current = false
    setErr(null)
    setBusy(label)
    try {
      let idx = 0
      const worker = async () => {
        while (idx < jobs.length) {
          const my = idx++
          if (cancelAll.current || quotaHit.current) {
            setQueued(q => q - 1)
            continue
          }
          await runOne(jobs[my].key, jobs[my].file)
        }
      }
      await Promise.all(
        Array.from({ length: Math.min(FILE_CONCURRENCY, jobs.length) }, worker),
      )
      await refresh()
    } finally {
      setBusy(null)
    }
  }

  const onPick = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const list = e.target.files
    if (!list || list.length === 0) return
    const isFolder = e.target === folderInput.current
    const jobs = Array.from(list).map(f => ({
      key: (f as any).webkitRelativePath || f.name,
      file: f,
    }))
    await runUploadJobs(jobs, isFolder ? t('status.uploadingFolder') : t('status.uploading'))
    if (fileInput.current) fileInput.current.value = ''
    if (folderInput.current) folderInput.current.value = ''
  }

  const onDownload = async (key: string) => {
    await withBusy(t('status.downloading'), async () => {
      const { url } = await api.download(key)
      window.location.href = url
    })
  }
  const onDelete = async (key: string) => {
    const ok = await confirm({
      title: t('files.deleteTitle'),
      message: t('files.deleteConfirm', { key }),
      confirmText: t('files.delete'),
      danger: true,
    })
    if (!ok) return
    await withBusy(t('status.deleting'), async () => { await api.deleteFile(key); await refresh() })
  }
  const onDeleteFolder = async (path: string) => {
    const ok = await confirm({
      title: t('files.deleteFolderTitle'),
      message: t('files.deleteFolderConfirm', { path }),
      confirmText: t('files.delete'),
      danger: true,
    })
    if (!ok) return
    await withBusy(t('status.deleting'), async () => { await api.deleteFolder(path + '/'); await refresh() })
  }
  const onDownloadFolder = (path: string) => {
    window.location.href = api.downloadZipUrl(path + '/')
  }

  const onMkdir = async () => {
    if (!newFolder?.trim()) return
    await withBusy(t('status.creating'), async () => {
      await api.mkdir(newFolder.trim())
      setNewFolder(null)
      await refresh()
    })
  }

  const onCancelUpload = (key: string) => {
    controllers.current.get(key)?.abort()
  }
  const onCancelAll = () => {
    cancelAll.current = true
    for (const c of controllers.current.values()) c.abort()
  }

  const clearDragState = () => {
    dragCounter.current = 0
    setIsDragging(false)
    setDragOverFolder(null)
    if (expandTimerRef.current !== null) {
      clearTimeout(expandTimerRef.current)
      expandTimerRef.current = null
    }
  }

  const onDragEnterFolder = (path: string) => {
    setDragOverFolder(path)
    if (expandTimerRef.current !== null) clearTimeout(expandTimerRef.current)
    expandTimerRef.current = setTimeout(() => {
      setExpanded(e => ({ ...e, [path]: true }))
      expandTimerRef.current = null
    }, 600)
  }

  const handleDrop = async (e: React.DragEvent, targetPrefix: string) => {
    e.preventDefault()
    clearDragState()
    const items = Array.from(e.dataTransfer.items)
    const jobs: { key: string; file: File }[] = []
    const prefix = targetPrefix ? targetPrefix + '/' : ''

    const processEntry = async (entry: FileSystemEntry, p: string) => {
      if (entry.isFile) {
        const file = await new Promise<File>((res, rej) =>
          (entry as FileSystemFileEntry).file(res, rej),
        )
        jobs.push({ key: p + entry.name, file })
      } else if (entry.isDirectory) {
        const subs = await readAllEntries((entry as FileSystemDirectoryEntry).createReader())
        await Promise.all(subs.map(sub => processEntry(sub, p + entry.name + '/')))
      }
    }

    const entries = items
      .map(item => item.webkitGetAsEntry?.())
      .filter((entry): entry is FileSystemEntry => !!entry)
    const hasFolder = entries.some(e => e.isDirectory)

    await Promise.all(entries.map(entry => processEntry(entry, prefix)))

    await runUploadJobs(jobs, hasFolder ? t('status.uploadingFolder') : t('status.uploading'))
  }

  const activeList = Object.values(active)

  const folderSet = new Set<string>()
  for (const f of files) {
    const parts = f.key.split('/').filter(Boolean)
    for (let i = 1; i < parts.length; i++) {
      folderSet.add(parts.slice(0, i).join('/'))
    }
  }
  const totalFiles = files.filter(f => !f.key.endsWith('/.keep')).length
  const totalFolders = folderSet.size

  const q = query.trim().toLowerCase()
  const filteredFiles = q
    ? files.filter((f) => f.key.toLowerCase().includes(q))
    : files

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

  const dragHandlers: DragHandlers = { dragOverFolder, onDragEnterFolder, onFolderDrop: handleDrop }

  const isProcessing = (path: string) =>
    processing.some(p => path === p || path.startsWith(p + '/'))

  const closePreview = () => {
    if (previewResult?.type === 'image' && previewResult.content?.startsWith('blob:')) {
      URL.revokeObjectURL(previewResult.content)
    }
    setPreviewKey(null)
    setPreviewResult(null)
  }

  const onPreview = async (key: string) => {
    closePreview()
    setPreviewKey(key)
    setPreviewLoading(true)
    try {
      setPreviewResult(await api.previewFile(key))
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
    await withBusy(t('status.renaming'), async () => { await api.rename(oldPath, newName); await refresh() })
  }

  return (
    <>
    <div
      onDragEnter={(e) => { e.preventDefault(); dragCounter.current++; setIsDragging(true) }}
      onDragLeave={() => { dragCounter.current--; if (dragCounter.current === 0) clearDragState() }}
      onDragOver={(e) => e.preventDefault()}
      onDrop={(e) => handleDrop(e, '')}
    >
      <input ref={fileInput} type="file" multiple onChange={onPick} style={{ display: 'none' }} />
      <input ref={folderInput} type="file" multiple webkitdirectory="" directory="" onChange={onPick} style={{ display: 'none' }} />
      <UploadCard
        entries={activeList}
        queued={queued}
        done={done}
        failed={failed}
        onCancelAll={onCancelAll}
        onCancelOne={onCancelUpload}
      />
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem', gap: '.5rem' }}>
        <div style={{ flex: 1 }}>
          {err && <p className="error" style={{ margin: 0 }}>{err}</p>}
          {renameErr && <p className="error" style={{ margin: 0 }}>{renameErr} <button className="ghost" style={{ padding: '.1rem .4rem', fontSize: '0.8rem' }} onClick={() => setRenameErr(null)}>✕</button></p>}
          {statusText && <p className="muted" style={{ margin: 0, fontSize: '0.9rem' }}>{statusText}</p>}
        </div>
        <div className="row" style={{ gap: '.5rem', flexShrink: 0 }}>
          <button type="button" className="ghost" disabled={isBusy} onClick={() => setNewFolder('')}>{t('files.newFolder')}</button>
          <button type="button" className="ghost" disabled={isBusy} onClick={() => fileInput.current?.click()}>{t('files.uploadFiles')}</button>
          <button type="button" className="ghost" disabled={isBusy} onClick={() => folderInput.current?.click()}>{t('files.uploadFolder')}</button>
        </div>
      </div>
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
          <button type="button" onClick={onMkdir} disabled={!newFolder.trim()}>{t('files.create')}</button>
          <button type="button" className="ghost" onClick={() => setNewFolder(null)}>{t('files.cancel')}</button>
        </div>
      )}
      <div
        style={isDragging && dragOverFolder === null ? {
          outline: '2px dashed rgba(59, 130, 246, 0.6)',
          borderRadius: '6px',
        } : undefined}
      >
        <table onDragEnter={() => setDragOverFolder(null)}>
          <thead><tr>
            <th>
              {t('files.name')}
              <button
                type="button"
                className="ghost"
                title={t('files.collapseAll')}
                onClick={() => setExpanded({})}
                style={{ padding: '.1rem .4rem', marginLeft: '.5rem', fontSize: '0.8rem', lineHeight: 1 }}
              >⊟</button>
            </th>
            <th>{t('files.size')}</th>
            <th>{t('files.modified')}</th>
            <th></th>
          </tr></thead>
          <tbody>
            {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, dragHandlers, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, onPreview, t)}
          </tbody>
        </table>
      </div>
      {me && (
        <StorageStats
          usedBytes={me.usedBytes}
          quotaBytes={me.quotaBytes}
          totalFiles={totalFiles}
          totalFolders={totalFolders}
          onRefresh={onRefresh}
          refreshing={busy === t('status.refreshing')}
        />
      )}
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
      <div className="preview-panel">
        <PreviewContent
          filename={previewKey.split('/').pop() ?? previewKey}
          result={previewResult}
          loading={previewLoading}
          onClose={closePreview}
        />
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
  dnd: DragHandlers,
  isProcessing: (path: string) => boolean,
  editingPath: string | null,
  editingValue: string,
  setEditingPath: (p: string | null) => void,
  setEditingValue: (v: string) => void,
  onCommitRename: (oldPath: string) => void,
  onPreview: (k: string) => void,
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
          <span className="muted" style={{ fontSize: '0.85rem' }}>{t('status.renamingInline')}</span>
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
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, dnd, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, onPreview, t))
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
            onClick={(e) => { e.stopPropagation(); onDownload(c.path) }}>⬇</button>
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
