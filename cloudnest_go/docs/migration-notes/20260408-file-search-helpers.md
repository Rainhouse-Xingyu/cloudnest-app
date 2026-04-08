# 2026-04-08 File 模块分功能迁移记录（检索 + 公共工具能力下沉）

## 1. 迁移范围

- 模块：File
- 子功能：检索与公共工具能力下沉
- 覆盖接口：
  - `GET /api/files/search`

## 2. 本次改动文件

- 修改：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 将检索核心逻辑下沉到 `FileService`：
  - 查询构建（可见性、名称筛选、类型筛选、目录筛选）
  - 时间筛选（start/end）
  - 分页统计与结果返回
- 新增 service 模型：
  - `FileSearchQuery`
  - `FileSearchResult`
- 将 File 模块时间解析 helper 内聚到 `file_service.go`：
  - `parseFlexibleTimeForFile`
- `FileHandler` 保留职责：参数绑定、错误映射、响应格式输出。

## 4. 功能不变性声明

以下行为保持不变：

- 路由、HTTP 方法、请求参数与响应结构不变
- 错误语义保持一致：
  - `invalid query`
  - `invalid start_time`
  - `invalid end_time`
  - `db error`
- 分页默认值与上限保持一致（page=1，limit 默认 50，最大 200）

## 5. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 6. File 模块收尾状态

### 已完成分段

- 第一段：分享管理 + 操作日志
- 第二段：文件树与基础操作
- 第三段：上传链路
- 第四段：下载与媒体访问能力下沉
- 第五段：文件流转
- 第六段：公共分享访问 + 批量下载
- 第七段：检索 + 公共工具能力下沉（本次）

### 结论

- File 模块主链路已完成从 handler 向 service 的分段迁移。
- 当前架构符合 `handler -> service -> storage` 目标方向，且已保持功能不变并通过编译验证。
