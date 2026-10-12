import type { Locator, Material, Me } from '../types'
import MaterialList from './MaterialList'
import Preview from './Preview'
import UploadPanel from './UploadPanel'

interface Props {
  me: Me
  materials: Material[]
  selectedId: number | null
  loading: boolean
  selected: Material | null
  locatorHint: Locator | null
  downloadError: string
  onView: (m: Material) => void
  onDownload: (m: Material) => void
  onUploaded: (m: Material) => void
  onClearHint: () => void
}

// MaterialView 是「本班资料」标签页：左侧 300px 列表 + 右侧预览自适应。
// 教师可见上传入口；学生通过 canUpload 控制隐藏。
export default function MaterialView({
  me,
  materials,
  selectedId,
  loading,
  selected,
  locatorHint,
  downloadError,
  onView,
  onDownload,
  onUploaded,
  onClearHint,
}: Props) {
  const canUpload = me.role === 'teacher'
  return (
    <div className="material-view">
      <div className="material-view-left">
        {canUpload && <UploadPanel onUploaded={onUploaded} />}
        {downloadError && canUpload && <div className="alert alert-error">{downloadError}</div>}
        <MaterialList
          materials={materials}
          selectedId={selectedId}
          loading={loading}
          onView={onView}
          onDownload={onDownload}
        />
      </div>
      <div className="material-view-right">
        <Preview material={selected} locatorHint={locatorHint} onClearHint={onClearHint} />
      </div>
    </div>
  )
}
