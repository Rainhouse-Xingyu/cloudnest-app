# 手动 OCR 与图片处理统一

`POST /api/ai/ocr` 现在与持久图片任务共用 `imageprep.Prepare` 和 `Ollama.AnalyzeImage`：授权 → 验证源摘要并打开固定 FD → 有界解码/EXIF/方向处理 → 真实像素视觉分析 → 当前源及权限复核 → 元数据/成功历史/审计原子写入。移除了旧整文件 `os.ReadFile`、两份重复 EXIF 解析器、重复照片摘要 helper 及 goexif 依赖。

## API 与模型行为

保留 item、exif、doc_summary 以及原有 EXIF 字段；新增 has_text、model、model_digest，EXIF 增加 capture_offset 与 capture_time_basis。无文字图返回空 OCR 文本及空 doc_summary，不再存储“未识别到有效文本”。有文字图的 doc_summary 使用同一次视觉调用的简短事实描述，不再对 OCR 文本另发生成调用或按字节截断 UTF-8。

模型为 `AI_OCR_MODEL`，独立于 embedding；不再使用旧默认生成模型处理图片。人工 tags/location/landmark/description 等输入仍保留原契约。合并 EXIF GPS 后的标签限制为 255 个 Unicode 字符，description 输入最多 8192 字节、用户 captured_at 最多 64 字节，请求体上限 16 KiB；超限 400。用户填写的日期只是其描述字段，不冒充 EXIF 日期。

当前图片解码支持 JPEG/PNG/GIF 首帧。HEIC/HEIF/WebP 尚未接入转换器，不能将旧接口曾接受后缀当成实际已支持。错误区分：不支持后缀 400，源校验/源变化 409，无权限 403，坏图片/元数据 422，图像上限 413，繁忙/期限 503，模型故障/非法响应 502；数据库或审计失败不返回成功。

## 有界 EXIF 与共享资源

在既有 JPEG APP1/PNG eXIf 上限内，只读取 IFD0、EXIF、GPS 三个目录，每个至多 256 项，阻止重复字段、循环/越界指针；忽略 MakerNote、缩略图和任意 next-IFD。新保留相机 Make/Model（每字段最多 256 字节含终止符）、经纬度和海拔。坐标要求有效方向和三个有理数，检查零分母、分秒及地理范围；缺少坐标或方向不拼凑位置。海拔按显式参考位保留正负，缺省参考按 0 处理，未知参考值拒绝。字段类型和方向/海拔语义参考 [CIPA/JEITA EXIF 2.31 草案，GPS 标签章节](https://home.jeita.or.jp/tsc/std-pdf/CP-3451D.pdf)；未声称覆盖所有更新版本扩展标签。

拍摄时间仍仅使用 DateTimeOriginal，不以文件修改时间或 GPS 时间推断；相机本地时间与显式偏移分开返回。经纬度来自可修改的元数据，不代表已验证的真实地点。

图片预处理仍与缩略图共用估算内存池。图片分析和 PDF OCR 另外共用单进程两个视觉调用名额，零排队，防止在模型阶段无限保留 PNG/base64 请求。取消或失败释放名额；这不是跨实例模型资源配额。手动请求整体三分钟，发布事务独立最多五秒；模型内部推理的实际停止行为由 provider 决定，客户端关闭请求不保证立即回收 GPU。

## 当前源、撤权与发布

模型调用在事务外。发布时按 file → permission 的顺序锁定，复核有效文件、storage_id、文件名及源 SHA-256。非所有者需仍有 read/write 文件授权（沿用该接口权限契约），并锁定实际授权行。源替换、回收、已完成的撤权会拒绝结果；并发撤权按事务锁顺序确定先后。

元数据 upsert、AiTask success 与 OperationLog ai_ocr 在同一事务；审计失败全部回滚。文件行锁使并发首次发布不会新建重复 metadata。既有重复行不被删除，本批不声称清理了历史数据。

`ai_metadata` 新增 `source_hash CHAR(64) NOT NULL DEFAULT ''`，普通启动只读检查，缺少或类型不符拒绝启动；必须显式 migrate。旧行保留空来源，不能猜成当前文件来源。旧关键词 API 只连接源摘要匹配当前 storage 的 metadata，因此换版后旧 OCR 不再命中；历史行/任务保留。需要恢复旧结果可由授权用户重新运行 OCR。没有执行生产迁移。

手动 OCR 不把持久索引任务伪标 done，也不直接生成向量；上传后的持久任务继续负责语义索引。模型失败任务仅记录有限错误码，成功事务以外不写“成功”。

## 验证与剩余范围

单元覆盖大小端、南纬/东经、负海拔、零分母/范围/非法引用/重复目录、部分 GPS 缺失、共享视觉名额和取消释放。数据库/API 专项覆盖权限及输入拒绝、实际准备后的 PNG 请求、模型故障无成功结果、模型期间换版/撤权/回收、审计回滚、源身份迁移不改历史、换版排除旧 OCR、并发 upsert 与空文字清空。

真实模型另通过公开手动 API 调用合成票据与海边图，断言实际商品/金额/IP、无文字结果、GPS/相机/日期契约及读取租约释放。协议故障测试中的 provider 响应只用于停住事务边界，不用于证明识别质量。完整运行结果及失败修正见 TESTING。

仍需 HEIC/HEIF/WebP、复杂图片/手写/中文评估、完整六类查询标注集与 Recall/MRR、旧二进制 Office、部署资源压力和 G06 继承权限；G04 尚未完成。
