# 2026-04-08 File 模块分功能迁移记录（公共分享访问 + 批量下载）

## 1. 迁移范围

- 模块：File
- 子功能：公共分享访问 + 批量下载
- 覆盖接口：
  - `GET /api/public/shares/:code`
  - `GET /api/public/shares/:code/download`
  - `POST /api/files/batch-download`

## 2. 本次改动文件

- 修改：`internal/service/file_service.go`
- 修改：`internal/handler/file_handler.go`

## 3. 迁移策略

- 采用“分功能迁移 + 功能不变”策略。
- 将以下业务判定与数据收集逻辑下沉到 `FileService`：
  - 公开分享有效性解析（分享存在、过期、文件存在）
  - 公开分享下载目标解析（目录/文件、storage 记录、物理文件存在）
  - 批量下载条目收集（权限过滤、文件/目录区分、storage 路径校验）
- `FileHandler` 保留职责：
  - 公开分享访问限流（Redis）
  - 目录打包 zip 输出与文件流输出
  - HTTP 错误码与响应结构映射

## 4. 关键下沉能力

- `GetPublicShareInfo`
- `ResolvePublicShareDownload`
- `CollectBatchDownloadEntries`
- 新增错误类型：
  - `ErrShareExpired`
  - `ErrStorageNotFound`
  - `ErrPhysicalFileNotFound`

## 5. 功能不变性声明

以下行为保持不变：

- 路由、HTTP 方法、返回结构保持不变
- 公开分享限流策略保持不变
- 关键错误语义保持不变：
  - `share not found`
  - `share expired`
  - `storage record not found`
  - `physical file not found`
  - `download failed`
  - `too many requests`
- 批量下载仍保持“跳过不可访问/异常条目，打包可用条目”的行为

## 6. 验证结果

- 已执行：`gofmt`
- 已执行：`go build ./...`
- 结果：编译通过

## 7. 当前进度与后续

### File 模块已完成

- 第一段：分享管理 + 操作日志
- 第二段：文件树与基础操作
- 第三段：上传链路
- 第四段：下载与媒体访问能力下沉
- 第五段：文件流转
- 第六段：公共分享访问 + 批量下载（本次）

### File 模块待迁移

- 检索能力：`SearchFiles` service 化
- 公共工具能力进一步下沉：时间解析、重名处理、树遍历等 helper 收敛

建议下一段迁移 `SearchFiles`，并同步收敛与检索相关的通用 helper，完成 File 模块收尾。
