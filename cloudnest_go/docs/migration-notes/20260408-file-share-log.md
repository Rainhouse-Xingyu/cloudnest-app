# 2026-04-08 File 模块分功能迁移记录（分享管理 + 操作日志）

## 1. 迁移范围

- 模块：File
- 子功能：
  - 分享管理：创建分享、我的分享列表、撤销分享
  - 操作日志：分页查询、CSV 导出
- 涉及接口：
  - `POST /api/files/:id/share`
  - `GET /api/shares`
  - `DELETE /api/shares/:code`
  - `GET /api/operation-logs`
  - `GET /api/operation-logs/export`

## 2. 本次改动文件

- 新增：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`
- 修改：`internal/app/container.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 将上述子功能的核心业务逻辑下沉到 `FileService`：
  - 权限校验
  - 分享码生成与分享数据写入
  - 操作日志查询过滤与导出数据准备
  - 撤销分享与审计日志写入
- `FileHandler` 保留职责：参数绑定、状态码映射、响应结构输出。

## 4. 功能不变性声明

本次迁移保持以下不变：

- 路由与 HTTP 方法不变
- 返回结构不变（含 `pagination` 与导出 CSV 字段）
- 错误语义不变（如 `permission denied`、`share not found`、`invalid start_time`、`invalid end_time`）
- 分享默认过期策略与取值范围校验不变

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### 已完成（File 模块）

- 分享管理（Create/List/Revoke）迁移到 service
- 操作日志（List/Export）迁移到 service

### 待迁移（File 模块）

- 文件树与基础操作：List/CreateFolder/Rename/Delete
- 上传流程：InitUpload/UploadChunk/CompleteUpload/UploadProgress
- 下载与媒体：Download/DownloadFolderZip/BatchDownload/StreamVideo/Thumbnail/Preview
- 文件流转：Move/Copy/RecycleBin/Restore/PermanentDelete
- 公共分享访问：PublicShareInfo/PublicShareDownload

下一步建议继续迁移“文件树与基础操作（List/CreateFolder/Rename/Delete）”，作为 File 模块第二段。
