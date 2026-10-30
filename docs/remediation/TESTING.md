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

## G04 持久索引任务检查点（2026-10-26）

- 新增上传与 outbox 原子提交、并发领取、崩溃重领、旧 token/代次隔离、退避/尝试上限、结果回滚/当前源检查、只读 schema 校验、SKIP LOCKED 与发布末尾过期测试。
- worker 执行器具有独立 blob Scope、任务续约、三分钟处理期限与停止清理；原后台分析租约测试移接真实队列 worker，仍验证上传 HTTP 完成后物理读保护、失败诊断与租约释放。新增取消 worker 的真实读取/重排队测试；没有删除原保护要求。
- 首批队列 8 项 race 专项通过（10.014s）；加入 worker 后的 11 项专项通过（11.988s）。这些只证明调度/隔离/生命周期，不证明已完成模型索引或检索。
- 中间完整运行曾编译失败：旧 `read_lease_test.go` 仍向已经移除的 `FileHandler.HTTPClient` 注入后台 transport。改为在新 worker processor 边界注入失败，保留实际 blob Pin/完整字节校验与释放断言。失败日志 `/tmp/cloudnest-ai-queue-full.log`；没有降低原安全断言。
- 最终 `make check`（vet/build）、`make test`（race 单元）通过，见 `/tmp/cloudnest-ai-queue-worker-unit.log`。最终完整 **248 项 race 集成全部通过（335.569s）**，保留六分钟期限，见 `/tmp/cloudnest-ai-queue-full-final.log`；无 FAIL 或 DATA RACE。模块完整性和仓库/diff 检查通过。
- 新队列表仅在隔离容器显式 migrate；没有修改生产库或删除历史 AI 数据。在当时 4c9c6d6 检查点，服务未接入真实 processor，新任务保持 queued；后续文本接入见下节。

## G04 实际文本处理与 embedding（2026-10-26）

- `make ai-model` / `scripts/test-ai-index-model.sh`：需要本机 127.0.0.1:11434 的 Ollama 与 qwen3-embedding:0.6b，明确设置合成文本模型测试开关。脚本仍通过原一次性 MySQL/Redis 守卫；不读取生产 `.env`，不发送真实用户文件。CI 只编译 aimodel 标签，不声称运行真实模型。
- 新文本单元测试检查 UTF-8/源位置/重叠、体积/取消/不支持格式、专用 embedding 请求、拒绝自动截断、响应维度/数值/数量/模型校验、响应体上限、拒绝重定向及 digest。协议测试中的合成向量不作为模型能力证据。`go test -race ./internal/aiindex` 通过（1.680s）。
- 正常 MySQL 专项包含模型端点不可达时持久 retry 且零结果、格式不支持/文本超限/实际磁盘损坏/非法 UTF-8 的独立诊断，连同队列和读取 scope 共 14 项顶层 race 测试通过（17.154s）。
- 第一轮真实本机模型 + 数据库链路通过（函数 4.89s / 包 6.925s），包括中文短文本、长文本多块、实际 1024 维向量内容/归一化/源位置、重建与永久删除。源码 `integration/ai_text_live_test.go` 保存输入与断言。
- 扩展到独立生产二进制的自动消费/SIGTERM 测试首次失败（包 5.066s）：Ollama 端点不可达。确认 curl connection refused、无 ollama serve 进程，且 embedding/视觉 manifest 引用文件完整后，重新启动本任务的 loopback Ollama。保留 `/tmp/cloudnest-ai-text-live-server.log`，没有把失败标为通过。
- 恢复后的真实模型 + 独立服务测试通过（函数 9.20s / 包 10.859s），见 `/tmp/cloudnest-ai-text-live-server-retry.log`。生产 server 无直接 Step 调用就消费了持久任务，并正常 SIGTERM 退出、释放读取租约。模型 digest `ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d`；迁移查询与迁移文档 cosine 0.769925、与海边描述文档 0.151140。它们是向量链路冒烟观测，不是完整检索 Recall/MRR。
- 当前源码 `make check/test` 通过，最终完整 **250 项普通 race 集成全部通过（354.027s）**，仍保留六分钟期限，无 FAIL/DATA RACE，见 `/tmp/cloudnest-ai-text-full.log`。aimodel 标签独立编译通过，仓库/diff 检查通过。前批 4c9c6d6 的 PR CI 40242563377 与 push CI 40242538550 已全部通过，不替代当前未提交代码的验证。
- PDF/Office/OCR/视觉、查询索引/混合检索/ACL/片段、历史与增量重建及完整效果评估尚未验收。此次新增 document/chunk 表仅在一次性库显式迁移，没有生产变更。

