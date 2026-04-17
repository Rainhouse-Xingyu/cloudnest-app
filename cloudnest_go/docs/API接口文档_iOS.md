# CloudNest API 接口文档（iOS 对接版）

更新时间：2026-04-09

## 1. 基础信息
- 服务默认端口：8080
- 建议基础地址：
  - 本地开发：http://127.0.0.1:8080
  - 接口前缀：/api
- 完整示例：
  - 登录：http://127.0.0.1:8080/api/auth/login

## 2. 鉴权规则
- 除以下接口外，其他 /api 接口都需要 Bearer Token：
  - POST /api/auth/login
  - GET /api/public/shares/:code
  - GET /api/public/shares/:code/download
- 请求头：
  - Authorization: Bearer <token>
- 未登录或 token 无效时，典型返回：
  - 401 + {"error": "missing bearer token"}
  - 401 + {"error": "invalid token"}
  - 401 + {"error": "token expired or logged out"}
- 权限不足时，典型返回：
  - 403 + {"error": "permission denied"}
  - 管理员接口可能返回 403 + {"error": "admin only"}

## 3. 统一响应约定
- 成功一般返回 200，主体常见为：
  - {"item": {...}}
  - {"items": [...]}
  - {"message": "..."}
  - 分页接口：
    - {"items": [...], "pagination": {"page": 1, "limit": 20, "total": 100}}
- 失败统一：
  - {"error": "错误描述"}

## 4. 接口总览

### 4.1 认证与会话
- POST /api/auth/login
- POST /api/auth/logout
- GET /api/auth/me

### 4.2 公共分享（免登录）
- GET /api/public/shares/:code
- GET /api/public/shares/:code/download

### 4.3 文件系统
- GET /api/files
- POST /api/files/folders
- PUT /api/files/:id/rename
- DELETE /api/files/:id
- POST /api/files/upload/init
- POST /api/files/upload/chunk
- POST /api/files/upload/complete
- GET /api/files/upload/progress
- GET /api/files/search
- GET /api/files/:id/download
- GET /api/files/:id/download-zip
- POST /api/files/batch-download
- POST /api/files/batch-delete
- GET /api/files/:id/stream
- GET /api/files/:id/thumbnail
- POST /api/files/move
- POST /api/files/copy
- GET /api/files/recycle-bin
- POST /api/files/:id/restore
- DELETE /api/files/:id/permanent
- GET /api/files/:id/preview

### 4.4 分享与日志
- POST /api/files/:id/share
- GET /api/shares
- DELETE /api/shares/:code
- GET /api/operation-logs
- GET /api/operation-logs/export

### 4.5 协作（workspace + 文件协作）
- POST /api/workspaces
- GET /api/workspaces
- POST /api/workspaces/:id/members
- GET /api/workspaces/:id/members
- PUT /api/workspaces/:id/members/:user_id
- DELETE /api/workspaces/:id/members/:user_id
- POST /api/files/:id/collaborators
- GET /api/files/:id/collaborators
- DELETE /api/files/:id/collaborators/:user_id
- POST /api/files/:id/versions
- GET /api/files/:id/versions

### 4.6 系统设置
- GET /api/settings
- PUT /api/settings
- GET /api/settings/permissions
- GET /api/settings/rate-window
- PUT /api/settings/rate-window

### 4.6.1 存储容量
- GET /api/storage/overview

### 4.7 FRP 设备与映射
- POST /api/frp/devices
- GET /api/frp/devices
- GET /api/frp/devices/:id
- PUT /api/frp/devices/:id
- DELETE /api/frp/devices/:id
- POST /api/frp/mappings
- GET /api/frp/devices/:id/mappings
- PUT /api/frp/mappings/:id
- DELETE /api/frp/mappings/:id
- POST /api/frp/configs/preview
- POST /api/frp/configs/save
- GET /api/frp/devices/:id/configs
- POST /api/frp/configs/:id/rollback
- POST /api/frp/devices/:id/deploy-config
- POST /api/frp/devices/:id/reload
- POST /api/frp/devices/:id/status/report
- GET /api/frp/devices/:id/status
- GET /api/frp/devices/:id/commands/next
- POST /api/frp/commands/:command_id/result
- GET /api/frp/devices/:id/commands

