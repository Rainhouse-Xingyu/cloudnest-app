# 单主图 HEIC/HEIF 的隔离解码

2026-10-30：新增可选原生助手 cloudnest-heif，Go 服务仍不依赖 CGO。设置绝对路径 IMAGE_HEIF_HELPER 后，HEIC/HEIF 可进入实际图片索引、手动 OCR 和缩略图。助手用 libheif >= 1.23.2 及内置 HEVC 解码器；默认未配置时，索引记录 extraction_unavailable 并重试，手动 OCR/缩略图返回 503，不生成伪成功结果。配置不触发下载安装或生产迁移。

[构建、协议及依赖说明](../../cloudnest_go/native/heif/README.md)。服务先验证权限并固定已校验源 FD，再将 FD 3 传给子进程。只接收一个主图，容器裁切/旋转/镜像由 libheif 完成；多主图文件在像素解码前拒绝。原图最多 64 MiB/24M 像素/16384 单边；返回最大 1600 边长 RGBA 与最多 256 KiB TIFF，Go 检查魔数、尺寸/缩放、alpha 标志、精确长度、EXIF 后编码受限 PNG。EXIF 日期仍是相机本地日历，不用上传时间填充；不重复应用 EXIF 方向。

子进程有 CPU/描述符/输出文件限制，Linux 使用 768 MiB RLIMIT_AS；macOS 为可超调的 RSS 监测。Go 限制操作十五秒、stdout/stderr 长度，取消整个进程组并等待监控结束。处理时独占共享图片解码预算；其 512 MiB 数值不是 native+Go 实测 RSS，也不是文件系统/网络沙箱。输出通过管道传送，无新增落盘临时图。原生依赖更新不由 govulncheck 覆盖。

## 已执行证据

- macOS 26.3.1 arm64，libheif 1.23.2/libde265 1.1.1；使用官方 v1.23.2 头文件（tag commit ac1cb05c39008f01525c991ff8b88f84ddf70fd2）与本机原生库编译，C11/-Wall/-Wextra/-Werror 通过。未把已安装的通用 heif-dec 直接作为服务命令。
- 原生测试真实解码合成发票、读取合成 EXIF 日期 2025-06-15 10:20:30/+08:00，以及 2000×1000 → 1600×800 缩放；Go 协议测试覆盖截断/多余数据/尺寸/alpha/多主图拒绝、子进程退出/诊断上限/取消和预算释放。最终补充“无 HEVC 解码器、多主图提前拒绝”后，重新编译助手并运行 imageprep/handler/config race，分别 6.593/2.690/1.404 秒通过，日志 /tmp/cloudnest-heif-final-unit.log。
- 第一轮实际隔离集成 FAIL 70.273s：索引与搜索通过，但手动 OCR 的夹具未指定实际 OCR 模型，缩略图扩展名白名单未包含 HEIC；已修复。此前口头猜测 MIME 不准确，实际原因以代码白名单为准。日志 /tmp/cloudnest-heif-integration.log。
- 第二轮 **PASS 122.227s**，真实模型链路子项 **113.62s**：实际上传 HEIC → 原生像素 → qwen3-vl:4b OCR → 独立 embedding → 索引发布 → 语义搜索、跨用户隔离、手动 OCR 和实际 JPEG 缩略图均通过；同时验证缺少助手 503/重试及损坏输入不发布部分索引。日志 /tmp/cloudnest-heif-integration-v2.log。这次完整链路使用补充提前拒绝前的助手，后续最终助手实际解码/EXIF/缩放已按上条单独复核。
- 完整 make check test 通过（/tmp/cloudnest-heif-final-check.log）；AI 专项 **29 个顶层测试 PASS 53.477s**（/tmp/cloudnest-heif-ai-search.log）；integration/aimodel/aieval/heifmodel 组合标签与实际分页契约通过 0.895s。CI 编译 heifmodel，但不冒称运行原生/Ollama。
- 上一提交 c5fe0d17c80dd46c4bb3f9dca7eaf5a6d96f683a 的 PR 40981958021 / push 40981953591 CI 均 SUCCESS，不用于替代本次提交 CI。

## 夹具来源

所有数据为已有合成发票 PNG 或确定性纯色图，使用 macOS sips/ImageIO 编码，不含真实照片/账号/票据。生成器 scripts/generate-heif-test-fixtures.py 写入全新目录；编码器版本不同可能改变字节，固定测试输入保持下列摘要。EXIF 日期由生成器显式注入，是解析测试数据，不是真实拍摄证据。

| 文件（internal/imageprep/testdata） | SHA-256 |
| --- | --- |
| invoice-synthetic.heic | a54dd3c028530300a0cb532af286e71428c80c1775834d2c4bf2bd7f5c6f6ca5 |
| invoice-exif.heic | 8abd94e6d798a7e70dfa1162e308d13579f833fb2afdf105942682bfcbb7489f |
| large-synthetic.heic | dcb73f3023b507619298d7b73c9c487d31152269f255782e5125d54be2341774 |

## 剩余验收

Linux 原生库运行、复杂 iPhone/HDR/色彩/辅助深度图变体、恶意容器及资源压力、多主图完整支持尚未通过验收。这里的真实链路只证明单主图合成 HEIC；不宣称覆盖所有 HEIF 或完成 G04。检索条件误报、旧 Office、复杂 PDF/中文手写与独立质量验收仍保持未完成。