## G04 混合检索（2026-10-26 开发，2026-10-27 核对终态）

- `make ai-search` / `scripts/test-ai-search.sh` 新增独立三分钟 `integration,aisearch` 套件，CI 实际运行全部六项；原普通 250 项仍执行完整六分钟/race，没有移出或删除旧断言。新套件检查跨用户/共享/撤权、代次与源变更、返回前复核、尾部候选、窗口、时间过滤、请求边界及旧关键词接口。
- 单元覆盖中文/英文词法、Unicode 片段位置、日期边界、不能用上传时间冒充拍摄时间、坏向量、有限候选排序、最终权限复核。补充 `nl_plan.query` snake_case 序列化契约，匹配冻结客户端既有 DTO；当前 make check/test 通过，见 `/tmp/cloudnest-ai-hybrid-checks-final.log`。
- 中间编译曾因新增 handler 引用 contract 但漏 import 失败，已补齐；首次搜索专项中，重建词法夹具直接重复 INSERT document 导致主键冲突（6.435s 的失败日志 `/tmp/cloudnest-ai-hybrid-integration.log`）。修复夹具为同事务替换旧文档/块，生产 processor 原本即支持替换，未降低断言；修复后六项通过（9.551s），最终 DTO 契约修正后的六项再次通过（10.479s），见 `/tmp/cloudnest-ai-hybrid-wire-final.log`。
- 真实模型的语义检索、跨用户近似内容隔离、模型 digest 隔离和断开模型后的正文关键词降级通过，第一名 cosine=0.818942。单独加入检索后的真实模型套件两项通过（16.201s），见 `/tmp/cloudnest-ai-hybrid-live.log`；不把成功示例当作完整检索评估。
- 扩展到 10,000 个 1024 维实际向量的逻辑副本扫描后，真实模型三项全部通过（21.717s）：规模测试函数 8.76s，索引/独立生产服务 8.56s，实际混合检索 2.35s。向量数据 40,960,000 字节，单次 API handler 查询 1.510170792s，数据末尾的相关文件第一名。输入为两个真实 embedding 的重复逻辑副本，不是 10k 篇独立语料；未测并发、生产网络延迟或峰值 RSS。见 `/tmp/cloudnest-ai-hybrid-live-scale.log` 与 `ai_search_scale_test.go`。
- 普通完整 **250 项 race 集成通过（355.678s）**，无 FAIL/DATA RACE，见 `/tmp/cloudnest-ai-hybrid-full.log`。该运行启动后另补了 query DTO 的 JSON 命名；这项纯序列化修正由最终完整单元和六项搜索专项验证，未为它重跑全部传输测试。仓库/diff 检查通过。前提交 1fe9edd 的远端 PR/push 构建和漏洞 CI 均通过，新提交需按精确 SHA 另行核验。
- 本机 qwen3-vl:4b 合成票据 OCR 预检通过（20.581s）；keep_alive=0，之后 /api/ps 确认 models=[]。图片索引尚未接入，证据在 `/tmp/cloudnest-vision-preflight/report.json`，不计入产品图像检索验收。
- 当前仍缺 PDF/Office/OCR/图片处理、拍摄时间索引、完整六类查询评估、增量/历史重建和查询状态 API；没有生产数据操作。


### G04 索引控制与历史重建（2026-10-27）

- 增加 GET /ai/index 游标历史列表、GET /ai/index/:id 诊断和 POST /ai/index/rebuild 所有者代次 CAS 批量重建，固定诊断字段、不暴露 worker 令牌。语义与错误契约见 AI-INDEX-CONTROL.md。
- `scripts/test-ai-search.sh` 现执行六项检索与五项控制专项，真实一次性 MySQL/Redis + race **11 项全部通过（12.838s）**，日志 `/tmp/cloudnest-ai-control-integration.log`。包括历史 generation=0、单项共享读/不可调度、撤权、整批授权/冲突回滚、八路并发只有一次 202/七次 409、审计失败回滚、源变更诊断与请求边界。
- `make -C cloudnest_go check test` 构建/vet/全部 race 单元通过，日志 `/tmp/cloudnest-ai-control-checks.log`；`go -C cloudnest_go test -tags=integration,aimodel,aisearch -run '^$' ./integration` 组合编译通过。此批仅增加控制 API，未重跑普通 250 项完整传输回归；前批完整结果 355.678s 仍为历史证据，不能当作本批重新执行。
- 已确认本机 Ollama /api/version=0.31.1 后，执行 `scripts/test-ai-index-model.sh -run '^TestAITextLiveOllamaPublishesRecoverableChunkVectors$'`，**通过（函数 10.68s / 包 16.700s）**。原直接 queue.Enqueue/Step 重建改为公开状态 API → 202 重建 → 独立生产 server 真实 embedding → 状态 done/generation=2/indexed_current=true，并保留后续上传自动索引、SIGTERM、永久删除与长文片段检查。日志 `/tmp/cloudnest-ai-control-live.log`。本次只运行此一项真实模型测试，未重跑前批 10k 扫描与混合检索模型测试。
- 本批未修改生产数据库、未部署/合并，格式提取、图片和完整效果评估仍未完成。混合检索提交 d469647 已推送 PR #5，新提交需另查精确 SHA CI。


