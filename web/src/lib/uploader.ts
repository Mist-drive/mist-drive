import { api } from './api'

export type UploadProgress = (loaded: number, total: number) => void

const PART_SIZE = 8 * 1024 * 1024 // 8 MiB
const CONCURRENCY = 4

// Uploads a single File to the given key using multipart presigned PUTs.
// Parallelism is bounded and per-part progress is aggregated. Pass an
// AbortSignal to cancel — all in-flight part PUTs are aborted and the
// multipart upload is asked to abort server-side (which releases the
// user's quota reservation).
export async function uploadFile(
  key: string,
  file: File,
  onProgress?: UploadProgress,
  signal?: AbortSignal,
): Promise<void> {
  if (signal?.aborted) throw new DOMException('aborted', 'AbortError')

  const { uploadId, partSize, urls } = await api.uploadInit(key, file.size, PART_SIZE)
  const loaded = new Array(urls.length).fill(0) as number[]
  const parts: { partNumber: number; etag: string }[] = new Array(urls.length)

  // Track active XHRs so a late signal.abort() can cancel them all.
  const inflight = new Set<XMLHttpRequest>()
  const onAbort = () => inflight.forEach((x) => x.abort())
  signal?.addEventListener('abort', onAbort)

  const total = file.size
  const tick = () => {
    if (!onProgress) return
    const sum = loaded.reduce((a, b) => a + b, 0)
    onProgress(sum, total)
  }

  try {
    let nextIdx = 0
    const worker = async () => {
      while (true) {
        if (signal?.aborted) return
        const i = nextIdx++
        if (i >= urls.length) return
        const start = i * partSize
        const end = Math.min(start + partSize, file.size)
        const blob = file.slice(start, end)
        const etag = await putPart(urls[i].url, blob, (l) => {
          loaded[i] = l
          tick()
        }, inflight, signal)
        parts[i] = { partNumber: urls[i].partNumber, etag }
      }
    }
    await Promise.all(
      Array.from({ length: Math.min(CONCURRENCY, urls.length) }, worker),
    )
    if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
    await api.uploadComplete(uploadId, parts)
  } catch (err) {
    // On *any* failure (cancel, network error, server 5xx) tell the
    // server to abort — that releases the quota reservation and lets
    // the GC drop the multipart state.
    try { await api.uploadAbort(uploadId) } catch { /* ignore */ }
    throw err
  } finally {
    signal?.removeEventListener('abort', onAbort)
  }
}

function putPart(
  url: string,
  blob: Blob,
  onProg: (loaded: number) => void,
  inflight: Set<XMLHttpRequest>,
  signal?: AbortSignal,
): Promise<string> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('aborted', 'AbortError'))
      return
    }
    const xhr = new XMLHttpRequest()
    inflight.add(xhr)
    xhr.open('PUT', url, true)
    xhr.upload.addEventListener('progress', (e) => {
      if (e.lengthComputable) onProg(e.loaded)
    })
    const done = () => inflight.delete(xhr)
    xhr.onload = () => {
      done()
      if (xhr.status >= 200 && xhr.status < 300) {
        const etag = xhr.getResponseHeader('ETag')?.replaceAll('"', '') ?? ''
        resolve(etag)
      } else {
        reject(new Error(`part upload ${xhr.status}`))
      }
    }
    xhr.onerror = () => { done(); reject(new Error('network error')) }
    xhr.onabort = () => { done(); reject(new DOMException('aborted', 'AbortError')) }
    xhr.send(blob)
  })
}
