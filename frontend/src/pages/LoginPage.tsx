import { FormEvent, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { login } from '../api'

export default function LoginPage() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const next = params.get('next') || '/'
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      await login(username.trim(), password)
      navigate(next, { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : '登录失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className="login-page">
      <form className="login-card" onSubmit={onSubmit}>
        <div className="brand">
          <span className="brand-mark">CC</span>
          <div>
            <h1>CampusClaw 班级知识库</h1>
            <p>教师 / 学生统一登录</p>
          </div>
        </div>
        <label className="field">
          <span>账号</span>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="请输入用户名"
            autoComplete="username"
            required
          />
        </label>
        <label className="field">
          <span>密码</span>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="请输入密码"
            autoComplete="current-password"
            required
          />
        </label>
        {error && <div className="alert alert-error">{error}</div>}
        <button className="btn btn-primary btn-block" type="submit" disabled={submitting}>
          {submitting ? '登录中…' : '登 录'}
        </button>
      </form>
    </div>
  )
}
