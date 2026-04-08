# 2026-04-08 Collaboration 分功能迁移记录（Workspace 与成员管理）

## 1. 迁移范围

- 模块：Collaboration
- 功能：Workspace 创建、查询、成员管理
- 涉及接口：
  - `POST /api/workspaces`
  - `GET /api/workspaces`
  - `POST /api/workspaces/:id/members`
  - `GET /api/workspaces/:id/members`
  - `PUT /api/workspaces/:id/members/:user_id`
  - `DELETE /api/workspaces/:id/members/:user_id`

## 2. 本次改动文件

- 新增：`internal/service/collaboration_service.go`
- 修改：`internal/handler/collaboration_handler.go`
- 修改：`internal/app/container.go`

## 3. 功能不变性声明

本次迁移保持以下不变：

- 路由与方法不变
- 返回结构不变
- HTTP 状态码不变
- 错误文案不变（如 `permission denied`、`workspace member not found`、`cannot change owner role` 等）
- 角色规则不变（owner/admin/member）
- 操作日志动作名与明细结构不变

## 4. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 本次只迁移 Workspace 与成员管理相关逻辑到 service：
  - handler 负责参数解析、鉴权分支、错误映射、响应输出
  - service 负责事务、查询、成员 upsert/update/delete、日志落库
- `collaboration_handler` 中文件权限与版本管理逻辑暂未迁移，保持原实现。

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 风险与后续

### 当前风险

- `CollaborationHandler` 仍同时持有 `DB` 与 `Svc`，属于过渡态；
- 文件权限与版本功能仍在 handler 内，后续需继续按子功能迁移。

### 下一步建议

- 迁移 `GrantFilePermission` / `ListFilePermissions` / `RevokeFilePermission` 到 `CollaborationService`；
- 再迁移 `CreateFileVersion` / `ListFileVersions`；
- 每次迁移完成后继续新增文档记录。
