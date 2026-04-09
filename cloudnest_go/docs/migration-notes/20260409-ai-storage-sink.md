# 2026-04-09 AI service -> storage 下沉记录

## 1. 迁移目标

- 模块：AI
- 目标：将 `internal/service/ai_service.go` 中 MySQL 访问下沉到 storage 接口层，保持 AI 业务行为不变。

## 2. 本次改动

- 新增：`internal/service/contract/ai_repo.go`
  - 定义 `AIRepository` 接口，以及 `AISearchQuery` / `AISearchRow`。
- 新增：`internal/storage/mysql/ai_repo.go`
  - 实现设置写入与审计、AI 检索查询、文件可读性相关查询、元数据 upsert、任务记录写入。
- 修改：`internal/service/ai_service.go`
  - 依赖从 `*gorm.DB` 改为 `contract.AIRepository`。
  - 保留原有业务逻辑、Ollama 调用逻辑与错误语义。
- 修改：`internal/app/container.go`
  - 新增 `AIRepo` 注入，改造 `AIService` 构造。

## 3. 行为保持不变

- OCR/文档摘要/NL 检索/Ollama 调用行为保持不变。
- 权限语义与错误映射保持不变。
- 设置变更与操作日志语义保持不变。

## 4. 验证结果

- 已执行：`gofmt -w ...`
- 已执行：`go build ./...`
- 结果：通过。

## 5. 后续建议

- 下一步优先清理 `file_service.go` 中残留 DB 直连。
- 再收口 handler 层（`ai_handler.go` / `live_photo_handler.go` / `frp_handler.go`）中仍保留的 DB helper。
