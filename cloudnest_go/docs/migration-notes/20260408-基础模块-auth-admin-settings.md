# 2026-04-08 基础模块迁移记录（Auth / Admin / Settings）

## 1. 迁移范围

- 模块：Auth、Admin、Settings
- 迁移目标：从 handler 直连 DB/Redis 的业务实现，迁移为 handler 注入 service

## 2. 本次改动文件

- 新增：`internal/service/auth_service.go`
- 新增：`internal/service/admin_service.go`
- 新增：`internal/service/settings_service.go`
- 修改：`internal/handler/auth_handler.go`
- 修改：`internal/handler/admin_handler.go`
- 修改：`internal/handler/settings_handler.go`
- 修改：`internal/app/container.go`

## 3. 功能不变性声明

本次迁移保持以下内容不变：

- 接口路由不变
- 业务规则不变（认证、权限更新、配置校验、速率窗口回落等）
- 响应结构不变
- HTTP 状态码不变
- 关键错误文案不变

## 4. 迁移策略

- 采用“1:1 下沉”策略：
  - handler 仅保留参数绑定、鉴权分支、错误映射、响应输出
  - service 承接原有事务、查询、校验、落库逻辑
- container 统一依赖注入：
  - AuthHandler <- AuthService
  - AdminHandler <- AdminService
  - SettingsHandler <- SettingsService

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 风险与后续

### 当前风险

- service 仍直接依赖 `*gorm.DB` 和 `*redis.Client`，后续需要继续下沉到 storage 接口层

### 下一步建议

- 从 Collaboration 开始做“分功能迁移”，建议优先：
  - workspace 创建与成员管理
  - 文件协作权限授予/回收
  - 文件版本管理

- 对 AI / File / FRP / LivePhoto 同样按“单功能单文档”推进
