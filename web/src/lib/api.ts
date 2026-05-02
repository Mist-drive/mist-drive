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
const REMEMBER_KEY = 'mist.remember'
const SAVED_LOGIN_KEY = 'mist.savedLogin'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}
export function getUser(): PublicUser | null {
  const s = localStorage.getItem(USER_KEY)
  return s ? JSON.parse(s) : null
}
export function isRemembered(): boolean {
  return localStorage.getItem(REMEMBER_KEY) === 'true'
}
export function getSavedLogin(): string {
  return localStorage.getItem(SAVED_LOGIN_KEY) ?? ''
}
export function setSession(token: string, user: PublicUser, remember = isRemembered()) {
  localStorage.setItem(TOKEN_KEY, token)
  localStorage.setItem(USER_KEY, JSON.stringify(user))
  localStorage.setItem(REMEMBER_KEY, remember ? 'true' : 'false')
  if (remember) {
    localStorage.setItem(SAVED_LOGIN_KEY, user.login)
  } else {
    localStorage.removeItem(SAVED_LOGIN_KEY)
  }
}
export function clearSession() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
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

// Reconnecting WebSocket client for server-pushed events. We only use
// it as a "refresh your view" signal — the server never sends deltas,
// just a tiny `{type: "files-changed"}` envelope, and subscribers
// react by re-fetching authoritative state. That way the ws channel
// and the store can never drift apart.
//
// Backoff is capped at 10s so a flaky network doesn't pin the tab at
// 100% reconnecting CPU. A hidden tab pauses reconnects until it's
// visible again to avoid wasting a socket on backgrounded tabs.
export type EventMsg = { type: 'files-changed' }

const _eventListeners = new Set<(e: EventMsg) => void>()
export function onEvent(l: (e: EventMsg) => void): () => void {
  _eventListeners.add(l)
  ensureWS()
  return () => { _eventListeners.delete(l) }
}

let _ws: WebSocket | null = null
let _wsBackoff = 500
function ensureWS() {
  if (_ws && (_ws.readyState === WebSocket.OPEN || _ws.readyState === WebSocket.CONNECTING)) return
  const tok = getToken()
  if (!tok) return
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${proto}//${location.host}/api/ws?token=${encodeURIComponent(tok)}`
  const ws = new WebSocket(url)
  _ws = ws
  ws.onopen = () => { _wsBackoff = 500 }
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data) as EventMsg
      _eventListeners.forEach((l) => l(msg))
    } catch { /* ignore */ }
  }
  ws.onclose = () => {
    _ws = null
    // Reconnect with capped exponential backoff. Skip while the tab
    // is hidden — we'll retry on visibilitychange.
    if (document.visibilityState === 'hidden') return
    setTimeout(ensureWS, _wsBackoff)
    _wsBackoff = Math.min(_wsBackoff * 2, 10_000)
  }
  ws.onerror = () => { ws.close() }
}
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') ensureWS()
})

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
  mkdir: (path: string) =>
    req<{ ok: boolean }>('/api/files/mkdir', {
      method: 'POST',
      body: JSON.stringify({ path }),
    }),
  recomputeUsage: () =>
    req<{ ok: boolean; usedBytes: number; count: number }>(
      '/api/files/recompute-usage',
      { method: 'POST' },
    ),
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
