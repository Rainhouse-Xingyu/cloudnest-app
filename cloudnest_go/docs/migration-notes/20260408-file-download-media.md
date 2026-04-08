# 2026-04-08 File 模块分功能迁移记录（下载与媒体）

## 1. 迁移范围

- 模块：File
- 子功能：下载与媒体访问能力下沉
- 本次覆盖接口：
  - `GET /api/files/:id/download`
  - `GET /api/folders/:id/download`
  - `GET /api/files/:id/stream`
  - `GET /api/files/:id/thumbnail`
  - `GET /api/files/:id/preview`

## 2. 本次改动文件

- 修改：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 本次优先下沉共享能力，而不是一次性搬迁全部输出逻辑：
  - 新增 `FileService.GetAccessibleFile`：统一文件可访问校验与文件记录读取
  - 新增 `FileService.GetAccessibleFileAndStorage`：统一存储记录解析与物理文件存在性校验
- `Download` 与 `DownloadFolderZip` 改为直接调用 service 获取数据。
- `StreamVideo/Thumbnail/Preview` 继续通过 handler 内部统一入口 `getAccessibleFileAndStorage`，该入口已改为转调 service，实现无感迁移。
- 文件流输出、Range 响应、缩略图生成、在线预览输出仍保留在 handler，避免行为变化。

## 4. 功能不变性声明

以下行为保持不变：

- 路由、HTTP 方法、请求参数与响应结构不变
- 错误语义保持一致：
  - `permission denied`
  - `file not found`
  - `folder not found`
  - `storage record not found`
  - `physical file not found`
  - `target is folder, use folder zip download`
  - `target is not folder`
- 下载限速、视频 Range 播放、缩略图缓存、预览分支逻辑不变

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### File 模块已完成

- 第一段：分享管理 + 操作日志
- 第二段：文件树与基础操作
- 第三段：上传链路
- 第四段：下载与媒体访问能力下沉（本次）

### File 模块待迁移

- 文件流转：Move/Copy/RecycleBin/Restore/PermanentDelete
- 公共分享访问：PublicShareInfo/PublicShareDownload
- 批量下载与检索等剩余复合逻辑的 service 化

建议下一段迁移“文件流转”，该部分包含目录层级约束和事务一致性，是 File 模块剩余核心复杂段。
