# 2026-04-08 File 模块迁移记录（下沉到 storage 接口层 Phase 3A）

## 1. 目标与范围

- 目标：继续推进 Phase 3，将写操作持久化细节从 `FileService` 下沉到 `storage`。
- 本次范围（Phase 3A）：
  - `CreateFolder`
  - `RenameFile`
  - `DeleteFile`

## 2. 本次改动文件

- 修改：`internal/service/contract/file_repo.go`
- 修改：`internal/storage/mysql/file_repo.go`
- 修改：`internal/service/file_service.go`

## 3. 新增 storage 接口能力

在 `FileRepository` 扩展写路径原语：

- `HasActiveNameConflict`
- `CreateFolderRecord`
- `UpdateFileName`
- `CountActiveChildren`
- `SoftDeleteFileWithLivePhotoCleanup`

## 4. Service 层接入情况

`FileService` 在以下方法中优先调用 repo（保留 fallback）：

- `CreateFolder`
  - 重名校验下沉
  - 目录记录创建下沉
- `RenameFile`
  - 同级重名校验下沉
  - 文件名更新下沉
- `DeleteFile`
  - 子节点数量统计下沉
  - 软删除 + Live Photo 关系清理事务下沉

## 5. 功能不变性说明

以下行为保持不变：

- 错误语义与状态映射不变（冲突/无权限/未找到等）
- 操作日志行为不变（create_folder/rename/delete）
- 删除前空目录约束不变

## 6. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 7. 下一步（Phase 3B）

继续下沉事务型流转写操作：

1. `RestoreFromRecycleBin`
2. `PermanentDeleteFromRecycleBin`
3. `MoveFiles`
4. `CopyFiles`

目标是把文件流转主要写路径也迁到 storage 层，进一步压缩 service 的 DB 直接访问范围。
