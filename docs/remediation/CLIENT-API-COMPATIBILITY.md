# 现有 Apple 客户端 API 权限兼容性核对

2026-10-21，以冻结的 iOS 客户端真实 Swift APIEndpoint 声明/调用为依据。未新增 iOS 功能，也未把尚未构建的 macOS 工程当作已支持。

共 57 组不同方法/路径：51 个普通业务、1 个自身会话、1 个公开登录、3 个必须管理员、1 个需要显式授权的权限汇总。所有路径都在实际 Gin Router 注册。来源与权限预期保存于 `cloudnest_go/internal/middleware/testdata/apple_client_routes.json`，每条有 Swift 文件和行号；两个 Go 契约测试分别核对真实路由注册与默认权限策略。该快照不是完整端到端业务测试。

## 规则和兼容性结果

- `GET /api/auth/me`、`POST /api/auth/logout` 及新增 `POST /api/auth/logout-all` 属于经过认证的用户自身会话操作，无需额外业务路径授权。仍先校验签名、有效期、Redis 会话归属和当前账号状态；不会放行未知/扩展 auth 路径。
- 自定义权限仍替换默认业务授权集合，并保留方法/具体路径/已有通配符语义。仅授予 GET files 不会附带 settings 或文件写权限。
- 新 JWT 的 JTI 使用 crypto/rand 生成 32 字节并以 base64url 编码。同一时间戳的 128 次签发均有独立 ID；真实重复登录与单会话注销测试验证 Redis 隔离。JWT 对客户端仍为不透明字符串，登录 JSON 字段、Bearer 头、日期表示均保持兼容。旧的已签名且在 Redis 有效的无 JTI/无版本会话，在账号 auth_version 为 0 时仍按原有效期处理；密码重置或退出全部会话递增版本后立即拒绝后续认证请求。新增 JTI 不是撤销旧会话，部署密钥轮换要求仍有效。
- `SettingsService.getPermissionSummary` 请求 `/api/settings/permissions`；该路径在此前基线就不是普通默认权限。显式授权仅允许本人范围，`all=1` 仍须管理员。现有设置页未按能力隐藏此项，普通用户会见到 403 错误提示，这是历史 UI 缺口，留给 G07/G09 能力展示，不放宽后端边界。
- 创建用户、全局系统设置写入、AI 修改全局设置始终要求管理员；即使配置 `*:*` 也不能绕过 Handler/RequireAdmin 检查。
- AuthViewModel 的健康探测无 Token，原逻辑将 401 视为服务可达；该接口仍需认证，没有为探测改为公开。
- 旧 SettingsViewModel 会忽略服务器注销错误并清除本地会话。这不是后端契约变化；G07 应明确提示服务端撤销失败并保留可重试状态。

## 方法/路由快照

