# Office 内容提取与资源边界

新索引 processor 已支持 DOCX、XLSX、PPTX。使用 Go 标准库读取已通过 blob 租约与完整 SHA-256 校验的同一 FD，不通过文件名重新打开源，不调用 Office 宏、外部链接或操作系统关联程序。提取完成后沿用已有 chunk → 专用 embedding → 受代次/租约约束的原子发布链路。

旧实现遍历 XML 后用正则去标签，会把 SpreadsheetML 的共享字符串编号当作正文，也可能吞掉 ZIP 读取错误。新解析器按 OPC 关系定位主文档和内容部分，核对命名空间及 XML 结构，读取错误或超限不返回部分成功结果。

## 实际支持范围

- DOCX：正文中跨格式 run 的文本、段落、换行、制表符和表格；正文引用的页眉/页脚、脚注和尾注。排除修订删除/moveFrom 内容、未引用的页眉及批注正文；不会把样式 XML 的配置混入文本。支持 Transitional 和主要 Strict 命名空间。
- XLSX：按 workbook 的工作表顺序及关系定位 sheet；解析 inline/shared/rich 字符串，忽略拼音标注和未被单元格使用的共享字符串；保留表名、单元格地址、数值、布尔、日期文本和错误文本。公式保留表达式，并把磁盘上的值明确标为 `[cached: ...]`；不计算公式，不把缺少缓存的值猜成计算结果。数值保持保存的原始表示，尚不套用显示格式或把 Excel 日期序号自动解释为日期。
- PPTX：按 presentation 中的 slide 关系顺序读取 DrawingML 文本，包含段落/表格文本和演讲备注，排除自动页码/日期字段；不按 ZIP 文件顺序或文件名排序猜测展示顺序。输出带 slide 编号和 notes 标签。
- 主文档部分不强制位于常见固定路径，正确解析相对关系。只有所需的内部内容关系会被读取，外部超链接不发起网络请求；需要外部正文或 DOCX altChunk 的输入明确失败。

输出是确定性的**提取文本流**，不是文档渲染或模型摘要。检索增加 `snippet_offset_basis=extracted_text_runes`，rune 偏移指向该文本流，不能当成 DOCX ZIP 字节偏移或 PDF 页坐标。输出中的表/幻灯片结构标记来自文件结构，不是 AI 生成的事实。

尚不支持旧二进制 DOC/XLS/PPT、宏扩展名、密码加密、ZIP64/多卷包、内嵌图片 OCR、图表/公式的视觉渲染、数学排版、外部链接刷新及嵌入对象的递归转换。只有图片且没有可提取正文的输入不会用虚构内容完成索引。普通文本提取不等于这些复杂 Office 内容已完整识别；图像/OCR 和其他格式继续纳入 G04。

## 有界处理

| 边界 | 当前值与行为 |
|---|---|
| Office 源文件 | 32 MiB，超出在校验/解析前按文件元数据预检；实际 FD 尺寸再次核对 |
| ZIP 中央目录 | 最多 1 MiB、2048 项；在 archive/zip 分配 entry 结构前验证目录条目与计数 |
| 声明的展开总量 | 全包最多 64 MiB，包括未选择的部分；溢出/伪造计数直接拒绝 |
| 实际读取 | 单 XML 部分最多 8 MiB，累计最多 32 MiB；读取到 EOF 验证 ZIP CRC，实际输出不依赖声明大小保证 |
| XML | 最多 128 层、累计 200,000 个节点；拒绝 DTD，自定义实体不扩展；每次读取/解析检查取消 |
| 文本 | 256 KiB，超过返回 extraction_limit，不静默截断 |
| 准入 | 每进程最多两个 Office 解析，超额返回 extraction_busy；worker 记录可重试错误 |

不在磁盘展开压缩包，拒绝重复 ZIP 名、路径逃逸、符号链接和加密条目。这里是输入/结构/并发限制，**没有测量或保证特定峰值 RSS**，也不等同于操作系统级恶意文件沙箱。worker 的三分钟总期限和续约规则保持不变。

## 摘要接口复用

现有 `/api/ai/summarize` 的 Office 路径复用相同解析器，通过同一已校验 FD 读取，并设置 30 秒提取上下文。旧 handler 的未使用提取副本、旧 service 的 OpenXML 正则提取已移除。忙返回 503、超限 413、无法提取的包 422；模型失败仍返回 502，不写成功记录。

旧 PDF/文本摘要 helper、OCR/生成 provider 和部分旧模型写入路径仍待统一，不能把本次 Office 重构描述为所有 AI helper 已整改完成。

## 验证与来源

标准生成器与合成文件位于 `scripts/generate-office-fixtures.py`、`integration/testdata/office/`，使用 python-docx/openpyxl/python-pptx，不保存假模型响应。生成器固定 ZIP 与 core 时间；两次再生成已验证字节相同。DOCX 使用合法的未压缩 ZIP，大小 830,849 字节，验证 Office 容器不能误套纯文本源上限。

单元测试覆盖跨 run/实体/表格、Strict 命名空间、引用和孤立页眉、共享/内联字符串、公式缓存标记、实际 slide 顺序/备注、真实生成器文件，以及重复项、路径逃逸、外部正文、损坏 XML/CRC、DTD、深度、输出/部分/条目上限、取消及两个并发解析后的拒绝。

集成测试验证三种格式成功提取后模型不可用进入 retry、坏包与旧格式进入明确 failed、没有部分 document/chunk；摘要接口复用检查实际提取内容、跨用户拒绝、坏包不发送给模型和失败不记成功。另用本机真实 Ollama 实际上传、提取、1024 维向量落库并查询，三种文件的预期文档均为首条并含 semantic 来源与片段。它是链路测试，**不是六类查询的 Recall@K/MRR 评估**。各次结果与编译失败记录见 [TESTING](TESTING.md)。

格式依据：[Microsoft WordprocessingML 结构](https://learn.microsoft.com/en-us/office/open-xml/word/structure-of-a-wordprocessingml-document)、[SpreadsheetML 单元格取值](https://learn.microsoft.com/en-us/office/open-xml/spreadsheet/how-to-retrieve-the-values-of-cells-in-a-spreadsheet)、[PresentationML 幻灯片文本](https://learn.microsoft.com/en-us/office/open-xml/presentation/how-to-get-all-the-text-in-a-slide-in-a-presentation)。实现使用 Go 标准库，没有引入 .NET SDK 或迁移业务数据库。
