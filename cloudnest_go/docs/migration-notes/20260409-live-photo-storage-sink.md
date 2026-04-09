# 2026-04-09 Live Photo service -> storage 下沉记录

## 1. 迁移目标

- 模块：Live Photo
- 目标：将 `internal/service/live_photo_service.go` 中直连数据库访问逻辑下沉到 storage 接口层，保持接口行为不变。

## 2. 本次改动

- 新增：`internal/service/contract/live_photo_repo.go`
  - 定义 `LivePhotoRepository` 仓储接口与 `LivePhotoBindPair` 数据结构。
- 新增：`internal/storage/mysql/live_photo_repo.go`
  - 实现 Live Photo 相关查询、权限计数、绑定/解绑事务、自动绑定持久化事务与日志写入。
- 修改：`internal/service/live_photo_service.go`
  - 依赖从 `*gorm.DB` 切换为 `contract.LivePhotoRepository`。
  - 保留业务逻辑（权限判断、文件类型校验、自动配对规则），将 DB 访问改为仓储调用。
- 修改：`internal/app/container.go`
  - 新增 `LivePhotoRepo` 注入，并改造 `LivePhotoService` 构造。

## 3. 行为保持不变

- 手动绑定/解绑流程及日志动作名保持不变。
- 详情、列表、按文件反查的权限语义保持不变。
- 自动绑定规则（同目录+同名主干）保持不变。
- 自动绑定返回中的 created/skipped 语义保持不变。

## 4. 验证结果

- 已执行：`gofmt -w ...`
- 已执行：`go build ./...`
- 结果：通过。

## 5. 后续建议

- 补充 `LivePhotoRepo` 仓储层单测骨架，重点覆盖：
  - 绑定冲突分支
  - 自动绑定事务中的重复跳过分支
  - 列表权限过滤分支
