import { useEffect, useRef, useState } from 'react'
import { api, getUser, ObjectInfo, setSession, getToken, PublicUser, onEvent } from '../../lib/api'
import { useConfirm } from '../../components/ConfirmDialog'
import ReplaceDialog from '../../components/ReplaceDialog'
import PreviewContent from '@shared/components/PreviewContent'
import StorageStats from '@shared/components/StorageStats'
import UploadCard from '@shared/components/UploadCard'
import { buildTree } from '@shared/lib/tree'
import { useTranslation } from '@shared/lib/i18n'
import { renderTree } from './FileTreeRows'
import { useUploadQueue } from './useUploadQueue'
import { useDragDropUpload } from './useDragDropUpload'
import { usePreview } from './usePreview'

// webkitdirectory isn't in the default React HTMLInputAttributes type
declare module 'react' {
  interface InputHTMLAttributes<T> {
    webkitdirectory?: string
    directory?: string
  }
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
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [query, setQuery] = useState('')
  const [newFolder, setNewFolder] = useState<string | null>(null)
  const confirm = useConfirm()
  const [downloadingKeys, setDownloadingKeys] = useState<Set<string>>(new Set())

  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)

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

  const {
    activeList, queued, done, failed, uploading, runUploadJobs,
    onCancelUpload, onCancelAllUploads,
    replaceConflicts, onReplaceConfirm, onReplaceDiff, onReplaceCancel,
  } = useUploadQueue({ files, refresh, setBusy, setErr, t })

  const { isDragging, dragOverFolder, dragHandlers, rootDropZoneProps, setDragOverFolder } =
    useDragDropUpload({ runUploadJobs, setExpanded, t })

  const { previewKey, previewResult, previewLoading, onPreview, closePreview } = usePreview()

  const isBusy = !!busy || uploading
  const statusText = busy ?? (uploading ? t('status.uploading') : null)

  const withBusy = async <T,>(label: string, fn: () => Promise<T>): Promise<T | null> => {
    setBusy(label); setErr(null)
    try { return await fn() }
    catch (e: any) { setErr(e.message); return null }
    finally { setBusy(null) }
  }

  const toggle = (path: string) =>
    setExpanded(e => ({ ...e, [path]: !e[path] }))

  const onRefresh = async () => {
    await withBusy(t('status.refreshing'), async () => { await api.recomputeUsage(); await refresh() })
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
    setDownloadingKeys(s => new Set(s).add(key))
    try {
      await withBusy(t('status.downloading'), async () => {
        const { url } = await api.download(key)
        window.location.href = url
      })
    } finally {
      setDownloadingKeys(s => { const n = new Set(s); n.delete(key); return n })
    }
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
  const onDownloadFolder = async (path: string) => {
    await withBusy(t('status.downloading'), async () => {
      await api.downloadZip(path + '/')
    })
  }

  const onMkdir = async () => {
    if (!newFolder?.trim()) return
    await withBusy(t('status.creating'), async () => {
      await api.mkdir(newFolder.trim())
      setNewFolder(null)
      await refresh()
    })
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

  const isProcessing = (path: string) =>
    processing.some(p => path === p || path.startsWith(p + '/'))

  return (
    <>
    <div {...rootDropZoneProps}>
      <input ref={fileInput} type="file" multiple onChange={onPick} style={{ display: 'none' }} />
      <input ref={folderInput} type="file" multiple webkitdirectory="" directory="" onChange={onPick} style={{ display: 'none' }} />
      <UploadCard
        entries={activeList}
        queued={queued}
        done={done}
        failed={failed}
        onCancelAll={onCancelAllUploads}
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
            {renderTree(buildTree(filteredFiles), 0, effectiveExpanded, toggle, onDownload, onDelete, onDeleteFolder, onDownloadFolder, dragHandlers, isProcessing, editingPath, editingValue, setEditingPath, setEditingValue, onCommitRename, onPreview, downloadingKeys, t)}
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
        onConfirm={onReplaceConfirm}
        onDiff={onReplaceDiff}
        onCancel={onReplaceCancel}
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
