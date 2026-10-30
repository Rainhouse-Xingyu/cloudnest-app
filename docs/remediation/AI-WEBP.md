# 静态 WebP 图片处理

2026-10-30：静态 WebP 接入共享 imageprep 后，实际内容识别、缩略图、手动 OCR 和持久图片索引使用同一有界解码。新增 golang.org/x/image v0.47.0；其模块依赖同时将 x/sys 更新为 v0.49.0、x/text 更新为 v0.43.0。没有数据库迁移或环境配置变更。

支持静态 VP8（有损）、VP8L（无损）、VP8X/ALPH 透明通道；模型仍只接收准备后的 PNG。扩展名只决定索引分支，实际容器由魔数识别。RIFF 总长、块边界、奇数字节填充、单个图像块、EXIF 标志与重复块会校验；动画标志、ANIM/ANMF 明确拒绝，不能用一张静态图冒充完整动画。缩略图对损坏的已识别 WebP 返回 422，对动画返回 415；索引提取失败不发布部分文档。

继续使用 64 MiB 编码体积、24M 像素、16384 单边、512 MiB 进程共享估算预算及两个解码名额。WebP 容器最多扫描 4096 块，以只读偏移跳过压缩数据与未知负载；EXIF 最多 256 KiB。VP8X 画布在解码前检查像素预算，固定依赖在分配 VP8/VP8L 像素前检查实际尺寸与画布一致。原头部读取 1 MiB、处理 15 秒边界保持；编解码器内部仍为协作取消，预算不是 OS 硬内存隔离。

EXIF 既接受 TIFF 数据，也接受常见 Exif\0\0 前缀，复用既有 TIFF 偏移/字段校验。元数据可位于像素之后；图片 AI 准备沿用方向纠正及相机本地时间，不从上传时间伪造拍摄时间。ICC/XMP 不用于地点、日期或文字推断。

用固定 3×2 颜色/透明像素验证三种编码，以带合成 EXIF 的容器验证方向与拍摄时间；异常覆盖长短边界、坏填充、重复/超限 EXIF、动画、块数量、超大画布和实际尺寸不符。已有发票 PNG 无损编码为 WebP，用于真实上传/模型/检索验收，不宣称增加了独立视觉样本。生成脚本、编码器版本与摘要见 integration/testdata/images/README.md。

仍未完成：动画 WebP、HEIC/HEIF、旧二进制 Office、复杂 PDF/中文手写、独立检索质量与完整资源压力；本项不构成 G04 完成。

依据：[Go 官方 WebP 解码器](https://pkg.go.dev/golang.org/x/image@v0.47.0/webp)、[WebP RIFF 容器规范](https://developers.google.com/speed/webp/docs/riff_container)。

## 已执行验证

- `make -C cloudnest_go check test` 通过：完整构建、vet、race 单元；日志 `/tmp/cloudnest-webp-check.log`。首轮单元发现超大 VP8X 画布错误分类不准确，已在解析画布时先执行统一 Weight 校验，后续完整检查通过。
- 隔离 MySQL/Redis、`integration,aisearch,aimodel` 三项专项通过，包 **89.260 秒**；真实 WebP 视觉/索引/语义搜索子项 **79.96 秒**。真实像素提取 Laptop、5999、192.0.2.42，独立向量、模型 digest 与文件权限检查通过；其余两项是缩略图有界 JPEG/跨用户拒绝及损坏格式状态回归。日志 `/tmp/cloudnest-webp-integration.log`。使用 qwen3-vl:4b digest `a1b29c246319cdcb1f3842f36d29f198f737c0cf516ba1bcba34b1b974e87980`。这是编码链路冒烟，不是全语料质量评估。
- govulncheck v1.9.0 通过：调用路径/导入包 0 个漏洞，另有 3 个依赖模块漏洞未被应用调用；不声称依赖模块完全没有公告。日志 `/tmp/cloudnest-webp-vuln.log`。

- AI 搜索专项 28 个顶层测试 PASS（49.003s），含新增 WebP 故障/无部分索引的两个子用例。日志 `/tmp/cloudnest-webp-ai-search.log`。
