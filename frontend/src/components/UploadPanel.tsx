import { FormEvent, useState } from 'react'
import { uploadMaterial } from '../api'
import type { Material } from '../types'

interface Props {
  onUploaded: (m: Material) => void
}

export default function UploadPanel({ onUploaded }: Props) {
  const [file, setFile] = useState<File | null>(null)
  const [message, setMessage] = useState<{ kind: 'error' | 'success' | 'info'; text: string } | null>(null)
  const [uploading, setUploading] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    if (!file) {
      setMessage({ kind: 'info', text: '请先选择 txt、md 或 pdf 文件' })
      return
    }
    setUploading(true)
    setMessage(null)
    try {
      const m = await uploadMaterial(file)
      setMessage({ kind: 'success', text: `已上传：${m.name}` })
      setFile(null)
      onUploaded(m)
    } catch (err) {
      setMessage({ kind: 'error', text: err instanceof Error ? err.message : '上传失败' })
    } finally {
      setUploading(false)
    }
  }

  return (
    <section className="card upload-card">
      <h2 className="card-title">上传资料</h2>
      <form onSubmit={onSubmit}>
        <div className="file-pick">
          <input
            type="file"
            accept=".txt,.md,.pdf"
            aria-label="选择要上传的文件"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
          <p className="file-hint">仅支持 txt / md / pdf，单文件不超过 32MB</p>
        </div>
        <button className="btn btn-primary btn-block" type="submit" disabled={uploading}>
          {uploading ? '上传中…' : '上传到本班知识库'}
        </button>
      </form>
      {message && <div className={`alert alert-${message.kind}`}>{message.text}</div>}
    </section>
  )
}
