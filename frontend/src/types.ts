export interface Me {
  id: number
  username: string
  displayName: string
  role: 'teacher' | 'student'
  classId: number
  className: string
}

export interface Material {
  id: number
  name: string
  fileType: 'txt' | 'md' | 'pdf'
  sizeBytes: number
  uploaderName: string
  createdAt: string
}
