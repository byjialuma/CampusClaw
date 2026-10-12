import { useEffect, useState } from 'react'
import { fetchStudents } from '../api'
import type { StudentInfo } from '../types'

function formatTime(s: string): string {
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// StudentList 是教师「学生名单」标签页：拉取本班学生并表格化展示。
export default function StudentList() {
  const [items, setItems] = useState<StudentInfo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const rows = await fetchStudents()
        if (cancelled) return
        setItems(rows)
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : '加载学生名单失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <section className="card student-list-card">
      <h2 className="card-title">
        本班学生
        <span className="card-count">{items.length}</span>
      </h2>
      {loading ? (
        <p className="muted">加载中…</p>
      ) : error ? (
        <div className="alert alert-error">{error}</div>
      ) : items.length === 0 ? (
        <p className="muted">本班还没有学生。</p>
      ) : (
        <table className="student-table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>显示名</th>
              <th>加入时间</th>
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id}>
                <td className="student-username">{s.username}</td>
                <td>{s.displayName}</td>
                <td className="muted">{formatTime(s.createdAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  )
}
