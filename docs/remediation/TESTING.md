# 测试执行

从仓库根目录运行：

```sh
python3 scripts/check-repository.py
cd cloudnest_go
make check
make test
make integration
make vuln
```

`make integration` 需要 Docker，创建独立 MySQL 8.0 与 Redis 7.4 容器，使用随机本机端口、测试专用库和账号；不读取 `.env`，不挂载任何既有数据目录。退出时仅删除此次创建的容器及其匿名卷。测试中的文件位于 Go 的临时目录。脚本不会执行生产迁移、清库或启动生产服务器。

集成测试使用 `integration` build tag。直接运行时必须通过测试专用环境变量，并检查 loopback、数据库名、账号和 disposable 标记；推荐始终使用脚本，禁止把已有数据库地址传入。测试失败返回非零；没有 Docker/依赖就标记未运行，不能算通过。

单元测试覆盖 JWT 签名/过期和路径权限匹配；API 集成测试使用真实 MySQL/Redis/文件系统，覆盖登录、未认证/管理员拒绝、目录创建、分片上传、字节一致下载、跨用户隔离、分享撤销、回收站恢复、注销。

G01 追加认证配置、缺失身份声明、历史密码拒绝、注销失败传播、首次管理员引导、版本来源授权与并发冲突、即时账户/角色/路径权限撤销、连接不建表测试。PR #2 审查回归另覆盖受限业务权限下的个人会话操作、同一时刻随机 JTI、真实多会话独立注销、57 组 iOS API 路由/授权契约及常用 API/登录 DTO 兼容性；登录限流进一步覆盖多实例并发额度、转发头伪造、Redis 故障、错误请求计数及 TTL 恢复，密码恢复追加真实 CLI、全会话撤销、旧/延迟 Token、审计故障回滚、旧结构迁移及历史明文恢复；完整集成用例目前为 17 项。最新结果见 [G01 报告](G01-SECURITY.md)。测试中的空表管理员引导场景在已验证的 disposable 数据库事务内执行并回滚，不涉及现有项目数据库。

AI 自动分析是当前实现的后台副作用。集成测试只等待其终态，Ollama 地址故意指向不可用端口，不返回假模型结果，也不将 AI 任务“success”当作语义能力证明。AI 实测与 FRP 隧道实测见各后续阶段。

