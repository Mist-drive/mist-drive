let _inflight = 0
const _listeners = new Set<(n: number) => void>()

function notify() {
  _listeners.forEach((l) => l(_inflight))
}

export function startLoading() {
  _inflight++
  notify()
}

export function endLoading() {
  if (_inflight > 0) _inflight--
  notify()
}

export function onLoading(l: (n: number) => void): () => void {
  _listeners.add(l)
  l(_inflight)
  return () => { _listeners.delete(l) }
}
