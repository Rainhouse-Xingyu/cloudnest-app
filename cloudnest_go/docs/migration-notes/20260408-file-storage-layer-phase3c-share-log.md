# File 模块 storage 下沉 Phase 3C（分享与日志）

## 背景

在 Phase 3C 第一段已完成上传链路下沉后，本阶段继续将分享管理与操作日志查询能力从 `FileService` 下沉至 `FileRepository`，进一步收敛 service 层的数据库访问。

## 本次改动

### 1. 扩展 FileRepository 契约

文件：`internal/service/contract/file_repo.go`

新增类型：

- `FileOperationLogQuery`

新增接口：

- `CreateShare`
- `ListShares`
- `RevokeShare`
- `ListOperationLogs`
- `ExportOperationLogs`

### 2. MySQL 仓储实现

文件：`internal/storage/mysql/file_repo.go`

新增实现：

- 分享创建、列表、撤销
- 操作日志列表查询（含 total）
- 操作日志导出查询

其中时间筛选沿用 `parseFlexibleTimeForFileRepo`，保持 `invalid start_time/end_time` 错误语义。

### 3. FileService 接入 repo 调用

文件：`internal/service/file_service.go`

以下方法已优先调用 `fileRepo`（保留 fallback）：

- `CreateShare`
- `ListShares`
- `RevokeShare`
- `ListOperationLogs`
- `ExportOperationLogs`

## 兼容性说明

- 路由与请求/响应结构未变。
- 分享创建/撤销的日志写入行为未变。
- 日志查询过滤字段与分页逻辑未变。

## 验证

执行：

```bash
gofmt -w internal/service/contract/file_repo.go internal/storage/mysql/file_repo.go internal/service/file_service.go
go build ./...
```

结果：构建通过（无输出）。

## 下一步建议

1. 评估 `resolveValidShare` 与下载路径中 remaining fallback 的收敛时机。
2. 继续清点 FileService 仍直接依赖 `s.db` 的路径，按风险分批下沉。
3. 在接口覆盖完整后，逐步移除已不再需要的 service 侧重复 helper。
