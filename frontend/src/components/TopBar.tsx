import type { Me } from '../types'
import { logout } from '../api'

interface Props {
  me: Me
}

export default function TopBar({ me }: Props) {
  async function onLogout() {
    await logout()
    window.location.replace('/login')
  }

  return (
    <header className="topbar">
      <div className="topbar-left">
        <span className="brand-mark brand-mark-sm">CC</span>
        <span className="class-name">{me.className}</span>
        <span className="dot" />
        <span className="user-name">{me.displayName}</span>
        <span className={`role-badge role-${me.role}`}>
          {me.role === 'teacher' ? '教师' : '学生'}
        </span>
      </div>
      <div className="topbar-right">
        <button className="btn btn-logout" type="button" onClick={onLogout}>
          退出登录
        </button>
      </div>
    </header>
  )
}