### G04 文件变更增量索引（2026-10-27）

- 文件版本写入/历史版本恢复、递归复制出的每个普通文件、回收恢复、改变扩展名都在元数据事务内入队。入队失败回滚整笔文件修改；搜索期间旧正文不可作为当前代次返回；同后缀改名与移动无需重算正文，目录不排队。
- 首轮检索/控制/增量 15 项 race 专项通过（18.177s），日志 `/tmp/cloudnest-ai-incremental-special.log`。随后补充未知超长后缀 file_type 不截断/不溢出处理及断言，最终 15 项通过 **20.437s**，日志 `/tmp/cloudnest-ai-incremental-special-final.log`。没有删除旧断言或把服务错误列为正常成功；故障注入用例明确要求文件及任务均回滚。
- 真实模型 `scripts/test-ai-index-model.sh -run '^TestAITextLiveIncrementalFileChangesRebuildActualVectors$'` 通过（函数 **4.49s / 包 6.358s**），日志 `/tmp/cloudnest-ai-incremental-live.log`。真实公开 API 版本替换、历史恢复、复制、回收恢复均自动生成新任务并生成对应原文的实际 1024 维向量；改为 .bin 明确 failed/unsupported_type，再改 .md 自动恢复处理，最终第六代 done/current。仅该项实测，不冒充重跑四项模型套件/完整质量评估。
- 最终 `make -C cloudnest_go check test` 构建/vet/全部 race 单元通过，日志 `/tmp/cloudnest-ai-incremental-checks-final.log`；组合 integration,aimodel,aisearch 标签编译通过。本批改变文件事务入口，因此另外运行普通完整集成回归，结果在下条补记。
- d469647 的 PR/push CI 被后续 a0b943e 提交按工作流并发策略取消，不记为通过。a0b943e 已推送并更新 PR #5，增量提交需另行核验精确 SHA。

- 普通完整 `scripts/test-integration.sh` **250 项顶层测试全部 PASS（354.739s）**，exit 0，六分钟期限未改变，无 FAIL/DATA RACE。日志 `/tmp/cloudnest-ai-incremental-full.log`；该次构建包含最终长后缀生产代码，后加测试仅在 aisearch/aimodel 标签，不改变普通套件源码范围。


### G04 DOCX/XLSX/PPTX 解析与真实检索（2026-10-27）

