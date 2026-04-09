# 2026-04-09 Collaboration service -> storage 下沉记录

## 1. 迁移目标

- 模块：Collaboration
- 目标：将 `internal/service/collaboration_service.go` 中直连 DB 的查询与事务下沉到 storage 接口层，保持对外行为不变。

## 2. 本次改动

- 新增：`internal/service/contract/collaboration_repo.go`
  - 定义 `CollaborationRepository` 以及协作模块查询返回结构。
- 新增：`internal/storage/mysql/collaboration_repo.go`
  - 实现 workspace、member、file_permission、file_version、operation_log 的持久化逻辑。
- 修改：`internal/service/collaboration_service.go`
  - 从 `*gorm.DB` 依赖切换为 `contract.CollaborationRepository` 依赖。
  - 保留业务层输入校验与角色归一化逻辑，数据访问改为调用 repository。
- 修改：`internal/app/container.go`
  - 新增 `CollaborationRepo` 注入并改造 `CollaborationService` 构造。

## 3. 行为不变性

以下行为保持不变：

- API 路由与 handler 返回结构不变。
- Workspace 成员 owner 角色保护规则不变。
- 文件访问权限判断与读写规则不变。
- 文件版本提交冲突规则（`version conflict`）与文件夹禁止版本化规则不变。
- 协作操作日志写入动作与默认值策略不变。

## 4. 验证结果

- 已执行：`gofmt -w ...`
- 已执行：`go build ./...`
- 结果：通过。

## 5. 后续建议

- 补充 `CollaborationRepo` 仓储层单测（可复用 sqlite 内存库测试骨架）。
- 继续推进 LivePhoto / FRP 模块同模式 service -> storage 下沉。
