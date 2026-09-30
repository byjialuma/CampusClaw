import { FormEvent, useState } from 'react'

interface Props {
  loading: boolean
  onAsk: (question: string) => void
}

// 知识问答卡片：学生/教师共用，不含任何班级选择控件——
// 班级范围完全由服务端会话决定。
export default function QAPanel({ loading, onAsk }: Props) {
  const [question, setQuestion] = useState('')

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const q = question.trim()
    if (!q || loading) return
    onAsk(q)
  }

  return (
    <section className="card search-card">
      <h2 className="card-title">知识问答</h2>
      <form className="search-form" onSubmit={onSubmit}>
        <input
          className="search-input"
          type="text"
          maxLength={500}
          placeholder="输入问题，基于本班材料生成回答"
          aria-label="知识问答问题"
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
        />
        <button className="btn btn-primary" type="submit" disabled={loading || !question.trim()}>
          {loading ? '思考中…' : '提问'}
        </button>
      </form>
      <p className="file-hint">仅基于你所在班级的材料回答，结果附带可溯源的引用</p>
    </section>
  )
}
