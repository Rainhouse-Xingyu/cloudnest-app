# 2026-04-08 File 模块迁移记录（下沉到 storage 接口层 Phase 3B）

## 1. 目标与范围

- 目标：继续推进 Phase 3，将文件流转事务型写操作下沉到 storage 层。
- 本次范围（Phase 3B）：
  - `MoveFiles`
  - `CopyFiles`
  - `RestoreFromRecycleBin`
  - `PermanentDeleteFromRecycleBin`

## 2. 本次改动文件

- 修改：`internal/service/contract/errors.go`
- 修改：`internal/service/contract/file_repo.go`
- 修改：`internal/storage/mysql/file_repo.go`
- 修改：`internal/service/file_service.go`

## 3. 契约层扩展

### 3.1 错误语义补充

在 `contract/errors.go` 新增：

- `ErrTargetNotFound`
- `ErrTargetNotFolder`
- `ErrCannotMoveToItself`
- `ErrCannotMoveToDesc`
- `ErrRecycleItemNotFound`

### 3.2 Repository 接口补充

在 `FileRepository` 新增：

- `MoveFiles`
- `CopyFiles`
- `RestoreFromRecycleBin`
- `PermanentDeleteFromRecycleBin`

## 4. MySQL 实现

`internal/storage/mysql/file_repo.go` 已实现上述方法，并补充 repo 内部辅助：

- `ensureUniqueFilenameForFileRepo`
- `appendNameSuffixForFileRepo`
- `isDescendantFolderForFileRepo`
- `collectDescendantIDsForFileRepo`
- `copyNodeForFileRepo`

实现特性：

- 保持事务边界在 storage 层
- 保持目录循环移动防护
- 保持重名避让策略
- 保持彻底删除前 Live Photo 关系清理

## 5. Service 层接入

`FileService` 相关方法已优先走 repo，并做 contract 错误到 service 错误映射：

- `MoveFiles`
- `CopyFiles`
- `RestoreFromRecycleBin`
- `PermanentDeleteFromRecycleBin`

同时保留 fallback 路径，便于平滑迁移和风险控制。

## 6. 功能不变性说明

以下行为保持不变：

- 路由、方法、响应结构不变
- 错误语义不变（权限不足、目标目录非法、循环移动、回收站不存在等）
- 审计日志行为不变（move/copy/restore/permanent_delete）

## 7. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 8. 阶段总结与下一步

- Phase 3A + 3B 完成后，File 模块主要写路径已具备 repo 下沉能力。
- 下一步建议（Phase 3C）：
  1. 上传链路持久化访问（task/chunk/storage/file）继续下沉
  2. 分享管理与操作日志查询写入进一步下沉
  3. 清理 service 中已不再需要的 fallback 与重复 helper（待稳定期后）
