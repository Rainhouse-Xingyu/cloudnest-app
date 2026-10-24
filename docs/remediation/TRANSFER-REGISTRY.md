# 上传临时对象登记、容量与回收

2026-10-23，G03 / PR #4。此协议覆盖新上传的分片和合并候选；ZIP 导出、系统临时文件和 AI 缓存尚未纳入。逻辑用户配额与此物理临时预算独立，G03 仍在进行。

## 容量与所有权

新任务 transfer_protocol=1。transfer_volume 保存随机卷 ID、规范根路径、持久额度及最低可用空间；transfer_artifact 保存候选 token、任务/用户、相对路径、种类、状态、保留字节、实际长度/摘要、设备/inode 与数据库期限。记录保留供审计，不因成功而删除日志。没有新增 storage_id 引用或阻止既有永久删除/GC 的外键。

必须先在事务内登记 intent 和预留，再以 Root/O_EXCL 创建 0600 文件、取得实际 FD 的排他 flock，Sync 文件及各级父目录后登记 writing/inode。随机路径不存在即可清理记录；若崩溃在创建文件与登记 inode 之间，现存文件没有可证明的登记身份，进入 blocked 并继续收费，不自动认领或按名称删除。已知尚未成功打开文件的目录错误可以事务撤回 intent/合并占用，修复目录后原任务可重试。

流式写入不得超过保留大小，Seal 核对内部计算的 SHA-256、精确长度并 Sync 文件/父目录，随后进入 ready。分片在原元数据事务内 ready→chunk；合并在文件/版本/完成回执/逻辑配额事务内 ready→published（实际采用）或 discard（去重采用已有对象）。只有 published 或最终 done 不占临时额度。discard/deleting/blocked、未知提交与过期记录持续计费，不能凭期限提前释放空间。已发布文件交还现有 Storage/引用/GC 生命周期，临时回收不会删除它。

分片写入预留正常片长 + 1 字节以检测越长，Seal 后缩到实际长度；合并另预留完整文件大小。因此完成前需要同时容纳现有分片及合并文件。额度不足返回 507。每任务只允许一个合并候选，每卷最多四个 intent/writing/ready 合并；活动竞争返回 409/Retry-After: 1，提交完成后原 upload_id 重试仍返回原始 200 回执。卷额度增长只在分配事务持有卷行锁；状态交接/回收只能减少额度，不长期锁住全局分配行。

TRANSFER_TEMP_LIMIT_BYTES 默认 8 GiB，仅用于首次登记新卷；合法范围 1 字节至 4 TiB，非法值启动失败，不能静默使用默认值。现有卷额度以数据库为准，通过显式 set-transfer-limit 修改并审计。调低到使用量以下会拒绝新分配，保留现有内容。分配还用实际 statfs 可用空间核对 64 MiB 余量及所有未写完预留，部分写入保守重复计入，不用逻辑配额冒充磁盘空闲。

## 租约与进程中断

90 秒数据库 UTC 租约、20 秒续约；续约失败取消操作，不能继续提交。过期并不授权删除正在 IO 的文件。根目录的 admission flock 覆盖文件创建至 FD 锁与 inode 登记之间的交接；写入/合并候选始终持有 FD 排他锁，合并读取分片持有共享锁。回收必须取得排他锁，即使数据库租约已过期仍不能删除活进程持有的文件。进程退出后内核释放锁，登记的过期对象方可回收。

服务处理 SIGTERM/Interrupt，取消请求和 worker，收紧正在读取 body 的 deadline，先关闭文件，再等待有界 Shutdown（10 秒，超时强制关闭）。正常退出与 SIGKILL 的测试分别覆盖真实服务慢体、真实子进程持锁；它们不是主机断电、文件系统损坏或数据库恢复演练。

## 回收与可见性

worker 每 30 秒进行一轮，最多选取 64 个符合条件的登记记录，单轮 30 秒；不扫描前缀认领旧文件。未使用的根不会在后台被自动创建。活任务的已确认 chunk 保留；取消/过期任务的登记 chunk、过期未挂接候选及 discard/deleting 可处理。取消响应释放逻辑预留，物理清理异步进行，不能保证响应时已经回收。

每个对象重新验证卷/任务、可信 Root、普通文件、设备/inode、占用上界、所有 Storage 路径别名与 UploadChunk 引用。替换 inode、符号链接、无登记 inode 或未知引用均保留并报告。先事务提交 deleting 与删除意图审计，再 unlink/Sync，最后事务提交 done/释放预算与完成审计。unlink 后最终事务失败仍收费，重试可处理已缺失文件。未知提交不冒险 unlink。blocked 需要操作者审核，没有“按年龄强制删除”或自动解除命令。

回收前要求证明整个当前数据库的 SELECT/TRIGGER 元数据可见性，并拒绝任何触发器；低权限看不见触发器不等于不存在。普通运行账号可以保留 discard，交由专门维护账号处理；可选 TRANSFER_MAINTENANCE_DSN 只供后台 worker，必须实际指向相同 MySQL server_uuid/数据库。它不需要 GC 引用审计专用的 PROCESS 权限。不得为方便自动扩大真实账号权限。DDL/升级时必须停写实例和回收 worker，不能并发更改结构。

