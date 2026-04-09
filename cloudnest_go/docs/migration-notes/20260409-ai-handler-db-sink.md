# 2026-04-09 AI Handler 去 DB 直连收口

## 背景
在 `AIService` 已完成仓储化后，`AIHandler` 仍保留 `*gorm.DB` 字段及若干直接查询/写入 helper。
此外，`FileHandler` 的自动分析异步流程会临时构造 `AIHandler` 并调用这些 helper，导致 handler 层仍承担存储职责。

## 本次改造
- `internal/handler/ai_handler.go`
  - 移除 `AIHandler` 的 `DB *gorm.DB` 字段。
  - 将 helper 改为委托 `AIService`：
    - `getReadableFileAndStorage` -> `Svc.GetReadableFileAndStorage`
    - `canAccessFile` -> `Svc.CanAccessFile`
    - `upsertAIMetadata` -> `Svc.UpsertAIMetadata`
    - `createAITask` -> `Svc.CreateAITask`
  - `executeAISearch` 由 handler 内 SQL 改为调用 `Svc.Search`。

- `internal/service/ai_service.go`
  - 新增对外方法（对内部逻辑的轻量封装）：
    - `GetReadableFileAndStorage`
    - `CanAccessFile`
    - `UpsertAIMetadata`
    - `CreateAITask`

- `internal/handler/file_handler.go`
  - `FileHandler` 新增 `AISvc *service.AIService` 注入。
  - `triggerAsyncAutoAnalyze` 构造 `AIHandler` 时改为注入 `Svc`，不再依赖 DB。

- `internal/app/container.go`
  - `AIHandler` 注入改为 `&handler.AIHandler{Cfg: cfg, Svc: aiService}`。
  - `FileHandler` 注入增加 `AISvc: aiService`。

## 验证
- 语言服务诊断：变更文件无错误。
- `go build ./...` 通过。

## 效果
- `AIHandler` 已移除 DB 直连，职责回归到 handler 编排。
- `FileHandler` 的 AI 自动分析路径也通过 service 层落库，避免跨层 DB 访问。