### 4.8 Live Photo
- POST /api/live-photos/bind
- POST /api/live-photos/auto-bind
- DELETE /api/live-photos/:id
- GET /api/live-photos/:id
- GET /api/live-photos
- GET /api/files/:id/live-photo

### 4.9 AI 能力
- POST /api/ai/ocr
- GET /api/ai/search
- POST /api/ai/search/nl
- POST /api/ai/summarize
- POST /api/ai/assistant/ask
- POST /api/ai/assistant/settings

### 4.10 管理员接口
- POST /api/admin/users
- PUT /api/admin/users/:id/permissions

## 5. 核心请求体定义（iOS 常用）

### 5.1 登录
接口：POST /api/auth/login
请求：
- username: string 必填
- password: string 必填
返回：
- token: string
- token_type: Bearer
- expires_at: 时间
- user: { id, username, email, is_admin }

### 5.2 文件目录与重命名
1) POST /api/files/folders
- parent_id: int64
- name: string 必填

2) PUT /api/files/:id/rename
- name: string 必填

### 5.3 分片上传
1) POST /api/files/upload/init
- upload_id: string 可选（续传时传）
- parent_id: int64
- filename: string 必填
- total_size: int64 必填
- chunk_size: int 可选
- total_chunks: int 可选
返回：
- upload_id
- uploaded_chunks
- uploaded_count
- status

2) POST /api/files/upload/chunk（multipart/form-data）
- upload_id: string
- chunk_index: int
- chunk_md5: string
- chunk: 二进制分片

3) POST /api/files/upload/complete
- upload_id: string 必填
- parent_id: int64

4) GET /api/files/upload/progress?upload_id=...

### 5.4 文件移动/复制/批处理
1) POST /api/files/move
2) POST /api/files/copy
请求体一致：
- file_ids: int64[] 必填
- target_parent_id: int64

3) POST /api/files/batch-delete
- file_ids: int64[] 必填

4) POST /api/files/batch-download
- file_ids: int64[] 必填

### 5.5 分享
1) POST /api/files/:id/share
- expire_hours: int 可选，0 表示永不过期

2) GET /api/public/shares/:code（免登录）
3) GET /api/public/shares/:code/download（免登录）

### 5.6 协作 workspace
1) POST /api/workspaces
- name: string 必填

2) POST /api/workspaces/:id/members
- user_id: int64 可选
- username: string 可选
- role: string 必填（admin/member）

3) PUT /api/workspaces/:id/members/:user_id
- role: string 必填（admin/member）

### 5.7 文件协作权限与版本
1) POST /api/files/:id/collaborators
- user_id: int64 可选
- username: string 可选
- permission: string 必填（read/write）

2) POST /api/files/:id/versions
- storage_id: int64 必填
- comment: string 可选
- base_version: int 必填
- size: int64 可选
- file_type: string 可选

### 5.8 设置
1) PUT /api/settings（管理员）
- items: 数组，元素字段：
  - setting_key: string 必填
  - setting_value: string 必填
  - description: string 可选

可用于容量卡片的推荐配置键：
- storage_quota_bytes：全局容量上限（单位 bytes）
- storage_default_quota_bytes：全局默认容量上限（单位 bytes，作为兜底）
- storage_quota_bytes_user_{uid}：用户级容量上限（单位 bytes，优先级最高）

2) PUT /api/settings/rate-window
- enabled: bool 可选
- start: string，可选，格式 HH:MM
- end: string，可选，格式 HH:MM

3) GET /api/storage/overview
- 返回：
  - item.used_bytes: int64，当前用户已使用容量
  - item.total_bytes: int64，容量上限（未配置时为 0）
  - item.remaining_bytes: int64，剩余容量（未配置时为 0）
  - item.usage_percent: float64，使用率百分比（0~100）
  - item.has_quota: bool，是否已配置容量上限
  - item.quota_source: string，配额来源（如 storage_quota_bytes_user_xxx / storage_quota_bytes / storage_default_quota_bytes / none）