- 增加共享的有界 OOXML parser：Word 结构与引用故事、Excel 实际单元格字符串/数值/缓存公式、PowerPoint 显示顺序和备注；沿用同一已校验 FD、持久队列及真实 embedding。摘要 Office 路径复用，移除正则去 XML 标签/吞读取错误的重复代码。详细支持范围与资源限额见 AI-OFFICE-EXTRACTION。
- 标准生成器文件使用 python-docx 1.2.0、openpyxl 3.1.5、python-pptx 1.0.2。固定 ZIP/core 时间后两次生成字节一致，最终 DOCX 830849 B、XLSX 5092 B、PPTX 34039 B；SHA 见 `/tmp/cloudnest-office-fixture-hashes.json`，生成器与文件一同入库。没有模型输出 fixture。
- 单元验证包括三种生成器文件、XML实体/跨run/共享字符串/工作表及slide顺序/注释、Strict命名空间，以及坏包/CRC/DTD/深度/展开/输出/中央目录/并发/取消；最终追加 Word 孤立页眉排除、PPT 自动页码不能假装正文和 snippet_offset_basis 断言。`make -C cloudnest_go check test` 最终构建/vet/全部 race 单元通过，日志 `/tmp/cloudnest-office-verified.log`；组合标签编译、仓库/diff 检查通过。
- `scripts/test-ai-search.sh` 第一轮 16 项顶层测试通过（22.360s）；加入摘要接口复用后 **17 项通过（24.839s）**，日志 `/tmp/cloudnest-office-integration-final.log`。其中五个 Office 子用例分别验证三个有效文件在模型断开后 retry/model_unavailable，以及坏包 extraction_failed、旧 .doc unsupported_type，均无部分结果；摘要测试证明实际提取文本被发送给生成端点，跨用户 403、生成端点注入 503 后接口 502、坏包 422 且不送模型/不记成功。注入端点只用于错误/请求协议验证，不冒充真实摘要。
- 随后 Word 引用筛选及提取偏移标记的最终相关单元通过（aiindex 2.672s/service 2.311s）；Office 两项专门回归再次通过（10.084s，`/tmp/cloudnest-office-target-final.log`）。最后 PPT 自动页码排除另由最终全部单元覆盖。
- 普通完整 **250 项顶层 race PASS（360.572s）**，exit 0，无 FAIL/DATA RACE，原 `-timeout=6m` 未调整；日志 `/tmp/cloudnest-office-full.log`。这轮在最后的 Word 引用筛选、PPT 自动页码排除及新增偏移字段之前启动，最终这些变更由对应单元/Office 专项/真实模型验证；不声称重新执行了另一轮全部传输测试。
- 首次真实 Office 测试因错误调用不存在的 app.NewRouter **编译失败**（`/tmp/cloudnest-office-live.log`）；修复为既有容器路由构造后实际模型链路通过（6.580s）。固定 fixture core 元数据后再次通过（6.523s）；Word 引用筛选后的三格式实际链路通过（函数 5.19s / 包 7.823s，`/tmp/cloudnest-office-live-reference-final.log`）。真实模型使用 1024 维 Ollama embedding，实际上传/切块/向量入库/公开混合查询，每种目标文件第一且含 semantic 来源和片段；这不是完整质量评估。
- 前提交 0fc5e77 的 PR/push CI 已精确核对全部成功（40390249796、40390240294）。当前 Office 提交须另查精确 SHA；没有合并、部署或修改生产库。

- 最后对最终生产源码运行完整 `scripts/test-ai-index-model.sh`，**五项真实模型测试全部 PASS（26.010s）**，exit 0：10k 精确扫描 7.50s、实际生产服务/公开重建 9.20s、实际混合查询 2.07s、增量更新 2.42s、三种 Office 检索 2.87s。日志 `/tmp/cloudnest-office-model-suite.log`；本轮 10k 单次查询 1.494429875s，仍是两个真实向量的逻辑副本实验，不是独立语料/生产并发质量。它覆盖最终 Word 引用与 PPT 自动字段过滤、固定 fixture 和偏移标记。

### G04 PDF 与扫描页 OCR（2026-10-27）

- 增加 `internal/pdfextract`：真实 Poppler 子进程、同 FD 输入、分页文本、无文字页 PNG 渲染、大小/页数/并发/CPU/时间/输出限额；Linux 内核 AS 限额与 macOS RSS 监测采用不同实现，未把采样阈值冒充硬 RSS 保证。配置和限制见 AI-PDF-EXTRACTION。
- 四个 reportlab/Pillow/pypdf 合成 PDF 已入库，生成两次 SHA-256 一致。双页文字和扫描票据渲染检查通过；票据无文字层，由真实视觉模型读取商品/金额/IP，不使用预制转录。
- 首次子进程输出限额测试失败（20.690s）：嵌入 `bytes.Buffer` 暴露 `ReadFrom`，`io.Copy` 绕过自定义 Write。已改私有 buffer，输出超限立即取消；后续真实 parser/race 测试通过（2.766s），扩展限额/环境/源大小和替换路径仍读原 FD 的最终专项通过（pdfextract 3.291s / service 1.682s）。首次失败未计为通过。
- 独立一次性 Alpine 3.22 容器安装真实 Poppler 后，Linux arm64 编译的 8 项 parser 测试全部通过，包括真实文字/扫描渲染、加密与页数拒绝、取消/输出限额，以及原生 exec 后 CPU=15s、FD=64、文件增长=0、AS=786432 KiB、无父进程测试秘密环境。容器已自动删除；Linux 运行早于新增的替换路径断言，该断言最终已在 macOS 通过。此测试未测恶意解析样本的峰值内存。
- `make check test` 构建/vet/全部 race 单元通过，组合 `integration,aisearch,aimodel,performance,network` 编译通过（0.740s），最终 vet/仓库/diff 检查通过。单元新增视觉协议拒绝：错模型/未完成/长度截断/空输出/无效 JSON/前后 digest 改变；合成 HTTP 响应不当作 OCR 能力证据。
- 检索/控制/增量/Office/PDF 共 18 项 race 专项通过（28.138s）。其后新增 PDF 手动摘要权限测试，PDF 两项专项通过（10.464s）：工具缺失和模型故障 retry、加密与页数限制 failed、无部分 document/chunk、读取租约释放；摘要先拒绝跨用户 403、坏 PDF 422 不触达模型、真实提取文本进入生成请求且注入 503 后返回 502/不写成功。CI 已显式安装 Poppler 并运行 PDF 专项。
- Ollama 端点连接拒绝且进程/端口均不存在后，恢复本任务 loopback 服务；未仅凭观察超时重启。首次 PDF 上传→实际提取/OCR→真实向量→公开语义检索通过（64.746s）。最终完整 **六项真实模型测试通过（77.525s）**：10k 扫描 8.02s、实际生产服务/重建 9.42s、混合检索 2.18s、增量 2.43s、Office 2.85s、PDF 50.70s。新增 PDF 文字文档由独立生产 server 自动消费并 SIGTERM 退出，扫描票据经真实 qwen3-vl 转录后落库，断言 Laptop、5999.00、192.0.2.42 和 OCR 页标记，目标检索第一且含 semantic 来源。日志 `/tmp/cloudnest-pdf-all-model.log`。
- 本轮 10k 单次查询 1.495824291s，40,960,000 向量字节，第一 cosine=0.818942；仍为两个真实向量的逻辑副本，不是 10k 独立语料、并发或 Recall/MRR 评价。
- 首次完整普通 race 回归在 239 项已通过后触发原六分钟总期限（363.927s 包退出），当时运行 TCP 上传暂停 remote 子项 15 秒，此前无断言失败；同机有真实模型与其他一次性数据库套件并行。保留 `/tmp/cloudnest-pdf-full.log`，不计通过，其他工作已退出后按原六分钟期限单独重跑：**最终普通 250 项 race 全部通过（359.521s）**，exit 0、无 FAIL/DATA RACE，日志 `/tmp/cloudnest-pdf-full-isolated.log`。G04 尚未完成：独立图片/视觉/EXIF、旧二进制 Office、复杂 PDF/OCR 准确率与六类查询完整评估仍待做，生产数据库未操作。前 Office 提交 a6f039d 的 PR/push CI 均已完成且成功（40433982449 / 40433980215）。


