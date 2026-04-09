# 2026-04-09 FRP Handler 去 DB 直连收口

## 背景
`FrpService` 已承担 FRP 模块主要业务与存储访问，但 `FrpHandler` 仍保留历史遗留的 DB/Redis helper 与字段，造成 handler 职责混杂。

## 本次改造
- `internal/handler/frp_handler.go`
  - `FrpHandler` 移除字段：
    - `DB *gorm.DB`
    - `RDB *redis.Client`
  - 删除已迁移后不再需要的历史 helper 与本地错误常量：
    - `findDeviceWithPermission`
    - `findMappingWithPermission`
    - `resolveConfigID`
    - `enqueueDeviceCommand`
    - `persistCommandData`
    - `convertLegacyQueuedCommand`
    - `allocateRemotePort`
    - `buildConfigTextForDevice`
    - `generateFRPCYaml`
    - `logFrpOperation`
    - 以及相关 `errFrp*` 本地错误与旧常量
  - 保留 handler 侧参数校验、错误映射、响应组装，核心逻辑统一走 `FrpService`。

- `internal/app/container.go`
  - `FrpHandler` 注入从 `&handler.FrpHandler{DB: db, RDB: rdb, Svc: frpService}` 调整为 `&handler.FrpHandler{Svc: frpService}`。

## 验证
- `gofmt -w internal/handler/frp_handler.go internal/app/container.go`
- `go build ./...` 通过。

## 结果
- `FrpHandler` 已无 DB/Redis 直连，职责回归 handler 编排层。
- FRP 模块链路进一步收敛为 `handler -> service -> storage(+redis)`。
