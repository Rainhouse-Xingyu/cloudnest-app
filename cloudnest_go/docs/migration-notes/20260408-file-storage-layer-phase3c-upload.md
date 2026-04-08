# File 模块 storage 下沉 Phase 3C（上传链路）

## 背景

在 Phase 3A/3B 已完成 File 基础写路径与流转事务下沉后，本阶段继续推进上传链路，将 `UploadTask / UploadChunk / 完成上传写入` 的持久化访问从 `FileService` 下沉到 `FileRepository`（MySQL 实现），保持 handler 与对外接口行为不变。

## 本次改动范围

### 1. 扩展 FileRepository 上传链路契约

文件：`internal/service/contract/file_repo.go`

新增接口：

- `GetUploadTaskByUploadID`
- `GetUploadTaskAndUploadedChunkIndexes`
- `CreateUploadTask`
- `UpsertUploadChunkAndRefreshProgress`
- `CountDoneUploadChunks`
- `CompleteUploadPersist`

### 2. MySQL FileRepo 实现上传链路持久化

文件：`internal/storage/mysql/file_repo.go`

新增实现：

- 上传任务读取（按 upload_id）
- 续传场景读取已上传 chunk 索引
- 上传任务创建
- chunk upsert + 已上传计数刷新（事务）
- 已完成 chunk 计数
- 完成上传事务落库（storage 去重 + 文件名去重 + file 创建 + task 完成 + chunk 清理）

新增内部 helper：

- `ensureStorageForFileRepo`

### 3. FileService 接入 repo 上传链路能力

文件：`internal/service/file_service.go`

以下方法优先走 `fileRepo`，保留 DB fallback：

- `InitUpload`
- `ValidateUploadChunk`
- `MarkChunkUploaded`
- `PrepareCompleteUpload`
- `CompleteUpload`
- `UploadProgress`

## 行为兼容性

- 路由与请求/响应结构未改动。
- 错误语义保持一致（如 `ErrUploadTaskNotFound`、`ErrChunksNotComplete`、`ErrUploadTaskCompleted`）。
- 上传完成后的操作日志写入行为保持不变。

## 验证

执行：

```bash
gofmt -w internal/service/contract/file_repo.go internal/storage/mysql/file_repo.go internal/service/file_service.go
go build ./...
```

结果：构建通过（无输出）。

## 下一步建议

继续 Phase 3C 的第二段：

1. 分享链路剩余写操作下沉（创建/撤销/访问计数）
2. 文件操作日志查询与导出下沉（List/Export）
3. 评估并清理对应 service fallback（在接口覆盖率满足后逐步收敛）