## 2026-10-28 图片视觉与 EXIF 日历检查点

- `make -C cloudnest_go check test`：build/vet/全部 race 单元通过。新增图像/EXIF/结构化响应测试；缩略图预算测试移入共享 imageprep 包，原断言保留。
- `CLOUDNEST_TEST_POPPLER_BIN=<实际 Poppler bin> ./scripts/test-ai-search.sh`：21 项顶层测试 PASS，49.355s，包含新增拍摄年份半开边界/未知日期/授权撤销和坏图片/模型不可用不发布；随后新增 Schema 专项单独 PASS（3.988s），证明只读检查不修表、显式迁移保留旧正文且不伪造拍摄/OCR 来源。CI 将运行全部 22 项。
- 单独实际图片模型测试最终 PASS（函数 127.72s / 包 129.651s）。实际上传无语义文件名的合成海边图和票据，调用 qwen3-vl:4b 与 qwen3-embedding:0.6b；分别持久化无文字/有文字标记、真实描述/标签/digest/1024 维向量，票据断言 Laptop、5999、192.0.2.42。海边图的合成 EXIF 保持本地日历，票据无拍摄时间；真实查询目标首位且含 semantic，跨用户/回收查询为空。
- 图片协议首次真实调用失败：Ollama 0.31.1 原生 grammar 拒绝 JSON Schema 的 `maxLength: 8192`，返回 400，任务保持 retry 而无部分发布。去掉模型端字符串长度约束，保留生成 token/HTTP 体积与本地严格校验后成功。第二次运行海边通过，随后日志时间从 21:41 跳到 22:14，读租约取消并失去任务租约，整组判失败；恢复后重新运行通过，没有将失败计作成功。
- 初次恢复测试还因 Docker daemon 未运行而未进入套件；通过本机 Docker 应用恢复后继续，只使用脚本创建的新 MySQL/Redis 容器。全部组合标签编译通过（0.722s），仓库卫生检查通过。

这些图片仅是功能冒烟夹具，生成方式、原始提示和合成日期见 integration/testdata/images/README.md；未获得真实语料 Recall/MRR 或峰值 RSS 结论。旧手动 OCR、GPS/相机信息解析和其余格式继续列为未完成。

最终全部真实模型七项 PASS（172.409s）：10k 精确扫描 6.56s、生产服务/重建 10.26s、混合检索 2.12s、增量 2.67s、Office 2.78s、PDF 47.07s、图片 98.96s。全部使用本机真实 Ollama 与一次性数据库；图片两项实际查询首位含 semantic，旧普通回归独立于模型套件执行。

