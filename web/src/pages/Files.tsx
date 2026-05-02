import { useEffect, useRef, useState } from 'react'
import { api, getUser, ObjectInfo, setSession, getToken, PublicUser, onEvent } from '../lib/api'
import { uploadFile } from '../lib/uploader'
import { useConfirm } from '../components/ConfirmDialog'
import ReplaceDialog from '../components/ReplaceDialog'

// webkitdirectory isn't in the default React HTMLInputAttributes type
declare module 'react' {
  interface InputHTMLAttributes<T> {
    webkitdirectory?: string
    directory?: string
  }
}

function fmt(n: number) {
  const u = ['B','KB','MB','GB','TB']; let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(1)} ${u[i]}`
}

function fmtEta(seconds: number): string {
  if (!isFinite(seconds) || seconds < 0) return '—'
  if (seconds < 1) return '<1s'
  if (seconds < 60) return `${Math.ceil(seconds)}s`
  if (seconds < 3600) {
    const m = Math.floor(seconds / 60)
    const s = Math.ceil(seconds % 60)
    return `${m}m ${s}s`
  }
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  return `${h}h ${m}m`
}

function etaFor(loaded: number, total: number, startedAt: number): string {
  if (loaded <= 0 || loaded >= total) return '—'
  const elapsed = (Date.now() - startedAt) / 1000
  if (elapsed < 0.5) return '—'
  const speed = loaded / elapsed
  return fmtEta((total - loaded) / speed)
}

type UP = {
  key: string
  pct: number
  loaded: number
  total: number
  startedAt: number
}

const FILE_CONCURRENCY = 4

type TreeNode = {
  name: string
  path: string
  isDir: boolean
  size: number
  lastModified?: string
  children?: Record<string, TreeNode>
}

function buildTree(files: ObjectInfo[]): TreeNode {
  const root: TreeNode = { name: '', path: '', isDir: true, size: 0, children: {} }
  for (const f of files) {
    const parts = f.key.split('/').filter(Boolean)
    let node = root
    for (let i = 0; i < parts.length; i++) {
      const isLeaf = i === parts.length - 1
      const name = parts[i]
      if (isLeaf && name === '.keep') break
      node.children = node.children || {}
      if (!node.children[name]) {
        node.children[name] = {
          name,
          path: parts.slice(0, i + 1).join('/'),
          isDir: !isLeaf,
          size: 0,
          children: isLeaf ? undefined : {},
        }
      }
      node = node.children[name]
      if (isLeaf) {
        node.size = f.size
        node.lastModified = f.lastModified
      }
    }
  }
  const sum = (n: TreeNode): number => {
    if (!n.isDir) return n.size
    let s = 0
    for (const c of Object.values(n.children || {})) s += sum(c)
    n.size = s
    return s
  }
  sum(root)
  return root
}

function sortedChildren(n: TreeNode): TreeNode[] {
  return Object.values(n.children || {}).sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })
}

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
  const [files, setFiles] = useState<ObjectInfo[]>([])
  const [processing, setProcessing] = useState<string[]>([])
  const [me, setMe] = useState<PublicUser | null>(getUser())
  const [recomputing, setRecomputing] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [editingPath, setEditingPath] = useState<string | null>(null)
  const [editingValue, setEditingValue] = useState('')
  const [renameErr, setRenameErr] = useState<string | null>(null)
  const [active, setActive] = useState<Record<string, UP>>({})
  const [queued, setQueued] = useState(0)
  const [done, setDone] = useState(0)
  const [failed, setFailed] = useState(0)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [query, setQuery] = useState('')
  const [newFolder, setNewFolder] = useState<string | null>(null)
  const [replaceConflicts, setReplaceConflicts] = useState<string[]>([])
  const [isDragging, setIsDragging] = useState(false)
  const [dragOverFolder, setDragOverFolder] = useState<string | null>(null)
  const replaceResolve = useRef<((ok: boolean) => void) | null>(null)
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
        setRenameErr(`Rename failed for "${e.path}": ${e.message}`)
        refresh()
      } else {
        refresh()
      }
    })
  }, [])

  const onRecompute = async () => {
    setRecomputing(true)
    try {
      await api.recomputeUsage()
      await refresh()
    } catch (e: any) {
      setErr(e.message)
    } finally {
      setRecomputing(false)
    }
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
          setErr('Quota exceeded — remaining uploads cancelled.')
        }
      }
    } finally {
      controllers.current.delete(key)
      setActive(a => {
        const { [key]: _, ...rest } = a
        return rest
      })
    }
  }

  const runUploadJobs = async (jobs: { key: string; file: File }[]) => {
    if (jobs.length === 0) return
    setQueued(q => q + jobs.length)

    const existingKeys = new Set(files.map(f => f.key))
    const conflicts = jobs.map(j => j.key).filter(k => existingKeys.has(k))
    if (conflicts.length > 0) {
      const ok = await new Promise<boolean>(resolve => {
        replaceResolve.current = resolve
        setReplaceConflicts(conflicts)
      })
      if (!ok) {
        setQueued(q => q - jobs.length)
        return
      }
    }

    cancelAll.current = false
    quotaHit.current = false
    setErr(null)
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
  }

  const onPick = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const list = e.target.files
    if (!list || list.length === 0) return
    const jobs = Array.from(list).map(f => ({
      key: (f as any).webkitRelativePath || f.name,
      file: f,
    }))
    await runUploadJobs(jobs)
    if (fileInput.current) fileInput.current.value = ''
    if (folderInput.current) folderInput.current.value = ''
  }

  const onDownload = async (key: string) => {
    const { url } = await api.download(key)
    window.location.href = url
  }
  const onDelete = async (key: string) => {
    const ok = await confirm({
      title: 'Delete file',
      message: `Delete ${key}? This cannot be undone.`,
      confirmText: 'Delete',
      danger: true,
    })
    if (!ok) return
    await api.deleteFile(key)
    refresh()
  }
  const onDeleteFolder = async (path: string) => {
    const ok = await confirm({
      title: 'Delete folder',
      message: `Delete ${path}/ and everything inside it? This cannot be undone.`,
      confirmText: 'Delete',
      danger: true,
    })
    if (!ok) return
    await api.deleteFolder(path + '/')
    refresh()
  }
  const onDownloadFolder = (path: string) => {
    window.location.href = api.downloadZipUrl(path + '/')
  }

  const onMkdir = async () => {
    if (!newFolder?.trim()) return
    try {
      await api.mkdir(newFolder.trim())
      setNewFolder(null)
      await refresh()
    } catch (e: any) { setErr(e.message) }
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

    await Promise.all(
      items
        .map(item => item.webkitGetAsEntry?.())
        .filter((entry): entry is FileSystemEntry => !!entry)
        .map(entry => processEntry(entry, prefix)),
    )

    await runUploadJobs(jobs)
  }

  const activeList = Object.values(active)
  let globalLoaded = 0
  let globalTotal = 0
  let earliestStart = Infinity
  for (const up of activeList) {
    globalLoaded += up.loaded
    globalTotal += up.total
    if (up.startedAt < earliestStart) earliestStart = up.startedAt
  }
  const globalEta = (() => {
    if (activeList.length === 0 || globalLoaded <= 0) return '—'
    const elapsed = (Date.now() - earliestStart) / 1000
    if (elapsed < 0.5) return '—'
    const speed = globalLoaded / elapsed
    return fmtEta((globalTotal - globalLoaded) / speed)
  })()

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

  const onCommitRename = async (oldPath: string) => {
    const newName = editingValue.trim()
    setEditingPath(null)
    setEditingValue('')
    if (!newName) return
    const oldName = oldPath.split('/').pop() ?? oldPath
    if (newName === oldName) return
    try {
      await api.rename(oldPath, newName)
      await refresh()
    } catch (e: any) {
      setErr(e.message)
    }
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
      {(activeList.length > 0 || queued > 0) && (
        <div className="card">
          <div className="row" style={{ justifyContent: 'space-between', marginBottom: '.8rem', gap: '.8rem' }}>
            <h3 style={{ margin: 0 }}>Uploads</h3>
            <div className="row" style={{ gap: '.8rem', flexShrink: 0 }}>
              <span className="muted">
                {activeList.length} active · {queued} queued · {done} done
                {failed > 0 ? ` · ${failed} failed` : ''}
                {activeList.length > 0 && ` · ETA ${globalEta}`}
              </span>
              {(activeList.length > 0 || queued > 0) && (
                <button className="danger" onClick={onCancelAll}>Cancel all</button>
              )}
            </div>
          </div>
          {activeList.map(up => (
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
                <button
                  className="ghost"
                  title="Cancel this upload"
                  onClick={() => onCancelUpload(up.key)}
                  style={{ flexShrink: 0, padding: '.25rem .55rem', fontSize: '0.8rem' }}
                >
                  ✕
                </button>
              </div>
              <div className="progress"><div style={{ width: `${up.pct}%` }} /></div>
            </div>
          ))}
        </div>
      )}
      {err && <p className="error" style={{ marginBottom: '.8rem' }}>{err}</p>}
      {renameErr && <p className="error" style={{ marginBottom: '.8rem' }}>{renameErr} <button className="ghost" style={{ padding: '.1rem .4rem', fontSize: '0.8rem' }} onClick={() => setRenameErr(null)}>✕</button></p>}
      <div className="row" style={{ marginBottom: '.6rem' }}>
        <input
          type="search"
          placeholder="Search files…"
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
            placeholder="folder/path"
            value={newFolder}
            onChange={(e) => setNewFolder(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') onMkdir()
              if (e.key === 'Escape') setNewFolder(null)
            }}
            style={{ flex: 1 }}
          />
          <button type="button" onClick={onMkdir} disabled={!newFolder.trim()}>Create</button>
          <button type="button" className="ghost" onClick={() => setNewFolder(null)}>Cancel</button>
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
              Name
              <button
                type="button"
                className="ghost"
                title="Collapse all folders"
                onClick={() => setExpanded({})}
                style={{ padding: '.1rem .4rem', marginLeft: '.5rem', fontSize: '0.8rem', lineHeight: 1 }}
              >⊟</button>
            </th>
            <th>Size</th>
            <th>Modified</th>
            <th>
              <div className="row" style={{ gap: '.5rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
                <button type="button" className="ghost" onClick={() => setNewFolder('')}>
                  New folder
                </button>
                <button type="button" className="ghost" onClick={() => fileInput.current?.click()}>
                  Upload files
                </button>
                <button type="button" className="ghost" onClick={() => folderInput.current?.click()}>
                  Upload folder
                </button>
              </div>
            </th>
          </tr></thead>
          <tbody>
            {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, dragHandlers, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename)}
          </tbody>
        </table>
      </div>
      {me && (
        <div className="muted" style={{ marginTop: '1rem', textAlign: 'center', fontSize: '0.85rem' }}>
          {fmt(me.usedBytes)} / {fmt(me.quotaBytes)} used
          {' '}({totalFiles} file{totalFiles === 1 ? '' : 's'},{' '}
          {totalFolders} folder{totalFolders === 1 ? '' : 's'})
          {' · '}
          <a
            href="#"
            onClick={(e) => { e.preventDefault(); if (!recomputing) onRecompute() }}
            style={{
              color: 'var(--accent-blue)',
              opacity: recomputing ? 0.5 : 1,
              cursor: recomputing ? 'default' : 'pointer',
            }}
          >
            {recomputing ? 'recomputing…' : 'recompute usage'}
          </a>
        </div>
      )}
    </div>
    {replaceConflicts.length > 0 && (
      <ReplaceDialog
        conflicts={replaceConflicts}
        onConfirm={() => { setReplaceConflicts([]); replaceResolve.current?.(true) }}
        onCancel={() => { setReplaceConflicts([]); replaceResolve.current?.(false) }}
      />
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
          <span className="muted" style={{ fontSize: '0.85rem' }}>renaming…</span>
        </div>
      ) : (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="ghost" title="Rename" style={iconBtn}
            onClick={(e) => { e.stopPropagation(); setEditingPath(c.path); setEditingValue(c.name) }}>✏️</button>
          <button className="ghost" title="Download as zip" style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDownloadFolder(c.path) }}>⬇</button>
          <button className="danger" title="Delete" style={iconBtn}
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
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, dnd, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename))
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
          <span className="muted" style={{ fontSize: '0.85rem' }}>renaming…</span>
        </div>
      ) : (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="ghost" title="Rename" style={iconBtn}
            onClick={() => { setEditingPath(c.path); setEditingValue(c.name) }}>✏️</button>
          <button className="ghost" title="Download" style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDownload(c.path) }}>⬇</button>
          <button className="danger" title="Delete" style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDelete(c.path) }}>✕</button>
        </div>
      )

      rows.push(
        <tr key={`f:${c.path}`}>
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
