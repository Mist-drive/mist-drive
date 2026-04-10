import { useEffect, useState } from 'react'
import {
  DeleteFile,
  DeleteFolder,
  DownloadFile,
  DownloadFolder,
  ListFiles,
  RecomputeUsage,
  UploadFile,
} from '../../wailsjs/go/main/App'
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
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null) // status line during uploads/downloads
  const [query, setQuery] = useState('')

  const refresh = async () => {
    try { setFiles((await ListFiles()) || []) }
    catch (e: any) { setErr(String(e?.message ?? e)) }
  }
  useEffect(() => {
    refresh()
    // Server pushes "files-changed" whenever any client mutates the
    // bucket; the envelope carries no delta so we just re-fetch.
    return EventsOn('files-changed', () => { refresh(); onQuotaChange?.() })
  }, [])

  const toggle = (p: string) => setExpanded((e) => ({ ...e, [p]: !e[p] }))

  const withBusy = async <T,>(label: string, fn: () => Promise<T>): Promise<T | null> => {
    setBusy(label); setErr(null)
    try { return await fn() }
    catch (e: any) { setErr(String(e?.message ?? e)); return null }
    finally { setBusy(null) }
  }

  const onUpload = async () => {
    const key = await withBusy('Uploading…', () => UploadFile(''))
    if (key) {
      await refresh()
      onQuotaChange?.()
    }
  }
  const onDownload = async (key: string) => {
    const dest = await withBusy('Downloading…', () => DownloadFile(key))
    if (dest) setBusy(`Saved to ${dest}`)
    // Leave the "saved to" message visible briefly.
    setTimeout(() => setBusy(null), 2500)
  }
  const onDelete = async (key: string) => {
    if (!confirm(`Delete ${key}?`)) return
    await withBusy('Deleting…', () => DeleteFile(key))
    await refresh()
    onQuotaChange?.()
  }
  const onDeleteFolder = async (path: string) => {
    if (!confirm(`Delete ${path}/ and everything inside?`)) return
    await withBusy('Deleting folder…', () => DeleteFolder(path))
    await refresh()
    onQuotaChange?.()
  }
  const onDownloadFolder = async (path: string) => {
    const dest = await withBusy('Downloading folder…', () => DownloadFolder(path + '/'))
    if (dest) setBusy(`Saved to ${dest}`)
    setTimeout(() => setBusy(null), 2500)
  }
  const onRecompute = async () => {
    await withBusy('Recomputing…', () => RecomputeUsage())
    onQuotaChange?.()
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
    <div className="card">
      <div className="row" style={{ justifyContent: 'space-between', marginBottom: '1rem' }}>
        <h3 style={{ margin: 0 }}>Files</h3>
        <div className="row" style={{ gap: '.5rem' }}>
          {busy && <span className="muted">{busy}</span>}
          <button className="ghost" onClick={onUpload} disabled={!!busy}>Upload file</button>
        </div>
      </div>
      {err && <p className="error" style={{ marginBottom: '.8rem' }}>{err}</p>}
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
          {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder)}
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
): JSX.Element[] {
  const rows: JSX.Element[] = []
  for (const c of sortedChildren(node)) {
    const indent = { paddingLeft: `${depth * 1.2 + 0.4}rem` }
    const iconBtn = { padding: '.3rem .55rem', fontSize: '0.95rem', lineHeight: 1 }
    const actions = (
      <div className="row" style={{ gap: '.4rem', justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
        <button className="ghost" title={c.isDir ? 'Download as zip' : 'Download'} style={iconBtn}
          onClick={(e) => { e.stopPropagation(); c.isDir ? onDownloadFolder(c.path) : onDownload(c.path) }}>⬇</button>
        <button className="danger" title="Delete" style={iconBtn}
          onClick={(e) => { e.stopPropagation(); c.isDir ? onDeleteFolder(c.path) : onDelete(c.path) }}>✕</button>
      </div>
    )
    if (c.isDir) {
      const open = !!expanded[c.path]
      rows.push(
        <tr key={`d:${c.path}`}>
          <td style={{ ...indent, cursor: 'pointer' }} onClick={() => toggle(c.path)}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}>
              {open ? '▾' : '▸'}
            </span>
            <span style={{ display: 'inline-block', width: '1.4rem' }}>{open ? '📂' : '📁'}</span>
            <strong>{c.name}</strong>
          </td>
          <td className="muted">{fmt(c.size)}</td>
          <td className="muted">—</td>
          <td>{actions}</td>
        </tr>,
      )
      if (open) {
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder))
      }
    } else {
      rows.push(
        <tr key={`f:${c.path}`}>
          <td style={indent}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}></span>
            <span style={{ display: 'inline-block', width: '1.4rem' }}>📄</span>
            {c.name}
          </td>
          <td>{fmt(c.size)}</td>
          <td className="muted">
            {c.lastModified ? new Date(c.lastModified).toLocaleString() : ''}
          </td>
          <td>{actions}</td>
        </tr>,
      )
    }
  }
  return rows
}
