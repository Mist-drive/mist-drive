type Listener = () => void
const _listeners = new Set<Listener>()

export function onSessionExpired(l: Listener): () => void {
  _listeners.add(l)
  return () => _listeners.delete(l)
}

export function notifySessionExpired(): void {
  _listeners.forEach(l => l())
}

export function is401(err: unknown): boolean {
  return String((err as any)?.message ?? err).startsWith('401:')
}
