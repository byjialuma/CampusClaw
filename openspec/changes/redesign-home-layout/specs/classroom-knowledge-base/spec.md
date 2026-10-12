## ADDED Requirements

### Requirement: 教师可查看本班学生名单

系统 MUST 提供教师查看本班学生名单的能力：教师调用 `GET /api/class/students` 时，系统 MUST 返回该教师所属班级的全部学生（id、用户名、显示名、创建时间），学生 MUST NOT 能调用此接口（返回 HTTP 403）。班级标识 MUST 来自 JWT claims，MUST NOT 读取请求参数中的班级标识。

#### Scenario: 教师查看本班学生名单

- **WHEN** 教师调用 `GET /api/class/students`
- **THEN** 系统返回该教师所属班级的全部学生列表，每条含 id、username、displayName、createdAt

#### Scenario: 学生无法查看学生名单

- **WHEN** 学生调用 `GET /api/class/students`
- **THEN** 系统返回 HTTP 403，不返回任何学生数据

#### Scenario: 未登录调用被拒绝

- **WHEN** 未携带有效 JWT access token 调用 `GET /api/class/students`
- **THEN** 系统返回 HTTP 401，不返回任何数据
