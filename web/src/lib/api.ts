export type PublicUser = {
  id: string
  login: string
  role: 'user' | 'admin'
  quotaBytes: number
  usedBytes: number
}

export type ObjectInfo = {
  key: string
  size: number
  etag: string
  lastModified: string
}

const TOKEN_KEY = 'mist.token'
const USER_KEY = 'mist.user'

export function getToken(): string | null {
  return sessionStorage.getItem(TOKEN_KEY)
}
export function getUser(): PublicUser | null {
  const s = sessionStorage.getItem(USER_KEY)
  return s ? JSON.parse(s) : null
}
export function setSession(token: string, user: PublicUser) {
  sessionStorage.setItem(TOKEN_KEY, token)
  sessionStorage.setItem(USER_KEY, JSON.stringify(user))
}
export function clearSession() {
  sessionStorage.removeItem(TOKEN_KEY)
  sessionStorage.removeItem(USER_KEY)
}

// Global in-flight counter for the loading bar. We only instrument
// `req()` — upload part PUTs go direct to MinIO via XHR and are tracked
// separately by the progress panel, so there's no double-counting.
let _inflight = 0
const _loadingListeners = new Set<(n: number) => void>()
function notifyLoading() {
  _loadingListeners.forEach((l) => l(_inflight))
}
export function onLoading(l: (n: number) => void): () => void {
  _loadingListeners.add(l)
  l(_inflight)
  return () => { _loadingListeners.delete(l) }
}

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Content-Type', 'application/json')
  const tok = getToken()
  if (tok) headers.set('Authorization', `Bearer ${tok}`)
  _inflight++; notifyLoading()
  try {
    const res = await fetch(path, { ...init, headers })
    if (!res.ok) {
      const text = await res.text()
      throw new Error(`${res.status}: ${text || res.statusText}`)
    }
    return res.json() as Promise<T>
  } finally {
    _inflight--; notifyLoading()
  }
}

export const api = {
  login: (login: string, password: string) =>
    req<{ token: string; user: PublicUser }>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ login, password }),
    }),
  me: () => req<PublicUser>('/api/me'),
  listFiles: (prefix = '') =>
    req<ObjectInfo[]>(`/api/files?prefix=${encodeURIComponent(prefix)}`),
  deleteFile: (key: string) =>
    req<{ ok: boolean }>(`/api/files?key=${encodeURIComponent(key)}`, { method: 'DELETE' }),
  deleteFolder: (prefix: string) =>
    req<{ ok: boolean; count: number; freed: number }>(
      `/api/files?prefix=${encodeURIComponent(prefix)}`,
      { method: 'DELETE' },
    ),
  download: (key: string) =>
    req<{ url: string }>(`/api/files/download?key=${encodeURIComponent(key)}`),
  // Folder-as-zip download. Can't go through fetch because the response
  // is a stream we want the browser to save directly, and can't set an
  // Authorization header on window.location — so the token is passed as
  // a query param (the server's auth middleware accepts either).
  downloadZipUrl: (prefix: string) => {
    const tok = getToken() ?? ''
    return `/api/files/download-zip?prefix=${encodeURIComponent(prefix)}&token=${encodeURIComponent(tok)}`
  },
  uploadInit: (key: string, size: number, partSize: number) =>
    req<{ uploadId: string; partSize: number; urls: { partNumber: number; url: string }[] }>(
      '/api/files/upload/init',
      { method: 'POST', body: JSON.stringify({ key, size, partSize }) },
    ),
  uploadComplete: (uploadId: string, parts: { partNumber: number; etag: string }[]) =>
    req<{ ok: boolean; size: number }>('/api/files/upload/complete', {
      method: 'POST',
      body: JSON.stringify({ uploadId, parts }),
    }),
  uploadAbort: (uploadId: string) =>
    req<{ ok: boolean }>('/api/files/upload/abort', {
      method: 'POST',
      body: JSON.stringify({ uploadId }),
    }),
  admin: {
    listUsers: () => req<PublicUser[]>('/api/admin/users'),
    createUser: (login: string, password: string, quotaBytes?: number) =>
      req<PublicUser>('/api/admin/users', {
        method: 'POST',
        body: JSON.stringify({ login, password, quotaBytes: quotaBytes ?? 0 }),
      }),
    patchQuota: (id: string, quotaBytes: number) =>
      req<PublicUser>(`/api/admin/users/${id}/quota`, {
        method: 'PATCH',
        body: JSON.stringify({ quotaBytes }),
      }),
    deleteUser: (id: string) =>
      req<{ ok: boolean }>(`/api/admin/users/${id}`, { method: 'DELETE' }),
  },
}
