import { useEffect, useState } from 'react'
import { onLoading } from '../lib/api'

// Thin indeterminate progress bar pinned to the top of the viewport.
// Shown whenever at least one api.req() call is in flight. We hold it
// visible for a brief tail after the last request ends so very fast
// responses (<100 ms) still get a short blink instead of a flicker.
export default function LoadingBar() {
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    let hideTimer: number | undefined
    const off = onLoading((n) => {
      if (n > 0) {
        if (hideTimer) { clearTimeout(hideTimer); hideTimer = undefined }
        setVisible(true)
      } else {
        hideTimer = window.setTimeout(() => setVisible(false), 150)
      }
    })
    return () => {
      off()
      if (hideTimer) clearTimeout(hideTimer)
    }
  }, [])

  if (!visible) return null
  return (
    <div className="loading-bar" aria-hidden>
      <div className="loading-bar-inner" />
    </div>
  )
}
