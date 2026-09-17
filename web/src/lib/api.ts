export type { PreviewResult } from '@shared/components/PreviewContent'
import type { PreviewResult } from '@shared/components/PreviewContent'
import { startLoading, endLoading } from '@shared/lib/loading'
export { onLoading } from '@shared/lib/loading'

export type PublicUser = {
  id: string
  login: string
  role: 'user' | 'admin'
  quotaBytes: number
  usedBytes: number
  totpEnabled: boolean
  email?: string
}

export type PublicSession = {
  id: string
  label: string
  createdAt: string
  lastUsedAt: string
  expiresAt: string
}

export type PublicDevice = {
  id: string
  label: string
  createdAt: string
  expiresAt: string
}

export type LoginRecord = {
  ip: string
  userAgent?: string
  at: string
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
const SESSION_ID_KEY = 'mist.sessionId'

// Refresh session id of this browser, only used to flag "this session".
export function getSessionId(): string | null {
  return localStorage.getItem(SESSION_ID_KEY)
}
function setSessionId(id?: string) {
  if (id) localStorage.setItem(SESSION_ID_KEY, id)
}

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
  localStorage.removeItem(SESSION_ID_KEY)
}

// refreshSession trades the HttpOnly refresh cookie for a new access
// token. Single-flight: parallel 401s share one POST, so the rotation
// happens once.
let _refreshing: Promise<boolean> | null = null
export function refreshSession(): Promise<boolean> {
  _refreshing ??= fetch('/auth/refresh', { method: 'POST', headers: { 'X-Client': 'web' } })
    .then(async (res) => {
      if (!res.ok) return false
      const { token, user, sessionId } = await res.json() as { token: string; user: PublicUser; sessionId?: string }
      setSession(token, user)
      setSessionId(sessionId)
      return true
    })
    .catch(() => false)
    .finally(() => { _refreshing = null })
  return _refreshing
}

// adoptToken keeps this tab logged in after an endpoint revoked every
// other session and returned a replacement token.
function adoptToken<T extends { token: string; sessionId?: string }>(res: T): T {
  const user = getUser()
  if (res.token && user) setSession(res.token, user)
  setSessionId(res.sessionId)
  return res
}

// logout drops the server-side refresh session, then the local one.
export async function logout() {
  await fetch('/auth/logout', { method: 'POST', headers: { 'X-Client': 'web' } }).catch(() => {})
  clearSession()
}

// authFetch sends the bearer token; on 401 it refreshes once and
// retries, and only then gives up to /login.
async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const send = () => {
    const headers = new Headers(init.headers)
    const tok = getToken()
    if (tok) headers.set('Authorization', `Bearer ${tok}`)
    return fetch(path, { ...init, headers })
  }
  let res = await send()
  if (res.status === 401 && await refreshSession()) res = await send()
  if (res.status === 401) {
    clearSession()
    window.location.replace('/login')
  }
  return res
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
export type EventMsg =
  | { type: 'files-changed' }
  | { type: 'rename-error'; message: string; path: string }

export type ListResponse = {
  objects: ObjectInfo[]
  processing: string[]
}

const _eventListeners = new Set<(e: EventMsg) => void>()
export function onEvent(l: (e: EventMsg) => void): () => void {
  _eventListeners.add(l)
  ensureWS()
  return () => { _eventListeners.delete(l) }
}

