# 基础模块 service -> storage 下沉（Auth/Admin/Settings）

## 目标

将基础模块 `AuthService`、`AdminService`、`SettingsService` 中的数据库访问从 service 层下沉到 storage 接口层，保持 handler 与接口行为不变。

## 本次改动

### 1) 新增基础仓储契约

文件：`internal/service/contract/base_repos.go`

新增：

- `AuthRepository`
- `AdminRepository`
- `SettingsRepository`
- 辅助类型：`MethodPathPermission`、`SettingUpsertItem`、`UserRoleRow`

### 2) 新增 MySQL 仓储实现

- `internal/storage/mysql/auth_repo.go`
- `internal/storage/mysql/admin_repo.go`
- `internal/storage/mysql/settings_repo.go`

能力覆盖：

- Auth：按用户名查询用户、管理员判定、密码升级、用户信息读取。
- Admin：创建用户+默认角色+权限写入事务；权限覆盖+审计日志事务。
- Settings：系统设置查询与批量 upsert；权限汇总相关查询；用户高峰时段设置读取与 upsert。

### 3) Service 层改造

- `internal/service/auth_service.go`
- `internal/service/admin_service.go`
- `internal/service/settings_service.go`

改造结果：

- 三个 service 不再直连 `gorm.DB`。
- 所有持久化访问改为通过对应 repository 接口调用。
- 现有错误语义保持不变（如 `ErrInvalidCredentials`、`contract.ErrConflict`、`gorm.ErrInvalidData/InvalidField` 等）。

### 4) 容器注入改造

文件：`internal/app/container.go`

新增注入：

- `AuthRepo`
- `AdminRepo`
- `SettingsRepo`

并更新服务构造：

- `NewAuthService(authRepo, rdb, cfg)`
- `NewAdminService(adminRepo, rdb)`
- `NewSettingsService(settingsRepo)`

## 验证

执行：

```bash
gofmt -w internal/service/contract/base_repos.go internal/storage/mysql/auth_repo.go internal/storage/mysql/admin_repo.go internal/storage/mysql/settings_repo.go internal/service/auth_service.go internal/service/admin_service.go internal/service/settings_service.go internal/app/container.go
go build ./...
```

结果：构建通过（无输出）。
