import { useEffect, useState } from 'react'
import { onLoading } from '@shared/lib/loading'

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