漏洞检查使用官方 [govulncheck](https://go.dev/doc/tutorial/govulncheck)，固定工具版本；其工具链需求与项目最低 Go 版本分开记录。工具下载失败、漏洞数据库不可达、发现可达漏洞分别记录，不能用忽略退出码制造成功。

iOS 只做历史基线：

```sh
xcodebuild -project cloudnest_ios/cloudnest_ios.xcodeproj \
  -scheme cloudnest_ios -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath /tmp/cloudnest-g00-ios-derived CODE_SIGNING_ALLOWED=NO build
```

当前所需 iOS 26.2 平台组件不可用；不得降低部署目标伪造通过。后续 macOS 工程使用独立构建/测试脚本。

G02 命名首批新增 7 项真实 API 回归，总计 24 项，覆盖根/共享目录、重名策略及字节保留、双实例并发、删除/创建竞争、复制子项权限/循环和非法/长名称。第二批新增 11 项，总计 35 项，覆盖递归 ZIP/批量下载/移动/永久删除的逐项授权、分享撤权/禁用和创建者身份、完整包失败语义、ZIP 名称和字节、审计回滚、循环/超深/重叠根及并发互相移动。另增 2 项只读命名审计回归，总计 37 项，验证实际 CLI、数据库排序规则、根/共享作用域、NULL 标量、无写入以及示例截断时保留完整总数。约束迁移再增 11 项，总计 48 项，覆盖直接 SQL 并发/非法元数据、API 409 回滚、历史冲突拒绝、迁移重试/中断/并发、结构漂移/部分对象拒绝、实际 CLI、旧库启动、原子 DDL 失败及 binary 排序规则。默认 API 夹具安装最终约束，仅历史异常测试在已验证的临时库内显式移除/恢复。详情与未完成范围见 [G02 报告](G02-FILE-CONSISTENCY.md)。

对象/版本整改新增 13 项，总计 61 项；覆盖真实并发同任务完成及跨用户哈希冲突、初始/复制/旧快照引用、历史下载/恢复 ACL/字节/并发、损坏对象修复、审计故障回滚、旧结构显式迁移及旧版本任意引用的来源隔离。首次去重夹具清理过早产生的测试自身 race 已修正并单独重跑，不作为产品 race 证据。完整范围及限制见 [对象与版本报告](BLOB-VERSION-CONSISTENCY.md)。

存储引用保留追加 11 项，总计 **72 项**。2026-10-22 完整 disposable MySQL/Redis/API 回归在 race 开启下通过（61.243s）；go mod verify、make check（vet/build）、make test（race 单元）及仓库检查通过。新增覆盖全部持久引用统计、回收站/未知旧版本保留、直接 SQL 与并发父子引用保护、实际审计/迁移 CLI、101+1 条悬空引用与无历史改写、跨表中断/ledger 间隙/并发/取消、审计后坏行的原子 DDL 失败及结构/额外外键/关闭检查拒绝；详见 [存储引用迁移](STORAGE-REFERENCE-MIGRATION.md)。首两轮定向测试的夹具失败与修正已记录，最终完整回归包含其修复。PR/push CI 按最终提交 SHA 单独核验。读取租约、对象状态和物理 GC 没有因此完成。

读取租约追加 11 项，总计 **83 项**；2026-10-22 最新完整 disposable 回归在 race 开启下通过（71.372s）。覆盖独立/复用 token、路径修复期间固定读取路径、RESTRICT 保留、超原 TTL 续租/取消/过期不复活、失去租约截断输出、首次 503、普通/Range/历史/目录/批量/公开包、真实单连接池、前台 AI、独立后台分析、显式升级/漂移/清理失败、默认 404 及无读取接口。完整用例还覆盖新对象/版本/名称/认证约束的既有回归。go mod verify、make check、make test 及仓库检查通过。定向夹具路由错误与 Gin 404 生命周期修复、单连接池调整分别保留记录；最终完整回归已包含最终修复，详见 [读取租约](STORAGE-READ-LEASES.md)。当前提交的远端构建/测试/漏洞扫描另按 SHA 核验。

显式托管/GC 追加 16 项，总计 **99 项**。2026-10-22 最终完整 disposable MySQL/Redis/文件系统/API 回归 race 通过（89.544s），go mod verify、make check（vet/build）、make test（race 单元）、仓库和 diff 检查通过。覆盖固定两段宽限期、全引用/活动读者保留、隔离恢复、审计/最终 FK 回滚、unlink 与完成记录间恢复、同哈希新发布、旧修复路径保留、双硬链接/冲突目标、损坏/越界/符号链接/可写根拒绝、并发操作者、256 条过期清理、真实 CLI 和不认领旧结构；还核对完整元数据可见性，实际未知/级联/未约束入边、真实触发器和隐藏触发器的低权限账号都拒绝。早期正常权限定向用例通过后，新增权限预检曾误拒夹具库级授权，修正授权模式核对后最终 99 项全部重跑通过，未把早期结果当作最终证明。临时 MySQL 的 PROCESS/触发器配置仅用于这些隔离测试。真实断电、生产备份恢复及未登记候选对账不在这 99 项证明内，见 [GC 报告](STORAGE-GC.md)。最终 SHA 的 PR/push CI 另行核验。

容量账本/预留追加 17 项，总计 **116 项**；2026-10-22 完整 disposable MySQL/Redis/文件系统/API 回归在 race 开启下通过（111.682s），最终 17 项额度定向再次通过（14.135s）。覆盖 12 并发预留、缓存不可作为权威、固定参数/目录/旧任务、核销/复制/版本/回收站、共享写入的所有者计量、额度降低/到期/发布中到期、取消与完成竞争、审计回滚、未知历史不泄露/错误目录指针复制拒绝、计量异常不能变零/仍可取消、默认配置优先级/非法配置、配置锁大小写别名及显式升级。go mod verify、make check、make test 通过，最终 SHA 的远端 CI 单独核验。首轮既有回归的两处夹具适配和版本 handler 的 500/507 传播修复详见 [额度报告](STORAGE-QUOTA.md)，未将失败计入通过。临时磁盘预算、分片稳定写/清理、空文件和大文件不在本批证明内。

G03 完成回执新增 8 项，总计 **124 项**；完整 disposable MySQL/Redis/文件系统/API 回归 race 通过（123.616s），go mod verify、make check、make test 通过。覆盖真实 TCP 提交后响应丢失、重建服务容器/过期/配置变化下复用、身份与固定目录、当前文件改动/永久删除不复活、不授予来源能力、回执与发布/计量/审计回滚、旧结构显式升级/漂移/损坏拒绝、分片元数据及物理清理并发窗口；既有候选竞争仍验证只发布一次和字节可下载，改为要求两次相同成功回执。首轮夹具编译错误及永久删除后实际 403/假设 404 的失败保留在 [G03 报告](G03-TRANSFER.md)，最终完整回归已包含修正。尚未实测断电/杀进程、稳定分片、临时空间预算及大文件；远端 CI 按本批最终 SHA 核验。

G03 不可变分片/入口预算新增 16 项，总计 **140 项**；2026-10-23 最终完整 disposable MySQL/Redis/文件系统/API 回归 race 通过（144.943s）。覆盖同/异内容并发、失败重试不删原片、精确末片和清单哈希、篡改/越界/符号链接、元数据回滚、旧结构不认领/回执兼容、JSON/声明/有无 Content-Length 的元信息预算、共享/过期/全局准入、实际 Redis 脚本 ACL 拒绝、真实 TCP 正常 Content-Length/chunked 上传、慢体与错误请求快速结束、可取消限速和晚到写入与取消/完成竞争。前轮截断体 500 已修成 400；首轮完整回归 169.533s 的唯一失败为大头部夹具少了 multipart 换行，已纠正并通过真实 chunked TCP 定向及最终完整重跑。内存 Recorder 的期限适配不用于真实 TCP 路径。go mod verify、make check、make test 通过；远端结果按最终 SHA 核验。完整协议/未做项见 [不可变分片报告](STABLE-CHUNKS.md)，不把入口并发预算当作持久磁盘容量或断电恢复证明。

G03 持久上传临时登记/预算追加 **22 项**，总计 **162 项**；2026-10-23 最终完整 disposable MySQL/Redis/文件系统/API 回归开启 race 通过（256.612s）。本批含真实 20 秒租约续约并越过原期限、持锁子进程 SIGKILL、实际 Go 服务 SIGTERM 中断未完成 HTTP body、并发容量/合并额度、取消/过期回收、半登记/替换 inode/未知别名保留、删除意图与 unlink 后审计失败恢复、分批 64+6/未知文件保留、真实 statfs 余量、策略/错卷/错误数据库拒绝、真实及隐藏触发器拒绝、显式旧结构升级、实际维护 CLI 和预创建失败后重试。完整边界/失败记录见 [临时对象报告](TRANSFER-REGISTRY.md)。此前 159 项完整运行因两个错误状态码差异失败，修正后定向又暴露旧夹具重建 inode 的成功假设，现改为恢复已登记的原 inode，最终 162 项包含所有修正；未放宽身份验证。

go mod verify、make check（vet/build）、make test（race 单元）、仓库/diff 检查通过；后端 darwin/linux × amd64/arm64 四种目标均交叉构建通过，不代表 macOS GUI 构建或真机验收。集成测试超时从 3 分钟调为 5 分钟，以容纳真实续约等待和新增回归，仍保留 race 和全部用例；最终本地运行同时进行交叉构建，耗时不是性能基准。远端 CI 按本批最终 SHA 单独核验。ZIP/缓存预算、暂停继续、空文件、下载恢复/速率策略、HTTP2、大文件/内存、主机断电和生产副本升级恢复未完成；测试只使用新建测试容器/临时目录和本次创建的子进程。

G03 不可提高的速率/高峰/Range 策略追加 **10 项**，总计 **172 项**；2026-10-23 完整 disposable MySQL/Redis/文件系统/API 回归开启 race 通过（233.478s）。覆盖实际上传和普通/Range 下载的高 query 绕过、个人关闭不覆盖全局高峰、六类下载坏策略拒绝、无效/重复参数不创建候选、数据库读取错误拒绝、真实 HTTP 写入失败截断/无错误 JSON/租约释放、个人窗口事务回滚、管理员并发更新下的一致策略，以及 suffix/multipart/If-Range/416/304。修改前同两项 API 复现测试实际失败：64 KiB 上传约 146 ms、32 KiB 普通/Range 约 10/22 ms；修复后约 1 秒/0.5 秒且字节正确。没有将修复前失败计为通过，小负载速率不当作性能基准。

make check（vet/build）、make test（race 单元）、go mod verify、仓库/diff 检查和后端 darwin/linux × amd64/arm64 交叉构建通过。完整回归运行期间另补齐配置键大小写/空白别名归一化与歧义拒绝，并单独通过 service/handler/middleware 的 race 单元，避免键别名被错当成未配置；最终提交的全量 CI 再按精确 SHA 核验。协议、兼容性和未完成的聚合带宽/慢下载/HTTP2 等范围见 [速率报告](TRANSFER-RATE-POLICY.md)。

G03 零字节上传追加 **12 项**，总计 **184 项**；2026-10-23 最终完整 disposable MySQL/Redis/文件系统/API 回归开启 race 通过（236.112s）。覆盖明确 0 与缺少/null/负数 size、零分片声明与拒绝空分片、实际空文件/空 SHA-256/Content-Length: 0、逻辑满额允许真实零字节但不允许额外字节、最少 1 字节临时登记及 507、八请求竞争只发布一次、所有者/固定参数/目录/进度/改名回执、发布审计回滚、跨用户去重/删除后一方仍可下载、历史恢复/复制/分享、取消/过期/旧协议及畸形零任务拒绝、未知分片行/外部路径保留，以及坏配额/临时策略拒绝。

修改前正确编译的端到端测试实际得到初始化 400；最初夹具曾引用不存在的 StorageOverview.AccountingValid 而编译失败，后改查实际账本。首轮十一项扩展运行的唯一失败为改名夹具使用 POST/filename，改为既有 PUT/name 后加入策略故障用例并完整重跑十二项及全部 184 项；未修改产品路由/DTO 迎合测试。make check、make test（race）、go mod verify、仓库/diff 检查和后端 darwin/linux × amd64/arm64 交叉构建通过；最终提交 CI 按精确 SHA 核验。没有新增数据库列，没有操作生产数据；旧实例/回滚边界与仍未建立的数量配额见 [空文件报告](EMPTY-UPLOADS.md)。

G03 暂停/继续与代次隔离追加 **12 项**，总计 **196 项**；2026-10-23 最终完整 disposable MySQL/Redis/文件系统/API 回归开启 race 通过（270.415s）。覆盖确认分片/预留/期限/进度保留、幂等与过时控制命令/边界输入/所有者、跨容器快速暂停恢复的旧写入拒绝、旧预检及 ready 合并不能发布、审计回滚、暂停额度/到期/取消/零任务、目录撤权/显式业务权限、持锁回收与暂停检查点保留、显式代次迁移和类型漂移、真实 TCP 本地与跨容器 body 中断（后者约 20 秒）、HTTP ready 发布中暂停返回 409，以及 X-Upload-Epoch 防止晚解析/旧客户端请求隐式获得新代次。已完成回执仍允许无代次头确认，未控制的 epoch=0 上传兼容旧请求。

修复前真实 pause 为 404。初期有一处候选构造夹具漏新参数的编译失败；首轮扩展的 race 为测试移除 GORM 钩子与发布后的 AI goroutine 竞争，已将移除提前到最终发布前。真实 TCP 初轮把受控取消的元数据响应改成 503，已保留句柄取消原因，并限定仅无 Pin/无 Pin 错误的元数据响应可在请求取消后发送；持有/失效/失败读取保护不放宽。相关定向读取/TCP 回归通过后，先完成 195 项回归（281.995s），再补入口头代次并完整重跑最终 196 项。最终没有数据竞争或失败。

make check、make test（race 单元）、go mod verify、仓库/diff 检查及后端 darwin/linux × amd64/arm64 交叉构建通过。集成总期限从五分钟改为六分钟：既有完整运行接近五分钟，新增跨容器真实续约等待；保留全部用例、race 和各操作原有期限，耗时不作为吞吐基准。最终提交 CI 按精确 SHA 核验。两列须显式 migrate 且停旧实例，尚未操作真实库；客户端 UI、下载恢复/强校验及大文件等未完成范围见 [上传控制](UPLOAD-CONTROL.md)。

G03 下载身份与一致出口追加 **10 项**，总计 **206 项**；2026-10-23 最终完整 disposable MySQL/Redis/文件系统/API 回归开启 race 通过（278.936s），专项 10 项通过（15.153s）。覆盖强 ETag/完整 Repr-Digest、同秒日期 If-Range 回退、强/弱/尾随/旧标签、If-Match/If-None-Match/416、历史/分享/ZIP/批量、损坏条件请求拒绝、视频/PDF/Office/文本限速、并发缩略图原子发布与源切换、撤权/禁用、真实 TCP 中断后匹配表示重组、multipart 和空内容替换。修改前两项真实复现分别因无身份头和日期续传返回新内容尾部 206 失败；修复后通过。初期 unused mime 编译失败已移除；定向回归纠正错误头清理以保留标准 416 Content-Range，并在前置错误删除旧 Content-Length，未降低产品断言。

make check（vet/build）、make test（race 单元）、go mod verify、仓库/diff 检查及后端 darwin/linux × amd64/arm64 交叉构建通过。FD 路径替换与错误头清理单元通过；完整集成结束后追加的旧 MD5 拒绝/文件不改写单元另行通过 blob/handler race。产品源码与完整 206 项运行相同，最终精确提交另核验 CI。保持六分钟集成期限、全部用例和 race；没有把运行时间当性能基准。每次整文件预校验、图片解码、客户端持久化续传、缓存/ZIP预算及其他未验收范围见 DOWNLOAD-INTEGRITY。

G03 ZIP 源校验与共享临时预算追加 **13 项**，最终总计 **219 项**；2026-10-24 完整 disposable MySQL/Redis/文件系统/API 回归开启 **race 通过（308.177s）**。覆盖实际损坏源/额度仅 1 字节的原失败复现、普通/公开/批量内嵌结构、无虚构上传任务/无额外内层临时包、跨 Manager 并发增长、共享准入及上传共用额度、过期 FD 保留/关闭后回收、真实 20 秒续约、路径替换不重开及保留、unlink 后 done 审计故障恢复、公开/批量损坏源不返回成功、实际 TCP 断开后释放归档/源租约、增长失败先于 IO、完整 JSON 及尾随输入限制、过期不扩容/不封存和封存 FD 破坏拒绝；批量业务审计保留。

修改前两项 API 都返回 200，修复后分别拒绝完整性 409/临时空间 507。首轮扩展有一处登记行统计未按当前 fixture 用户限定，误纳入之前用例，已修正并重跑，产品断言没有放宽；首轮完整 218 项通过（314.622s），补充业务审计与过期/坏 FD 边界后再审查发现 JSON 首值后未检查输入结束。为纳入该修复主动终止一次中间运行（不计通过），定向尾随空白/第二值回归通过，再完整重跑最终 219 项；最终无失败或数据竞争。

最终源码 make check（vet/build）、make test（race 单元）、go mod verify、仓库/diff 检查，以及后端 darwin/linux × amd64/arm64 四目标交叉构建通过。保持六分钟集成期限和所有用例/race，不将耗时作为性能指标。测试仅使用本次新建容器、临时目录、测试自己的网络连接与子进程；没有操作生产/历史数据。归档 SIGKILL/断电、本机恢复、大文件/峰值内存/吞吐/HTTP2、缓存/数量预算、客户端持久化等未验收范围见 [归档协议](ARCHIVE-TRANSFERS.md)。最终远端 CI 以本批精确提交核验。

G03 缩略图解码资源边界追加 **5 项**集成回归，2026-10-24 最终完整 **224 项 race 通过（321.014s）**。真实 MySQL/Redis/文件系统/API 覆盖超限 PNG 配置在像素解码前拒绝、坏 PNG/JPEG/GIF 编码 422/未知格式 415、正常三格式实际 JPEG 输出尺寸/摘要/304、跨用户先拒绝访问、旧缓存不绕过输入上限，以及实际 64 MiB + 1 字节稀疏源返回 413 且保留。既有并发缩略图发布/同秒源版本切换、上传恢复、迁移、租约与归档回归全部保留。定向四项新测试加原缩略图测试通过（8.998s），后补编码体积用例单独通过（3.167s），最终全量包含五项及后续头读取健壮性修正。

修复前有效但超限 PNG 头的真实 API 复现返回通用 500，未声明资源拒绝；修复后 413。没有构造巨幅像素数组来复现，未让服务尝试危险的大图分配。新增六项单元验证维度边界、估算内存/活动数/等待队列、取消/超时/释放、头读取与编码体积、取消不写缓存及解码后几何不一致；最终 make check（vet/build）、make test（race）、go mod verify、仓库/diff 检查和 Go 后端 darwin/linux × amd64/arm64 四目标交叉构建通过。没有新增依赖/数据库列，也未改冻结的 iOS 工程。

六分钟集成期限、全部既有测试及 race 保留，最终无失败/数据竞争。输入/准入边界不构成 RSS 硬上限或大图性能指标；解码取消是协作式，源预哈希聚合 IO、多副本预算、缓存磁盘容量/淘汰与崩溃对账仍待做。详见 [缩略图资源协议](THUMBNAIL-RESOURCES.md)。本批最终提交的远端 CI 单独核验。

G03 新缩略图缓存登记与生命周期追加 **13 项**，2026-10-24 最终完整 **237 项 race 回归通过（324.857s）**，无失败/数据竞争。最终缓存/缩略图/归档专项 **33 项通过（57.322s）**；覆盖 1 字节共享卷的真实绕过复现、跨 Manager 同键复用、实际 JPEG 字节持续计量、24 小时到期与 SH 读取锁、只淘汰空闲受控缓存、未登记旧文件保留、重复/遗弃/过期生成者、缓存本身坏摘要与路径替换、显式 cache_key/唯一索引升级检查、数量准入、ZIP/缩略图共享活动槽、发布审计失败回滚、unlink 后 done 审计失败的计量与恢复、真实 TCP 取消后释放读锁、单输出超限先于 IO/淘汰、缓存命中仍检查源 ACL/完整性，以及坏策略/root binding 不读不淘汰。

修复前，真实 API 在临时卷额度仅 1 字节时仍生成并返回 200 JPEG；修复后 507。首轮新增回归中有一处夹具以未规范化 t.TempDir 路径查询已规范化卷而未找到记录，改用真实路径；扩展运行又发现夹具按 64 像素输出估算的容量不足以单独容纳 96 像素输出，现明确留足单项空间并仍断言两个变体无法共存、读取中不可淘汰。产品资源/身份断言没有放宽。数量上限用 4096 个明确 blocked/不确定预留验证，它们不是成功生成的图片，也没有冒充 4096 张实际图片压力测试。

最终源码 make check（vet/build）、make test（race）、go mod verify、仓库/diff 检查及后端 darwin/linux × amd64/arm64 四目标交叉构建通过；完整回归保留原六分钟期限、全部旧用例和 race。新列/索引在隔离测试库显式迁移，未修改生产库、未合并 PR 或部署服务。缓存写进程 SIGKILL/断电、本机恢复、历史缓存人工接管、多副本 CPU/IO、大图/大文件吞吐/RSS、全业务元数据数量及客户端持久化恢复仍未验收，详见 [缓存协议](THUMBNAIL-CACHE.md)。最终提交 CI 按精确 SHA 另行核验。

G03 大文件验收新增独立 `integration,performance` 用例和 `scripts/test-transfer-performance.sh` / `make performance` 入口，复用隔离容器和数据库保护，单独 15 分钟期限；普通集成仍为 237 项、race、6 分钟。2026-10-24 在 Apple M1 / 16 GiB / Darwin arm64 / Go 1.26.10 上实际通过（用例 220.93s、包 222.782s）。16 个不同内容的 64 MiB 分片实际上传 1 GiB，半程 pause + SIGTERM 重启保留所有确认片/epoch，resume 后续传，完成回执复用；下载 200 在 64 MiB 取消，新的精确 If-Range 请求 206 接续，整体 SHA-256 一致。分片上传 105.124s / 9.741 MiB/s，complete 3.908s；下载 103.535s / 9.890 MiB/s，首次/接续首字节 0.559s / 0.578s；两次服务进程峰值 RSS 分别 30,785,536 / 30,851,072 字节。

被测服务是独立非 race 二进制，测试驱动启用 race，RSS 来自成功退出后的操作系统 Rusage；不包含驱动或 MySQL/Redis，也不代表全机页缓存占用。性能测试本机编译、vet、Linux amd64 测试二进制交叉编译、脚本语法/仓库/diff 检查通过；CI 新增只编译性能标签，实际 1 GiB 运行结果单独记录。产品源码与 ca4ba91 相同，已有 237 项本地完整回归仍适用，最终提交远端 CI 另核验。详见 [性能报告及原始 JSON](TRANSFER-PERFORMANCE.md)。仍未验收冷盘、多用户/多实例/聚合 IO、HTTP2、1 TiB 和生产客户端持久化恢复。

G03 下载阻塞写入治理追加两个独立 `integration,network` 测试函数（包含 HTTP/1.1 与真实 TLS HTTP/2 子例），本地通过 **106.183s**；TCP 停止读取时 handler 自行终止、租约释放、最终正文为截断原始前缀，拒绝以客户端自己的读取超时代替服务端断流；HTTP/2 单流超时后同一连接继续完成 Range。1 KiB/s 的健康 HTTP/2 下载持续 32.24s 并完整成功，证明没有引入 30 秒固定总时长。首轮网络运行 103.697s 通过，随后加强 TCP 断流断言、扩大读取缓冲并排除客户端超时，最终结果为上述 106.183s。修复前真实 HTTP/1.1 在停止接收后 45 秒仍未退出（用例失败），没有把连接主动关闭作为通过依据。

新增两项传输单元测试验证最后一段 Flush 错误穿过 Gin 仍被报告，以及真实 HTTP/2 已阻塞在 Write/Flush 后，服务端作用域取消一秒内终止 IO（独立十秒写入期限未耗尽）。源、租约、速率、下载和缩略图的 49 项专项回归通过（59.951s）。普通完整回归首轮为 236 通过 / 1 失败（337.196s），失败是既有空文件八请求竞争完成出现 503，而非被忽略的绿灯。

该失败定位到本机首次并发 openat(O_CREATE) 打开 `.transfer-admission-v1` 返回 ENOENT。原真实 API 用例重复 30 轮，7 轮失败；只操作新建临时目录的最小复现中，普通创建 4,800 次有 493 次失败，独占创建后仅打开现存文件同量运行零失败。产品现采用后者，保留已有 gate inode/内容和只读不创建约束；新增 64 × 16 并发打开者的 inode/权限测试及只读/既有内容测试。修复后原 API 用例连续 **30 轮通过（23.487s）**，仍仅允许 200/409 并检查单个文件/版本与无额外预留，未放宽为允许 503。临时诊断输出已移除，原用例只改为在失败时附带响应正文。

2026-10-24 最终 **237 项完整 race 回归通过（329.401s）**，无失败/数据竞争，六分钟期限不变。最终 make check/test、四目标 darwin/linux × amd64/arm64 后端构建、模块校验和仓库/diff 检查通过。CI 新增执行独立网络入口（三分钟），它不替代普通回归；生产源码未新增 schema/依赖/TLS 部署开关。写入期限不约束源预哈希/磁盘 IO/数据库总耗时，聚合公平性、多流压力、生产客户端恢复等仍未验收，见 DOWNLOAD-WRITE-BOUNDS。最终精确提交的 CI 与后续 1 GiB 复测单独核验。

写入期限与卷初始化修复的产品提交 b8cdfd2 随后独立完成 1 GiB 复测（2026-10-24；用例 219.79s / 包 221.774s）：上传 104.949s / 9.757 MiB/s，complete 4.344s；下载 103.575s / 9.887 MiB/s，初始/Range 首字节 0.570/0.610s；两次服务进程峰值 RSS 30,064,640 / 30,539,776 字节。半程暂停/重启续传、完成回执、实际连接取消后的 206 接续与完整 SHA-256 全部通过，见 TRANSFER-PERFORMANCE 和对应原始 JSON；不把单次本机结果外推多用户或生产性能。

2026-10-25 首次远端 Linux 网络专项揭示夹具的跨平台问题：服务端 handler 已按期限退出、租约已释放，但测试将客户端 SO_RCVBUF 缩至 1 KiB，使 Linux 内核排队数据在关闭后仍缓慢排出，最终命中测试客户端自己的读取期限。该次 PR/push CI 网络项失败，普通 237 项和漏洞检查通过；没有将失败标为产品验收成功。夹具现改为限制测试服务端 SO_SNDBUF（16 KiB），保留客户端正常接收窗口，继续用真实 TCP、默认 30 秒写入期限、租约计数、完整截断前缀和“读取错误不能是客户端 timeout”断言。产品代码未改变。修正后 macOS 独立网络 race 测试两项通过（104.087s）；另以交叉编译的非 race Linux arm64 测试二进制，在新建 MySQL/Redis 共享网络命名空间和 Alpine 容器内实际执行两项，同样通过。Linux 预检没有冒充 race 验收；最终 GitHub Linux race 网络项按新提交重新核验。

## 2026-11-02 G05 独立身份

`scripts/test-integration.sh -run '^TestAgent'` 通过五项 race 集成，包耗时 9.574s。实际编译的服务完成 TLS 请求、注册码兑换、心跳及撤销。低权限账号缺少 TRIGGER 时，普通 definer 视图初版无法验证定义，已改为只读 definer 例程。首次例程测试因测试用户缺少授予权限失败，调整一次性数据库的授权后验证了触发器缺失、同名定义漂移和显式迁移拒绝覆盖。脚本只对新测试数据库授权。构建、vet 与全量 race 单元通过。详见 [G05 记录](G05-DEVICE-IDENTITY.md)。

第一次普通全套运行发现归档回收和上传清理拒绝了四个新触发器。原传输清理契约拒绝库内的任何触发器。现先验证维护连接的元数据可见性，再读取完整触发器清单，核对名称、目标表、事件、时机和实际动作。未知触发器、缺失触发器、同名改写及权限不足均拒绝清理。针对触发器漂移、隐藏触发器和受影响的归档/上传路径重跑通过，包耗时 14.586s，见 `/tmp/cloudnest-g05-agent-transfer-guard.log`。

官方 FRP 0.71.1 已用于实际 TCP/HTTP/HTTPS 回环转发测试，三个请求均到达上游，race 包通过（1.909s）。本次启动者是测试程序。Agent 派发、进程控制及重连仍待验收。

## 2026-11-03 G05 配置边界及回归诊断

G00/G01 状态再次核对。PR #1 仍开放，原始漏洞基线检查失败。PR #2 仍开放并基于 G00 分支，`588a187` 的构建/测试及漏洞检查全部通过。当前 G05 分支包含该提交，自定义权限下的 me/logout、固定时刻随机 JTI、独立登录会话及客户端契约回归保留。

触发器修复 `f631cc9` 的两次远端 CI 已通过，运行 ID 为 `41396504541`、`41396499491`。本机修复后的完整集成仍以失败记录，包耗时 360.712s，触发六分钟总时限。此前 231 个顶层用例通过、无顶层失败，超时时跨实例暂停用例仅运行了 4 秒。该用例需要等待真实 20 秒心跳检查。独立重跑该用例通过，用例 22.10s、包 25.577s。保留 `/tmp/cloudnest-g05-full-fixed.log` 和 `/tmp/cloudnest-g05-config-api.log`，不将局部通过写成完整套件通过。

新增配置回归覆盖 YAML 标量原值保留、注入/模板/边界拒绝，以及实际编译 TLS 服务的预览、保存、历史和回滚响应脱敏。官方 FRP 0.71.1 用含空格、冒号和井号的凭据验证三条实际隧道仍通过，race 包 1.891s，日志 `/tmp/cloudnest-g05-frp-native-quoted.log`。构建、vet、全量 race 单元、模块校验及仓库检查通过。

最终按测试清单将普通套件分为 `^Test[A-M]` 与 `^Test[N-Z]` 两批串行运行，各自使用新建 MySQL/Redis，并保留 race 和每批六分钟期限。两批分别 **92 项通过（140.961s）**、**151 项通过（263.567s）**，合计 **243 项**，无失败、跳过或数据竞争。通过用例集合与 `go test -tags=integration -list '^Test' ./integration` 清单逐项相等，无遗漏或重叠。命令为 `scripts/test-integration.sh -run '^Test[A-M]'` 和 `scripts/test-integration.sh -run '^Test[N-Z]'`，日志为 `/tmp/cloudnest-g05-config-batch-am.log`、`/tmp/cloudnest-g05-config-batch-nz.log`。这是分批完整覆盖的结果，单次默认六分钟命令在本机超时的记录保留；脚本默认超时和 CI 完整套件入口未改动。新增 TLS 配置用例自身耗时 3.89s。

## 2026-11-03 G05 加密配置

新增 AES-256-GCM 配置写入、绑定设备/版本的认证数据、私有密钥环加载、事务审计和显式审计/迁移 CLI。单元测试覆盖随机 nonce、逐字节篡改、跨设备/版本搬用、错误密钥、未知版本、明文拒绝、密钥文件权限/链接/体积及格式错误。实际编译的 TLS 服务验证新配置数据库中不再含凭据明文，回滚内容相同但密文和版本身份不同，管理响应仍脱敏，保存/回滚审计保留来源 IP。

首次专项集成失败源于测试共用一次性数据库，先前配置记录仍被全库迁移扫描到。已仅回收各测试自己创建的配置并将业务计数限定到本设备，没有更改迁移的全库范围。原失败日志保留在 `/tmp/cloudnest-g05-encryption-api.log`。修正后的三项 FRP 专项通过，包耗时 10.339s，日志 `/tmp/cloudnest-g05-encryption-api-v2.log`。实际 CLI 覆盖只读审计、明文迁移、重复执行不改写、缺旧密钥失败、旧密钥重加密及退役后读取；仓储测试覆盖越权、密文搬用、审计失败回滚、损坏行停止及重新执行恢复。

前一配置边界提交 `d6d4d46` 的 PR CI `41580652999` 和 push CI `41580647351` 均已通过，当前加密提交的 CI 单独核验。部署和剩余风险见 [配置加密](FRP-CONFIG-ENCRYPTION.md)，未操作生产配置或真实凭据。

加密版本最终按完整清单分批通过 **94 + 151 = 245 项普通 race 集成**，两批包耗时分别 186.731s、250.538s，无遗漏、重叠、失败、跳过或数据竞争。仍使用原隔离脚本、每批六分钟期限，日志 `/tmp/cloudnest-g05-encryption-batch-am.log`、`/tmp/cloudnest-g05-encryption-batch-nz.log`；实际通过集合与当前 `go test -tags=integration -list '^Test'` 清单相等。构建、vet、全量 race 单元、darwin/linux × amd64/arm64 四目标构建及仓库检查通过。新增系统调用只用于读取私有密钥文件，未修改宿主密钥、生产数据或历史备份。

## 2026-11-04 G05 持久命令

前一配置加密提交 `6779778` 的 PR `41582159454` 与 push `41582152998` CI 均通过。当前命令批次新增 agent_command 表、显式迁移/启动索引检查、管理端幂等入队、设备租约领取/续租、限定配置读取及幂等终态回执。

首轮并发测试失败，日志 `/tmp/cloudnest-g05-command-api.log`。默认 REPEATABLE READ 的管理事务先读取所有权提示再等待设备行锁，后续普通查询仍使用旧快照，导致并发相同请求 ID 触发唯一约束错误。事务改为 READ COMMITTED 后，原测试通过；追加不同请求并发争用最后两个队列名额，验证最多 32 条活动命令。保留唯一索引和原断言，没有将重复键异常静默视为成功。

四项专项在 `/tmp/cloudnest-g05-command-api-v3.log` 通过（9.341s），覆盖实际编译 TLS 服务、用户 JWT 拒绝、跨设备拒绝、配置内容变更检测、回执重试、领取/完成审计故障回滚、租约到期重领及旧回执拒绝、凭据代次隔离、命令期限和重试上限，以及索引丢失后启动失败。进一步将命令时间以数据库 UTC 微秒值和独立 DATETIME 编解码保存，避免 DSN loc=Local 影响租约；第五项用 UTC 与 Asia/Shanghai 两个实际连接双向入队、领取、续租和完成，最终五项专项通过（12.238s），日志 `/tmp/cloudnest-g05-command-api-v4.log`。

服务管理器重建使用相同数据库记录恢复状态。上述结果不替代 Agent 本地执行记录、实际进程崩溃对账和断线重连验收，见 [命令协议](FRP-DURABLE-COMMANDS.md)。

最终普通 race 集成按清单分批通过 **99 + 151 = 250 项**，包耗时 154.721s、244.380s，无遗漏、重叠、失败、跳过或数据竞争。每批保留原六分钟期限，日志 `/tmp/cloudnest-g05-command-batch-am.log`、`/tmp/cloudnest-g05-command-batch-nz.log`。构建、vet、全量 race 单元、darwin/linux × amd64/arm64 四目标构建与仓库检查通过。测试使用新建隔离 MySQL/Redis，本批未操作生产数据库。

完整回归后进一步要求所有失败的配置响应也不包含测试凭据，针对性重跑实际 TLS 配置/回执用例通过，包耗时 6.393s，日志 `/tmp/cloudnest-g05-command-disclosure.log`。生产实现未因此更改。

## 2026-11-04 G05 前台 Go Agent

前一持久命令提交 `dba212c` 的 PR CI `41749572937`、push CI `41749567346` 均完成并通过。本批新增前台 Agent CLI、HTTPS 客户端、私有状态与继承锁、固定官方 frpc 校验、本机配置授权、执行记录及进程管理，不修改生产数据库或旧设备。

首轮实际 CLI 集成失败，日志 `/tmp/cloudnest-g05-agent-native-integration.log`（70.190s）。Agent 心跳版本字符串使用 `/`，不符合既有服务端允许字符，故始终未领取命令。改用 `cloudnest-agent-0.1.0`，并将永久协议错误改为退出而非静默无限重试。第二轮真实 CLI、TLS 服务、隔离 MySQL/Redis 和官方 frpc/frps 完成三种隧道、reload、stop/start、正常 Agent 重启、frps 重连与恶意配置拒绝，日志 `/tmp/cloudnest-g05-agent-native-integration-v2.log`，测试 22.36s、包 24.215s。

后续新增“未完成租约失效关闭意图”测试首次断言失败，`/tmp/cloudnest-g05-agent-final-native.log` 与 `/tmp/cloudnest-g05-agent-final-check.log` 保留失败。原因是测试把 JSON 再次解码到已有非零对象，已省略的 pending 字段保留旧值；磁盘记录和执行器状态正确。改为读取到新的零值对象，未改变协议或放宽断言。

最终使用新下载并验证归档的 `scripts/test-agent-native.sh` 在 Darwin arm64 完整通过：**9 项 Agent 顶层 race 测试**（3.973s）和 **1 项实际 CLI/后端/隧道 race 集成**（测试 22.19s，包 24.147s），零跳过。日志 `/tmp/cloudnest-g05-agent-final-native-v2.log`。最终集成还为 frps 配置真实临时证书并要求 TLS，Agent 使用本机指定 CA 校验 FRP 控制连接；HTTPS 映射客户端独立验证上游证书。原生锁测试使用真实官方 frpc 验证父描述符关闭后拒绝第二 Agent，子进程退出后释放。

最终 `make check test build-macos build-linux` 通过，含 vet、全部普通 race 单元、服务端与 Agent 的 darwin/linux × amd64/arm64 构建，日志 `/tmp/cloudnest-g05-agent-final-check-v2.log`。仓库检查和 diff 检查通过。本批本地未重复前一提交的 250 项普通数据库回归；Linux CI 保留完整原测试并新增 agent-native 作业，具体提交结果另行核验。四目标构建不代表四平台运行验收；本地真实隧道平台为 Darwin arm64。

尚未验证或完成：系统级 SIGKILL/掉电自动接管、长时间离线/休眠、连接级状态/日志、Keychain、本地配置加密及真实公网部署。正常 `ok` 回执只表示本地进程操作，隧道可达性由上述端到端访问另行证明，不能把进程存活推广为所有映射健康。运行方法与边界见 [Agent 文档](FRP-AGENT.md)。

提交前复核让 Agent 退出时传播子进程停止失败，并把响应上限测试改成有效但超限的 JSON。随后九项原生/race Agent 测试再次通过（3.502s，`/tmp/cloudnest-g05-agent-review-check.log`），实际 CLI 三隧道及恢复集成再次通过（测试 22.81s、包 24.988s，`/tmp/cloudnest-g05-agent-review-native.log`），专项 vet 通过。另核对 PR #1/#2 仍开放、#2 的 base 仍为 G00 分支：#1 原基线漏洞检查失败保留，#2 四项 CI 全通过；自定义权限会话例外、随机 JTI 及旧客户端契约测试仍在当前代码中，未重复实现。

## 2026-11-04 G05 Agent 异常退出监督

前一 Agent 提交 `ce89b6bb7d0aab5e6be75671a14eb1a50edb4836` 的 PR `41781004742`、push `41780999783` CI 均全部通过，包括完整后端回归、漏洞扫描和新增 Linux amd64 原生 Agent/隧道测试。

本批新增独立监督进程，通过继承的目录/锁和存活/就绪管道管理官方 frpc。Agent 被 SIGKILL 时，监督进程停止并回收 frpc；监督进程自身被 SIGKILL 时，Agent 明确退出，残留 frpc 的锁拒绝另一个 Agent。没有引入 PID 文件接管、按名称杀进程或远程自定义命令。

真实 CLI 故障测试首版已验证 Agent SIGKILL 后回收并恢复隧道（包 29.681s，`/tmp/cloudnest-g05-supervisor-native.log`）。随后增加监督进程 SIGKILL：验证 Agent 八秒内明确报错退出、残留 frpc 仍持锁，测试只停止提前取得句柄的本夹具子进程，锁释放后再次恢复三种隧道。最终集成通过，测试 27.31s、包 29.217s，日志 `/tmp/cloudnest-g05-supervisor-native-final.log`；仍包含可信 FRP TLS、正常启停/reload、frps 重连和危险配置拒绝。测试没有触碰用户已有的 frpc 进程。

最终 **11 项原生/race Agent 顶层测试**全部通过，包 2.941s，日志 `/tmp/cloudnest-g05-supervisor-native-components.log`，包括新增继承目录/锁不匹配拒绝和监督进程未知退出后拒绝启动。锁保留测试改为占用临时回环端口，不访问固定端口上的已有服务。`make check test build-macos build-linux` 全通过，日志 `/tmp/cloudnest-g05-supervisor-check-final.log`；本地运行平台仍为 Darwin arm64，新提交的 Linux 结果需由对应 CI 验证。

边界：重新运行 Agent 的动作由测试执行，尚未安装系统服务来自动重启 Agent。监督进程自身丢失后的残留恢复、整个进程组同时被强制终止、掉电与休眠尚未全面验收。文件凭据/配置的 Keychain 与本地加密、连接级状态仍未完成，G05 继续进行。

## 2026-11-04 G05 Agent 本机密钥与持久状态（11-05 核验）

前一提交 `6c8d0b5aec43cc65fd8fe0589784714862d77294` 的 PR `41782677910`、push `41782669413` 三个作业均通过，包含 Linux amd64 原生异常退出测试。11-05 复核：PR #2 的 base 仍是 PR #1 的分支，#1 保留原基线漏洞失败，#2 四项检查均通过。

Darwin arm64 使用测试专用临时文件钥匙串，通过真实 Security.framework 验证密钥创建、查找、锁定后拒绝读取、解锁后恢复及已编译 CLI 跨进程迁移（两项，包 5.166s，`/tmp/cloudnest-g05-keychain-cli.log`）。合并运行原生 FRP、Keychain 及 race 测试：17 项顶层用例通过，零跳过，包 10.486s（`/tmp/cloudnest-g05-vault-components-final-v2.log`）。另验证跨 vault/跨文件替换、密文损坏、旧状态迁移重试和禁用 cgo 时明确拒绝 Keychain：专项 race 2.773s、禁用 cgo 0.562s。

真实 CLI/TLS、隔离 MySQL/Redis 和官方 frpc/frps 使用单独的外部密钥文件验证 TCP/HTTP/HTTPS、异常退出及恢复。增加磁盘密文、正常停止后运行配置消失以及使用错误密钥无法启动的断言；通过（用例 27.56s、包 29.865s，`/tmp/cloudnest-g05-vault-native-final.log`）。这项端到端测试没有使用登录钥匙串。`make -C cloudnest_go check test build-macos build-linux` 通过（`/tmp/cloudnest-g05-vault-check-final.log`）；本批未在本地重新运行完整普通数据库套件。

运行中的 frpc 仍需可重复读取的私有配置；停止时删除不能擦除已有快照或备份。探索性 Darwin POSIX 共享内存探针不能按普通文件写入/定位（ENXIO），未用于实现，亦不算产品测试。SEC-010、正式签名后升级的钥匙串访问和生产历史迁移仍待验收。详见 [Agent 本机密钥与恢复](FRP-AGENT-VAULT.md)。

## 2026-11-05 G05 macOS 用户 LaunchAgent 自动恢复

增加 `cloudnest-agent launch-agent --state ...`，从实际二进制位置生成固定参数的用户服务定义。含空格、引号、Unicode 和 XML 特殊字符的路径通过系统 plutil 逐项解析；相对路径、控制字符和非规范路径被拒绝（race，包 1.642s）。生成不会加载服务或读取密钥。部署与卸载步骤见 [Agent 运行指南](FRP-AGENT.md)。

Darwin arm64 首次真实 launchd 测试通过（35.27s，`/tmp/cloudnest-g05-launchd-native.log`）；补充 bootout 后释放状态锁、清理 active.json 的断言，再与原 CLI 三隧道/双故障回归一起执行，均通过：28.47s + 35.24s，包 66.565s（`/tmp/cloudnest-g05-launchd-native-final.log`）。测试使用临时 plist、随机服务标签、已编译 Agent、隔离 MySQL/Redis、实际 TLS、官方 frpc/frps，先确认三个上游可达，再对该临时服务执行 SIGKILL。launchd 自动启动不同 PID 的 Agent，新认证命令完成且 TCP、HTTP、HTTPS 再次实际可用；没有人工重启 Agent，没有在用户启动目录安装产品服务。

`make -C cloudnest_go check test build-macos build-linux` 通过，日志 `/tmp/cloudnest-g05-launchd-check.log`。本地服务集成必须有 macOS 图形登录域和 Docker，显式运行 `scripts/test-integration.sh -tags=integration,frpnative,launchdnative -run '^TestLaunchAgentNativeCrashRecovery$'` 并提供官方二进制目录。GitHub macOS 作业单独检查真实 Keychain 和系统 plist 解析，不等于运行过完整 launchd/数据库集成。未实测用户注销/重新登录、休眠、钥匙串锁定后的无人值守服务、签名更新或 Linux 服务管理。

## 2026-11-05 G05 运行时 FRP token 不落盘

固定版本官方源码确认认证初始化只解析一次文件 token 源，普通配置路径则会重复读取。先以独立真实 frpc/frps 验证继承匿名管道可完成认证，frps 重启后同一 frpc 仍能重连（`/tmp/cloudnest-g05-token-source-probe.log`）。随后接入实际 Agent 和监督进程，固定 fd 4 的源只由本机生成，不放开远端配置中的文件路径或执行能力。

最终 20 项顶层 Agent 普通/原生/真实 Keychain race 用例通过，零跳过，包 6.027s（`/tmp/cloudnest-g05-tokenpipe-components-final.log`）。新增最大长度/Unicode/空 token 原样传递、缓存不含 token、拒绝首尾空白且不产生缓存，以及旧状态遇到无法原样传递的 token 时保持认证命令通道的回归。原先规范化加密状态格式未改，不静默改变历史凭据。

实际 CLI 和 launchd 两项集成均通过（27.79s、35.01s，包 64.772s，`/tmp/cloudnest-g05-tokenpipe-e2e.log`），增加运行中配置只有固定 fd 路径、没有认证 token 的检查。保持三种隧道、reload、Agent/监督进程 SIGKILL、frps 重连和 launchd 自动拉起的真实验证。`make -C cloudnest_go check test build-macos build-linux` 全通过（`/tmp/cloudnest-g05-tokenpipe-check.log`）。

先前 vault 提交 `58d0428` 的两次 CI 中，真实 macOS Keychain、Linux 原生隧道和漏洞扫描分别通过；完整 build-test 尚未结束，随后因较新提交排队而整次取消，不能记为全绿。新提交的完整 CI 需按实际 SHA 单独核验。历史明文备份/生产迁移、实际签名升级和所有连接的实时健康仍未验收。

## 2026-11-05 G05 控制后端停机/重启与运行设备撤销

实际 TLS 服务夹具支持停止、等待已启动进程退出及在同一地址/证书/隔离存储上重新启动。原来的三个真实 TLS 调用方（设备注册轮换、配置脱敏注入、持久命令回执）重新运行全部通过（包 11.364s，`/tmp/cloudnest-g05-restart-helper-regression.log`）；带原生标签的集成 vet 通过。

实际 Agent 隧道测试新增：停止已编译后端并确认连接失败，观察 Agent 的固定网络错误，确认既有三种隧道可达、数据库中新命令仍为 pending，再启动后端并等待命令成功回执。随后在真实隧道运行中撤销该测试设备凭据，要求 Agent 在十秒内报告失败退出、TCP 端口停止监听且状态锁释放。此项通过（35.59s）。与原 launchd 自动恢复测试（35.44s）一起通过，包 73.120s，运行日志 `/tmp/cloudnest-g05-backend-restart-e2e.log`。此次为短时中断，不是长期离线、睡眠或全机掉电测试；所有操作仅针对测试创建的进程与隔离数据库。

`37d1e7c195e1500d76928584b16ff92fee5cc75c` 的 PR `41943203704`、push `41943196762` CI 均四作业全通过。后继 `8a4a7c4` 已核对 Linux 原生 token 管道/隧道、macOS Keychain 和漏洞作业通过，完整后端回归尚需等待该 SHA 的终态。

### CI 上传取消检查点纠正

`8a4a7c423a7036eeac49eba778eb6c0ff7f1aea4` 的 push `41963150571` 全部通过，但 PR `41963154776` 的普通集成在 `TestUploadBodyCancellationInterruptsRateWait` 失败（“canceled rate wait leaked candidate”），其余三个作业通过。失败日志保存为 `/tmp/cloudnest-g05-tokenpipe-ci-failed.log`；不把这次 PR CI 记为成功。

旧测试的 100ms 期限从请求入口开始，可能在 inode 登记前触发，混淆了“已取得候选句柄的限速取消”与“身份尚不确定的登记失败”。后者必须保留文件和预算，既有 `TestTransferUnprovenIntentAndReplacedInodeAreBlocked` 明确要求如此。修改前者，先确认数据库已提交 writing/inode 且磁盘有部分数据，再取消限速等待，仍要求取消后 400ms 内返回 408、释放准入并删除候选。未放宽清理时限或删除未知文件。

另新增真实数据库回调阻塞在 inode 更新之前，等请求期限到达：确定复现 408、intent/inode=0 和保留的文件，证明这是独立的安全边界。上述三个用例各重复八次，24 次全部通过，包 24.648s（`/tmp/cloudnest-g05-cancel-checkpoint-final.log`）；带完整标签的集成 vet 通过。服务端逻辑未更改。

撤销测试进一步要求日志出现固定 HTTP 401 拒绝原因且 active.json 已清理，避免将任意异常退出误认为撤销成功。真实 CLI 全部场景再次通过（用例 35.16s、包 36.996s，`/tmp/cloudnest-g05-revoke-final.log`）。

## 2026-11-05 G05 官方注册状态与持久诊断快照

前一提交 `88d411bfa2f78d7e3d452b37575b0d0a88ab280b` 的 PR `41964277371`、push `41964268639` 均四作业全通过，包含上传取消检查点修正、完整后端回归、Linux 原生与 macOS Keychain 测试。

本批对照固定 0.71.1 源码接入实际代理状态 API。接口同时包含管理写能力，因此仅绑定回环地址，使用每次启动独立的 TLS 身份及随机 Basic 凭据；Agent 只调用状态 GET。TLS 私钥走匿名管道；Basic 凭据在 0600 启动配置中短暂存在，可信 API 读取后删除配置和公共证书。具体边界、升级次序与快照语义见 [注册诊断](FRP-AGENT-DIAGNOSTICS.md)，没有把“已注册”当作上游应用健康。

本机原生/race/Keychain 测试最终 **23 项顶层通过、零跳过**：Agent 包 8.987s、报告协议包 1.531s（`/tmp/cloudnest-g05-diagnostics-review-components.log`）。实际官方 frpc 覆盖没有 FRP 控制连接时不能报告成功、Basic 认证拒绝、回环假冒 TLS 拒绝。协议测试验证固定状态/计数、自相矛盾或额外字段拒绝、规范化时间、回执重启/换租约后不替换原观察。

隔离数据库与真实 CLI 的最终 **14 项集成均通过**，包 115.976s（`/tmp/cloudnest-g05-diagnostics-regression-final.log`）。其中实际 CLI 46.25s、launchd 48.55s，验证 frps 拒绝一个映射后管理端 HTTPS 历史显示 degraded/registered=2/failed=1，恢复后显示三条 registered；其余隧道、异常退出、后端重启、撤销、加密及运行缓存清理断言仍保留。另覆盖已有命令行的列迁移、缺列启动拒绝、跨设备配置、无效/非诊断动作快照、不可变回执、旧无快照回执、单次审计及身份/租约回归。

复核后将 launchd 测试的新进程探测顺序明确为“先实际三隧道访问，再断言 registered 快照”，防止把正常的注册中状态误当作测试失败；单独再次通过（48.74s、包 50.731s，`/tmp/cloudnest-g05-diagnostics-launchd-review.log`）。进一步确认监督进程状态不明时只报告 unavailable，并保留启动失败与清理错误；专项 race 再次通过（Agent 2.827s、报告协议 2.168s，`/tmp/cloudnest-g05-diagnostics-final-review.log`）。

`make -C cloudnest_go check test build-macos build-linux` 全通过（`/tmp/cloudnest-g05-diagnostics-review-check.log`），包含普通 race/vet 及四目标服务端/Agent 构建。新增数据库列只在隔离测试库上迁移；新提交的完整远端 CI 需按 SHA 独立核验。未宣称逐映射实时流、上游应用健康、长期离线/休眠或生产升级已验收。

## 2026-11-06 G05 映射并发与预留

复核 PR #1/#2 仍为 OPEN，#2 的 base 仍为 `codex/g00-audit-baseline`。#1 `4462b74` 两次构建成功、两次原漏洞基线失败；#2 `588a187` 四项检查全部成功。当前代码保留自定义权限下的 me/logout、独立随机 JTI、多会话注销和客户端契约回归；本批没有重新开发 G01。

诊断提交 `e0da206` 的 push `41999443725` 四项成功。PR `41999454355` 初次 native job 在下载固定官方 FRP 时连续 HTTP 504、curl 退出 22，尚未执行 native 测试，其余三项成功；失败日志保留于 `/tmp/cloudnest-g05-diagnostics-ci-failed.log`。仅重跑失败任务，attempt 2 四项全部成功。没有通过删除测试或替换官方二进制来掩盖失败。

映射首轮六项顶层 race 集成通过（10.107s，`/tmp/cloudnest-g05-mapping-regression.log`），包括 24 并发自动分配、24 并发规范域名竞争、跨用户真实 HTTP 相同端口竞争、1000 端口耗尽和空位复用、数据库直接写约束、事务审计故障、三个设备归属读取/加锁交错场景及历史重复数据迁移保留。开发验证曾发现 MySQL 将 SUBSTRING 表达式保存为 SUBSTR，以及域名规范化双重去前缀错误；已修复实现并保留严格校验/非法输入断言，未绕过 schema 验证。

完整回归与最终复核结果见下文。本批边界见 [映射协议](FRP-MAPPING-RESERVATIONS.md)。

首次完整套件在 238 项顶层用例通过后触发六分钟总看门狗，`TestTreeCyclesDepthAndOverlappingRootsFail` 刚开始（0s），没有业务断言失败；包退出 363.134s，日志 `/tmp/cloudnest-g05-mapping-full.log` 保留为**未通过**。当前普通套件共 260 项，已将隔离脚本的套件看门狗从 6m 调整为 8m，以容纳新增集成与每夹具 schema 校验。单项取消、锁等待、网络与资源释放断言没有延长；完整重跑另记结果，不将此超时计为成功。

最终映射专项加真实 Agent CLI 共九项顶层 race 集成通过、零跳过（60.098s，`/tmp/cloudnest-g05-mapping-native-review.log`）。八项映射测试追加锁等待取消、删除释放、更新冲突、同名生成列篡改拒绝及审计自身 1062 错误不能冒充映射冲突。第九项使用固定官方 frpc/frps 和实际编译后端/Agent，验证三隧道、重连、重启、诊断及撤销链路在新 schema 下仍通过。

`make -C cloudnest_go check test build-macos build-linux` 全通过（`/tmp/cloudnest-g05-mapping-check-final.log`），包括普通 race/vet、四目标服务端/Agent 构建；审计错误分类最后调整后四个受影响包 race 再次通过（`/tmp/cloudnest-g05-mapping-review-unit.log`），integration-tag vet、仓库检查及补丁格式检查通过。

最终完整普通 race 集成 **260 项全部通过，零跳过**（412.869s，`/tmp/cloudnest-g05-mapping-full-final.log`）。包含原 G01 自定义会话权限、独立登录/注销和客户端 API 边界，以及文件/传输/身份/持久命令/诊断与本批八项映射测试。脚本退出 0，随后确认无残留的隔离测试容器。最终提交的远端 CI 仍按其 SHA 单独核验；本地通过不替代远端结果，也不表示 G05 或全项目完成。

## 2026-11-06 G05 设备退役与历史保留

上一映射提交 `33a0eb3727579e5eed78495a453014a2737929ff` 的 PR `42143136276`、push `42143128855` 四项检查均全部成功。G00/G01 与 PR #1/#2 状态沿用上节复核，没有合并操作。

设备首轮定向 race 集成 15 项全部通过（22.992s，`/tmp/cloudnest-g05-retirement-targeted.log`）：五项新增设备用例、八项映射用例、两项身份/最低权限 schema 校验。覆盖历史/预留保留、过期身份拒绝、未完成命令取消、审计故障完全回滚、设备更新/退役归属加锁交错、旧结构升级、v1 例程保留及新触发器篡改拒绝。

真实官方 frpc/frps、实际编译后端/Agent 的两项场景分别通过：原凭据撤销 47.88s，新增设备退役 47.75s；两者都保留 TCP/HTTP/HTTPS、重启、重连、后端停机恢复和诊断验证，并在最终撤销/退役后要求真实 HTTP 401、Agent 非成功退出、TCP 隧道关闭、单例锁释放和运行配置消失。连同六项设备测试共八项顶层 race 集成通过、零跳过（106.138s，`/tmp/cloudnest-g05-retirement-native.log`）。CI 的官方 FRP 脚本现分别运行两种退出场景。

`make -C cloudnest_go check test build-macos build-linux` 全通过（`/tmp/cloudnest-g05-retirement-check.log`）。最终响应明确标记 retired/reservations_retained 后，四个受影响包的 race 与带 integration/frpnative 的 vet 单独复核，记录于 `/tmp/cloudnest-g05-retirement-final-review.log`。完整回归和最后 schema 默认值复核另记，不用专项结果替代全部验收。

退役主体完整普通 race 集成 **266 项全部通过，零跳过**（443.983s，`/tmp/cloudnest-g05-retirement-full.log`）。最后另外收紧了退役列必须无默认表达式/自动更新行为的启动检查：初次两项专项中的故障注入 DDL 使用了 MySQL 不接受的 SET DEFAULT 函数语法，在注入前退出，日志 `/tmp/cloudnest-g05-retirement-schema-final.log` 保留为未通过；已改为 MODIFY COLUMN 的等价故障注入，不修改生产行为或放松校验。更正后专项结果另记。完整回归与最后 schema 专项的范围分开记录。

更正故障注入语法后的两项最终 schema race 复核全部通过（7.248s，`/tmp/cloudnest-g05-retirement-schema-corrected.log`），实际验证自动退役默认值拒绝、无 DDL/TRIGGER 权限的运行账号校验、旧例程保留、新列迁移、缺失/篡改触发器拒绝及重复迁移保留退役状态。没有剩余测试进程或隔离容器；最终提交 CI 按新 SHA 单独核验。

## 2026-11-07 G05 服务器身份、策略版本与托管配置

退役提交 `2bb27577fed4f6c22e21409b274e4e2ee5d420fd` 的 PR `42172431560`、push `42172425352` 四个作业均全部成功。按用户最新要求，G01/G02 两份正式 Review 未读取，保留到整个 Goal 完成后处理，见 [延后审查记录](DEFERRED-REVIEWS.md)。

服务器策略/信封单元及相关仓储/schema 的 race 检查通过（`/tmp/cloudnest-g05-server-unit.log`）。首轮集成编译发现测试构造器遗漏 Redis 参数，未执行用例，日志 `/tmp/cloudnest-g05-server-targeted.log` 保留为失败；修正夹具后五项服务器定向 race 集成全部通过（11.216s，`/tmp/cloudnest-g05-server-targeted-fixed.log`）。覆盖 TLS/所有者隔离/无秘密响应、八并发 CAS 仅一个成功、规范端点唯一、审计 1062 不能冒充业务冲突、生成策略/回滚/设备转移、托管配置关联认证、两类历史密钥轮换、schema 漂移拒绝和旧明文不擅自关联。

真实 Agent 测试现通过实际后端 HTTPS 创建服务器、保存固定版本的生成配置，再使用官方 0.71.1 frpc/frps 访问三种上游。首轮原撤销场景通过（49.51s）；第二场景在建映射时被前一夹具遗留的 `example.com` 全局预留拒绝，未进入隧道验收。该轮六项服务器专项也通过，但整轮为失败（64.214s，`/tmp/cloudnest-g05-server-native.log`）。已增加仅针对各自临时设备的映射清理，保留产品唯一约束、退役保留语义及原真实连接断言。重跑结果另记。

`make -C cloudnest_go check test build-macos build-linux` 全部通过（`/tmp/cloudnest-g05-server-check.log`）：普通 race/vet 和四目标服务端/Agent 构建。带 integration/frpnative 的 vet、仓库卫生与 diff 检查通过。后续全部回归以最终结果为准，不用这些定向结果代替整体 Goal 验收。

清理修正后的真实 Agent 与服务器专项 **8 项顶层 race 集成全部通过，零跳过**（105.088s，`/tmp/cloudnest-g05-server-native-fixed.log`）；凭据撤销场景 47.83s、设备退役场景 48.64s，均由新服务器 API 生成配置后完成原三隧道/诊断/故障恢复/认证退出链路。新服务器快照篡改和轮换审计故障用例同时通过；未削弱任何唯一约束或退役保留断言。最后区分部署权限冲突与真实数据库错误后，受影响包 race 和带原生标签的 vet 另行复核通过。

服务器管理批次完整普通 race 集成 **272 项顶层全部通过，零跳过**（415.733s，`/tmp/cloudnest-g05-server-full.log`），进程最终退出 0，2026-11-09 恢复任务后再次核验终态。覆盖已有 G01 安全/客户端契约、文件与传输、设备/命令/退役及本批服务器测试。服务器级端口命名空间、运行配置占用和其余里程碑仍未完成；没有据此完成整个 Goal。新提交的远端 CI 单独跟踪。

## 2026-11-09 G05 清除恢复配置与排他命令

上一服务器管理提交 `80fdb6adcac2d86261d658e4684208cba4808c98` 的 PR `42540614490`、push `42540606874` 均四作业全部成功，包含普通/网络集成、Linux 真实 Agent、macOS Keychain 及漏洞扫描。PR #1/#2 仍 OPEN，#2 base 仍为 `codex/g00-audit-baseline`；G01 四项检查通过。仅查询分支与 checks，未读取用户指定延后的正式 Review。

本批增加 `forget_config`，区分停止进程和清除可再次启动的恢复配置。单元 race 检查通过（Agent 3.037s，`/tmp/cloudnest-g05-forget-unit.log`），新增用例实际断开回执连接，检查发出成功回执前磁盘状态已清空、重建 Runner 只重发一次持久结果、start/reload 不恢复旧配置，以及失去租约时不伪造清除结果。

两项排他队列 race 集成与两项真实 Agent 场景 **4 项顶层全部通过，零跳过**（125.816s，`/tmp/cloudnest-g05-forget-native.log`）。队列覆盖版本/心跳新鲜度、pending/leased 排他、同请求 ID 重试、重复回执单次审计、12 并发只有一个成功及审计失败不提交。原生撤销场景 61.56s、退役场景 60.17s：通过真正的 HTTPS 所有者 API 发出 forget，验证真实端口关闭、解密当前 Agent 状态无恢复配置、重启后 start/reload 拒绝及 diagnose 的配置 ID 为零；新部署后三条隧道重新可达。全部原先故障/恢复/退出断言保留。

`make -C cloudnest_go check test build-macos build-linux` 全部通过（`/tmp/cloudnest-g05-forget-check.log`），进程退出 0。设备/FRP 全部相关集成和最终 CI 另记结果；本批没有放宽全局唯一约束或宣称自动端口释放。

最终全部设备身份、持久命令、配置/映射/服务器相关 race 集成 **35 项顶层全部通过，零跳过**（48.095s，`/tmp/cloudnest-g05-forget-regression.log`），脚本退出 0。带 integration/frpnative 的 vet、仓库检查和补丁格式检查通过。全后端普通/网络回归由新提交的 CI 执行，按 SHA 独立核验。


## 2026-11-12 G05 运行资源占用与清除回执

上一批 `50ac977d416823487ee8ba2bdb63462610ad930c` 已按精确 SHA 核验：PR `42542622909`、push `42542617091` 均为 success，四项 build-test、agent-native、agent-keychain、vulnerabilities 全部通过。本次只查询运行状态/任务，没有读取用户指定延后的 G01/G02 Review。

本批增加七项资源占用集成用例。最终 Agent/FRP 定向 race **42 项通过、零跳过（72.400s）**，日志 `/tmp/cloudnest-g05-reservation-regression-fixed.log`。验证部署入队及审计原子登记、删除/修改映射后的端口与域名保留、HTTP/HTTPS 独立资源、普通 stop/失败/超时/退役/删除配置不释放、新身份代次不能清旧占用、并发部署只允许一个取得相同资源、首次 forget 回执/审计原子释放及旧回执重放不影响后来部署；还覆盖 TLS/归属/脱敏、结构漂移、unknown 历史回填和幂等重跑。

官方固定 FRP 0.71.1 归档重新下载并验证 SHA-256 后，`scripts/test-agent-native.sh` 实际通过：Agent 单元/原生进程 **22 项、5.495s**，两项实际 Agent CLI/后端/frpc/frps 集成 **122.934s**（60.98s、60.02s），均零跳过。两个场景都包含三种隧道到达上游、stop 后运行占用保留、forget 后真实端口关闭与账本清空、重启 start/reload 拒绝、重新部署恢复三条隧道并重新登记，以及原有 SIGKILL/监督进程丢失/后端重启/凭据撤销或设备退役验证。日志 `/tmp/cloudnest-g05-reservation-native.log` 的最终消息明确包含 runtime resource retention/forget/reacquisition。

前一轮合并标签回归失败 **77.025s**：两项原生用例因旧临时目录中的 frpc 已不存在而被官方摘要校验拒绝；一项服务器回滚测试以普通字符串充当配置，严格资源解析将其记为 unknown 后正确阻止其与其他映射并存。现重新获取已验证官方包，并让该回滚测试调用正式配置生成器；没有放宽校验、占用约束或原来的回滚/授权/轮换断言。失败日志 `/tmp/cloudnest-g05-reservation-regression.log` 保留。早期编译还发现共享类型移动后遗漏 proxy 别名，已修正，见 `/tmp/cloudnest-g05-reservation-compile.log`。这些失败不计为验收通过。

最终源码 `make check test build-macos build-linux` 通过，覆盖 vet、全部 race 单元、服务端与 Agent 的 Darwin/Linux × amd64/arm64 构建；标签 integration,frpnative 的 vet、仓库与 diff 检查通过。日志 `/tmp/cloudnest-g05-reservation-check.log`。本地测试全部使用新 disposable MySQL/Redis、临时目录和测试自己的进程，未迁移生产库。

最终全量普通集成 **281 项 race 通过、零跳过（473.974s）**，没有失败或数据竞争，原八分钟总期限及逐操作期限不变。日志 `/tmp/cloudnest-g05-reservation-full.log`；运行结束无剩余 cloudnest.test 容器。本批仍保留全局映射唯一性；服务器分区索引草稿没有进入实现。旧配置回填为 unknown 且新身份不能自动清除，受控历史对账和升级演练未完成，见 [FRP-RUNTIME-RESERVATIONS](FRP-RUNTIME-RESERVATIONS.md)。G05 和整个 Goal 继续进行。


## 2026-11-12 Go 标准库安全更新

运行资源提交 `cef1bce3103b296c7e3bea205b14db8faf2b4293` 的 PR `43168756267` 和 push `43168750114` 均是 **failure**：build-test、agent-native、agent-keychain 三项通过，vulnerabilities 失败。失败原因是 Go 1.26.10 标准库的 GO-2026-6859（net/http MaxBytesReader 并发响应头问题），扫描器指出修复版本为 1.26.11；没有重跑旧提交来隐藏这个结果。

`cloudnest_go/go.mod` 最低版本现更新至 Go 1.26.11，CI 继续从该文件选择工具链。官方 [Go 发布记录](https://go.dev/doc/devel/release#go1.26.11) 记录该版本于 2026-11-10 发布并含 net/http 安全修复。没有关闭或放宽漏洞检查。

本地实际工具链为 go1.26.11 darwin/arm64，漏洞扫描 **0 个可达漏洞**（另有三个仅位于所需模块、当前调用路径不可达的告警），`make check test` 的 vet/build/race 单元均通过。原始记录为 `/tmp/cloudnest-go12611-version.log`、`/tmp/cloudnest-go12611-vuln.log`、`/tmp/cloudnest-go12611-check.log`。此处不把上一版 Go 1.26.10 的全量集成计为新版工具链的全量验收；新提交的远端 CI 单独核验。

## 2026-11-13 G05 服务器作用域与公网验收（进行中）

工具链修复提交 `843180f3582ff7d14663d256fb9386abc302b087` 的 PR `43237919993`、push `43237905382` 已按精确 SHA 核验为 success，build-test、agent-native、agent-keychain、vulnerabilities 四项全部成功。

最终映射/作用域/运行资源专项 **20 项顶层 race 通过、零跳过（45.551s）**，日志 `/tmp/cloudnest-g05-scope-final-targeted.log`。新增五项作用域测试验证不同服务器复用资源、全局零作用域与所有服务器重叠、管理员不能混用跨用户服务器、策略与协议切换、分区账本清除、并发全局/分区竞争，以及缺少 server_id 的旧表真实升级后保持原记录与原文、重复迁移、非唯一索引漂移拒绝。早期迁移用例误把数据库填充的创建时间与插入前零值比较而失败；已改为升级前读取真实历史行，保持全部保留断言，失败记录 `/tmp/cloudnest-g05-scope-migration.log` 不计为通过。此前口头专项计数有误，以此处实际日志的 20 项顶层用例为准。

`make check test build-macos build-linux` 通过（`/tmp/cloudnest-g05-scope-check.log`），包含 vet、race 单元及 Darwin/Linux × amd64/arm64 服务端/Agent 构建。固定官方 FRP 0.71.1 原生测试 **22 项 Agent 用例通过（5.331s）**；实际三隧道的 CLI 撤销和退役两项集成通过（59.99s、59.87s，包 121.718s），日志 `/tmp/cloudnest-g05-scope-native.log`。这两项已改用正值 server_id 创建映射，全部原有重启、故障恢复、诊断、资源保留/释放断言保留。

公网测试使用操作者的独立 Ubuntu frps 及本机私有目录中的同一 CA/token。设置 IP DIRECT 后控制 TLS 验证成功。第一次探测曾误把 TUN 接受本地 TCP socket 当作远端端口已监听，未部署即失败（`/tmp/cloudnest-g05-public-first.log`）；现以 TLS 认证、真实注册和随机上游响应为证据，未放宽证书校验。

北京时间 10:31 开始的 Agent 公网验收部署回执成功但 TCP 随机响应失败（40.46s，`/tmp/cloudnest-g05-public-direct.log`）；10:33 紧跟部署的 diagnose 返回 diagnostic_failed（18.72s，`/tmp/cloudnest-g05-public-diagnostic.log`）。早期注册快照现仅作为诊断，数据探测后仍须断言三个代理全部 registered。操作者提供的服务器 UTC 日志确认两次 `device_1_map_1` 均为 `port already used`（02:31:16.523、02:33:51.738）；10:35 独立官方 frpc 成功登录后注册 TCP 20080 也被相同原因拒绝。操作者此前手工 frpc 0.71.0 已注册同一测试端口。正在等待释放该手工测试映射后重跑，不能把手工连通或本地原生验收计作公网完整验收。详细运行与边界见 [公网验收说明](FRP-PUBLIC-ACCEPTANCE.md)。

本批首次完整普通 race 回归在 **275 项顶层通过后触发八分钟套件看门狗**，包 480.804s，日志 `/tmp/cloudnest-g05-scope-full.log` 保留为失败。触发时跨实例上传暂停测试运行 17s，仍处于其原有 27s 单项等待限额内，未有业务断言失败；这不计作完整通过。新增用例后普通套件为 286 项，套件期限改为十分钟；逐操作取消、资源租约、网络和单项超时全部不变。最终完整验收由新提交 CI 独立核验。

公网释放端口后的第一轮三代理均 registered，但使用 example.com Host 的 TCP 请求等待响应头超时（42.52s，`/tmp/cloudnest-g05-public-released.log`）。分开探测后，TCP 使用 IP Host 与 HTTPS 使用 example.com SNI 均通过随机响应校验；HTTP 的 example.com Host 仍超时（43.53s，`/tmp/cloudnest-g05-public-routes.log`）。正在以 IP 作为 HTTP 虚拟主机作对照，不能把这些部分结果记作完整验收，也未确定 HTTP 域名路径超时的最终原因。

最终公网 IP 虚拟主机对照 **完整通过（74.39s，包 76.431s）**，日志 `/tmp/cloudnest-g05-public-iphost.log`。真实 Agent 0.3.0 + 官方 frpc 0.71.1 经 HTTPS 管理接口部署到操作者的独立 frps：TCP 20080、HTTP 18080（Host 为测试 IP）、HTTPS 18443（example.com SNI 且校验上游证书）均返回本轮随机 nonce。stop/start、正常 Agent 重启、forget 后三种上游响应消失且控制 TLS 仍可认证、账本 3→0、重启 start/reload 拒绝旧配置、重新部署三隧道及再次清除全部通过。未修改正式 frpc/frps 或远端配置。example.com 纯 HTTP 路径超时仍单独开放，不能宣称真实 DNS/所有代理配置已通过。

受全量套件看门狗中断的上传暂停/恢复相关四项顶层 race 补测通过（28.314s，`/tmp/cloudnest-g05-scope-timeout-tail.log`），包含原有跨实例 27s 取消界限；不以补测替代新提交完整 CI。带全部三种测试标签的 vet、脚本语法、仓库卫生和补丁格式检查通过。

提交 `57c38b62b990368709e8346c068e4dd31cec2309` 的 PR `43354306624` 和 push `43354300132` 四项 CI 均成功。PR 日志确认完整普通 race 集成 376.724s、独立网络集成 97.765s，覆盖新旧映射迁移。此前本地总期限超时保留原记录。

G05 历史占用维护命令的 8 项 race 集成通过（16.737s，`/tmp/cloudnest-g05-reconciliation-final.log`）。覆盖旧账本加列且保留 ID/有效状态、拒绝仍可领取旧 JWT 命令的设备、拒绝当前凭据代次、审计故障回滚、重复确认及恢复分配。`make check test`、带 integration/frpnative/frppublic 标签的 vet、仓库卫生和补丁格式检查通过。所有写入均限于临时测试库。

G05 旧版升级演练使用 `50ac977d416823487ee8ba2bdb63462610ad930c` 编译的真实旧服务端在 disposable MySQL 创建结构，再执行当前 `migrate` 和对账 CLI。首次运行在最后新部署时被夹具留在队列中的 `forget_config` 排他屏障正确拒绝（`/tmp/cloudnest-g05-real-upgrade.log`）；修正夹具通过真实回执结束保留命令，屏障规则保持原样。最终演练通过（5.47s，包 7.315s，`/tmp/cloudnest-g05-real-upgrade-fixed.log`）。验证重复迁移、旧映射、密文逐字节保留且可解密、旧身份心跳、启动类命令取消/其他命令继续完成、保留账本以及对账后新部署。

补充旧租约与新占用断言后，同一真实旧版升级演练再次通过（4.61s，包 6.427s，`/tmp/cloudnest-g05-real-upgrade-final.log`）。旧启动类命令的成功回执被拒绝，重新入队的部署生成 TCP 占用。8 项运行占用 race 专项通过（14.640s，`/tmp/cloudnest-g05-upgrade-reservations.log`），覆盖已升级库中仍有未结束启动命令时拒绝对账。`make check test`、升级标签 vet、脚本语法和补丁检查通过。

G05 Linux 用户服务增加 `systemd-unit` 及临时服务验收。路径转义/非法路径测试、`make check test build-linux`、Linux amd64 专项集成编译及脚本语法通过（`/tmp/cloudnest-g05-systemd-check.log`）。本机是 Darwin，systemd 运行验证由新提交的 Ubuntu CI 执行。
