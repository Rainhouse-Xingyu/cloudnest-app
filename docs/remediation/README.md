# CloudNest 整改工作台

基线：`cloudnest` / `cbad48baded392fd214150545c5ef861cecc030d`，审查日期 2026-10-21（Asia/Shanghai）。

用户粘贴的任务正文是执行要求。Downloads 中两份参考文档未找到，因此未作为代码事实或额外授权依据。保留 Go 分层单体与现有数据，iOS 仅作为历史参考；macOS 在独立目录开发。

- [里程碑与验收](MILESTONES.md)
- [问题登记表](ISSUES.md)
- [基线验证报告](BASELINE.md)
- [测试执行方法](TESTING.md)
- [G01 安全整改与升级边界](G01-SECURITY.md)
- [现有客户端 API 权限兼容性](CLIENT-API-COMPATIBILITY.md)
- [设备身份边界设计（G05 待实现）](DEVICE-IDENTITY-DESIGN.md)
- [受控账号恢复与会话撤销](ACCOUNT-RECOVERY.md)
- [G02 文件一致性与递归安全](G02-FILE-CONSISTENCY.md)
- [只读文件名称约束预检](FILE-NAME-AUDIT.md)
- [版本化活动文件名唯一约束](FILE-NAME-MIGRATION.md)
- [文件对象发布与版本引用](BLOB-VERSION-CONSISTENCY.md)
- [存储引用审计与版本化保留约束](STORAGE-REFERENCE-MIGRATION.md)
- [物理读取租约与生命周期边界](STORAGE-READ-LEASES.md)
- [显式托管与分阶段 GC](STORAGE-GC.md)
- [容量计量、账本与上传预留](STORAGE-QUOTA.md)
- [G03 传输引擎与完成回执](G03-TRANSFER.md)
- [不可变分片与上传入口预算](STABLE-CHUNKS.md)
- [上传临时对象登记、容量与回收](TRANSFER-REGISTRY.md)
- [上传下载速率策略](TRANSFER-RATE-POLICY.md)
- [零字节文件上传](EMPTY-UPLOADS.md)
- [上传暂停、继续与代次隔离](UPLOAD-CONTROL.md)

状态定义：待做、进行中、代码完成待审、验收完成、外部阻塞。代码存在、构建通过、单项测试通过均不等同于业务验收完成。每个后续 PR 必须更新问题、测试结果及本表，保留失败和未执行项。

G00 已提交 [PR #1](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/1)，G01 持续更新 [PR #2](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/2)，#2 的 base 是 #1 的分支，两者仍为未合并草稿。已核实 #1 的原始漏洞基线失败、#2 首批代码 `cee6db8` 、审查修复 `c61f337`、登录限流 `38416ad` 及账号恢复 `588a187` 的构建/测试/漏洞 CI 全通过；后续提交以对应 SHA 的 PR checks 为准。

G01 本次补齐自定义权限下的会话查询/注销、随机 JTI、客户端 API 契约测试及 Redis 原子登录限流。受控密码恢复/会话撤销已实现，17 项本地集成及迁移回归通过，G01 代码完成待审，最终提交 CI 单独核验；设备身份边界设计已记录，G05 实现前仍保留风险项；下一阶段按依赖推进 G02 文件一致性。G02 已实现命名隔离/事务锁、递归权限整改、版本化活动名称唯一约束及对象/版本一致性整改；追加存储引用审计、两步 RESTRICT 外键、读取租约及显式托管/分阶段 GC，容量账本与上传预留已接入，116 项本地集成回归通过，已提交独立 [PR #3](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/3)，base 为 PR #2 分支；未登记候选崩溃对账、稳定传输协议、目录继承和真实库升级演练仍在推进。G02–G11 未完成，整体目标保持进行中。所有生产数据迁移、旧凭据轮换、真实 FRP 服务器访问和最终发布必须在有相应凭据/授权后执行。

G03 独立 [PR #4](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/4)（codex/g03-transfer-foundation，base 为 PR #3 分支）已追加完成回执/结果复用、不可变分片/入口预算，140 项本地完整集成回归通过；[传输报告](G03-TRANSFER.md) 记录协议、升级及尚未完成的验收。G02 的 a4eaaf0 远端 PR/push 构建、单元/集成及漏洞扫描均已通过。

G03 后续追加持久候选登记、上传临时卷容量、FD 锁/租约、取消/过期回收与维护 CLI，见 [临时对象协议](TRANSFER-REGISTRY.md)。覆盖分片/合并，ZIP/缓存预算及完整传输验收仍待做；此前 140 项是上一批证据，本批 162 项 race 集成通过（256.612s），结果另记 TESTING，不能混用。

G03 又补齐请求参数不可提速、全局/个人高峰只能叠加收紧、所有下载/Range 共用响应限速、坏策略拒绝及流中错误终止，见 [速率协议](TRANSFER-RATE-POLICY.md)。十项新增集成及完整 172 项 race 回归已通过，CI 以 PR 精确提交为准。每次传输限速不等于聚合带宽治理，后者仍待做。

G03 现已支持真实零字节上传（明确 size=0、零分片、直接完成），统一零字节配额/最小临时登记及原回执，见 [空文件协议](EMPTY-UPLOADS.md)；完整 184 项 race 集成已通过（236.112s）。G03 尚有暂停继续、下载恢复和性能/资源治理验收，整体目标保持进行中。

G03 后续已接入真实 pause/resume、期望代次 CAS、在途句柄/提交隔离及暂停检查点保留，见 [控制协议](UPLOAD-CONTROL.md)；完整 196 项 race 集成通过（270.415s）。新 schema 须显式迁移，旧写实例不可混跑；客户端暂停 UI、下载恢复和性能验收仍未完成。
