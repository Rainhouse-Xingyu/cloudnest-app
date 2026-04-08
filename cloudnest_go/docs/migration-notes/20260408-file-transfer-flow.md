# 2026-04-08 File 模块分功能迁移记录（文件流转）

## 1. 迁移范围

- 模块：File
- 子功能：文件流转
- 覆盖接口：
  - `POST /api/files/move`
  - `POST /api/files/copy`
  - `GET /api/recycle-bin`
  - `PUT /api/recycle-bin/:id/restore`
  - `DELETE /api/recycle-bin/:id`

## 2. 本次改动文件

- 修改：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 将流转核心事务与树结构处理下沉到 `FileService`：
  - `MoveFiles`：目标目录校验、循环移动防护、重名自动处理、事务更新
  - `CopyFiles`：批量复制与递归复制子树
  - `ListRecycleBin`：回收站查询
  - `RestoreFromRecycleBin`：恢复并自动避让重名
  - `PermanentDeleteFromRecycleBin`：递归收集后代、清理 Live Photo 关系、彻底删除
- 新增 service 内部辅助：
  - `copyNodeInService`
  - `isDescendantFolderInService`
  - `collectDescendantIDsInService`
  - `uniqueInt64InService`
- `FileHandler` 保留职责：参数绑定、错误映射、响应格式输出；恢复后自动 Live Photo 重建触发保持在 handler。

## 4. 功能不变性声明

以下行为保持不变：

- 路由、HTTP 方法、请求参数与响应结构不变
- 错误语义保持一致：
  - `invalid move parameters`
  - `invalid copy parameters`
  - `permission denied`
  - `target folder not found`
  - `target is not folder`
  - `cannot move to itself`
  - `cannot move folder into its descendant`
  - `file not found`
  - `recycle item not found`
  - `move failed` / `copy failed` / `restore failed` / `permanent delete failed`
- 审计日志行为保持一致（move/copy/restore/permanent_delete）

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. 当前进度与后续

### File 模块已完成

- 第一段：分享管理 + 操作日志
- 第二段：文件树与基础操作
- 第三段：上传链路
- 第四段：下载与媒体访问能力下沉
- 第五段：文件流转（本次）

### File 模块待迁移

- 公共分享访问：`PublicShareInfo` / `PublicShareDownload`
- 批量下载：`BatchDownload` service 化
- 检索能力：`SearchFiles` service 化（查询构建、分页统计、时间筛选下沉）
- 公共工具能力下沉：统一文件访问解析、目录树遍历、命名冲突处理等 helper 的 service 内聚

建议后续执行顺序：

1. 公共分享访问（`PublicShareInfo` / `PublicShareDownload`）
2. 批量下载（`BatchDownload`）
3. 检索与公共工具能力下沉（`SearchFiles` + 共用 helper 内聚）

以上三项均已纳入 File 模块收尾计划。
