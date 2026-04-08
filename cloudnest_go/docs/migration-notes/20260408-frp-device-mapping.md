# 2026-04-08 FRP 分功能迁移记录（设备与映射）

## 1. 迁移范围

- 模块：FRP
- 功能：设备管理 + 映射管理
- 涉及接口：
  - `POST /api/frp/devices`
  - `GET /api/frp/devices`
  - `GET /api/frp/devices/:id`
  - `PUT /api/frp/devices/:id`
  - `DELETE /api/frp/devices/:id`
  - `POST /api/frp/mappings`
  - `GET /api/frp/devices/:id/mappings`
  - `PUT /api/frp/mappings/:id`
  - `DELETE /api/frp/mappings/:id`

## 2. 本次改动文件

- 新增：`internal/service/frp_service.go`
- 修改：`internal/handler/frp_handler.go`
- 修改：`internal/app/container.go`

## 3. 迁移策略

- 本次采用“分功能迁移 + 功能不变”策略。
- 将设备与映射相关核心逻辑从 handler 下沉到 `FrpService`。
- handler 继续负责：参数绑定、基础参数校验、错误码映射、响应结构输出。
- 为兼容分阶段迁移，`FrpHandler` 保留 `DB/RDB` 字段，未迁移的配置与命令功能继续走原逻辑。

## 4. 功能不变性声明

本次迁移保持以下不变：

- 路由与 HTTP 方法不变
- 响应 JSON 结构不变
- 状态码与错误文案语义不变（例如：`device not found`、`permission denied`、`remote_port already exists`、`domain already exists`）
- 端口自动分配策略与冲突检查逻辑不变
- 操作日志动作名与 detail 格式不变

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### 已完成

- FRP 设备/映射子功能完成 handler -> service 迁移。

### 待迁移

- FRP 配置子功能：`preview/save/list/rollback`
- FRP 命令与状态子功能：`deploy/reload/status report/status query/pull next/result/history`

下一步建议继续迁移“配置子功能”，完成后再处理“命令与状态子功能”。
