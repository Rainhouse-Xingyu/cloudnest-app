# 2026-04-08 Collaboration 分功能迁移记录（文件协作权限）

## 1. 迁移范围

- 模块：Collaboration
- 功能：文件协作权限授予、查询、回收
- 涉及接口：
  - `POST /api/files/:id/collaborators`
  - `GET /api/files/:id/collaborators`
  - `DELETE /api/files/:id/collaborators/:user_id`

## 2. 本次改动文件

- 修改：`internal/service/collaboration_service.go`
- 修改：`internal/handler/collaboration_handler.go`

## 3. 功能不变性声明

本次迁移保持以下不变：

- 路由与方法不变
- 返回结构不变
- HTTP 状态码不变
- 错误文案不变（如 `invalid file id`、`permission denied`、`target user not found`、`cannot grant permission to self`）
- 权限判断规则不变（owner 或 write 权限判断逻辑不变）
- 操作日志动作名与 detail 结构不变

## 4. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 本次只迁移文件协作权限三接口到 service：
  - handler 保留参数解析、输入校验、错误映射、响应输出
  - service 承接权限判定、事务 upsert、列表查询、删除操作与日志落库
- 文件版本相关接口仍在 handler 侧，后续单独迁移。

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 风险与后续

### 当前风险

- `CreateFileVersion` / `ListFileVersions` 尚未迁移，`CollaborationHandler` 仍保留 `DB` 依赖。

### 下一步建议

- 迁移 `CreateFileVersion` / `ListFileVersions` 到 `CollaborationService`；
- 完成后可进一步收敛 `CollaborationHandler` 对 DB 的直接依赖。
