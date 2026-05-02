import { useEffect, useRef, useState } from 'react'
import { useConfirm } from '../components/ConfirmDialog'
import {
  CreateFolder,
  DeleteFile,
  DeleteFolder,
  DownloadFile,
  DownloadFolder,
  GetSettings,
  ListFiles,
  PickFile,
  RecomputeUsage,
  RenameFile,
  UploadPicked,
} from '../../wailsjs/go/main/App'
import ReplaceDialog from '../components/ReplaceDialog'
import { apiclient } from '../../wailsjs/go/models'
import { EventsOn } from '../../wailsjs/runtime/runtime'

function fmt(n: number) {
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(1)} ${u[i]}`
}

type TreeNode = {
  name: string
  path: string
  isDir: boolean
  size: number
  lastModified?: string
  children?: Record<string, TreeNode>
}

function buildTree(files: apiclient.ObjectInfo[]): TreeNode {
  const root: TreeNode = { name: '', path: '', isDir: true, size: 0, children: {} }
  for (const f of files) {
    const parts = f.key.split('/').filter(Boolean)
    let node = root
    for (let i = 0; i < parts.length; i++) {
      const leaf = i === parts.length - 1
      const name = parts[i]
      if (leaf && name === '.keep') break  // folder marker — create parent dirs but no file node
      node.children = node.children || {}
      if (!node.children[name]) {
        node.children[name] = {
          name,
          path: parts.slice(0, i + 1).join('/'),
          isDir: !leaf,
          size: 0,
          children: leaf ? undefined : {},
        }
      }
      node = node.children[name]
      if (leaf) {
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

type Props = { onQuotaChange?: () => void }

// Phase-2 file browser. Mirrors the web UI's tree view but all the
// actual HTTP / multipart work happens in Go via Wails bindings — the
// JWT never leaves the Go side.
export default function Files({ onQuotaChange }: Props) {
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
  const [replaceConflicts, setReplaceConflicts] = useState<string[]>([])
  const [syncRoots, setSyncRoots] = useState<Set<string>>(new Set())
  const replaceResolve = useRef<((ok: boolean) => void) | null>(null)
  const confirm = useConfirm()

  const refresh = async () => {
    try {
      const resp = await ListFiles()
      setFiles(resp.objects || [])
      setProcessing(resp.processing || [])
    }
    catch (e: any) { setErr(String(e?.message ?? e)) }
  }
  useEffect(() => {
    refresh()
    GetSettings().then(s => {
      setSyncRoots(new Set(s.folders.map(f => f.remotePrefix.replace(/\/$/, ''))))
    }).catch(() => {})
    const unsubChange = EventsOn('files-changed', () => { refresh(); onQuotaChange?.() })
    const unsubErr = EventsOn('rename-error', (message: string, path: string) => {
      setRenameErr(`Rename failed for "${path}": ${message}`)
      refresh()
    })
    return () => { unsubChange(); unsubErr() }
  }, [])

  const toggle = (p: string) => setExpanded((e) => ({ ...e, [p]: !e[p] }))

  const withBusy = async <T,>(label: string, fn: () => Promise<T>): Promise<T | null> => {
    setBusy(label); setErr(null)
    try { return await fn() }
    catch (e: any) { setErr(String(e?.message ?? e)); return null }
    finally { setBusy(null) }
  }

  const onUpload = async () => {
    const key = await withBusy('Picking file…', () => PickFile(''))
    if (!key) return
    const conflict = files.some(f => f.key === key)
    if (conflict) {
      const ok = await new Promise<boolean>(resolve => {
        replaceResolve.current = resolve
        setReplaceConflicts([key])
      })
      if (!ok) return
    }
    await withBusy('Uploading…', () => UploadPicked(key))
    await refresh()
    onQuotaChange?.()
  }
  const onDownload = async (key: string) => {
    const dest = await withBusy('Downloading…', () => DownloadFile(key))
    if (dest) setBusy(`Saved to ${dest}`)
    // Leave the "saved to" message visible briefly.
    setTimeout(() => setBusy(null), 2500)
  }
  const onDelete = async (key: string) => {
    const ok = await confirm({ title: 'Delete file', message: `Delete ${key}? This cannot be undone.`, confirmText: 'Delete', danger: true })
    if (!ok) return
    await withBusy('Deleting…', () => DeleteFile(key))
    await refresh()
    onQuotaChange?.()
  }
  const onDeleteFolder = async (path: string) => {
    const ok = await confirm({ title: 'Delete folder', message: `Delete ${path}/ and everything inside? This cannot be undone.`, confirmText: 'Delete', danger: true })
    if (!ok) return
    await withBusy('Deleting folder…', () => DeleteFolder(path))
    await refresh()
    onQuotaChange?.()
  }
  const onDownloadFolder = async (path: string) => {
    const dest = await withBusy('Downloading folder…', () => DownloadFolder(path + '/'))
    if (dest) setBusy(`Saved to ${dest}`)
    setTimeout(() => setBusy(null), 2500)
  }
  const onMkdir = async () => {
    if (!newFolder?.trim()) return
    await withBusy('Creating folder…', () => CreateFolder(newFolder.trim()))
    setNewFolder(null)
    await refresh()
  }

  const onRecompute = async () => {
    await withBusy('Recomputing…', () => RecomputeUsage())
    onQuotaChange?.()
  }

  const isProcessing = (path: string) =>
    processing.some(p => path === p || path.startsWith(p + '/'))

  const isSyncRoot = (path: string) => syncRoots.has(path)

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
    <div className="card">
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: '1rem' }}>
        <h3 style={{ margin: 0 }}>Files</h3>
        <div className="row" style={{ gap: '.5rem' }}>
          {busy && <span className="muted">{busy}</span>}
          <button className="ghost" onClick={() => setNewFolder('')} disabled={!!busy}>New folder</button>
          <button className="ghost" onClick={onUpload} disabled={!!busy}>Upload file</button>
        </div>
      </div>
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
          <button onClick={onMkdir} disabled={!newFolder.trim()}>Create</button>
          <button className="ghost" onClick={() => setNewFolder(null)}>Cancel</button>
        </div>
      )}
      <table>
        <thead>
          <tr>
            <th>
              Name
              {/* Collapse-all lives in the header so it's discoverable
                  without a menu. Intentionally no "expand all" — a
                  blind expand on a large bucket would render thousands
                  of rows, whereas collapsing is cheap and is the bulk
                  action users actually want. */}
              <button
                className="ghost"
                title="Collapse all folders"
                onClick={() => setExpanded({})}
                style={{ padding: '.1rem .4rem', marginLeft: '.5rem', fontSize: '0.8rem', lineHeight: 1 }}
              >⊟</button>
            </th>
            <th>Size</th>
            <th>Modified</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, isSyncRoot)}
        </tbody>
      </table>
      <div style={{
        textAlign: 'center',
        marginTop: '1rem',
        fontSize: '0.82rem',
        color: 'var(--text-secondary)',
      }}>
        <a
          onClick={(e) => { e.preventDefault(); onRecompute() }}
          href="#"
          style={{ color: 'var(--accent)', textDecoration: 'underline', cursor: 'pointer' }}
        >recompute usage</a>
      </div>
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
  isProcessing: (path: string) => boolean,
  editingPath: string | null,
  editingValue: string,
  setEditingPath: (p: string | null) => void,
  setEditingValue: (v: string) => void,
  onCommitRename: (oldPath: string) => void,
  isSyncRoot: (path: string) => boolean,
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
          <span className="muted" style={{ fontSize: '0.85rem' }}>renaming…</span>
        </div>
      ) : (
        <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
          <button className="ghost" title={syncRoot ? 'Sync root — cannot rename' : 'Rename'} style={{ ...iconBtn, ...(syncRoot ? disabledStyle : {}) }}
            onClick={(e) => { e.stopPropagation(); if (!syncRoot) { setEditingPath(c.path); setEditingValue(c.name) } }}>✏️</button>
          <button className="ghost" title="Download as zip" style={iconBtn}
            onClick={(e) => { e.stopPropagation(); onDownloadFolder(c.path) }}>⬇</button>
          <button className="danger" title={syncRoot ? 'Sync root — cannot delete' : 'Delete'} style={{ ...iconBtn, ...(syncRoot ? disabledStyle : {}) }}
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
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, isSyncRoot))
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
