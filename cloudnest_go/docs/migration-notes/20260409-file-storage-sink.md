# 2026-04-09 File 模块 service->storage 下沉补齐

## 背景
在前几轮迁移后，`FileService` 仍保留大量 `s.db.WithContext(...)` 直连路径，存在 service 与存储层职责边界不清的问题。

## 本次改造
- `internal/service/file_service.go`
  - 移除 `FileService` 中 `db *gorm.DB` 依赖，保留 `fileRepo contract.FileRepository`。
  - `NewFileService` 构造函数调整为仅接收 `fileRepo`。
  - 将以下能力统一改为走 `fileRepo`：
    - 目录/文件基础操作：列表、创建目录、重命名、删除
    - 上传链路：初始化、分片校验、分片进度更新、完成上传
    - 文件访问与存储读取
    - 移动/复制、回收站、分享
    - 搜索、操作日志查询/导出
    - Live Photo 自动绑定候选与落库
    - 系统设置读取、权限校验、日志写入
  - 删除不再使用的 DB fallback 辅助逻辑（如 upload fallback helper）。

- `internal/service/contract/file_repo.go`
  - 新增 `CreateOperationLog(...)` 接口，支撑 service 层日志落库去 DB 直连。

- `internal/storage/mysql/file_repo.go`
  - 实现 `CreateOperationLog(...)`。

- `internal/app/container.go`
  - `FileService` 注入改为 `service.NewFileService(fileRepo)`。

## 结果
- `FileService` 内部已无 `s.db` 直接访问。
- `go build ./...` 编译通过。

## 影响与后续
- 本次改造不改变对外 handler API 行为，仅收敛 service 层存储访问路径。
- 下一步建议继续清理 handler 层残留 DB helper（优先 `ai_handler` / `live_photo_handler` / `frp_handler`）。
