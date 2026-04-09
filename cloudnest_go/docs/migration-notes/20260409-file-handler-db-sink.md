# 2026-04-09 File Handler 去 DB 直连收口

## 背景
随着 File/AI/LivePhoto/FRP service 层能力逐步下沉，`FileHandler` 中 `DB *gorm.DB` 已不再被业务逻辑使用，仅保留为历史注入字段。

## 本次改造
- `internal/handler/file_handler.go`
  - 从 `FileHandler` 结构体移除 `DB *gorm.DB` 字段。
  - 其余逻辑保持不变，继续通过 `FileService` / `AIService` 调用。

- `internal/app/container.go`
  - `FileHandler` 注入从
    - `&handler.FileHandler{DB: db, RDB: rdb, Cfg: cfg, AISvc: aiService, Svc: fileService}`
  - 调整为
    - `&handler.FileHandler{RDB: rdb, Cfg: cfg, AISvc: aiService, Svc: fileService}`

## 验证
- `gofmt -w internal/handler/file_handler.go internal/app/container.go`
- `go build ./...` 通过。

## 结果
- `FileHandler` 已移除 DB 注入依赖，handler 职责保持为编排层。
- 该改动与此前 AI/LivePhoto/FRP handler 收口形成一致的依赖注入风格。