最终复查发现 encoding/json 会替换非法 UTF-8，因此模型 HTTP 层新增原始字节校验；故障响应实际包含 0xff，确认整项拒绝且不返回部分描述/digest。最终 aiindex race 单元通过（2.618s）。该拒绝路径修正在上述完整模型/普通回归启动后加入，不将此前运行声称为已覆盖这条新错误路径。

普通完整 250 项 race 集成最终全部 PASS（342.872s，exit 0），无 FAIL/DATA RACE，保持原六分钟期限；日志 `/tmp/cloudnest-image-full.log`。本机模型套件全部结束后单独启动普通回归。最终提交的 Linux CI 另按精确 SHA 核验，不以旧提交通过状态代替。


## 2026-10-28 手动 OCR、GPS 与当前源发布

上一图片提交 dd6baff6859786f474a06f78b069360a1cec3a24 的 PR CI 40591164475 与 push CI 40591156645 均全部 SUCCESS（build-test/vulnerabilities）；这些结果不冒充下述未提交改动的 CI。

- 新 GPS/相机 parser、共享视觉准入及取消单元通过；完整 build/vet/race 单元通过，go mod verify 与仓库检查通过。去除 goexif 直接依赖，没有新增解析库。
- 全部检索/控制/增量/格式/手动 OCR 专项 **27 项 PASS（44.787s）**，包括 read grant 撤销、实际版本替换、回收发生于模型处理中、审计失败回滚、只读 source_hash Schema 检查/显式迁移、旧来源数据保留但不冒充当前内容、并发 upsert 与空 OCR 清空。
- 首次手动协议/真实模型测试失败：测试 fixture 直接构造 Config，未配置 AIOCRModel，服务在模型初始化阶段返回 502。生产 Config 有默认值。仅给新测试明确配置 qwen3-vl:4b 后，原断言通过；未将 502 改成允许值。
- 真实手动 API 两张图片单独通过（函数 129.04s / 包 130.957s）：票据实际识别 Laptop/5999/192.0.2.42，返回合成相机/GPS/负海拔；无文字海边图返回空 OCR/空文档摘要，保留合成拍摄时间/偏移。该首次成功发生在随后补 source_hash 之前，最终完整模型回归另记录。
- 视觉准入测试初版的 HTTP 假服务未读请求体便等待连接取消，导致 httptest.Close 挂起。确认具体测试 PID 后发 SIGQUIT 保留堆栈（52.883s，判 FAIL）；栈位于测试 handler 等待处。修改夹具先读完请求体，保留“两种入口占满两个名额、第三次拒绝、取消释放”原断言，最终 race 单元通过（aiindex 3.240s / imageprep 2.575s）。没有修改产品取消行为来迁就夹具。

HTTP 协议夹具只用于错误/并发边界测试，不替代模型识别验收。手动结果新增源摘要需显式迁移；历史 metadata/任务保留，旧来源未知行不自动补摘要。仍未覆盖其余图片格式、完整语料效果或生产集群压力。

最终全部真实模型 **8 项 PASS（289.963s）**：10k 扫描 7.76s、生产服务/公开重建 7.80s、混合 2.06s、增量 2.26s、Office 2.75s、PDF 50.35s、图片 120.09s、手动 OCR 92.68s。该组已包含 source_hash 发布/筛选代码。随后补充非法 tags 响应的 502 映射、有限失败码和将标签校验提前，最终五项手动专项全部 PASS（12.395s）；service/handler race 单元通过（1.920s/2.680s）。全部组合标签编译通过（0.817s）。普通回归单独运行，记录另附。

最后将手动服务的三分钟上下文提前到授权/Pin 之前，并保留源验证被取消时的原始上下文错误；service race 单元通过（1.752s），handler 使用已通过缓存。这项期限覆盖修正晚于完整模型/普通回归编译，前述运行不声称覆盖新增期限位置。

本批普通完整 **250 项 race 集成 PASS（347.529s，exit 0）**，无 FAIL/DATA RACE，原六分钟期限不变；真实模型套件结束后独立执行。日志 `/tmp/cloudnest-manual-full.log`。最终提交的 Linux CI 状态另按 SHA 核验。

## 2026-10-28：六类真实检索评估

`make ai-eval` / `scripts/test-ai-retrieval-eval.sh` 与原 `make ai-model` 分开。前者要求绝对新报告路径 CLOUDNEST_AI_EVAL_REPORT 和原生 Poppler 目录 CLOUDNEST_TEST_POPPLER_BIN，使用 `integration,aimodel,aieval`、race、15 分钟总限额、全新 MySQL/Redis；未改变普通六分钟期限或生产请求预算。CI 编译完整标签并执行无需模型/数据库的分页响应契约测试，不执行真实质量评估。

