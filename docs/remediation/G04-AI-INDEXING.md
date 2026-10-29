# G04 AI 索引与检索（进行中）

## 当前实际能力

已接入上传事务内持久任务、带租约和代次的 worker，以及真实 UTF-8 与 DOCX/XLSX/PPTX 结构化文本提取、切块、独立 Ollama embedding 和原子向量保存。生产服务启动时运行 worker，并在 SIGTERM 时取消和等待退出。原上传后吞错 goroutine 已移除。

**目前支持文本类、DOCX/XLSX/PPTX、PDF 文本与扫描页真实 OCR，以及实际混合检索；JPEG/PNG/GIF 图片视觉与 EXIF 日历检索也已接入；其他图片格式、旧二进制 Office 与完整效果评估仍未完成。** 未支持的后缀会明确失败为 `unsupported_type`；模型不可用会记录 `model_unavailable` 并重试，不写“分析成功”。手动 OCR、摘要和原关键词搜索接口仍保留，不能据此认定完整 G04 已完成。

旧 `ai_task` 和 `ai_metadata` 保留，新 `ai_index_job` 单独记录文件当前期望的索引代次。没有修改既有用户数据，也没有执行生产迁移。本分支为开发中的阶段检查点，不能作为完成的 AI 系统发布。

## 已实现的事务协议

- 上传完成在同一 MySQL 事务中创建 file、初始版本、AIIndexJob、配额账本与完成回执。入队失败则全部回滚。完成回执重放直接返回原结果，不重建任务。
- `file_id` 是任务主键；`generation`、源 SHA-256 与 pipeline 标识一起描述输入代次。同源同 pipeline 的重复入队不改变任务，显式 force 或源变更增加 generation 并重置尝试次数。调用方必须在已有事务内执行 Enqueue；锁顺序为已有 namespace/tree → file → job。
- 领取使用 READ COMMITTED 和 `SELECT … FOR UPDATE SKIP LOCKED`。90 秒租约使用数据库 UTC 时间；token 为密码学随机的 256 位值。进程内不持有队列真相。其他 worker 可以接管到期租约。
- 每次实际领取消耗一次尝试，最多五次，包含进程崩溃未确认的领取。失败仅存储允许的错误码，不存模型原始响应、路径、内容或凭据。可重试失败以 5、10、20、40 秒等待；第五次失败终止，必须通过后续显式重建重试。
- 续约、失败与发布均以 generation/source/pipeline/token/未到期租约为条件。旧 worker 即使在重领或重建后完成模型调用，也不能修改新任务。
- 发布先锁当前文件并核对仍为有效文件、源摘要一致，再锁任务，运行短数据库结果写入。发布结束再次核对租约，并在同一事务标记 done。回调失败或租约失效会撤销结果写入。回调不允许模型调用或文件 IO。
- 任务没有 storage_id 外键，不让排队任务长期保留旧物理对象。worker 持有独立 blob Scope，文本 processor 按当前文件 Pin，核对完整 SHA-256/尺寸并从同一已校验 FD 读取；测试已验证真实读取与 scope 释放。永久删除文件已在同一事务删除其任务；普通回收文件不可发布新结果。

worker 单次处理默认三分钟、领取/续约/发布操作五秒上限，每 20 秒续约任务。停止时取消 processor，尝试在独立五秒清理上下文记录 worker_stopped/retry，关闭并等待读取 scope。processor 必须响应取消；执行器不是强制终止任意解析代码的沙箱。服务已启动一个索引 worker，与传输维护 worker 一起受 SIGTERM 生命周期控制；模型任务不在请求 goroutine 内运行。

模型还未调用时就把任务标记成功、错误降级为成功、在数据库事务内等待模型、用旧 generation 覆盖新结果，均不属于有效实现。

## Schema 与升级

显式 `migrate` 新增 `ai_index_job`、`ai_index_document`、`ai_index_chunk`，不把历史 `ai_task` 行猜测成可执行任务。普通启动只检查 InnoDB、字段类型/精度/可空性、主键和调度索引，缺失则拒绝启动；不会自动修表。

升级仍需备份、在副本演练、停止旧写实例，再使用已有显式迁移流程；旧二进制不会入新队列，不能混跑。现已提供所有者显式调用的历史列表/批量重建 API（AI-INDEX-CONTROL），没有单独 CLI；旧文件不因部署自动发送给模型。没有为 AI 迁移整个业务数据库。

## 文本与 embedding 边界

