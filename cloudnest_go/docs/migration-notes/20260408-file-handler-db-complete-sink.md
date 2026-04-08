# FileHandler 直连 DB 下沉完成说明

## 目标

将 `internal/handler/file_handler.go` 中直连 DB 的能力全部下沉到 `FileService`，并继续向 `storage(FileRepository)` 下沉，保持接口行为不变。

## 本次变更

### 1. storage 契约与实现扩展

文件：`internal/service/contract/file_repo.go`

新增接口：

- `ListActiveChildrenByParent`
- `GetSystemSettingByKey`
- `FindLivePhotoAutoBindCandidate`
- `CreateLivePhotoAutoBindIfAbsent`

文件：`internal/storage/mysql/file_repo.go`

新增实现：

- 目录子节点查询（打包目录树）
- 系统设置按 key 查询
- Live Photo 自动绑定候选匹配
- Live Photo 自动绑定幂等创建 + 操作日志写入

### 2. FileService 聚合下沉能力

文件：`internal/service/file_service.go`

新增/增强能力：

- `GetStorageByID`
- `RecordOperation`
- `BatchDeleteFiles`
- `CollectFolderZipEntries`
- `TryAutoBindLivePhotoForFile`
- `GetSystemSettingInt64`
- `GetSystemSettingString`
- `IsInPeakWindow`
- `ResolvePeakWindowSetting`

并将系统设置读取优先走 `fileRepo.GetSystemSettingByKey`。

### 3. FileHandler 去除直连 DB

文件：`internal/handler/file_handler.go`

完成改造：

- AI 自动分析日志写入改为 `Svc.RecordOperation`
- `buildAutoAnalyzeResult` 的 storage 查询改为 `Svc.GetStorageByID`
- `BatchDelete` 改为调用 `Svc.BatchDeleteFiles`
- `BatchDownload` 的日志写入改为 `Svc.RecordOperation`
- 自动 Live Photo 绑定逻辑改为调用 `Svc.TryAutoBindLivePhotoForFile`
- `createFolderZip` 的目录树查询改为 `Svc.CollectFolderZipEntries`
- 高峰时段/限速配置读取改为调用 `Svc` 方法
- 删除/替换 handler 内多处 DB helper（保留无 DB 的通用工具）

## 验证

执行：

```bash
gofmt -w internal/service/contract/file_repo.go internal/storage/mysql/file_repo.go internal/service/file_service.go internal/handler/file_handler.go
go build ./...
```

结果：构建通过（无输出）。

## 兼容性

- 路由、返回结构、错误文案保持不变。
- 批量删除返回结构（`deleted_ids` + `failed`）保持不变。
- 公开分享下载与目录打包行为保持不变。
