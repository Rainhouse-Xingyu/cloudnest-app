# CloudNest iOS 模块目录对照表（按项目功能汇总）

更新时间：2026-04-12

## 1. 当前工程现状

- 当前 Feature 目录：Auth、Home、Files、Profile、Settings、Smart
- 当前分层：Views / ViewModels / Models / Services（部分模块尚未补齐）
- 架构：SwiftUI + MVVM + Core/Shared 公共层

## 2. 模块与目录对照（可直接用于任务拆分）

| 模块 | 目录（建议） | 当前状态 | 功能范围 | 核心接口（示例） |
|---|---|---|---|---|
| 账号与鉴权 | `Features/Auth/` | 已有基础 | 登录、退出登录、会话恢复、权限初始化 | `POST /api/auth/login` `POST /api/auth/logout` `GET /api/auth/me` |
| 首页总览 | `Features/Home/` | 已有基础 | 最近文件/照片、快捷操作、容量与状态卡片、FRP设置 | `GET /api/files` `GET /api/live-photos` |
| 文件管理（核心） | `Features/Files/` | 需补齐 | 目录树、上传下载、重命名、移动复制删除、批量操作、断点续传 | `GET /api/files` `POST /api/files/folders` `PUT /api/files/:id/rename` `DELETE /api/files/:id` `POST /api/files/upload/*` |
| 搜索与筛选 | `Features/Files/`（可先内聚） | 需补齐 | 文件名/类型/时间搜索，筛选与排序 | `GET /api/files/search` |
| 预览与媒体 | `Features/Files/`（子页面） | 需补齐 | 图片/文本/PDF预览，视频流播放，缩略图 | `GET /api/files/:id/preview` `GET /api/files/:id/stream` `GET /api/files/:id/thumbnail` |
| 分享与回收站 | `Features/Files/`（子页面） | 需补齐 | 创建分享、我的分享、失效、回收站恢复/彻删 | `POST /api/files/:id/share` `GET /api/shares` `DELETE /api/shares/:code` `GET /api/files/recycle-bin` `POST /api/files/:id/restore` |
| Live Photo | `Features/LivePhoto/`（新增） | 建议新增 | 手动绑定/解绑、列表、详情、按文件反查 | `POST /api/live-photos/bind` `DELETE /api/live-photos/:id` `GET /api/live-photos` `GET /api/files/:id/live-photo` |
| 协作与权限 | `Features/Collaboration/`（新增） | 建议新增 | 空间、成员、文件协作读写权限、版本协作 | `POST /api/workspaces` `GET /api/workspaces` `POST /api/files/:id/collaborators` `GET /api/files/:id/versions` |
| AI 智能 | `Features/Smart/` | 已建壳 | 结构化搜索、自然语言搜索、摘要、助手问答 | `GET /api/ai/search` `POST /api/ai/search/nl` `POST /api/ai/summarize` `POST /api/ai/assistant/ask` |
| FRP 设备管理 | `Features/FRP/`（新增） | 建议新增 | 设备、映射、配置预览/保存/回滚、状态监控 | `POST /api/frp/devices` `GET /api/frp/devices` `POST /api/frp/configs/preview` `POST /api/frp/configs/save` |
| 设置 | `Features/Settings/` | 已有基础 | 系统配置、限速策略、高峰时段、权限规则查看 | `GET /api/settings` `PUT /api/settings` `GET /api/settings/permissions` `PUT /api/settings/rate-window` |
| 日志与审计 | `Features/AdminLogs/`（新增，可管理员可见） | 建议新增 | 关键操作日志查询、导出 | `GET /api/operation-logs` `GET /api/operation-logs/export` |

## 3. 每个 Feature 建议的标准文件骨架

每个模块按以下骨架建立，便于统一维护：

```text
Features/<Module>/
  Views/
    <Module>View.swift
    <Module>ListView.swift
    <Module>DetailView.swift
  ViewModels/
    <Module>ViewModel.swift
    <Module>ListViewModel.swift
    <Module>DetailViewModel.swift
  Models/
    <Module>Models.swift
  Services/
    <Module>Service.swift
```

## 4. 公共层建议补齐

### 4.1 Core 层

- `Core/Network/`：请求封装、错误映射、鉴权头注入
- `Core/Storage/`：本地缓存、会话持久化
- `Core/Components/`：通用列表项、空状态页、加载态、错误态
- `Core/Utils/`：时间格式化、文件大小格式化、扩展名识别

### 4.2 Shared 层

- `Shared/Managers/SessionManager.swift`：会话管理（已存在）
- `Shared/Managers/UploadQueueManager.swift`：上传队列管理（建议新增）
- `Shared/Managers/PermissionManager.swift`：权限态管理（建议新增）
- `Shared/Models/Pagination.swift`：分页结构（建议新增）

## 5. 与你当前产品策略保持一致的说明

- OCR 不提供前端按钮，不做手动触发入口。
- 上传成功后由后端自动执行 OCR，前端只负责展示 OCR 结果（如智能搜索命中、摘要片段）。

## 6. 建议 MVP 落地顺序（iOS）

1. Auth + Home + Files（基础 CRUD + 上传下载 + 搜索）
2. 分享 + 回收站 + Settings（限速/策略配置）
3. Live Photo + 协作权限 + Smart（搜索/摘要）
4. FRP + 日志审计（管理员向）

## 7. 文件级任务拆分示例（可直接抄到迭代计划）

### 迭代 1（MVP）

- `Features/Files/Views/FilesView.swift`：文件列表、目录导航、多选工具条
- `Features/Files/ViewModels/FilesViewModel.swift`：列表加载、搜索、批量状态
- `Features/Files/Services/FilesService.swift`：文件接口封装
- `Features/Settings/Views/SettingsView.swift`：设置页基础项
- `Features/Home/Views/HomeView.swift`：首页分类卡片（照片/文件）

### 迭代 2

- `Features/Files/Views/RecycleBinView.swift`：回收站
- `Features/Files/Views/ShareListView.swift`：分享列表
- `Features/Smart/Views/SmartSearchView.swift`：AI 检索页

### 迭代 3

- `Features/LivePhoto/*`：Live Photo 全链路页面
- `Features/FRP/*`：FRP 设备与配置页面
- `Features/AdminLogs/*`：日志与导出页面