let _ws: WebSocket | null = null
let _wsBackoff = 500
// Consecutive failed connects. An idle tab makes no fetches, so a dead
// server would otherwise go unnoticed until the next click — after a
// few failed WS reconnects we probe /health and route to login if the
// server is truly gone (vs. just a websocket hiccup).
let _wsFails = 0
function ensureWS() {
  if (_ws && (_ws.readyState === WebSocket.OPEN || _ws.readyState === WebSocket.CONNECTING)) return
  const tok = getToken()
  if (!tok) return
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  // No token in the URL — authenticate via the first message instead, so
  // the JWT never lands in proxy/access logs.
  const url = `${proto}//${location.host}/ws`
  const ws = new WebSocket(url)
  _ws = ws
  ws.onopen = () => {
    _wsBackoff = 500
    _wsFails = 0
    ws.send(JSON.stringify({ type: 'auth', token: tok }))
  }
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data) as EventMsg
      _eventListeners.forEach((l) => l(msg))
    } catch { /* ignore */ }
  }
  ws.onclose = () => {
    _ws = null
    _wsFails++
    // Server closes a socket whose token expired or predates a restart.
    if (_wsFails === 2 && getToken()) void refreshSession()
    if (_wsFails >= 3 && getToken()) {
      // r.ok matters: through a proxy a dead backend still "answers".
      fetch('/health')
        .then((r) => { if (r.ok) { _wsFails = 0 } else { serverLost() } })
        .catch(() => serverLost())
    }
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

// serverLost routes the user back to the login page with a friendly
// "connection lost" notice instead of leaving screens stuck on raw
// fetch errors. The notice survives the redirect via sessionStorage;
// Login reads and clears it. No-op while already on /login so a failed
// login POST shows its own error instead of looping.
export const NETWORK_ERROR = 'NETWORK'
function serverLost() {
  if (window.location.pathname === '/login') return
  // An OPEN push channel is proof of life: if we're still receiving
  // websocket frames the server is up, and whatever failed was local
  // congestion (e.g. the browser's per-origin connection pool jammed
  // by a refresh storm during a mass upload). Never log the user out
  // on a false positive.
  if (_ws && _ws.readyState === WebSocket.OPEN) return
  sessionStorage.setItem('mist.notice', 'serverLost')
  clearSession()
  window.location.replace('/login')
}

// A dead backend behind a proxy does NOT reject fetch — Vite's dev
// proxy answers 500 and Traefik answers 502/503. So any 5xx is only a
// SUSPICION of a dead server; /health is the discriminator (a live
// server always answers it, a single buggy endpoint doesn't take it
// down). Probe once, redirect only when /health is dead too.
let _probing = false
function maybeServerLost() {
  if (_probing) return
  _probing = true
  fetch('/health')
    .then((r) => { if (!r.ok) serverLost() })
    .catch(() => serverLost())
    .finally(() => { _probing = false })
}

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Content-Type', 'application/json')
  headers.set('X-Client', 'web')
  startLoading()
  try {
    let res: Response
    try {
      res = await authFetch(path, { ...init, headers })
    } catch {
      // fetch rejects only when even the proxy/edge is unreachable.
      serverLost()
      throw new Error(NETWORK_ERROR)
    }
    if (!res.ok) {
      if ([500, 502, 503, 504].includes(res.status)) {
        maybeServerLost() // async probe; redirects only if /health is dead
      }
      const text = await res.text()
      throw new Error(`${res.status}: ${text || res.statusText}`)
    }
    return res.json() as Promise<T>
  } finally {
    endLoading()
  }
}

export type Features = { sso: boolean; auditLog: boolean }
export const defaultFeatures: Features = { sso: false, auditLog: false }

export async function fetchHealth(): Promise<{ version: string; features: Features }> {
  try {
    const res = await fetch('/health')
    if (!res.ok) return { version: '', features: defaultFeatures }
    return res.json()
  } catch {
    return { version: '', features: defaultFeatures }
  }
}

export type LoginResult =
  | { totp_required: true }
  | { token: string; user: PublicUser; sessionId?: string }