- 支持 TXT/Markdown、JSON/YAML/XML/TOML/INI/CONF/ENV、日志、CSV/SQL 和常见源码后缀；读取上限 256 KiB，超过直接记录 extraction_limit。拒绝非法 UTF-8 和 NUL，不把截断内容冒充完整提取。
- 每块最多 768 个 Unicode 字符，优先在末段句末/换行处分割，保留 96 字符重叠；存储提取文本流的 rune 起止位置，最多 512 块。空白文件可以完成为零块，不伪造向量。
- `AI_EMBEDDING_MODEL` 默认 `qwen3-embedding:0.6b`，与 `AI_OLLAMA_MODEL` 的视觉/生成用途分离。每次调用专用 `/api/embed`，最多 8 块，明确 truncate=false；HTTP 120 秒、响应 2 MiB 上限，拒绝重定向。当前 keep_alive=1m，便于分批重用后自动卸载。
- 严格校验返回模型、数量、维度（1–4096）、有限值和非零范数；归一化后按 float32 little endian 保存。任一块失败不发布部分结果。
- 文档保存源 hash、pipeline、generation、模型名/digest/维度、块数与原文字节数。embedding 前后读取 `/api/tags` 的 digest，不一致则 model_changed/retry。远端接口无原子模型快照，运维不可在索引过程中反复改写同一模型 tag；后续模型切换须按 digest 重建并隔离检索空间。
- 块、文档摘要、成功审计与任务 done 在同一受租约保护的 MySQL 事务发布。重建替换旧块；永久删除一起删除文档/块/任务。向量暂存于既有 MySQL，作为持久可重建数据；现已接入流式精确向量扫描、关键词/RRF 融合、当前文件权限/代次复核及片段；当前选择和 10k 向量实测见 [混合检索协议](AI-HYBRID-SEARCH.md)。这不代表完整 G04 验收。