- 指标/清单 race 单元通过（1.732s）；首次标签编译通过（0.719s）。
- 首次实际执行 30 文件索引成功，但评估器误读 total，第一查询断言失败，包 259.916s。日志 `/tmp/cloudnest-eval-baseline.log`，报告 AI-RETRIEVAL-BASELINE-20261028.json（completed=false）。改为 pagination.total；一次字段替换不完整导致编译失败，修复后契约测试通过（0.912s/0.720s）。
- 第二次实际执行包 219.828s，24 查询全部执行，质量失败。完整原始报告 AI-RETRIEVAL-BASELINE-V2-20261028.json；日志 `/tmp/cloudnest-eval-baseline-v2.log`。不将非零退出隐藏为成功，也未修改标签或质量门槛。
- 初始评估器 `make check`、完整 race 单元通过（`/tmp/cloudnest-eval-check.log`、`/tmp/cloudnest-eval-unit.log`）。候选门槛/配置类型修改后 config/service race 单元通过（1.640s/1.806s）；最终 `make check test` 通过（`/tmp/cloudnest-eval-final-unit.log`）。
- 上一手动 OCR 提交 210e0a29dda66af957c83182c92a874ccdf3093b 的 PR 40650376141 / push 40650369377 两次 Backend verification 均 SUCCESS。它们不验证本轮新代码，后续 CI 按精确 SHA 核验。

后续真实运行和回归证据如下；G04 和总体目标保持进行中。

- 候选阈值校准 `0a55bdc` 的全量评估 **FAIL（262.479s）**：dev 通过、heldout 因海岸图片漏召回和发票排序失败，完整报告 AI-RETRIEVAL-CALIBRATED-20261028.json，日志 `/tmp/cloudnest-eval-calibrated.log`。没有据 dev 通过提前认定质量完成。
- 查询指令/日历分离后 aiindex/service race 单元通过（2.793s/2.406s），组合标签/分页契约通过（0.933s），`make check test` 通过（`/tmp/cloudnest-eval-query-check.log`）。
- 同一清单先执行 dev-only 实际评估 **PASS（263.238s）**，再执行 all **PASS（252.296s）**。报告 AI-RETRIEVAL-QUERY-DEV-20261028.json、AI-RETRIEVAL-QUERY-ALL-20261028.json，日志 `/tmp/cloudnest-eval-query-dev.log`、`/tmp/cloudnest-eval-query-all.log`。全量两组 R@5=0.958333、MRR=1/0.916667，负例6/6空结果、六项边界通过；发票族 R@5=0.75 及误匹配仍未解决。已暴露保留集不等于盲测。未再次重复上一检查点的八项模型冒烟作为本轮证据。
- 最终本地 `scripts/test-ai-search.sh` **27 项 PASS（45.716s）**，无 FAIL/DATA RACE，日志 `/tmp/cloudnest-eval-protocol.log`，没有降低三分钟专项期限。
- `e72e7898c632abf28f334630559719c15eabac53` 的 PR CI **40698520271** / push CI **40698511791** 均 SUCCESS。已核对 steps：依赖校验、构建/vet/race 单元、普通隔离集成、真实网络套件、AI 专项、performance/aimodel/aieval 编译/分页契约、漏洞扫描均通过。普通完整集成本轮以该 Linux CI 为证据，不冒称又执行了一遍本地 250 项。
- 一次 push 返回本地代理 1082 连接失败和非零退出；直接 `ls-remote` 核实远端已是 e72e789，随后 CI 也确认同 SHA。没有据错误文本重建分支或改系统代理。此前 afebb0674503a657d91fc0257be61d0513f2152f 的 PR/push CI 40695344082 / 40695333973 也均通过。

后续只补报告/说明的提交以新 SHA 单独核验 CI；上面的全量评估证明的是 e72e789 代码，标签/门槛保持固定。HEIC/HEIF/WebP、旧二进制 Office、复杂语义/rerank、独立语料与资源压力仍未完成。

## 2026-10-29 重排协议与 SQL 竞态

