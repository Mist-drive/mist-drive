type Listener = () => void
const _listeners = new Set<Listener>()

export function onSessionExpired(l: Listener): () => void {
  _listeners.add(l)
  return () => _listeners.delete(l)
}

export function notifySessionExpired(): void {
  _listeners.forEach(l => l())
}

// Server-lost channel: same pub/sub pattern as session expiry, but for
// network-level failures (API stopped/unreachable). App.tsx reacts by
// returning to the login screen with a friendly "connection lost"
// notice instead of leaving screens stuck on raw errors.
const _lostListeners = new Set<Listener>()

export function onServerLost(l: Listener): () => void {
  _lostListeners.add(l)
  return () => _lostListeners.delete(l)
}

export function notifyServerLost(): void {
  _lostListeners.forEach(l => l())
}

export function is401(err: unknown): boolean {
  return String((err as any)?.message ?? err).startsWith('401:')
}

// isNetworkError classifies Go http-client failures surfaced through
// Wails bindings (no status prefix, just the transport error text).
export function isNetworkError(err: unknown): boolean {
  const s = String((err as any)?.message ?? err)
  return /connection refused|dial tcp|no such host|context deadline|connection reset|EOF$/i.test(s)
}
