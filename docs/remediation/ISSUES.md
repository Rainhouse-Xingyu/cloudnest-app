# 持续问题登记

日期：2026-10-21。路径均相对仓库根目录；Go 路径省略 `cloudnest_go/`。P0 表示上线阻断，P1 表示核心可靠性/安全问题，P2 表示体验/维护问题。下列复现仅限隔离环境。静态证据不冒充攻击实测。

| 编号/优先级 | 证据位置与当前风险 | 复现/验证方法 | 最小整改与阶段 | 状态 |
|---|---|---|---|---|
| SEC-001 P0 | `.env.example` 包含非占位 MySQL/Redis 凭据，已进入 Git 历史 | 只检查键是否为占位值，禁止输出值 | G00 清理示例/防回归；操作者轮换有效凭据，另行决定历史清理 | 示例已清理；轮换待人工 |
| SEC-002 P0 | `internal/store/mysql.go:SeedAdminUser` 固定创建 admin/admin，既有同名用户还会被绑定管理员角色 | 空测试库启动后默认密码登录；预置同名普通用户再启动 | G01 移除启动自动播种，显式首次引导/强密码/事务/拒绝提升既有用户 | 已修代码/测试；旧账号密码重置与会话轮换待人工 |
| SEC-003 P0 | `internal/config/config.go:Load` 固定 JWT 默认密钥，未校验配置 | 未设置 JWT_SECRET 仍能启动认证路径；Redis 会话检查不能替代密钥保护 | G01 必填强随机密钥与 TTL 校验，在连接数据库之前失败 | 已修代码/测试；实际密钥轮换待人工 |
| SEC-004 P0 | `internal/storage/mysql/collaboration_repo.go:CreateFileVersion` 任意 storage_id，只有存在性检查 | A 用自己的文件创建版本，传 B 的存储 ID，然后下载；期望 403 且无写入 | G01 在事务中校验目标写权限和来源对象授权，尺寸取服务端；跨用户/并发测试 | 已复现 200 越权；修复后 403 且原文件不变，待 PR 审查 |
| SEC-005 P1 | `internal/service/auth_service.go:verifyPassword/Login` 接受明文密码，透明升级忽略错误 | 测试库插入明文密码；登录当前可成功 | G01 拒绝明文，提供明确重置路径，不静默写旧密码 | 已修：拒绝明文并提供事务化维护命令恢复；真实账号重置待人工 |
| SEC-006 P1 | `internal/middleware/auth.go:defaultAllowPath` 除两个排除项外放行全部 `/api/`，不区分方法 | 为新路径挂测试 handler，无权限用户也会获准 | G01 显式业务方法/路由模板清单，新增路由默认拒绝；资源级授权仍必需 | 已修：显式路由与即时权限读取，待审 |
| SEC-007 P1 | `internal/auth/jwt.go:ParseToken` 不强制 exp，也未检查 user_id/subject 一致性 | 用测试密钥签无 exp/零 ID/不匹配 subject token | G01 必须过期时间及合法身份声明 | 已修：必需 exp/身份校验，待审 |
| SEC-008 P1 | `auth_service.go:Logout` 忽略 Redis 删除错误；角色/禁用状态来自历史 JWT | Redis 断开后注销；登录后撤销角色/禁用再访问 | G01 注销错误传播、会话撤销/用户状态检查 | 已修：注销错误传播、实时身份与权限，待审 |
| SEC-009 P1 | `app/container.go` FRP 上报/拉命令同用户 JWT 入口 | 用用户身份调用设备命令 API；无独立设备认证模型 | G05 设备 enrollment、独立凭据哈希/轮换/撤销与权限范围 | G01 已记录 [边界设计](DEVICE-IDENTITY-DESIGN.md)，G05 代码与实测待做 |
| SEC-010 P1 | `service/frp_service.go:generateFRPCYaml/SaveConfig` 明文 token 拼入配置并持久化 | 隔离环境读取配置记录/响应；不要记录真实 token | G05 加密敏感配置、脱敏响应、最小 Agent 权限 | G01 已记录设计，明文风险仍在，G05 实施待做 |
| SEC-011 P1 | iOS `Shared/Managers/SessionManager.swift` 用 UserDefaults 存 token | 检查保存/读取路径 | G07 新 macOS 用 Keychain；冻结旧 iOS 开发 | 已确认，待做 |
| SEC-012 P1 | `cmd/server/main.go` 用 `gin.Run` 无显式 HTTP 超时；上传入口缺少请求体总限额 | 慢请求/超大 multipart；需受限资源测试 | G01/G03 显式服务器超时、请求/分片尺寸上限、登录速率限制 | G01 请求头/空闲超时及登录体积/限流已修；G03 上传资源预算和慢请求压测待做 |
| SEC-013 P1 | G01 初版自定义权限覆盖默认路由，导致 GET auth/me、POST auth/logout 被拒绝 | 真实 API 用户仅授予 GET files，me 返回 403；注销同受影响 | 在完整身份认证后独立允许精确方法/自身会话路由，业务与管理员权限不扩大 | 修复前已复现；修复后集成测试通过 |
| SEC-014 P1 | JWT 仅含用户与秒级时间，相同用户同秒登录 token 相同，注销互相影响 | 真实重复登录返回同一 token；固定时刻签发测试 | crypto/rand 256-bit JTI；独立 Redis 会话与单会话注销验证 | 修复前已复现；修复后集成测试通过 |
| SEC-015 P1 | 登录入口未限流，Gin 默认相信任意代理转发头 | 同一实际 IP 在两实例并发 40 次、变更转发头；Redis 不可用仍进入认证 | 登录前 Redis 原子窗口、规范化实际 IP、429/Retry-After、Redis 故障 503、关闭任意可信代理 | 修复前已复现；修复后完整 12 项集成回归通过；分布式撞库仍不在本限流覆盖范围 |
| SEC-016 P1 | 无密码恢复命令及用户全部会话撤销入口，无法安全处理历史账号或丢失设备登录 | 历史明文恢复、当前/旧/延迟签发 Token、多用户隔离、审计故障、旧结构迁移 | G01 auth_version + 受控密码恢复命令 + 精确 logout-all；密码/版本/审计同事务 | 代码已实现，详见 [账号恢复](ACCOUNT-RECOVERY.md)，真实账号未操作 |
| SEC-017 P1 | config.Load 的 JWT_EXPIRE_HOURS 非数字输入静默回退为 168 小时 | 设置 one-hour，Validate 原先通过 | G01 错误文本/溢出转为无效值，由 Validate 在连接前拒绝 | 修复前测试失败，修复后通过；未设置仍为 168 小时 |
| CLIENT-001 P2 | 旧 SettingsViewModel 对普通用户也请求需显式授权的权限汇总，并展示管理员操作入口 | 57 路由契约核对；真实 GET settings/permissions 对普通用户 403 | G07/G09 按服务端能力展示；不放宽后端管理员/跨用户边界，冻结 iOS 功能开发 | 已记录兼容性边界，待新客户端实现 |
| CLIENT-002 P2 | 旧 SettingsViewModel.logout 忽略服务端撤销错误，直接清本地会话 | 静态调用链证据；后端错误已正确返回 503 | G07 保留可重试的远端注销并展示失败状态 | 已记录，待新客户端实现 |
| DEP-001 P1 | 初次 govulncheck 报告 x/net、x/text、quic-go 可达漏洞；旧 Go 标准库另查 | `make vuln`；见 BASELINE | G01 按公告最小升级依赖和工具链，重新构建/扫描 | 已升级；可达/导入包告警 0，另 1 项未调用 OpenPGP 模块告警 |
| DATA-001 P1 | `internal/store/mysql.go:NewMySQL` 每次启动 AutoMigrate，无版本/审核开关 | 审查启动调用链；仅在测试库观察建表 | G01 关闭隐式生产迁移；G06/G10 版本化迁移、备份/回滚演练 | 普通启动不再迁移；显式迁移已测，版本化迁移待 G06/G10 |
| FILE-001 P1 | `storage/mysql/file_repo.go:HasActiveNameConflict` 根目录 parent_id=0 未含 user_id | 两用户根目录创建同名目录；第二个不应冲突 | G02 对根按所有者作用域，非根按共享父目录；事务并发约束 | 已复现跨用户 409；修复后通过根/共享目录与并发测试，待 G02 PR 审查 |
| FILE-002 P1 | `service/file_service.go:InitUpload` 不比较声明分片数与计算值 | total_size=10, chunk_size=5, total_chunks=1 | G03 溢出安全计算、数量及每片/总字节校验 | 已确认，待修 |
| FILE-003 P1 | `CompleteUploadPersist` 无任务行锁/CAS，无完成结果复用；mergeChunks 直接覆盖路径 | 两个完成请求并发/超时后重试；检查重复 file/物理路径损坏 | G02/G03 完成状态机、互斥/幂等结果、临时文件原子落盘 | 静态确认，并发实测待做 |
| FILE-004 P1 | `CompleteUploadPersist` 直接创建同名记录，CreateFolder/Rename 则拒绝 | 同用户同目录同名上传两次 | G02 统一命名/冲突策略，数据库约束及恢复规则 | 应用写入命名锁与数字后缀已修；只读审计已覆盖历史重名/异常标量，最终唯一约束和审核修复仍待做 |
| FILE-005 P1 | `StorageService.GetOverview` 计算配额；InitUpload/CompleteUpload 无配额预留/核销 | 设置低配额后上传超限文件；并发超额 | G02/G03 配额账本/预留/释放，版本和回收站计量政策 | 已确认，待修 |
| FILE-006 P1 | `ensureStorageForFileRepo` 以 hash 去重；PermanentDelete 只删逻辑记录 | 上传重复内容后永久删除，统计物理对象和引用 | G02 显式引用图/版本引用/安全 GC；不直接删未知对象 | 已确认，待设计 |
| FILE-007 P1 | 递归 collectDescendantIDs/ListActiveChildren 仅按 parent_id 查询，授权集中在根 | 混合所有者子目录、共享只读子项、循环/异常目录树 | G02 定义继承语义，逐层授权与循环检测/深度上限 | ZIP/批量下载/复制/移动/永久删除已逐项验证既有 ACL，循环/超限拒绝、树锁后重读；目录继承与扩展并发/性能矩阵待做 |
| FILE-008 P1 | 上传任务无持久 parent_id/结果 file_id；恢复参数和完成目录可变化 | 用原 upload_id 带不同目录/文件元信息继续操作 | G03 固化任务契约并验证重试参数 | 已确认，待修 |
| FILE-013 P1 | 新建/重命名先检查后写入，复制后缀使用秒级时间 | 12 路并发新建实际 9 成功/3 冲突；双 Router 并发复制/重命名 | G02 同目录事务行锁、READ COMMITTED、重读来源、数字后缀 | 修复前已复现；修复后新建 1 成功/11 冲突，复制与重命名回归通过 |
| FILE-014 P1 | 删除目录与新建子项的父目录状态检查不在同一锁定边界 | 并发删除目录/创建子项，再查活跃子项是否指向已删除父目录 | G02 删除锁住子项命名范围，事务内重查空目录和有效父目录 | 8 轮真实 API 竞争检查通过；完整树并发矩阵仍待做 |
| FILE-015 P1 | 批量下载跳过不可访问/缺失对象，ZIP 写入错误被忽略，名称后缀可能重复 | 两用户文件混选；磁盘文件缺失；report.zip 与 report 目录混选 | 先验证完整清单，任何失败中止临时包，预留完整最终 ZIP 名称 | 修复前越权选择仍 200；修复后 403，缺失 404，ZIP 名称/字节与空目录回归通过 |
| FILE-016 P1 | ResolveValidShare 不检查创建者当前状态和权限 | 读授权用户创建链接后撤销其权限或禁用其账号 | 分享详情/下载检查当前创建者；目录子项逐项按创建者检查 | 修复前撤权后仍 200；修复后详情/下载 404；不能以所有者权限代替分享者 |
| FILE-017 P1 | 永久删除删除 operation_log，元数据删除与审计不原子 | 历史审计保留、审计写入失败、混合所有者及仍活动子项 | 保留历史，完整树只允许本人已删除记录，新审计同事务提交 | 其他所有者 403、活动子项 409；错误注入完整回滚，保留历史与客户端 IP 回归通过；物理 GC 待做 |
| FILE-009 P2 | 文件搜索多列 LIKE、ListFiles/目录递归无分页/批量规划 | 数据规模测试、EXPLAIN、查询计数 | G02/G10 索引/游标分页/批量目录查询 | 结构已确认，性能未测 |
| FILE-010 P2 | Go `sendFileWithRateLimit` 已 ServeContent/io.Copy/固定缓冲；客户端 NetworkClient 使用 data(for:) | 大文件峰值内存测试 | G03/G08 新客户端 URLSession 文件下载任务；不重写已流式 Go 普通下载 | 后端整文件内存假设未成立；性能未测 |
| FILE-011 P1 | Share 模型及 CreateShare 只有 code/expiry，无提取码/访问次数约束 | 查看真实字段/API | G06 提取码哈希/访问计数、限速、撤销/到期测试 | 缺功能 |
| FILE-012 P1 | 文件版本只创建/列出，没有专用恢复/上传完整协议；客户端可传 size | 版本覆盖/恢复与配额一致性测试 | G01 先保护 storage_id；G02 完整版本引用/恢复协议 | 缺功能 |
| AI-001 P1 | `triggerAsyncAutoAnalyze` 内存 goroutine，AiTask 仅写状态记录 | 上传后杀进程重启；任务无恢复 worker | G04 事务 outbox、持久任务、租约/超时/重试/诊断 | 已确认，待做 |
| AI-002 P1 | `service/ai_service.go:NLSearch` 解析条件后调用 `storage/mysql/ai_repo.go:Search` 的 LIKE；无 embedding/vector | 执行语义同义查询/带无关干扰的评估集 | G04 独立 embedding、chunk、向量索引、混合检索/文件级 ACL/片段 | 已确认，待做 |
| AI-003 P1 | `buildAutoAnalyzeResult` 忽略 OCR/模型错误并写“分析完成”；照片标签描述请求仅文本 | 关闭 Ollama 上传图片；查看任务状态和描述 | G04 区分提取/OCR/视觉失败、真实视觉输入、可诊断降级 | 已确认，待修 |
| AI-004 P1 | `os.ReadFile` 图片全量载入，PDF/OpenXML 提取有上限但无完整资源预算 | 大图/损坏 PDF/压缩炸弹，受限容器观察资源 | G04 文档解析隔离、页/像素/展开字节限额和取消 | 待安全实测 |
| AI-005 P2 | `handler/ai_handler.go` 与 `service/ai_service.go` 重复提取/模型 helper | 函数清单对比；调用图核对 | G04 统一 provider/parser 接口，保留单一实现与测试 | 已确认，待重构 |
| FRP-001 P1 | `PullNextCommand` LPop 后更新状态，没有租约回收任务 | 领取后 Agent 崩溃；队列命令丢失/卡 running | G05 持久命令/ACK/幂等/超时重发/终态约束 | 已确认，待做 |
| FRP-002 P1 | 仓库没有设备 Agent/frpc 进程管理实现 | 检查 Go cmd 与 Swift 调用，不存在可启动 Agent | G05 官方 frpc、白名单 start/stop/reload/diagnose、自动恢复 | 缺功能；未测隧道 |
| FRP-003 P1 | YAML 使用用户 serverAddr/token/domain 原文拼接 | 输入换行/冒号/特殊字符串，运行 frpc verify | G05 typed config 序列化+官方验证，拒绝注入 | 已确认，待修 |
| FRP-004 P1 | 端口冲突先查后写，server 作用域/唯一性与心跳离线任务需核对 | 并发相同端口、设备失联、配置回滚 | G05 唯一索引/端口预留/周期判离线/版本关联 | 部分实现，未验收 |
| COLLAB-001 P1 | Workspace 成员管理存在；通用文件 ACL 主要用 owner/file_permission，空间角色链路不完整 | 空间 viewer/editor 对文件 CRUD 矩阵；禁止越级授权 | G06 明确空间/文件权限继承和管理员边界 | 待完整矩阵验证 |
| LIVE-001 P2 | LivePhoto 绑定/解绑/查询存在；并发去重及实际播放未测 | 相同 image/video 并发绑定、跨用户配对与 macOS 播放 | G06/G09 唯一约束/权限与原生预览 | 静态审查，未验收 |
| LOG-001 P1 | 多处 `_ = logOperation`，写操作可能无审计；SQL 日志策略未配置 | 注入日志写失败/数据库故障；确保无密钥与文件正文泄露 | G06 事务审计或可靠 outbox、结构化脱敏日志 | 已确认，待修 |
| ENG-001 P1 | 原仓库无 Go 测试，Swift 测试仅模板 | 基线 go test 全部 no test files | G00 加真实测试/CI；后续随修复扩展 | 首批单元/真实 API 回归已通过；全业务覆盖待后续 |
| ENG-002 P2 | 已跟踪 dist/main/*.o/xcuserdata | `git ls-files`；检查工作树文件仍在 | G00 取消跟踪并 ignore；历史 patch 需独立核对后归档 | 当前产物已取消跟踪 |
| ENG-003 P2 | file handler 2165 行、service 1193 行；已有契约与仓储可复用 | 逐功能调用链审查和单元边界 | G02–G06 随业务拆模块，不全盘重写 | 待做 |
| ENG-004 P1 | 无原有 CI/运行监控/备份恢复演练 | 检查 workflows/部署脚本，执行隔离发布演练 | G00 CI；G10 发布/指标/健康检查/备份恢复 | CI 已执行，G00 构建/测试通过、漏洞检查失败；运行治理待做 |
| MAC-001 P1 | `cloudnest_macos/` 空目录 | 文件清单 | G07–G09 独立原生 SwiftUI/AppKit 应用与 Keychain；无 UIKit 依赖 | 未开始 |
| ENV-001 P2 | iOS target 26.2，Xcode 无相应平台；模拟器仅 18.6 可用 | xcodebuild 返回 70，destination 不可用 | 保留失败证据；macOS 用可用 SDK 独立验证 | 环境阻塞；非代码编译结论 |

## 审查边界

本轮遍历了所有后端 Handler/Service/Repository/Middleware/Model 的清单与主要调用入口，深入追踪认证、上传完成、版本、AI、FRP 与客户端网络链路。不是逐行形式化审计，也没有读取生产库结构/索引/数据。真实库与模型差异、递归权限组合、外部模型效果、真实隧道及所有 UI 操作仍需后续实测。

每个问题关闭必须关联代码/PR及测试。静态推断项先补复现；已通过的单文件生命周期不能推导批量/大文件/并发功能可靠。现有系统设置的管理员写检查、Live Photo 双文件权限检查、文件常规下载授权等应复用并补测试。
