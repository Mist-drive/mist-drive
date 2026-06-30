import { useEffect, useRef, useState } from 'react'
import { type ObjectInfo } from '../../lib/api'
import { uploadFile } from '../../lib/uploader'
import { type ConflictEntry } from '../../components/ReplaceDialog'
import { type UploadEntry } from '@shared/lib/upload'
import { useTranslation } from '@shared/lib/i18n'

const FILE_CONCURRENCY = 4

type Deps = {
  files: ObjectInfo[]
  refresh: () => Promise<void>
  setBusy: (label: string | null) => void
  setErr: (msg: string | null) => void
  t: ReturnType<typeof useTranslation>['t']
}

// Owns the upload queue end to end: the active/queued/done/failed
// counters, per-file abort controllers, the replace-conflict dialog
// handshake, and the bounded-concurrency worker pool. busy/err are
// shared page-level state (other actions like delete/rename use them
// too), so they're injected rather than owned here.
export function useUploadQueue({ files, refresh, setBusy, setErr, t }: Deps) {
  const [active, setActive] = useState<Record<string, UploadEntry>>({})
  const [queued, setQueued] = useState(0)
  const [done, setDone] = useState(0)
  const [failed, setFailed] = useState(0)
  const [replaceConflicts, setReplaceConflicts] = useState<ConflictEntry[]>([])
  const replaceResolve = useRef<((choice: 'replace' | 'diff' | 'cancel') => void) | null>(null)
  const controllers = useRef<Map<string, AbortController>>(new Map())
  const cancelAll = useRef(false)
  const quotaHit = useRef(false)

  const uploading = Object.keys(active).length > 0 || queued > 0

  useEffect(() => {
    if (!uploading) return
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
      return ''
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [uploading])

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

  const onCancelUpload = (key: string) => {
    controllers.current.get(key)?.abort()
  }
  const onCancelAllUploads = () => {
    cancelAll.current = true
    for (const c of controllers.current.values()) c.abort()
  }

  return {
    activeList: Object.values(active),
    queued,
    done,
    failed,
    uploading,
    runUploadJobs,
    onCancelUpload,
    onCancelAllUploads,
    replaceConflicts,
    onReplaceConfirm: () => { setReplaceConflicts([]); replaceResolve.current?.('replace') },
    onReplaceDiff: () => { setReplaceConflicts([]); replaceResolve.current?.('diff') },
    onReplaceCancel: () => { setReplaceConflicts([]); replaceResolve.current?.('cancel') },
  }
}
