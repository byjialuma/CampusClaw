import { FormEvent, useState } from 'react'

interface Props {
  searching: boolean
  onSearch: (query: string) => void
}

// 知识库检索卡片：学生/教师共用，不含任何班级选择控件——
// 班级范围完全由服务端会话决定。
export default function SearchPanel({ searching, onSearch }: Props) {
  const [query, setQuery] = useState('')

  function onSubmit(e: FormEvent) {
    e.preventDefault()
    const q = query.trim()
    if (!q || searching) return
    onSearch(q)
  }

  return (
    <section className="card search-card">
      <h2 className="card-title">知识库检索</h2>
      <form className="search-form" onSubmit={onSubmit}>
        <input
          className="search-input"
          type="text"
          maxLength={500}
          placeholder="输入知识点或关键词，检索本班讲义"
          aria-label="知识库检索关键词"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <button className="btn btn-primary" type="submit" disabled={searching || !query.trim()}>
          {searching ? '检索中…' : '检索'}
        </button>
      </form>
      <p className="file-hint">仅检索你所在班级的材料，结果可溯源到页码 / 章节 / 行号</p>
    </section>
  )
}
