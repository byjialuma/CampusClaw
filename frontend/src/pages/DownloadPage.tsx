import { useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import { downloadByTicket } from '../api'

export default function DownloadPage() {
  const [params] = useSearchParams()
  const started = useRef(false)

  useEffect(() => {
    // 严格只执行一次；刷新会因票据已消费而被服务端拒绝。
    if (started.current) return
    started.current = true

    const ticket = params.get('ticket')
    if (!ticket) {
      window.location.replace('/login')
      return
    }

    let objectUrl = ''
    ;(async () => {
      const result = await downloadByTicket(ticket)
      if (!result.ok) {
        // 票据已使用（刷新）、过期或伪造：一律回登录页。
        window.location.replace('/login')
        return
      }
      objectUrl = URL.createObjectURL(result.blob)
      const a = document.createElement('a')
      a.href = objectUrl
      a.download = result.fileName
      document.body.appendChild(a)
      a.click()
      a.remove()
      setTimeout(() => URL.revokeObjectURL(objectUrl), 10_000)
    })()
  }, [params])

  return (
    <div className="download-page">
      <div className="download-card">
        <span className="brand-mark">CC</span>
        <h1>正在准备下载…</h1>
        <p>若浏览器未开始保存，请返回材料列表重新点击下载。</p>
        <p className="muted">本页面不可刷新复用；凭证失效后将自动跳转登录页。</p>
      </div>
    </div>
  )
}
