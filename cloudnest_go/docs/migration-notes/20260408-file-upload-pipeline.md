# 2026-04-08 File 模块分功能迁移记录（上传链路）

## 1. 迁移范围

- 模块：File
- 子功能：上传链路
- 覆盖接口：
  - `POST /api/uploads/init`
  - `POST /api/uploads/chunk`
  - `POST /api/uploads/complete`
  - `GET /api/uploads/progress`

## 2. 本次改动文件

- 修改：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 将上传任务生命周期核心逻辑下沉到 `FileService`：
  - 初始化上传任务（含分片大小默认值、续传 task 查询、权限校验）
  - 分片上传状态校验与分片记录落库
  - 合并前校验（任务状态、分片齐全、元数据完整）
  - 完成上传后 storage/file/task/chunk 持久化更新
  - 上传进度聚合（已上传、缺失分片、百分比）
- `FileHandler` 保留职责：
  - HTTP 参数绑定与响应
  - 分片文件落盘与 MD5 校验
  - 上传完成后的 AI 自动分析触发
  - 上传完成后的 Live Photo 自动绑定触发

## 4. 功能不变性声明

以下行为保持不变：

- 路由、HTTP 方法、请求参数与响应结构不变
- 关键错误语义保持一致：
  - `invalid upload parameters`
  - `upload task not found`
  - `permission denied`
  - `upload task already completed`
  - `chunk_index out of range`
  - `chunks not fully uploaded`
  - `upload task metadata incomplete`
  - `merge chunks failed`
  - `complete upload failed`

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### File 模块已完成

- 第一段：分享管理 + 操作日志
- 第二段：文件树与基础操作
- 第三段：上传链路（本次）

### File 模块待迁移

- 下载与媒体：Download/DownloadFolderZip/BatchDownload/StreamVideo/Thumbnail/Preview
- 文件流转：Move/Copy/RecycleBin/Restore/PermanentDelete
- 公共分享访问：PublicShareInfo/PublicShareDownload

建议下一段迁移“下载与媒体”，可与存储读取、限速输出、目录压缩等能力形成闭环。
