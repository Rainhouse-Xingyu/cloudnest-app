# 2026-04-08 File 模块迁移记录（下沉到 storage 接口层 Phase 2）

## 1. 目标与范围

- 目标：继续减少 `FileService` 对 `gorm.DB` 直接访问，推动 `service -> storage` 解耦。
- 本阶段范围（增量可编译）：
  - 文件列表读取能力下沉
  - 回收站读取能力下沉
  - 文件访问判定能力下沉
  - 文件/存储基础读取能力下沉

## 2. 本次改动文件

- 修改：`internal/service/contract/file_repo.go`
- 修改：`internal/storage/mysql/file_repo.go`
- 修改：`internal/service/file_service.go`

## 3. 新增/扩展的 storage 接口能力

在 `FileRepository` 中新增：

- `ListFiles`
- `ListRecycleBin`
- `CanAccessFile`
- `GetActiveFileByID`
- `GetStorageByID`

## 4. Service 层接入情况

`FileService` 现已优先走 `fileRepo`（保留 fallback）：

- `ListFiles`
- `ListRecycleBin`
- `GetAccessibleFile`
- `GetAccessibleFileAndStorage`
- `canAccessFile`

说明：保留 fallback 方案用于平滑迁移与风险控制，不影响现有接口行为。

## 5. 功能不变性说明

以下保持不变：

- 文件列表排序与可见性规则
- 回收站列表返回结构
- 文件访问权限判定语义
- 文件/存储读取失败语义（not found / no access）

## 6. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 7. 下一阶段建议（Phase 3）

继续下沉写操作与事务型流程：

1. 文件树基础写操作：CreateFolder / Rename / Delete
2. 文件流转：Move / Copy / Restore / PermanentDelete
3. 上传链路相关写入：Init / Chunk / Complete / Progress 的持久化访问

目标是让 `FileService` 逐步收敛为“业务编排层”，把持久化细节统一封装到 `storage`。
