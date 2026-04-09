# 2026-04-09 Live Photo Handler 去 DB 直连收口

## 背景
`LivePhotoService` 已具备完整仓储访问能力，但 `LivePhotoHandler` 仍保留 `DB *gorm.DB` 字段和一组历史 DB helper，导致 handler 层职责不够纯粹。

## 本次改造
- `internal/handler/live_photo_handler.go`
  - 移除 `LivePhotoHandler` 的 `DB *gorm.DB` 字段。
  - 删除 handler 内已下沉后不再使用的 DB helper：
    - `canAccessFile`
    - `loadLivePhotoFiles`
    - `loadAutoBindCandidates`
    - `buildAutoBindKey`
  - 保留接口编排逻辑与响应结构不变，所有业务访问继续通过 `LivePhotoService`。

- `internal/app/container.go`
  - `LivePhotoHandler` 注入从 `&handler.LivePhotoHandler{DB: db, Svc: livePhotoService}` 调整为 `&handler.LivePhotoHandler{Svc: livePhotoService}`。

## 验证
- `gofmt -w internal/handler/live_photo_handler.go internal/app/container.go`
- `go build ./...` 通过。

## 结果
- `LivePhotoHandler` 已无 DB 直连，职责回归 handler 编排层。
- Live Photo 模块 `handler -> service -> storage` 链路进一步收口。
