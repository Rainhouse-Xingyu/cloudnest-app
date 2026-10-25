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
- [下载身份、完整性与续传约定](DOWNLOAD-INTEGRITY.md)
- [ZIP 源校验、临时预算与回收](ARCHIVE-TRANSFERS.md)
- [缩略图解码资源边界](THUMBNAIL-RESOURCES.md)
- [缩略图持久缓存与回收](THUMBNAIL-CACHE.md)
- [大文件恢复与性能验收](TRANSFER-PERFORMANCE.md)
- [下载写入期限与连接释放](DOWNLOAD-WRITE-BOUNDS.md)
- [G04 持久索引任务与实现边界](G04-AI-INDEXING.md)

状态定义：待做、进行中、代码完成待审、验收完成、外部阻塞。代码存在、构建通过、单项测试通过均不等同于业务验收完成。每个后续 PR 必须更新问题、测试结果及本表，保留失败和未执行项。

G00 已提交 [PR #1](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/1)，G01 持续更新 [PR #2](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/2)，#2 的 base 是 #1 的分支，两者仍为未合并草稿。已核实 #1 的原始漏洞基线失败、#2 首批代码 `cee6db8` 、审查修复 `c61f337`、登录限流 `38416ad` 及账号恢复 `588a187` 的构建/测试/漏洞 CI 全通过；后续提交以对应 SHA 的 PR checks 为准。

G01 本次补齐自定义权限下的会话查询/注销、随机 JTI、客户端 API 契约测试及 Redis 原子登录限流。受控密码恢复/会话撤销已实现，17 项本地集成及迁移回归通过，G01 代码完成待审，最终提交 CI 单独核验；设备身份边界设计已记录，G05 实现前仍保留风险项；下一阶段按依赖推进 G02 文件一致性。G02 已实现命名隔离/事务锁、递归权限整改、版本化活动名称唯一约束及对象/版本一致性整改；追加存储引用审计、两步 RESTRICT 外键、读取租约及显式托管/分阶段 GC，容量账本与上传预留已接入，116 项本地集成回归通过，已提交独立 [PR #3](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/3)，base 为 PR #2 分支；未登记候选崩溃对账、稳定传输协议、目录继承和真实库升级演练仍在推进。G02–G11 未完成，整体目标保持进行中。所有生产数据迁移、旧凭据轮换、真实 FRP 服务器访问和最终发布必须在有相应凭据/授权后执行。

G03 独立 [PR #4](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/4)（codex/g03-transfer-foundation，base 为 PR #3 分支）已追加完成回执/结果复用、不可变分片/入口预算，140 项本地完整集成回归通过；[传输报告](G03-TRANSFER.md) 记录协议、升级及尚未完成的验收。G02 的 a4eaaf0 远端 PR/push 构建、单元/集成及漏洞扫描均已通过。

G03 后续追加持久候选登记、上传临时卷容量、FD 锁/租约、取消/过期回收与维护 CLI，见 [临时对象协议](TRANSFER-REGISTRY.md)。覆盖分片/合并，ZIP/缓存预算及完整传输验收仍待做；此前 140 项是上一批证据，本批 162 项 race 集成通过（256.612s），结果另记 TESTING，不能混用。

G03 又补齐请求参数不可提速、全局/个人高峰只能叠加收紧、所有下载/Range 共用响应限速、坏策略拒绝及流中错误终止，见 [速率协议](TRANSFER-RATE-POLICY.md)。十项新增集成及完整 172 项 race 回归已通过，CI 以 PR 精确提交为准。每次传输限速不等于聚合带宽治理，后者仍待做。

G03 现已支持真实零字节上传（明确 size=0、零分片、直接完成），统一零字节配额/最小临时登记及原回执，见 [空文件协议](EMPTY-UPLOADS.md)；完整 184 项 race 集成已通过（236.112s）。G03 尚有暂停继续、下载恢复和性能/资源治理验收，整体目标保持进行中。

G03 后续已接入真实 pause/resume、期望代次 CAS、在途句柄/提交隔离及暂停检查点保留，见 [控制协议](UPLOAD-CONTROL.md)；完整 196 项 race 集成通过（270.415s）。新 schema 须显式迁移，旧写实例不可混跑；客户端暂停 UI、下载恢复和性能验收仍未完成。

G03 已追加同一已校验 FD 发送、强 ETag/完整表示摘要、保守 If-Range 和视频/预览统一限速，并修复缩略图同秒旧缓存及并发发布；完整 206 项 race 集成通过（278.936s），见 [下载协议](DOWNLOAD-INTEGRITY.md)。客户端安全续传、整文件预校验成本、大文件与聚合资源验收仍待做，本批不新增数据库字段。

G03 后续已将普通/批量/公开 ZIP 接入共享临时卷、写前持久增长、活动准入、FD/租约与统一回收，并逐项校验实际复制的源内容；内嵌 ZIP 无额外磁盘包。协议见 [归档传输](ARCHIVE-TRANSFERS.md)，完整回归结果另记 TESTING。缩略图缓存/数量配额、客户端恢复及大文件/聚合资源验证仍未完成。

ZIP 批次最终完整 219 项 race 回归通过（308.177s），单元、四目标后端构建与仓库检查通过；G03 仍进行中，PR/CI 按最终提交核验。

G03 已追加缩略图源体积/头读取/像素上限、进程内解码数量与估算内存准入、有界等待和协作式取消，见 [缩略图资源边界](THUMBNAIL-RESOURCES.md)。持久缓存磁盘预算/淘汰及实际峰值内存仍未完成；最终完整 224 项 race 回归通过（321.014s），结果见 TESTING。

G03 后续将新缩略图缓存接入共享传输卷、独立字节/条目预算、不可变缓存代次、24 小时保留、读取 FD 锁、压力 FIFO 淘汰与统一回收，见 [缓存协议](THUMBNAIL-CACHE.md)。新增 cache_key/唯一索引须显式迁移并同步更新全部实例/worker/CLI；旧未登记缓存保留。最终完整 237 项 race 回归通过（324.857s），证据及未验收范围见 TESTING，整体目标保持进行中。

G03 已完成独立 1 GiB 真实服务验收：16 × 64 MiB 上传在半程暂停/重启后继续，下载取消后强 ETag/Range 接续并校验完整摘要；分片上传 9.741 MiB/s、下载 9.890 MiB/s，两个服务进程 RSS 峰值约 29.4 MiB。方法和局限见 [性能报告](TRANSFER-PERFORMANCE.md)，普通 237 项 race 套件保持独立。单次本机结果不替代聚合带宽、多实例/HTTP2、冷盘 IO 或生产客户端验收。

G03 已补齐下载写入/刷新 30 秒期限、上下文取消唤醒和最终刷新错误传播；独立真实网络套件覆盖 HTTP/1.1、TLS HTTP/2、超时租约释放/同连接后续 Range 与健康低速长下载，见 [写入期限](DOWNLOAD-WRITE-BOUNDS.md)。完整回归另揭示 macOS 首次并发创建卷锁文件的 ENOENT，已改独占创建后打开现存 inode，保留失败记录和原断言；最终验证结果见 TESTING。

本批最终普通 237 项 race 回归通过（329.401s），独立网络两项通过（106.183s），空文件并发完成重复 30 轮通过；完整失败诊断和验证边界已保留于 TESTING。

G04 已开始：上传事务与持久索引任务原子提交，增加租约领取/重试/代次隔离/结果事务；移除吞错的上传后 goroutine。当前任务仅排队，实际处理 worker、向量索引、混合检索及评估仍待接入，详见 [G04 记录](G04-AI-INDEXING.md)。G03 最新 a34ba25 的 PR/push CI 均已通过，Linux 网络夹具修复不改变产品源码。
