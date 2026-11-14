# PDF 提取与扫描页 OCR（G04 检查点）

## 实际实现

上传 PDF 仍走既有持久任务、代次/租约、独立 blob Scope、完整源摘要验证和原子发布协议。processor 从已校验的同一文件描述符传给原生 Poppler，不通过历史路径重新打开。`pdfinfo` 核对页数和加密状态，`pdftotext` 提取 UTF-8 文本，按换页符验证实际页数。输出包含 `[Page N]`，检索偏移仍是整个提取文本流的 rune 偏移，不是 PDF 字节或页面坐标。

无文本页经 `pdftoppm` 渲染为最长边 1600 的 PNG，再将实际像素通过 Ollama `/api/chat` 发送到独立 `AI_OCR_MODEL`（默认 `qwen3-vl:4b`）。转录标为 `[Page N OCR]`，模型前后 digest 不一致、协议不完整、模型名错误、生成因长度停止、空转录、无效编码和超限都拒绝整份结果；不发布已经提取的前几页。真实 OCR 仍可能误读或产生幻觉，结构验证不等于识别准确率。

OCR 请求关闭 stream/think，temperature=0，num_predict=4096，num_ctx=8192；响应复用有界 HTTP client（120 秒、2 MiB、不跟随重定向）。每页转录最多 32 KiB。系统提示要求仅转录，将图片文字视为数据。处理后继续调用独立 embedding 模型，持久化真实向量并提供混合检索。当前 document 记录 embedding digest；OCR digest 仅在每次转录前后核对，尚未保存为可查询的独立提取来源字段，多页处理期间仍禁止运维改写模型 tag。

手动 PDF 摘要复用同一 FD/parser/OCR 链路及资源边界，仍先检查文件读取权限；旧进程内 `GetPlainText` 提取与 `ledongthuc/pdf` 依赖已移除。其他旧图片 OCR/生成 helper 的统一尚未完成。

## 资源和错误边界

- PDF 源最多 32 MiB、64 页、其中最多 8 个需要 OCR 的页；总提取文本含页标记最多 256 KiB，拒绝静默截断。
- 每进程同时最多两份 PDF 提取，满额立即 `extraction_busy/retry`；整份提取（含 OCR）两分钟上限。既有 worker 总预算三分钟，超时/退出仍取消并等待。
- 每次原生工具独立进程组：20 秒墙钟、15 秒 CPU、64 个 FD、禁 core dump/普通文件增长；stdout 按信息/文本/PNG 分别限制为 64 KiB/256 KiB/8 MiB，stderr 16 KiB，超量取消并等待退出。没有 shell 拼接用户文件名，也不传数据库/JWT 环境变量。
- Linux 在原生 exec 前设置 768 MiB `RLIMIT_AS`。macOS 当前内核实测拒绝所需地址空间限制，因此用每 100 ms 的 RSS 监测（单次监测最多 1 秒），超过 768 MiB 终止；**这是监测阈值，存在采样间超调，不是硬 RSS 上限**。并发预算也不等于整台机器/服务总 RSS。
- 这是解析故障和资源隔离，**不是文件系统/网络安全沙箱**。原生工具仍以服务身份执行，需要受限服务账户和持续更新的系统 Poppler；完整部署沙箱及恶意样本内存峰值评估仍待后续。
- 缺工具/无效工具配置：`extraction_unavailable/retry`；OCR 或 embedding 不可用/无效 OCR 响应：`model_unavailable/retry`；模型变更：`model_changed/retry`；已知边界超限：`extraction_limit/failed`；加密、坏 PDF 和不支持结构：`extraction_failed/failed`。部分原生工具内部分配失败只返回非零退出，无法可靠区分损坏文件与其内部资源错误，不伪造精确诊断。
- 手动摘要对应提取缺失/繁忙 503、内容超限 413、无效文档 422、OCR provider 错误 502，失败不写成功任务。

## 部署与回归

配置 `AI_PDF_BIN_DIR` 为包含 **原生** `pdfinfo`、`pdftotext`、`pdftoppm` 的绝对目录。Linux 安装 `poppler-utils` 后通常为 `/usr/bin`；macOS 可使用 Homebrew Poppler 的 `bin` 目录。此配置默认空：其他格式继续工作，PDF 明确进入有限重试，不猜测运行时 PATH。不提交本机 Codex runtime 路径。缺依赖后的失败文件通过既有显式重建 API 重试，不自动全库扫描。主进程入口的私有 `--internal-pdf-tool` 只为子进程设置限额并 exec，不初始化数据库。

测试使用 `scripts/generate-pdf-fixtures.py` 生成的公开合成 PDF：双页迁移/权限文档、无文字层票据、加密页、65 页超限文档。生成两次 SHA-256 一致，文字页和扫描票据已渲染检查。真实模型测试不使用预制向量或伪造 OCR；HTTP 合成响应测试只验证拒绝协议错误。

```sh
CLOUDNEST_TEST_POPPLER_BIN=/usr/bin go -C cloudnest_go test -race ./internal/pdfextract ./internal/aiindex
CLOUDNEST_TEST_POPPLER_BIN=/usr/bin ./scripts/test-ai-search.sh
CLOUDNEST_TEST_POPPLER_BIN=/usr/bin ./scripts/test-ai-index-model.sh
```

CI 显式安装 Poppler 并设置测试路径；真实模型仍为本地 opt-in。当前 250 项普通 race（359.521s）、六项真实模型（77.525s）、PDF 故障/摘要专项（10.464s）通过；具体范围及中间失败见 TESTING。

## 尚未完成

独立图片索引/场景描述/EXIF、旧二进制 Office、复杂多栏/表格/手写/中文扫描件的识别评估、混合页中仅图片区域的额外 OCR、空白页的可靠判别、完整提取来源 schema、六类查询的 Recall@K/MRR 与 G04 余项。当前遇到空白无文字页也尝试 OCR，空输出明确失败；不把失败猜测成“空白成功”。不能据本检查点宣称完整 PDF 版式恢复或完整 G04 已完成。

依据：[Poppler 项目与版本](https://poppler.freedesktop.org/)、[Ollama 视觉输入](https://docs.ollama.com/capabilities/vision)、[Apple setrlimit 契约](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setrlimit.2.html)。本地实测 Poppler 26.05.0；其不是当前最新版本，正式部署须使用维护中的系统包，不从本开发辅助环境打包依赖。
