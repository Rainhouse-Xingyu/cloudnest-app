# 独立自然照片检索质量评估

2026-11-27/28 UTC：扩展既有两个合成图像的基线。此前同像素/OCR/EXIF/真实检索测试使用本机 qwen3-vl:2b 再次通过，用例 55.88s、包 58.642s；它不证明本节自然照片的质量。自然照片首轮失败与后续诊断如下，完整质量结果仍另行核对。

## 执行前固定的输入与门槛

清单 `cloudnest_go/integration/testdata/retrieval/dataset-photos-v1.json` 的 SHA-256 为 `df42938c7f032ec6afecff297d0033d1b511c6e6bbc209296f8b2a0214e1b515`。六张照片均在第一次模型调用前逐张人工查看，记录可见内容、作者、原始下载地址、许可、字节数、尺寸和 SHA-256。三张 CC0，三张由作者或 NPS 发布到公有领域；每张的确切出处及许可说明由清单链接，不把 Wikimedia 网站页脚的结构化数据许可当成照片许可。

照片分别来自 [Julianibarra 的海岸](https://commons.wikimedia.org/wiki/File:Waikiki_Beach,_Hawaii.jpg)、[Jon Preston/NPS 的 Ruby Beach](https://commons.wikimedia.org/wiki/File:Ruby_Beach_2_Jon_Preston_(23104527056).jpg)、[Paolo Neo 的林间溪流](https://commons.wikimedia.org/wiki/File:Forest_nature.jpg)、[jhvanwa/Jeff Hollett 的巨树](https://commons.wikimedia.org/wiki/File:Redwoods_in_CA_-_53645819332.jpg)、[Bobtalbot61 的雪山](https://commons.wikimedia.org/wiki/File:MRSR.Mt.Rainier.jpg)和 [CommunistSquared 的城市](https://commons.wikimedia.org/wiki/File:Seattle_Skyline_WSB.jpg)。不向这些作者发送消息。

这是六个独立拍摄输入、七份实际上传：额外一份是 Bob 私有的近似海岸照片，不计作第七个独立场景。Bob 的巨树照片明确共享给 Alice。16 条查询分为 dev/heldout 各六条正例、两条负例，覆盖普通海岸、去年海岸、溪流、巨树木栏杆、雪山、城市及无答案/无拍摄日期情况。标签仅使用可见内容；不要求从像素识别地名。实现者可见全部查询，heldout 不是盲测。

每组门槛沿用旧基准：正例宏 Recall@5 ≥ 0.90、MRR@10 ≥ 0.80，负例空结果率 = 1。Precision@5 分母固定为五，同时记录 Recall@1/3/5。保留失败，不在看到输出后修改标签、模型提示词或门槛来宣称通过。

测试先校验所有原始 JPEG，再使用 Go JPEG 解码和 PNG 重编码剥离原 EXIF、注释和文件名，以 `entry-N.png` 通过真实 API 上传。原始作者说明、人工观察、地址和标签都不会进入索引。部分 PNG 明确添加合成的去年/今年六月十五日 +08:00 EXIF；这些时间不代表原照片的真实拍摄日期，其他文件不补时间。报告分别保留原文件和上传表示的 SHA。

真实 worker 调用实际视觉模型和 embedding，验证源摘要、代次、两个模型 digest、1024 维向量、图像来源和时间，再调用生产自然语言检索。额外验证跨账户、共享后撤权、回收站/未知拍摄日期、真实图像重建时旧代次不可见、新代次可检索，以及模拟模型服务 503 时明确降级且保持授权。503 是明确故障注入，不当作真实模型成功响应。

## 复现

`python3 scripts/prepare-ai-photo-corpus.py /tmp/<新的私有目录>` 下载固定原始字节，不下载模型。拒绝重定向、非固定主机、已有目标、摘要或长度变化；网络中断保留已下载文件供核查，换新目录重试。不得将临时下载提交仓库。初次素材探查连续请求收到 Wikimedia 429，随后改成批量元数据请求并对下载间隔五秒；没有修改来源或许可来绕过限制。

设置 `CLOUDNEST_AI_PHOTO_ROOT` 为该目录，`CLOUDNEST_AI_EVAL_REPORT` 为不存在的绝对 JSON 路径，`CLOUDNEST_TEST_AI_MODEL=1`、`CLOUDNEST_TEST_AI_EVAL=1`。使用 `scripts/test-macos-backend.py --go-tags integration,aimodel,aieval --filter '^TestAINaturalPhotoRetrievalEvaluation$'` 启动独立 MySQL/Redis。可显式设置 `CLOUDNEST_TEST_IMAGE_VISION_MODEL=qwen3-vl:2b`，报告保存实际 digest；省略仍使用 4B，不更改生产默认配置。8 GiB 主机串行推理时不同时运行构建或其他推理。

普通 CI 只验证冻结清单、许可/摘要/路径边界、标签与 opt-in 测试编译，不伪造本机模型或联网下载成 CI 的质量通过。选取的六个场景仍很小，不代表生产照片分布、并发吞吐、恶意媒体资源峰值、盲测或跨模型质量承诺。此前 4B 的 model_unavailable 原因仍未确认，生产默认模型不因这次 2B 验证改变。

## 首轮失败及诊断整改

[首轮原始报告](AI-NATURAL-PHOTOS-INITIAL-20261127.json)：用例 165.44s、包 168.037s，第一张图片处理 88468ms 后进入 retry/model_unavailable，没有文档、向量或检索质量结果。不能把未执行的 16 个查询算作通过。300 次独立两秒采样覆盖整个失败及自动卸载；观察到 runner RSS 最高 3239888 KiB、模型报告的 VRAM 2654197512 字节，只是采样观测，不是进程内存上界或 OOM 根因证明。

原服务日志显示 2026-11-27 06:31:27 UTC 的 `/api/chat` 返回 200，生成 4096 token。随后用相同生产 `imageprep.Prepare` 与 `AnalyzeImage`，经只捕获该公开测试请求的 loopback 代理独立复测：[提供方诊断](AI-NATURAL-PHOTOS-PROVIDER-20261128.json)确认 HTTP 200、done=true、done_reason=length、最终 content 为空，存在反复生成的推理字段。调用明确发送 think=false；私有原响应保留摘要，报告不发布原始推理。由此定位的是这套模型/runtime 组合的未完成输出，不将它描述为已证实的网络中断、模型丢失或 OOM，也不据此解释此前 4B 的所有失败。

新增稳定诊断：`vision_output_limit` 为终止失败，模型/配置调整后由所有者显式重建；`invalid_vision_response` 为有上限的自动重试；真正连接/HTTP 不可用保留 `model_unavailable`。图片分析和 PDF 转录均在长度终止时返回专用错误，它仍兼容原 ErrVisionResponse 拒绝路径。队列仅接受固定诊断码，绝不写入模型原文；无需改表、迁移历史任务或重新索引成功文档。手动 OCR 的失败历史也保留输出上限原因，原 HTTP 错误契约保留。原生页增加这两类可操作说明。

四种真实 API/队列回归先复现三个旧错误：无效 JSON、仅推理耗尽上限、看似完整 JSON 但 length 终止，均被错误归类为 model_unavailable。首轮用例 34.74s/包36.812s 失败；整改后同四用例 32.98s/包34.700s 通过。均验证未发布文档/向量、释放任务和物理读取租约，API 仅返回固定码，没有泄漏合成的私有模型标记。普通单元继续覆盖完整响应、截断、错模型、来源变更和取消，不降低严格校验。

根据用户允许下载模型的授权，另取官方[显式 2b-instruct 包](https://ollama.com/library/qwen3-vl:2b-instruct)作同语料复测。它与原[2b 包](https://ollama.com/library/qwen3-vl:2b)有不同 manifest 和模型/projector 包装，报告分别记录 digest；旧包保留，不做同名覆盖。没有修改固定清单、质量门槛、提示词、4096 生成预算或 8192 上下文以掩盖失败。

## 显式 Instruct 结果与局限

[完整原始报告](AI-NATURAL-PHOTOS-INSTRUCT-20261128.json)通过，用例 318.25s、包 321.000s。七份上传全部由真实 worker 完成；私有近似图不计独立来源。视觉模型 `qwen3-vl:2b-instruct` 的 digest 为 `afc393e3572c07db0bc84aa460d928a16c75f3fbb9ca9e0dccc4fbd2dafb573e`，本机下载 1930568531 字节；embedding 仍为 `qwen3-embedding:0.6b` / `ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d`。同 Ollama 0.40.17 的 `/api/show` 中，新包不声明 thinking 能力，旧包声明；这里只报告实际包和 runtime 差异，不外推所有同名模型的行为。

| 分组 | 正例/负例 | 宏 Recall@1 | Recall@3/5 | MRR@10 | 负例空结果 | 固定分母 Precision@5 |
| --- | --- | --- | --- | --- | --- | --- |
| dev | 6 / 2 | 0.916667 | 1 / 1 | 1 | 2/2 | 0.233333 |
| heldout | 6 / 2 | 0.916667 | 1 / 1 | 1 | 2/2 | 0.233333 |

所有正例首项均相关；两条海岸查询各有两个相关文件，所以宏 Recall@1 不等于首位正确率。共享可检索→撤销后不可见、跨账户日历、回收与未知日期、真实城市图重建期间旧代次不可见/第2代可检索、注入模型503后明确降级且不越权，七项边界均通过。全部查询均实际使用 embedding；没有假向量、预计算描述或通过文件名作弊。

**仍有七条尾部误匹配**：stream-dev 返回多余雪山/巨树，stream-held 返回多余巨树/海岸，trunk-dev 返回多余海岸/雪山，snow-held 返回多余海岸。正例实际返回21项，其中14项相关，返回项精确率为14/21，不能把固定门槛通过说成结果全部准确。生产阈值0.4/0.25、重排默认关闭均未修改；仅提高阈值会同时丢掉分数0.4707的正确日历海岸结果，不据这个可见小集调阈值宣告通用质量完成。尾部相关性与独立语料验证保持开放。

使用同一新包再跑既有两图真实回归，通过（用例30.31s、包33.525s）：票据 OCR 保留 Laptop、5999 和文档保留IP，实际1024维/语义命中、EXIF去年过滤、跨用户及回收拒绝均通过。普通 Swift 113注册/86实际/27条件跳过（Keychain，0.342s），Go check/单元race、八目标服务/Agent构建和双架构Release/严格签名均通过。初次AI专项45项顶层通过，但1个顶层与4个PDF子项因缺少Poppler跳过（81.017s），不冒充完整PDF验证，原生补验另记。新原生诊断文字由Release编译确认，没有声称重新操作过其GUI。

素材准备脚本在另一个新私有目录实际下载六个原文件，长度/SHA全部与清单一致。测试前后329个相关Go/App/Core/评估源文件指纹一致，报告保存运行时diff/harness/manifest哈希；日志、资源分段和源码摘要见[验证记录](AI-NATURAL-PHOTOS-VALIDATION-20261128.json)。自然照片报告时间窗内111次资源观测：模型报告视觉VRAM最高2654732286字节、embedding1239752376字节，单个runner观测RSS最高3076832KiB；不当作硬内存上界或OOM根因。完整300次采样另含后续票据回归，末次模型均已自动卸载。

补验使用官方[Poppler 26.11.0 bottle](https://formulae.brew.sh/formula/poppler)，SHA及依赖清单记录于验证JSON；系统Homebrew目录不可写，因此只在私有运行时目录重定位动态库并重新临时签名，未更改系统安装/服务。pdfinfo读取普通PDF成功，加密PDF明确报告密码错误。最终47个AI顶层回归全部通过、零跳过，包65.399s；包括原PDF故障矩阵、手动摘要授权，以及后续单独加入的真实扫描PDF输出上限用例0.53s，证明length不会发布看似完整的转录或留下源租约。此后新增的PDF测试有单独源码hash，未将其倒记为先前真实模型运行覆盖。本机Poppler26.11结果不替代历史Linux26.01故障根因，后者仍未确认。

精确新提交的远端CI发布后另核对。未合并PR、修改生产库或删除旧模型/用户数据；G04剩余质量、G10资源/异常及全部G11仍继续。
