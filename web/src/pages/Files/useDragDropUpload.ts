import { useRef, useState } from 'react'
import { type DragHandlers } from './FileTreeRows'
import { useTranslation } from '@shared/lib/i18n'

async function readAllEntries(reader: FileSystemDirectoryReader): Promise<FileSystemEntry[]> {
  const all: FileSystemEntry[] = []
  while (true) {
    const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej))
    if (!batch.length) break
    all.push(...batch)
  }
  return all
}

type Deps = {
  runUploadJobs: (jobs: { key: string; file: File }[], label?: string) => Promise<void>
  setExpanded: (fn: (e: Record<string, boolean>) => Record<string, boolean>) => void
  t: ReturnType<typeof useTranslation>['t']
}

// Owns drag state (root drop zone + per-folder hover/auto-expand) and the
// recursive FileSystemEntry walk that turns a drop event into upload
// jobs. Delegates the actual upload work to runUploadJobs from
// useUploadQueue.
export function useDragDropUpload({ runUploadJobs, setExpanded, t }: Deps) {
  const [isDragging, setIsDragging] = useState(false)
  const [dragOverFolder, setDragOverFolder] = useState<string | null>(null)
  const dragCounter = useRef(0)
  const expandTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

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

  const dragHandlers: DragHandlers = { dragOverFolder, onDragEnterFolder, onFolderDrop: handleDrop }

  const rootDropZoneProps = {
    onDragEnter: (e: React.DragEvent) => { e.preventDefault(); dragCounter.current++; setIsDragging(true) },
    onDragLeave: () => { dragCounter.current--; if (dragCounter.current === 0) clearDragState() },
    onDragOver: (e: React.DragEvent) => e.preventDefault(),
    onDrop: (e: React.DragEvent) => handleDrop(e, ''),
  }

  return { isDragging, dragOverFolder, dragHandlers, rootDropZoneProps, setDragOverFolder }
}
