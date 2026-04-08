import { useEffect, useRef, useState } from 'react'
import { api, ObjectInfo } from '../lib/api'
import { uploadFile } from '../lib/uploader'
import { useConfirm } from '../components/ConfirmDialog'

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

// Human-friendly duration. "—" when we don't yet have enough data to
// estimate (no bytes uploaded, or less than half a second of history).
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
  const speed = loaded / elapsed // bytes / sec
  return fmtEta((total - loaded) / speed)
}

type UP = {
  key: string
  pct: number
  loaded: number
  total: number
  startedAt: number
}

// How many files upload in parallel. Each file internally parallelises
// its own multipart parts (see uploader.ts CONCURRENCY).
const FILE_CONCURRENCY = 4

type TreeNode = {
  name: string
  path: string           // full key from root
  isDir: boolean
  size: number           // file size, or sum for dirs
  lastModified?: string  // file only
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
  // compute dir sizes bottom-up
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
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1 // dirs first
    return a.name.localeCompare(b.name)
  })
}

export default function Files() {
  const [files, setFiles] = useState<ObjectInfo[]>([])
  const [err, setErr] = useState<string | null>(null)
  // Only tracks files currently being uploaded. Finished / errored ones
  // are removed from the map so the panel stays clean.
  const [active, setActive] = useState<Record<string, UP>>({})
  const [queued, setQueued] = useState(0)
  const [done, setDone] = useState(0)
  const [failed, setFailed] = useState(0)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [query, setQuery] = useState('')
  const confirm = useConfirm()

  // One AbortController per in-flight upload, keyed by the same key as
  // the `active` map. Lives in a ref because mutating it should not by
  // itself trigger a re-render — progress updates already do that.
  const controllers = useRef<Map<string, AbortController>>(new Map())
  // Set when the user clicks "Cancel all" so the worker pool drains
  // pending jobs without starting them.
  const cancelAll = useRef(false)
  // Set on the first 413 from /upload/init. Any further queued file
  // would just bounce off the same quota — cheaper and more honest to
  // stop the whole batch and surface a clear error.
  const quotaHit = useRef(false)
  // Used to force a re-render roughly every 500ms while uploads are
  // running so the ETA keeps ticking down even between progress events.
  const [, setTick] = useState(0)
  useEffect(() => {
    if (Object.keys(active).length === 0) return
    const t = setInterval(() => setTick((n) => n + 1), 500)
    return () => clearInterval(t)
  }, [active])
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)

  const toggle = (path: string) =>
    setExpanded(e => ({ ...e, [path]: !e[path] }))

  const refresh = async () => {
    try { setFiles(await api.listFiles()) }
    catch (e: any) { setErr(e.message) }
  }
  useEffect(() => { refresh() }, [])

  const onPick = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const list = e.target.files
    if (!list || list.length === 0) return

    // For folder picks, webkitRelativePath has the full path incl. the
    // top-level folder name, which becomes the S3 key. For plain file
    // picks, fall back to the file name.
    // Note: Chrome shows its own "Upload N files to this site?" prompt
    // on webkitdirectory inputs, so we don't add a second confirm here.
    const jobs: { key: string; file: File }[] = Array.from(list).map(f => ({
      key: (f as any).webkitRelativePath || f.name,
      file: f,
    }))
    setQueued(q => q + jobs.length)

    const runOne = async ({ key, file }: { key: string; file: File }) => {
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
          // user-initiated cancel — stay silent, don't count as failed
        } else {
          console.error('upload failed:', key, err)
          setFailed(f => f + 1)
          // Stop the batch on quota exhaustion so we don't hammer the
          // API with doomed init requests. api.req throws with a
          // "<status>: <body>" message; matching on "413" is enough.
          if (typeof err?.message === 'string' && err.message.startsWith('413')) {
            quotaHit.current = true
            // Abort everything else that's currently mid-upload too —
            // they're almost certainly on the same trajectory.
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

    cancelAll.current = false
    quotaHit.current = false
    setErr(null)
    // Bounded worker pool over the job list.
    let idx = 0
    const worker = async () => {
      while (idx < jobs.length) {
        const my = idx++
        if (cancelAll.current || quotaHit.current) {
          // Drain remaining queued jobs silently.
          setQueued(q => q - 1)
          continue
        }
        await runOne(jobs[my])
      }
    }
    await Promise.all(
      Array.from({ length: Math.min(FILE_CONCURRENCY, jobs.length) }, worker),
    )

    await refresh()
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
    // Navigating directly lets the browser stream the zip to disk
    // without buffering it in memory, which matters for big folders.
    window.location.href = api.downloadZipUrl(path + '/')
  }

  const onCancelUpload = (key: string) => {
    controllers.current.get(key)?.abort()
  }
  const onCancelAll = () => {
    cancelAll.current = true
    for (const c of controllers.current.values()) c.abort()
  }

  // Aggregate stats across currently-active uploads. Global speed is
  // computed from the earliest start time so it reflects the sustained
  // throughput of the whole batch, not just one file.
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

  // Client-side search: case-insensitive substring match on the full
  // key. All the data is already in memory (the full listing is tiny —
  // a few hundred KB gzipped even for GBs of files) so there's no
  // reason to round-trip the API.
  const q = query.trim().toLowerCase()
  const filteredFiles = q
    ? files.filter((f) => f.key.toLowerCase().includes(q))
    : files

  // When a search is active, auto-expand every ancestor folder of a
  // match so the hits are actually visible without manual clicking.
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
    <div>
      <h2>Files</h2>
      <div className="card">
        <div className="row">
          <label style={{ margin: 0 }}>
            <button type="button" className="ghost" onClick={() => fileInput.current?.click()}>
              Upload files
            </button>
            <input
              ref={fileInput}
              type="file"
              multiple
              onChange={onPick}
              style={{ display: 'none' }}
            />
          </label>
          <label style={{ margin: 0 }}>
            <button type="button" className="ghost" onClick={() => folderInput.current?.click()}>
              Upload folder
            </button>
            <input
              ref={folderInput}
              type="file"
              multiple
              webkitdirectory=""
              directory=""
              onChange={onPick}
              style={{ display: 'none' }}
            />
          </label>
        </div>
      </div>
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
                {/* flex:1 + minWidth:0 lets the path shrink and ellipsize
                    instead of pushing the stats onto a second line. */}
                <span
                  title={up.key}
                  style={{
                    flex: 1,
                    minWidth: 0,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
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
      {err && <p className="error">{err}</p>}
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
        <thead><tr><th>Name</th><th>Size</th><th>Modified</th><th></th></tr></thead>
        <tbody>
          {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder)}
        </tbody>
      </table>
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
    if (c.isDir) {
      const isOpen = !!expanded[c.path]
      rows.push(
        <tr key={`d:${c.path}`}>
          <td style={{ ...indent, cursor: 'pointer' }} onClick={() => toggle(c.path)}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}>
              {isOpen ? '▾' : '▸'}
            </span>
            <strong>{c.name}/</strong>
          </td>
          <td className="muted">{fmt(c.size)}</td>
          <td className="muted">—</td>
          <td className="row">
            <button
              className="ghost"
              onClick={(e) => { e.stopPropagation(); onDownloadFolder(c.path) }}
            >
              Download
            </button>
            <button
              className="danger"
              onClick={(e) => { e.stopPropagation(); onDeleteFolder(c.path) }}
            >
              Delete
            </button>
          </td>
        </tr>,
      )
      if (isOpen) {
        rows.push(...renderTree(c, depth + 1, expanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder))
      }
    } else {
      rows.push(
        <tr key={`f:${c.path}`}>
          <td style={indent}>
            <span className="muted" style={{ display: 'inline-block', width: '1.2rem' }}>·</span>
            {c.name}
          </td>
          <td>{fmt(c.size)}</td>
          <td className="muted">
            {c.lastModified ? new Date(c.lastModified).toLocaleString() : ''}
          </td>
          <td className="row">
            <button className="ghost" onClick={() => onDownload(c.path)}>Download</button>
            <button className="danger" onClick={() => onDelete(c.path)}>Delete</button>
          </td>
        </tr>,
      )
    }
  }
  return rows
}