export const api = {
  login: async (login: string, password: string, totpCode?: string, rememberDevice?: boolean): Promise<LoginResult> => {
    const headers = new Headers({ 'Content-Type': 'application/json', 'X-Client': 'web' })
    startLoading()
    try {
      const res = await fetch('/auth/login', {
        method: 'POST',
        headers,
        body: JSON.stringify({
          login,
          password,
          ...(totpCode ? { totpCode } : {}),
          ...(rememberDevice ? { rememberDevice } : {}),
          refresh: true,
        }),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(`${res.status}: ${text || res.statusText}`)
      }
      const result = await res.json() as LoginResult
      if ('sessionId' in result) setSessionId(result.sessionId)
      return result
    } finally {
      endLoading()
    }
  },
  totp: {
    setup: () => req<{ secret: string; uri: string }>('/api/totp/setup'),
    enable: (secret: string, code: string, password: string) =>
      req<{ backupCodes: string[]; token: string }>('/api/totp/enable', {
        method: 'POST',
        body: JSON.stringify({ secret, code, password, refresh: true }),
      }).then(adoptToken),
    disable: (password: string, code: string) =>
      req<{ ok: boolean; token: string }>('/api/totp/disable', {
        method: 'DELETE',
        body: JSON.stringify({ password, code, refresh: true }),
      }).then(adoptToken),
    regenBackup: (code: string) =>
      req<{ backupCodes: string[] }>('/api/totp/regen-backup', {
        method: 'POST',
        body: JSON.stringify({ code }),
      }),
  },
  devices: {
    list: () => req<PublicDevice[]>('/api/devices'),
    revoke: (id: string) => req<{ ok: boolean }>(`/api/devices/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    revokeAll: () => req<{ ok: boolean }>('/api/devices', { method: 'DELETE' }),
  },
  sessions: {
    list: () => req<PublicSession[]>('/api/sessions'),
    revoke: (id: string) => req<{ ok: boolean }>(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  },
  loginHistory: () => req<LoginRecord[]>('/api/login-history'),
  me: () => req<PublicUser>('/api/me'),
  updateEmail: (email: string) =>
    req<PublicUser>('/api/me/email', { method: 'PUT', body: JSON.stringify({ email }) }),
  changePassword: (currentPassword: string, newPassword: string, totpCode?: string) =>
    req<{ ok: boolean; token: string }>('/api/me/password', {
      method: 'PUT',
      body: JSON.stringify({ currentPassword, newPassword, ...(totpCode ? { totpCode } : {}), refresh: true }),
    }).then(adoptToken),
  logoutAll: (opts: { password?: string; totpCode?: string }) =>
    req<void>('/api/me/logout-all', { method: 'POST', body: JSON.stringify(opts) }),
  listFiles: (prefix = '') =>
    req<ListResponse>(`/api/files?prefix=${encodeURIComponent(prefix)}`),
  deleteFile: (key: string) =>
    req<{ ok: boolean }>(`/api/files?key=${encodeURIComponent(key)}`, { method: 'DELETE' }),
  deleteFolder: (prefix: string) =>
    req<{ ok: boolean; count: number; freed: number }>(
      `/api/files?prefix=${encodeURIComponent(prefix)}`,
      { method: 'DELETE' },
    ),
  download: (key: string) =>
    req<{ url: string }>(`/api/files/download?key=${encodeURIComponent(key)}`),
  // Folder-as-zip download. The stream must be saved by the browser via
  // navigation, which can't set an Authorization header. Instead of
  // leaking the session JWT in the URL, we mint a short-lived single-use
  // ticket (authenticated POST, header) bound to {user, prefix}, then
  // navigate to the top-level stream route with just the ticket.
  downloadZip: async (prefix: string) => {
    const { ticket } = await req<{ ticket: string }>('/api/files/download-zip-ticket', {
      method: 'POST',
      body: JSON.stringify({ prefix }),
    })
    window.location.href = `/download-zip?ticket=${encodeURIComponent(ticket)}`
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
  rename: (path: string, newName: string) =>
    req<{ ok: boolean }>('/api/files/rename', {
      method: 'POST',
      body: JSON.stringify({ path, newName }),
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
  previewFile: async (key: string): Promise<PreviewResult> => {
    const res = await authFetch(`/api/files/preview?key=${encodeURIComponent(key)}`)
    if (!res.ok) {
      throw new Error(`${res.status}: ${res.statusText}`)
    }
    const ptype = res.headers.get('X-Preview-Type') ?? 'binary'
    if (ptype === 'image') {
      const blob = await res.blob()
      return { type: 'image', content: URL.createObjectURL(blob) }
    }
    if (ptype === 'text') {
      return { type: 'text', content: await res.text() }
    }
    return { type: 'binary' }
  },
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
    revokeAllSessions: () =>
      req<{ ok: boolean; users: number }>('/api/admin/sessions/revoke-all', { method: 'POST' }),
  },
}