| 方法 | 路由模板 | 权限类别 |
|---|---|---|
| DELETE | `/api/files/:id/collaborators/:user_id` | 默认业务；自定义账号需授权 |
| DELETE | `/api/files/:id/permanent` | 默认业务；自定义账号需授权 |
| DELETE | `/api/frp/devices/:id` | 默认业务；自定义账号需授权 |
| DELETE | `/api/frp/mappings/:id` | 默认业务；自定义账号需授权 |
| DELETE | `/api/shares/:code` | 默认业务；自定义账号需授权 |
| DELETE | `/api/workspaces/:id/members/:user_id` | 默认业务；自定义账号需授权 |
| GET | `/api/ai/search` | 默认业务；自定义账号需授权 |
| GET | `/api/files` | 默认业务；自定义账号需授权 |
| GET | `/api/files/:id/collaborators` | 默认业务；自定义账号需授权 |
| GET | `/api/files/:id/download` | 默认业务；自定义账号需授权 |
| GET | `/api/files/:id/download-zip` | 默认业务；自定义账号需授权 |
| GET | `/api/files/:id/live-photo` | 默认业务；自定义账号需授权 |
| GET | `/api/files/:id/preview` | 默认业务；自定义账号需授权 |
| GET | `/api/files/:id/thumbnail` | 默认业务；自定义账号需授权 |
| GET | `/api/files/recycle-bin` | 默认业务；自定义账号需授权 |
| GET | `/api/files/search` | 默认业务；自定义账号需授权 |
| GET | `/api/frp/devices` | 默认业务；自定义账号需授权 |
| GET | `/api/frp/devices/:id/mappings` | 默认业务；自定义账号需授权 |
| GET | `/api/frp/devices/:id/status` | 默认业务；自定义账号需授权 |
| GET | `/api/live-photos` | 默认业务；自定义账号需授权 |
| GET | `/api/operation-logs` | 默认业务；自定义账号需授权 |
| GET | `/api/operation-logs/export` | 默认业务；自定义账号需授权 |
| GET | `/api/resource/health` | 默认业务；自定义账号需授权 |
| GET | `/api/settings` | 默认业务；自定义账号需授权 |
| GET | `/api/settings/permissions` | 显式授权；all=1 需管理员 |
| GET | `/api/settings/rate-window` | 默认业务；自定义账号需授权 |
| GET | `/api/shares` | 默认业务；自定义账号需授权 |
| GET | `/api/storage/overview` | 默认业务；自定义账号需授权 |
| GET | `/api/workspaces` | 默认业务；自定义账号需授权 |
| GET | `/api/workspaces/:id/members` | 默认业务；自定义账号需授权 |
| POST | `/api/admin/users` | 必须管理员 |
| POST | `/api/ai/assistant/ask` | 默认业务；自定义账号需授权 |
| POST | `/api/ai/assistant/settings` | 必须管理员 |
| POST | `/api/ai/ocr` | 默认业务；自定义账号需授权 |
| POST | `/api/ai/search/nl` | 默认业务；自定义账号需授权 |
| POST | `/api/ai/summarize` | 默认业务；自定义账号需授权 |
| POST | `/api/auth/login` | 公开 |
| POST | `/api/auth/logout` | 认证后自己的会话 |
| POST | `/api/files/:id/collaborators` | 默认业务；自定义账号需授权 |
| POST | `/api/files/:id/restore` | 默认业务；自定义账号需授权 |
| POST | `/api/files/:id/share` | 默认业务；自定义账号需授权 |
| POST | `/api/files/batch-delete` | 默认业务；自定义账号需授权 |
| POST | `/api/files/copy` | 默认业务；自定义账号需授权 |
| POST | `/api/files/folders` | 默认业务；自定义账号需授权 |
| POST | `/api/files/move` | 默认业务；自定义账号需授权 |
| POST | `/api/files/upload/chunk` | 默认业务；自定义账号需授权 |
| POST | `/api/files/upload/complete` | 默认业务；自定义账号需授权 |
| POST | `/api/files/upload/init` | 默认业务；自定义账号需授权 |
| POST | `/api/frp/configs/save` | 默认业务；自定义账号需授权 |
| POST | `/api/frp/devices` | 默认业务；自定义账号需授权 |
| POST | `/api/frp/mappings` | 默认业务；自定义账号需授权 |
| POST | `/api/live-photos/auto-bind` | 默认业务；自定义账号需授权 |
| POST | `/api/workspaces` | 默认业务；自定义账号需授权 |
| POST | `/api/workspaces/:id/members` | 默认业务；自定义账号需授权 |
| PUT | `/api/frp/devices/:id` | 默认业务；自定义账号需授权 |
| PUT | `/api/settings` | 必须管理员 |
| PUT | `/api/settings/rate-window` | 默认业务；自定义账号需授权 |

## 验证范围

真实集成测试覆盖首页/设置常用读取接口、权限汇总的显式授权和管理员边界、登录 DTO 字段、自定义权限下查询与注销、重复登录会话隔离。文件流程仍由已有上传/下载/分享/恢复/版本测试覆盖。57 路由契约通过不能推出所有按钮业务已完成，OCR/模型效果、FRP 隧道与 iOS UI 运行不在本次结论内。iOS SDK 缺失的原构建限制保持记录。

新增 logout-all 为可选的自身会话能力，不改动冻结 iOS 57 项快照；G07 客户端需在 200 时清理会话、503 时保留重试，并正确处理重置密码后的 401。账号升级及接口语义见 [恢复指南](ACCOUNT-RECOVERY.md)。

G02 新增历史版本下载/恢复两个业务路由，见 [对象与版本报告](BLOB-VERSION-CONSISTENCY.md)。自定义路径权限仍需授权具体方法和路径/已有前缀通配符，资源读取/写入权限独立验证。版本列表新增 file_type/source_verified 及真实初始版本行；上传/复制从独立版本 1 开始。相同 upload_id 竞争完成的失败方保持 409，尚未增加完成结果复用。冻结的 iOS 57 条快照和登录/读取 DTO 回归继续通过，新增路由未冒充旧客户端已提供相应 UI。

G02 旧历史 source_verified 默认为 false，不能凭 file_version 关联重新打开升级前的任意 storage 引用；只有已有当前内容能力时才可读取/恢复，未证明来源的历史可能返回 403，需显示具体失败，不能伪装为空历史或恢复成功。

G02 容量概览改为当前内容、已验证保留历史和回收站的逻辑对象计量，并增加预留/可用于上传额度及未计量引用数等字段。Init/恢复/完成参数需与已持久化契约一致，旧未完成任务及已过期任务 409，配额不足 507，异常计量/配置 503。现有 Swift DTO 和三类上传入口均已发送 parent_id 及对应尺寸/分片参数；iOS 源码保持冻结，未进行 UI/SDK 验收。新增 POST upload/cancel 和完整语义见 [额度报告](STORAGE-QUOTA.md)，macOS 需区分有效预留后的可用额度和普通 remaining_bytes。
