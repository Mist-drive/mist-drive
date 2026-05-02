import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'

// A single reusable modal confirm dialog. Exposed via useConfirm(),
// which returns a promise-based function so callers can write:
//
//   if (!(await confirm({ title: 'Delete?', message: '...' }))) return
//
// Replacing window.confirm() with this gives us styling that matches
// the rest of the UI and keeps keyboard/focus behaviour sensible.

export type ConfirmOptions = {
  title: string
  message: string
  confirmText?: string
  cancelText?: string
  /** Render the confirm button in danger style. */
  danger?: boolean
}

type Pending = ConfirmOptions & { resolve: (v: boolean) => void }

const Ctx = createContext<((opts: ConfirmOptions) => Promise<boolean>) | null>(null)

export function useConfirm() {
  const fn = useContext(Ctx)
  if (!fn) throw new Error('useConfirm must be used inside <ConfirmProvider>')
  return fn
}

export function ConfirmProvider({ children }: { children: React.ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null)
  const confirmBtn = useRef<HTMLButtonElement>(null)

  const confirm = useCallback((opts: ConfirmOptions) => {
    return new Promise<boolean>((resolve) => {
      setPending({ ...opts, resolve })
    })
  }, [])

  const close = useCallback((answer: boolean) => {
    setPending((p) => {
      p?.resolve(answer)
      return null
    })
  }, [])

  // Autofocus confirm button + Escape-to-cancel + Enter-to-confirm.
  useEffect(() => {
    if (!pending) return
    confirmBtn.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close(false)
      else if (e.key === 'Enter') close(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [pending, close])

  return (
    <Ctx.Provider value={confirm}>
      {children}
      {pending && (
        <div
          className="modal-backdrop"
          onClick={(e) => {
            if (e.target === e.currentTarget) close(false)
          }}
        >
          <div
            className="modal"
            role="dialog"
            aria-modal="true"
            aria-labelledby="confirm-title"
          >
            <h3 id="confirm-title" className="modal-title">{pending.title}</h3>
            <p className="modal-message">{pending.message}</p>
            <div className="modal-actions">
              <button className="ghost" onClick={() => close(false)}>
                {pending.cancelText ?? 'Cancel'}
              </button>
              <button
                ref={confirmBtn}
                className={pending.danger ? 'danger' : ''}
                onClick={() => close(true)}
              >
                {pending.confirmText ?? 'Confirm'}
              </button>
            </div>
          </div>
        </div>
      )}
    </Ctx.Provider>
  )
}