### 5.9 FRP
1) POST /api/frp/devices
- device_name: string 必填
- device_ip: string 可选

2) PUT /api/frp/devices/:id
- device_name: string 可选
- device_ip: string 可选
- status: int8 可选

3) POST /api/frp/mappings
- device_id: int64 必填
- local_port: int 必填
- remote_port: int 可选
- domain: string 可选
- status: int8 可选

4) PUT /api/frp/mappings/:id
- local_port: int 可选
- remote_port: int 可选
- domain: string 可选
- status: int8 可选

5) POST /api/frp/configs/preview 与 /api/frp/configs/save
- device_id: int64 必填
- server_addr: string 必填
- server_port: int 可选
- token: string 可选

6) POST /api/frp/devices/:id/status/report
- online: bool
- last_heartbeat: string
- last_error: string
- failure_reason: string
- client_version: string
- current_config_id: int64 可选

7) POST /api/frp/commands/:command_id/result
- success: bool
- result_message: string
- failure_reason: string
- executed_at: string

### 5.10 Live Photo
1) POST /api/live-photos/bind
- image_file_id: int64 必填
- video_file_id: int64 必填

2) POST /api/live-photos/auto-bind
- parent_id: int64 可选
- file_ids: int64[] 可选
说明：parent_id 与 file_ids 至少传一个

3) GET /api/live-photos
查询参数：
- parent_id: int64 可选
- page: int 默认 1
- limit: int 默认 20，最大 100

### 5.11 AI
1) POST /api/ai/ocr
- file_id: int64 必填
- location: string 可选
- landmark: string 可选
- captured_at: string 可选
- tags: string 可选
- description: string 可选

2) GET /api/ai/search
查询参数：
- keyword, file_type, location, landmark, start_time, end_time
- tag_only: bool
- page: int
- limit: int

3) POST /api/ai/search/nl
- question: string 必填
- page: int 可选
- limit: int 可选

4) POST /api/ai/summarize
- file_id: int64 必填
- max_points: int 可选

5) POST /api/ai/assistant/ask
- question: string 必填

6) POST /api/ai/assistant/settings
- setting_key: string 必填
- setting_value: string 必填
- description: string 可选

### 5.12 管理员
1) POST /api/admin/users
- username: string 必填
- password: string 必填
- email: string 可选
- permissions: [{ method, path }]

2) PUT /api/admin/users/:id/permissions
- permissions: [{ method, path }] 必填

## 6. iOS 对接建议

### 6.1 Token 管理
- 登录后持久化 token（建议 Keychain）
- 所有受保护接口统一注入 Authorization 头
- 401 时触发登录态失效流程

### 6.2 上传流程建议
- 先调用 init 获取 upload_id
- 分片并发上传 chunk（建议 2 到 4 并发）
- 每片计算 MD5 后上传
- 完成后调用 complete
- 失败可调用 progress 做断点续传

### 6.3 下载与预览
- 普通下载：/files/:id/download
- 目录下载：/files/:id/download-zip
- 视频播放：/files/:id/stream（支持 Range）
- 缩略图：/files/:id/thumbnail?size=256
- 在线预览：/files/:id/preview

### 6.4 分页统一处理
- 读取 pagination.page、pagination.limit、pagination.total
- 当 items 为空且 page=1 时展示空状态

## 7. 常见错误码速查
- 400 参数错误
- 401 未登录或 token 失效
- 403 权限不足
- 404 资源不存在
- 409 冲突（如重名、重复绑定、端口占用）
- 500 服务内部错误

## 8. 后续可选增强
- 生成 OpenAPI 3.0（yaml/json）供 Swagger 与 iOS 自动生成模型
- 提供统一业务错误码字段（code + error）
- 增加接口版本号前缀（如 /api/v1）避免客户端升级冲突
