# 2026-04-08 File 模块分功能迁移记录（文件树与基础操作）

## 1. 迁移范围

- 模块：File
- 子功能：文件树与基础操作
- 覆盖接口：
  - `GET /api/files`
  - `POST /api/folders`
  - `PUT /api/files/:id/rename`
  - `DELETE /api/files/:id`

## 2. 本次改动文件

- 修改：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 业务逻辑下沉到 `FileService`：
  - 目录读取权限校验与文件树查询
  - 创建目录时父级校验、重名校验
  - 重命名时写权限与同级重名校验
  - 删除时空目录校验、软删除事务、Live Photo 关系清理
  - 对应操作日志写入
- `FileHandler` 仅负责：参数绑定、输入基础校验、业务错误到 HTTP 状态码映射。

## 4. 功能不变性声明

以下行为保持不变：

- 路由与 HTTP 方法不变
- 响应结构不变（如 `items`、`item`、`message`）
- 核心错误语义不变：
  - `invalid parent_id`
  - `permission denied`
  - `parent not found`
  - `parent is not folder`
  - `name already exists in this folder`
  - `file not found`
  - `folder is not empty`
  - `delete failed` / `rename failed` / `create folder failed`

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### File 模块已完成

- 第一段：分享管理 + 操作日志
- 第二段：文件树与基础操作（本次）

### File 模块待迁移

- 上传链路：InitUpload/UploadChunk/CompleteUpload/UploadProgress
- 下载与媒体：Download/DownloadFolderZip/BatchDownload/StreamVideo/Thumbnail/Preview
- 文件流转：Move/Copy/RecycleBin/Restore/PermanentDelete
- 公共分享访问：PublicShareInfo/PublicShareDownload

建议下一段迁移“上传链路”，可保持与存储记录、断点续传、分片校验逻辑的完整闭环。
