# 2026-04-09 FRP service -> storage 下沉记录

## 1. 迁移目标

- 模块：FRP
- 目标：将 `internal/service/frp_service.go` 中 MySQL 直连访问下沉到 storage 接口层；Redis 队列/状态逻辑继续保留在 service 层。

## 2. 本次改动

- 新增：`internal/service/contract/frp_repo.go`
  - 定义 `FrpRepository`、创建/更新映射输入结构、设备详情元数据结构。
- 新增：`internal/storage/mysql/frp_repo.go`
  - 实现设备、映射、配置、日志、配置解析相关 MySQL 访问与事务。
  - 包含 remote_port 自动分配逻辑（原 service 内逻辑下沉）。
- 修改：`internal/service/frp_service.go`
  - 依赖从 `*gorm.DB` 切换为 `contract.FrpRepository`。
  - 保留 Redis 命令队列、状态上报、命令回执等运行态逻辑。
- 修改：`internal/app/container.go`
  - 新增 `FrpRepo` 注入，改造 `FrpService` 构造。

## 3. 行为保持不变

- 设备/映射/配置的权限语义保持不变。
- 映射域名冲突、端口冲突、设备禁用等冲突判断语义保持不变。
- 配置生成、回滚、下发命令与命令历史查询语义保持不变。
- `ErrFrpNoConfigForDevice` 对外错误语义保持不变。

## 4. 验证结果

- 已执行：`gofmt -w ...`
- 已执行：`go build ./...`
- 结果：通过。

## 5. 后续建议

- 为 `FrpRepo` 补仓储层单测骨架，优先覆盖：
  - remote_port 自动分配边界
  - domain/remote_port 冲突分支
  - `ResolveConfigID` 在无配置时返回路径
