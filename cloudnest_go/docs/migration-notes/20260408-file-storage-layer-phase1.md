# 2026-04-08 File 模块迁移记录（下沉到 storage 接口层 Phase 1）

## 1. 目标与范围

- 目标：将 File 模块从 `service` 继续下沉到 `storage` 接口层，形成更清晰的 `handler -> service -> storage`。
- 本阶段范围（增量可编译落地）：
  - `SearchFiles` 的数据访问下沉
  - 公共分享有效性解析的查询下沉
  - 批量下载条目收集的查询下沉

## 2. 本次改动文件

- 新增：`internal/service/contract/file_repo.go`
- 新增：`internal/storage/mysql/file_repo.go`
- 修改：`internal/service/contract/errors.go`
- 修改：`internal/service/file_service.go`
- 修改：`internal/app/container.go`

## 3. 设计说明

### 3.1 新增 storage 契约

- `FileRepository`（定义于 `internal/service/contract/file_repo.go`）
  - `SearchFiles`
  - `ResolveValidShare`
  - `CollectBatchDownloadEntries`

### 3.2 新增 MySQL 实现

- `FileRepo`（定义于 `internal/storage/mysql/file_repo.go`）
  - 承接上述三类查询逻辑
  - 保持原有 SQL 过滤与排序行为
  - 共享错误语义（如 `share expired`）

### 3.3 Service 注入与回退策略

- `FileService` 新增 `fileRepo` 依赖。
- 构造函数升级为：`NewFileService(db, fileRepo)`。
- 已下沉方法优先走 `fileRepo`；保留 DB fallback 实现，便于平滑迁移和回滚。

### 3.4 容器接线

- `Container` 注入新增 `FileRepo`，并传入 `FileService`。

## 4. 功能不变性说明

以下行为保持不变：

- 检索接口过滤逻辑、分页规则与返回结构不变
- 公共分享过期/不存在语义不变
- 批量下载“跳过不可用条目，仅打包可用项”行为不变

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 下一阶段建议（Phase 2）

继续将 FileService 其余高频 DB 访问下沉到 storage 接口层：

1. 文件树与基础操作：List/CreateFolder/Rename/Delete
2. 流转操作：Move/Copy/RecycleBin/Restore/PermanentDelete
3. 上传链路：Init/Chunk/Complete/Progress
4. 下载与媒体访问：可访问文件与 storage 解析统一走 repo

这样可逐步减少 `FileService` 对 `gorm.DB` 的直接依赖，最终收敛为“service 编排 + storage 访问”。