- `508a54e`：新增真实 Ollama 二元 token 评分、六秒整批预算和共享 OCR 名额；可选排序前 20 条、保留尾部、失败全量回退、模型后再次 SQL 验证。`make -C cloudnest_go check test` PASS（日志 `/tmp/cloudnest-rerank-check.log`）；服务/provider/config 专项 race PASS（`/tmp/cloudnest-rerank-service-unit.log`）。
- `scripts/test-ai-search.sh` PASS，包 **51.615s**，日志 `/tmp/cloudnest-rerank-protocol.log`。日志实际有 **27 个顶层测试**；新增一个重排测试包含撤权、撤权后模型失败、重命名、源替换、代次更新五个子用例。模型返回是协议夹具，SQL 与公开 API 为真实执行。此前口头计数“28 项”有误，以此日志计数为准。
- `integration,aisearch,aimodel,aieval` 组合标签及分页契约 PASS（0.726s），日志 `/tmp/cloudnest-rerank-eval-compile.log`。
- `14fb1a6`：复核发现 JSON 缺失 logprob 会落成 float64 零值，改为必须存在的指针字段，拒绝缺失/null，不推测概率；aiindex/service race PASS（2.655s/2.297s），日志 `/tmp/cloudnest-rerank-presence-unit.log`。
- 两轮实际 18 对开发文本预检与来源/提示/模型哈希详见 AI-RERANK；不能代替全 API 评估。首轮预检脚本随后修改，第二轮记录最终源哈希；一轮启动前 503 没有进入评分，已如实记录。
- 完整真实 API 重排评估以独立 JSON 记录执行状态；只有 completed/pass 及逐条实际 applied 状态才能证明相应范围。当前未把重排或 G04 标记完成。

复现完整实际重排：在六类评估命令设置 `CLOUDNEST_AI_EVAL_RERANK_MODEL=qwen3-vl:4b`（或另一个已安装并验证的兼容模型）；仅改变本轮测试容器的应用配置，不修改用户 .env。仍使用原标签/门槛，报告增加模型 digest 与每条重排状态/分数。配置模型却发生回退时评估失败，即使基础检索指标通过也不冒充重排成功。

- 完整实际重排已执行 **PASS 296.575s**，函数 294.58s。代码 `508a54e`，日志 `/tmp/cloudnest-eval-rerank.log`、报告 AI-RETRIEVAL-RERANK-20261029.json。30 文件/24 查询，15 applied / 9 单候选或空结果 skipped / 0 fallback，dev/已暴露 heldout R@5=1、MRR=1，6/6 负例为空，六项边界通过；发票族 R@5 从 0.75 改善到 1。配置查询及结果尾部仍有误匹配，见 AI-RERANK，未声称全结果精确。
- `508a54e` 的 PR/push CI 40821274600/40821269034 被后续提交按并发工作流取消，**不是通过**；`14fb1a6` 的 PR/push CI 40821445728/40821438853 已启动，待按精确 SHA 核对结果。不要把上一个 26894d2 的绿色状态冒充本次检查。

后续核验：`14fb1a6` 的上述两个流水线均因后续提交取消，不记通过。报告提交 `2a4cd353fe3836d8f768f7358079006d82a92348` 的 PR **40822104735** / push **40822099118** 均 SUCCESS；完成构建/vet/race、普通隔离集成、网络、AI 专项、组合标签/分页契约及漏洞检查。

## 2026-10-29 扩展压力集失败基线

- 先提交 `3609763` 冻结 stress-v2：48 文件、24 新查询，清单 SHA `a60973d6b4b4d6ee7cab5fa1df732526e53af61f8a6669c18f4c9a70e2da9776`。原 v1 源和门槛不变，新增近似技术、物品、签约/付款状态干扰及私有样本。指标/清单 race PASS（1.722s），评估标签/分页契约 PASS（0.786s）。
- 全量实际运行 **FAIL 340.159s**，日志 `/tmp/cloudnest-eval-stress-v2.log`、报告 AI-RETRIEVAL-STRESS-V2-20261029.json。48/48 文件、24/24 查询、六项权限等边界完成。正例两组 R@5/MRR=1，负例空结果 dev=0/3、heldout=1/3；另一次重排超时，19 applied / 1 fallback / 4 skipped。没有隐藏非零退出或放宽六秒请求预算。
- v1 通过不代表扩展问题已经解决。保留失败报告和标签；仅检视开发集逐条分数进行下一步诊断，所有 split 仍对实现者可见，不称盲测。专用模型下载与推理验证尚未完成。


## 2026-10-30 约束模型实验归档

专用 0.6B Torch 预检 v1/stress-v2 均完成但仍有高分误报；18 对提示比较与 312 对紧凑结构化判定也未达到过滤要求。数据、可复现脚本与局限见 AI-RERANK，未把进程退出 0 等同于产品质量通过。

`909432c8d6fffa43def1454d0db32049e083ba3f` 的 PR **40896369338** / push **40896358173** 均已核实 completed/success；这是该提交的 CI，不覆盖随后 WebP 源码。