接口契约来自 [Ollama embedding](https://docs.ollama.com/api/embed) 和 [模型 digest 列表](https://docs.ollama.com/api/tags)。

## 本地模型预检（与产品验收分开）

已在 loopback Ollama 0.31.1 下载并实际调用 `qwen3-embedding:0.6b`。2026-10-25 的三个合成文本返回 3 × 1024 维有限向量；输入、digest 和观测值见 [原始预检记录](AI-MODEL-PREFLIGHT-20261025.json)。请求关闭自动截断并设置 keep_alive=0，调用后模型已卸载。结果只是 provider 可用性验证，不是 CloudNest 检索 Recall/MRR，也不是模型选型最终质量结论。

现有 qwen3-vl:4b、deepseek-ocr:3b、deepseek-r1:1.5b 虽有 manifest，但预检时其引用的权重/config blob 缺失；不能认定视觉/OCR 已可用，没有删除这些旧记录。2026-10-26 已通过官方 pull 恢复 qwen3-vl:4b，manifest 引用的 5 个 blob 均存在；已对合成票据图片实际调用成功，商品、金额、测试 IP 转录准确（20.581s），仍不是 CloudNest 图片索引验收。deepseek 的两项旧缓存仍不完整，没有删除。后续 PDF 检查点已接入扫描页真实 OCR；独立图片与真实视觉标签仍需建立并验证处理链路。

## 验证与剩余工作

`integration/ai_index_queue_test.go` 在守卫验证过的全新 MySQL/Redis 容器上检查原子入队、并发领取、重建后的代次隔离、过期重领、有限重试、结果事务回滚、源变更/回收拒绝、只读 schema 核对、跳过锁定任务和发布末尾过期。测试中的 metadata 标记只验证事务隔离，不冒充模型输出或索引内容。普通 API 夹具现在验证 `queued` 状态，不再用旧后台审计行冒充分析成功。队列检查点 248 项完整 race 集成通过（335.569s）；本批接入文本后的最终 250 项完整 race 集成通过（354.027s），构建/vet/race 单元通过；中间编译失败与修复见 [测试记录](TESTING.md)。

真实模型测试 `scripts/test-ai-index-model.sh` 使用合成公开文本、一次性 MySQL/Redis 和本机 Ollama，验证实际上传、完整提取、短/长文切块、1024 维向量落库、重新构造 worker 后重建、永久删除和生产服务自动消费/SIGTERM 退出。最新运行通过（函数 9.20s / 包 10.859s）。迁移查询与数据库文档 cosine=0.769925、与海边描述文档=0.151140，只是链路冒烟检查，未测完整搜索 Recall/MRR。一次运行因 Ollama 进程已停止而失败；确认服务确实不存在、模型文件完整后恢复本机服务再通过，详见 TESTING。

接下来的必要实现：

1. DOCX/XLSX/PPTX 已接入有界结构解析与真实模型链路，见 [Office 提取](AI-OFFICE-EXTRACTION.md)；PDF/扫描页已接入独立进程与真实视觉转录，见 [PDF 提取](AI-PDF-EXTRACTION.md)；图片视觉与 EXIF 日历见 [图片索引](AI-IMAGE-INDEXING.md)；继续扩展旧二进制 Office、其余图片格式并验证完整效果。
2. 手动 OCR 与新图片处理器、Office/PDF 摘要已统一；继续评审其余旧生成/问答 helper。
3. 扩展真实语料和并发规模测量，校准词法/语义阈值与是否需要 ANN/rerank。
4. 完善混合检索的复杂意图、其余 EXIF 元数据与继承权限，保留当前文件级过滤及降级。
5. 状态、历史回填/重建及版本/复制/回收恢复/扩展名变化的增量入队已实现，见 [索引控制](AI-INDEX-CONTROL.md) 与 [增量索引](AI-INDEX-INCREMENTAL.md)。后续格式扩展须继续保留代次/删除隔离与回归验证。
6. 六类 30 文件/24 查询合成评估集和真实失败基线已建立，见 AI-RETRIEVAL-EVALUATION；继续校准并扩展独立语料，保留 Recall@K/MRR 与失败案例。

G04 尚未完成，G05–G11 仍按里程碑推进。

2026-10-29：可选重排接入前 20 个候选，六秒整批预算、与 OCR 共享推理名额，失败保留原顺序；成功或失败后均再次检查 SQL 权限/源/代次。五个真实 SQL 竞态子用例通过；完整实际 v1 重排回归 PASS，两组 R@5/MRR=1，发票排序改善，尾部误匹配仍存在。默认关闭，独立质量和冷启动仍需验证，见 [AI-RERANK](AI-RERANK.md)。

2026-10-27：新增公开状态/历史回填/重建接口，五项控制测试与原六项检索专项通过（12.838s）。真实模型测试已改为通过公开重建 API 交由独立生产服务消费，最终状态 done/current（函数 10.68s / 包 16.700s），详见 AI-INDEX-CONTROL 与 TESTING。

2026-10-27：Office parser 已按 OPC 关系提取 Word 正文/引用页眉注脚、Excel 实际单元格值与公式缓存标记、PowerPoint 顺序/备注；摘要接口复用同一解析器，移除旧重复正则提取。源 32 MiB、XML/展开/节点/并发及输出上限、三种真实生成器文件与真实 Ollama 检索验收见 AI-OFFICE-EXTRACTION 和 TESTING。

2026-10-27：PDF 同 FD 原生提取、空文本页渲染与实际 qwen3-vl 转录已接入持久任务及摘要 API。Linux 地址空间限额、macOS RSS 监测边界、依赖配置和未完成项见 AI-PDF-EXTRACTION。Office 提交 a6f039d 的 PR/push CI 均已通过。

2026-10-28：JPEG/PNG/GIF 首帧经共享解码预算、EXIF 方向纠正和真实像素视觉调用生成描述/标签/文字，再由独立 embedding 索引。新增八个来源字段须显式迁移；拍摄时间采用相机本地日历且不回退上传时间。范围、兼容性故障和剩余工作见 AI-IMAGE-INDEXING 与 TESTING。

2026-10-28：手动 OCR 改为同 FD 有界处理与真实结构化视觉调用，保留 GPS/相机/日期字段，新增 source_hash 升级检查、源/权限发布复核与成功审计事务；旧关键词查询排除不同源的 OCR，详见 AI-MANUAL-OCR。

2026-10-28：新增固定六类检索评估与独立执行脚本，30 个文件全部真实上传/提取/embedding，24 条查询实际调用搜索 API。初始基线失败：dev Recall@5 72.08%，六条负查询均产生误匹配。候选词覆盖/语义门槛、配置类型、同义日历和查询指令整改后，两组 Recall@5 均 95.83%，六条负例均为空；发票族仍只有 75%，更大独立验证及细分类待做。无生产迁移。见 AI-RETRIEVAL-EVALUATION。

2026-10-29：执行前冻结 48 文件/24 新问题的 stress-v2 后，实际评估 FAIL（340.159s）：两组正例 R@5/MRR=1，但负例空结果只有 0/3 与 1/3，另一次重排超时。六项权限等边界通过；保留失败与标签，不能由 v1 通过推断完整相关性通过。见 AI-RERANK。
