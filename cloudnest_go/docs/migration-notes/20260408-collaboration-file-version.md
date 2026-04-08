# 2026-04-08 Collaboration 分功能迁移记录（文件版本）

## 1. 迁移范围

- 模块：Collaboration
- 功能：文件版本创建与历史查询
- 涉及接口：
  - `POST /api/files/:id/versions`
  - `GET /api/files/:id/versions`

## 2. 本次改动文件

- 修改：`internal/service/collaboration_service.go`
- 修改：`internal/handler/collaboration_handler.go`
- 修改：`internal/app/container.go`

## 3. 功能不变性声明

本次迁移保持以下不变：

- 路由与方法不变
- 返回结构不变
- HTTP 状态码不变
- 错误文案不变（如 `storage_id and base_version are required`、`file or storage not found`、`folder does not support versioning`、`base_version is outdated`）
- 版本冲突、文件夹禁用版本、锁更新策略保持原行为
- 操作日志动作名与 detail 结构不变

## 4. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 本次迁移内容：
  - `CreateFileVersion` 事务逻辑下沉到 `CollaborationService.CreateFileVersion`
  - `ListFileVersions` 查询逻辑下沉到 `CollaborationService.ListFileVersions`
  - `CanAccessFile` 权限判断统一走 service
- 迁移后 `CollaborationHandler` 不再直接依赖 DB。

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 风险与后续

### 当前风险

- 目前 service 仍直接依赖 `gorm.DB`，后续可继续向 storage/repository 层下沉。

### 模块状态

- `Collaboration` 模块在 handler -> service 层面的分功能迁移已全部完成（workspace、成员管理、文件协作权限、文件版本）。
- 下一阶段可进入 storage 层解耦（repository 接口化）。