根控制文件 .transfer-volume-v1 绑定随机卷 ID 与实际 MySQL server_uuid/数据库的摘要；错库、错根或复制的控制文件不自动重新绑定。根路径与数据库记录也必须一致。支持本机 Linux/macOS 的 flock/statfs，拒绝已知 NFS/SMB/WebDAV 等网络卷；不支持多主机网络文件系统或活跃克隆卷。主机迁移/设备号变化/数据库恢复必须审核，可能保守 blocked，不能自动猜测所有权。控制文件及父目录须和数据一起备份，不能单独删除重建以规避记账。

## 显式升级与维护

停所有旧写实例、备份并在副本演练后，沿既有顺序执行显式 migrate 及文件名/引用约束迁移；普通启动只核对。新增 transfer_volume/transfer_artifact 表，upload_task 的 transfer_protocol/transfer_volume/merge_artifact，upload_chunk.artifact_token。协议默认 0，不把升级前正在进行的分片认领为新登记对象；未完成任务继续/进度/完成返回 409。G02 contract_version=1 的所有者可取消释放逻辑预留，旧物理字节保留；更早无可信契约的任务保持拒绝。旧有效完成回执仍可复用。

以下为部署操作者的命令说明，本轮未对真实库执行。使用受控环境提供 MYSQL_DSN；维护命令不要求 JWT/Redis，必须显式 DATA_DIR，目录是包含 storage 的应用数据目录。CLI 10 分钟总期限，每次对账仍最多处理 64 条；有积压可重复执行。

```sh
DATA_DIR=/absolute/cloudnest-data ./cloudnest audit-transfers
DATA_DIR=/absolute/cloudnest-data ./cloudnest set-transfer-limit 8589934592
DATA_DIR=/absolute/cloudnest-data ./cloudnest reconcile-transfers
```

audit-transfers 只读，无 SQL 写入或文件初始化；未初始化卷明确失败，不伪造零用量。两个检查命令输出 checked/removed/busy/blocked、卷 ID、持久额度及 charged_bytes/unwritten_bytes/broken_records，不泄露磁盘路径。blocked/busy/异常计量返回非零；批次之外仍有积压时需继续处理。set-transfer-limit 会创建新卷控制文件（若尚未初始化），更新持久额度并审计，不删除内容。CLI 使用 MYSQL_DSN；TRANSFER_MAINTENANCE_DSN 仅由正常服务 worker 使用。

## 证据和剩余范围

新增 22 项真实集成：并发不超额、分片加合并预算/交接、statfs 余量、租约过期但 FD 活跃、真实 20 秒续约与原期限后 Seal、真实子进程 SIGKILL、实际 Go 服务 SIGTERM 中断慢体、删除前/后审计故障恢复、半登记/替换 inode/未知别名保留、合并并发上限、70 条分批回收和未登记相似文件保留、取消/过期不删 published、容量策略损坏/错卷、内容/摘要/封存限制、发布回滚、旧结构迁移、实际 CLI，以及真实/隐藏触发器、错数据库和预创建失败恢复。完整测试结果见 [测试记录](TESTING.md)。

此前 159 项完整回归发现两个状态码预期差异：可信分片路径损坏应 409，配置不可用应 503；已修正错误映射/预期。定向回归又发现旧符号链接夹具重建 inode 后期望成功，与登记身份契约冲突，改为保留再恢复原 inode，另有独立替换 inode 拒绝测试。失败不计为通过。

尚缺 ZIP/缓存预算、下载恢复校验/聚合带宽、真实大文件与峰值内存/HTTP2、主机断电、备份恢复及生产副本升级演练。完成时整文件校验仍可能占用较长事务/连接，需大文件验证再优化，不能以声明上限宣称已验证 1 TiB。旧未登记文件继续保留；没有不经审核自动认领的恢复捷径。G04–G11 未完成。

每次传输速率的 query/全局高峰/Range 绕过已在后续批次修复，见 [速率报告](TRANSFER-RATE-POLICY.md)；上传写入前等待仍持有 FD 锁/登记容量，并响应已有期限取消。

新空文件的合并候选仍登记/收费至少 1 字节，实际 Seal 长度为 0，不跳过卷/FD/审计约束；逻辑用户预留为 0，详见 [空文件协议](EMPTY-UPLOADS.md)。

后续 pause/resume 已贯通空任务和普通任务；暂停保留逻辑/临时预留与确认分片，未挂接旧代次候选依实际锁回收。新增代次字段须显式迁移，详见 [控制协议](UPLOAD-CONTROL.md)。

## 请求级 ZIP 扩展

后续增加 kind=archive、task_id=0/transfer_epoch=0 的派生登记，不虚构上传任务；严格 .archives-v1 随机路径，按实际压缩写入前申请增长，与上传共享卷/真实空闲量。HTTP 输出结束前保持收费与 FD 锁，90/20 秒续约不检查 UploadTask；同一 reconciler 可回收过期归档，未知身份/引用保留。旧 worker/CLI 必须整体更新，本批不新增列或自动接管旧路径。完整限制、错误与实际证据见 [归档协议](ARCHIVE-TRANSFERS.md)；缓存持久预算仍待做。

新缩略图持久缓存后续已增加 thumbnail/cached、可空 cache_key 与唯一索引、共享卷/独立子预算、FD 读取保护及统一受控回收，见 [缓存协议](THUMBNAIL-CACHE.md)。旧未登记缓存不采用；所有写实例/worker/维护 CLI 须同步升级并显式 migrate，不能由旧二进制接管新类型。
