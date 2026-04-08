# 2026-04-08 Live Photo 模块迁移记录

## 1. 迁移范围

- 模块：Live Photo
- 迁移目标：`handler -> service`
- 涉及接口：
  - `POST /api/live-photos/bind`
  - `POST /api/live-photos/auto-bind`
  - `DELETE /api/live-photos/:id`
  - `GET /api/live-photos/:id`
  - `GET /api/live-photos`
  - `GET /api/files/:id/live-photo`

## 2. 本次改动文件

- 新增：`internal/service/live_photo_service.go`
- 修改：`internal/handler/live_photo_handler.go`
- 修改：`internal/app/container.go`

## 3. 迁移策略

- 将 Live Photo 业务逻辑下沉到 `LivePhotoService`，包括：
  - 绑定/解绑事务
  - 详情与列表查询
  - 文件反查
  - 自动配对与批量绑定
  - 访问权限校验、文件类型校验、操作日志写入
- `LivePhotoHandler` 仅保留：参数绑定、输入校验、错误码映射、响应构造。

## 4. 功能不变性声明

本次迁移保持以下不变：

- 路由与 HTTP 方法不变
- JSON 返回结构不变（`item`、`items`、`pagination`、`summary` 字段保持兼容）
- 状态码与错误语义保持一致（如 `permission denied`、`live photo relation already exists`、`auto bind failed` 等）
- 自动绑定规则保持一致：同目录 + 同名主干配对，已绑定跳过

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 模块状态

- Live Photo 模块在 `handler -> service` 层面迁移已完成。
- 下一阶段可按计划推进 `service -> storage`（repository 化）解耦。
