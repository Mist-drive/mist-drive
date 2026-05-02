export type UploadEntry = {
  key: string
  pct: number
  loaded: number
  total: number
  startedAt: number
}

export function fmtEta(seconds: number): string {
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

export function etaFor(loaded: number, total: number, startedAt: number): string {
  if (loaded <= 0 || loaded >= total) return '—'
  const elapsed = (Date.now() - startedAt) / 1000
  if (elapsed < 0.5) return '—'
  const speed = loaded / elapsed
  return fmtEta((total - loaded) / speed)
}

export function computeGlobalEta(entries: UploadEntry[]): string {
  if (entries.length === 0) return '—'
  let totalLoaded = 0, totalSize = 0, earliestStart = Infinity
  for (const e of entries) {
    totalLoaded += e.loaded
    totalSize += e.total
    if (e.startedAt < earliestStart) earliestStart = e.startedAt
  }
  if (totalLoaded <= 0) return '—'
  const elapsed = (Date.now() - earliestStart) / 1000
  if (elapsed < 0.5) return '—'
  const speed = totalLoaded / elapsed
  return fmtEta((totalSize - totalLoaded) / speed)
}
