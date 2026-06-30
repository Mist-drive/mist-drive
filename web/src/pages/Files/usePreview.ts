import { useState } from 'react'
import { api, type PreviewResult } from '../../lib/api'

export function usePreview() {
  const [previewKey, setPreviewKey] = useState<string | null>(null)
  const [previewResult, setPreviewResult] = useState<PreviewResult | null>(null)
  const [previewLoading, setPreviewLoading] = useState(false)

  const closePreview = () => {
    if (previewResult?.type === 'image' && previewResult.content?.startsWith('blob:')) {
      URL.revokeObjectURL(previewResult.content)
    }
    setPreviewKey(null)
    setPreviewResult(null)
  }

  const onPreview = async (key: string) => {
    closePreview()
    setPreviewKey(key)
    setPreviewLoading(true)
    try {
      setPreviewResult(await api.previewFile(key))
    } catch {
      setPreviewResult({ type: 'binary' })
    } finally {
      setPreviewLoading(false)
    }
  }

  return { previewKey, previewResult, previewLoading, onPreview, closePreview }
}
