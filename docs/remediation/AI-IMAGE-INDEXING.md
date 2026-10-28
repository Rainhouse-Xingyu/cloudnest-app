# 图片视觉索引与 EXIF 日历

G04 图片检查点支持 JPEG、PNG 和 GIF 首帧：上传事务入队 → 校验并固定源 FD → 有界解码/方向纠正/缩放 → 实际 Ollama 视觉模型 → 文本切块与独立 embedding → 受租约/代次保护的原子发布。文件名不会代替图像内容输入模型。

## 图像处理边界

`internal/imageprep` 与缩略图共用同一进程准入池，避免两个独立 512 MiB 预算叠加。编码文件不超过 64 MiB，头配置读取不超过 1 MiB，单边不超过 16,384，像素不超过 24 MP；最多两个解码任务、16 个等待者、等待一秒。估算权重为像素 × 16 + 16 MiB，总准入 512 MiB，**不是 OS 内存硬上限或实测峰值 RSS**。

处理全部八种 EXIF 方向，按最长边 1600 缩放，输出 PNG 不超过 8 MiB；模型调用前释放解码预算。解码、变换和编码共用十五秒上下文，读入/逐行处理/写出检查取消；标准库编解码内部 CPU 不能强制抢占。源始终为已校验 FD，不重新按路径打开。GIF 只读取首帧，不声称索引所有动画画面。

JPEG APP1 和 PNG eXIf 解析有界，最多检查 4096 个标记/块，PNG EXIF 上限 256 KiB 并检查 CRC。仅访问 IFD0、EXIF 与 GPS 子目录，每目录最多 256 项，检查偏移/重复标签/环；不遍历缩略图、MakerNote 或任意 next-IFD。捕获 Orientation、DateTimeOriginal、OffsetTimeOriginal，以及有界的相机/GPS 字段，不用文件修改时间、上传时间或模型推断补齐日期。损坏元数据明确失败。

## 真实视觉输出及来源

沿用 `AI_OCR_MODEL`（默认 `qwen3-vl:4b`），向 `/api/chat` 提交准备后的真实像素、结构化 JSON Schema 与禁止执行图内指令的提示。返回简短中文场景描述、最多十二个标签、原语言可见文字和 has_text。没有文字的场景允许空 OCR；不会插入“未识别到有效文本”作为搜索内容。

生成上限 4096 token，HTTP 响应 2 MiB/120 秒；验证模型名、完成状态/stop、assistant、必需字段/类型和文本边界。描述最多 4096 字节、每标签 192 字节、转录 32 KiB，拒绝 NUL、非法 UTF-8、矛盾 has_text、超长和截断响应。模型端 Schema 不设置字符串 maxLength：本机 Ollama 0.31.1 的 grammar 编译拒绝 8192 次重复；边界继续由服务端强制校验。

视觉调用前后核对模型 digest。PDF 多页 OCR 也必须使用同一 digest；随后独立 `AI_EMBEDDING_MODEL` 产生向量。描述、标签和可见文字分别带来源标记写入提取文本流。向量、八个新来源字段与 done 状态在同一个发布事务内更新；图片改成文本后新代次会清空图片来源字段，旧代次不参与搜索。

新增 `ai_index_document` 字段：extraction_kind、vision_model、vision_digest、has_ocr、captured_at_local、capture_offset、visual_description、visual_tags。普通启动只检查 Schema；升级须显式 migrate。旧行默认空来源/false/NULL，**不会自动认定旧 OCR 成功或补拍摄时间**，需所有者显式重建。没有执行生产迁移。

## 拍摄时间与查询

`captured_at_local` 是相机日历上的墙上时间，`capture_offset` 单独保留 EXIF 明示的偏移。MySQL 绑定格式化字符串以避免 DSN loc 改写，返回 `captured_at_local`、`capture_offset` 和 `capture_time_basis=exif_camera_local`，不伪装成 UTC 时间点。可接受年份 1000–9999；相机时钟和元数据本身可能不准确。

“去年/今年/上个月/本月”先按产品 Asia/Shanghai 日历确定年份/月度。图片或明确“拍摄/拍的”查询且没有“上传”时，用相机本地日历范围（含起点、不含终点），附 `capture_time_uses_exif_camera_local_calendar` 警告。未知拍摄时间排除；偏移不改变相机日历分类。明确“上传”仍使用 UTC 上传时间范围。

搜索仅返回当前授权且匹配源 hash、done 任务、generation/pipeline 的结果；返回前再次核对。tags/description/has_ocr_index 来自当前索引，不再固定为空或 false；拍摄信息与描述遵循相同权限。尚不支持复杂混合时间意图或任意自然语言日期解析。

## 验证及待完成

单元覆盖八方向、大小端 TIFF、损坏偏移/计数/CRC/日期、取消、路径替换后读取原 FD、共享准入及模型协议拒绝。数据库专项覆盖缺字段启动拒绝/显式迁移、未知日期与年份半开边界、跨用户/授权撤销、模型不可用/坏图片不发布部分结果。实际模型测试使用合成海边图和文字票据，上传无语义文件名，检查真实视觉来源、1024 维向量、拍摄时间与自然语言搜索；运行结果另记 TESTING。

素材来源与完整生成提示见 [图片夹具说明](../../cloudnest_go/integration/testdata/images/README.md)。合成 EXIF 是测试输入，不是现实拍摄事实。这两张图只证明功能链路，不能代表准确率、Recall/MRR 或真实照片分布。

手动 OCR 统一及 GPS/相机有界解析已接入，见 [手动 OCR](AI-MANUAL-OCR.md)。仍需：HEIC/HEIF/WebP、复杂图片/中文和手写评估、模型资源并发压力和完整六类查询评估。G04 和整体 G00–G11 目标均未完成。
