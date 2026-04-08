# File 模块 storage 下沉 Phase 3C（收敛补丁）

## 目标

在上传链路、分享与日志下沉完成后，继续做低风险收敛，减少 `FileService` 对 `s.db` 的直接依赖。

## 本次改动

文件：`internal/service/file_service.go`

1. `ResolvePublicShareDownload` 增加 repo 优先查询：
   - 当 `fileRepo` 可用时，使用 `GetStorageByID` 获取 storage。
   - 保留原 fallback（repo 为空时走原 DB 查询）。

2. 清理 `PrepareCompleteUpload` 的重复元数据校验：
   - 移除重复的 `task.TotalSize/ChunkSize/TotalChunks` 判空逻辑，避免重复分支。

## 兼容性

- 对外接口与返回结构不变。
- 错误语义保持一致：
  - `storage` 不存在仍映射为 `ErrStorageNotFound`
  - 物理文件缺失仍映射为 `ErrPhysicalFileNotFound`

## 验证

执行：

```bash
gofmt -w internal/service/file_service.go
go build ./...
```

结果：构建通过（无输出）。
