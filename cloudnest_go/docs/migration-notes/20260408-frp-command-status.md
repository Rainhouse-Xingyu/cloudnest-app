# 2026-04-08 FRP 分功能迁移记录（命令与状态）

## 1. 迁移范围

- 模块：FRP
- 功能：配置下发/重载命令、设备状态上报与查询、命令拉取与结果回传、命令历史查询
- 涉及接口：
  - `POST /api/frp/devices/:id/deploy-config`
  - `POST /api/frp/devices/:id/reload`
  - `POST /api/frp/devices/:id/status/report`
  - `GET /api/frp/devices/:id/status`
  - `GET /api/frp/devices/:id/commands/next`
  - `POST /api/frp/commands/:command_id/result`
  - `GET /api/frp/devices/:id/commands`

## 2. 本次改动文件

- 修改：`internal/service/frp_service.go`
- 修改：`internal/handler/frp_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”。
- 将命令与状态相关核心逻辑下沉到 `FrpService`：
  - 命令队列写入/拉取
  - 命令数据持久化与状态流转
  - 旧队列 JSON 兼容转换
  - 设备状态 Redis 存储与查询
  - 命令执行结果更新
- handler 继续负责：参数绑定、状态码映射、响应结构输出。

## 4. 功能不变性声明

本次迁移保持以下不变：

- 路由与 HTTP 方法不变
- 响应结构不变
- 状态码与关键错误语义不变（如 `invalid queued command`、`command data not found`、`command not found`、`executed_at must be RFC3339`）
- 命令 ID 生成规则、历史队列保留策略、Redis Key 结构不变
- 操作日志动作名与 detail 格式不变

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 模块状态

- FRP 模块在 handler -> service 层面的分功能迁移已全部完成：
  - 设备与映射
  - 配置管理
  - 命令与状态

下一阶段可按计划继续推进 service -> storage 的 repository 化解耦。
