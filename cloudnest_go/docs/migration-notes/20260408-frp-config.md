# 2026-04-08 FRP 分功能迁移记录（配置管理）

## 1. 迁移范围

- 模块：FRP
- 功能：配置预览、保存、历史查询、回滚
- 涉及接口：
  - `POST /api/frp/configs/preview`
  - `POST /api/frp/configs/save`
  - `GET /api/frp/devices/:id/configs`
  - `POST /api/frp/configs/:id/rollback`

## 2. 本次改动文件

- 修改：`internal/service/frp_service.go`
- 修改：`internal/handler/frp_handler.go`

## 3. 迁移策略

- 本次继续采用“分功能迁移 + 功能不变”。
- 配置相关核心逻辑下沉到 `FrpService`：
  - 设备权限校验
  - 生效映射查询
  - `frpc.yaml` 文本生成
  - 配置保存与回滚
- handler 保留职责：参数绑定、状态码与错误文案映射、响应结构输出。

## 4. 功能不变性声明

本次迁移保持以下不变：

- 路由与 HTTP 方法不变
- 返回 JSON 结构不变
- 关键错误语义不变（如 `device not found`、`permission denied`、`no active mapping`、`server_addr is required`、`invalid config parameters`、`config has no device`）
- 配置文本生成规则（tcp/http/https 代理分支）不变

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### 已完成

- FRP 设备/映射子功能完成 handler -> service 迁移。
- FRP 配置管理子功能完成 handler -> service 迁移。

### 待迁移

- FRP 命令与状态子功能：
  - `deploy/reload`
  - `status report/status query`
  - `pull next/result/history`

下一步建议迁移“命令与状态子功能”，完成后 FRP 模块可在 handler -> service 层面宣布迁移完成。
