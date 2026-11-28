# 原生照片与 Live Photo

G09 的照片侧栏调用真实文件检索、缩略图、版本下载及 Live Photo API，不依赖系统照片图库权限，不导入或改写图库。新增媒体分类和配对版本字段，不增加数据库表/列，不删除源文件；本批仍属草稿，整个 Goal 未完成。

## 浏览、权限和版本

`GET /api/files/search` 可选 `media_kind=image` 或 `live_photo_motion`，响应回显实际支持的分类。图片使用允许的后缀类型，动态候选仅 MOV，均限定活动非目录文件，并在 SQL 中沿用当前资源级读取权限。旧空 file_type 使用文件名后缀识别，无需先迁移历史记录；与原 file_type 条件取交集，旧未指定分类的检索行为保留。排序增加同名 ID 次序，原生每页 50 项并替换页面；分页不是快照，文件或权限变化可能移动后续页面。

原生要求分类回显、页码/数量、正值 ID、去重及资源类型有效，旧服务器不能忽略新筛选后冒充已支持。名称筛选不自动提交，后续页面沿用已提交查询。缩略图最多四个并发、每页最多 50 个、单个编码体最多 512 KiB，核验 JPEG 表示、强摘要及完整长度；视图另核对像素边界并以 ImageIO 有界解码。取消/翻页/离页丢弃旧缓存并取消网络请求；失败显示占位和原因，可独立预览/下载原图。

Live Photo 列表/详情/绑定回执在每个源资源中增加 current_version；原字段保留。原生校验两文件和关联 ID，查询和预览要求两个资源均可读，绑定/解绑由后端在原事务中复核两者写权限。原生确认实际文件名，未知写入结果锁定表单并要求关闭/刷新，无自动重发或持久幂等承诺。解绑只移除关联，两个原文件及版本保留。

照片和配对预览复用传输引擎的有界私有缓存、摘要/长度校验和已授权版本接口。缓存项不写持久传输 journal；每个源固定所选 current_version 下载，配对预览在下载前后重新读取关联，变化则拒绝展示。关闭等待两项任务停止及临时副本清理。已有每文件 256 MiB、总 512 MiB/16 项预览准入保留，超限可以改用原文件下载。

## 系统显示与真实媒体

静态预览使用 ImageIO 缩小至最长 1600 像素，并拒绝超过 6400 万像素的头信息。动态组件由 AVFoundation 读取视频边界：最长 30 秒、宽高各不超过 4096 像素；再通过 PHLivePhoto 从两个已验证本地资源加载，等待完整非降级结果，检查两个资源和正值尺寸。系统加载有取消及超时提示，不能解码时保留明确的分别 Quick Look 入口，不把绑定成功标为可播放。

实现使用 [Apple 的资源加载 API](https://developer.apple.com/documentation/photos/phlivephoto/request(withresourcefileurls:placeholderimage:targetsize:contentmode:resulthandler:)) 与 [PHLivePhotoView](https://developer.apple.com/documentation/photosui/phlivephotoview)。占位图来自同一已验证资源的有界解码；播放须用户点击，默认静音。系统 delegate 区分开始/结束，异常快速结束提示未完成；加载对象存在不替代 GUI 播放验收。

`scripts/generate-live-photo-fixture.swift` 只在命名的私有临时目录生成 320×240、三秒的合成矩形 JPEG/MOV 和 PNG。JPEG 的 Apple Maker Note 与 MOV content identifier 使用同一 UUID，MOV 带 still-image-time 定时元数据；结构依据 [Apple WWDC 说明](https://developer.apple.com/videos/play/wwdc2016/501/?time=2225)。辅助工具再调用真实 PHLivePhoto 加载验证，不通过系统图库、摄像头、网络样本或模型伪造成功；生成文件不提交 Git。为运行 macOS 26，测试生成器保留兼容的 AVAssetWriter API，macOS 27 的弃用提示不隐藏。

## 验证与失败

- 普通 Swift 最终 113 项注册、86 项实际通过、27 项条件跳过，0.161s，启用独立 Keychain 测试。新增协议/状态测试覆盖分类能力、坏/重复资源、四请求上界、页面替换取消、403 清空、配对/版本校验、未知结果不重发及离页迟到回执隔离。
- 媒体/旧类型/原 Live Photo 专项第二轮 race 25.127s；其中真实原生五角色 16.38s。实际 JPEG/PNG 缩略图、原图固定版本回读、配对下载后再次系统加载、部分权限/两文件读取/撤权/受限路径、读者写入拒绝和解绑保留均通过。随后另补响应途中关闭测试，早期专项 18.625s 观察 256 字节后上下文取消；最终完整回归增强为必须是真实 206 响应。
- 最终完整 TLS/Go/MySQL/Redis 原生 race 161.352s，通过现有全部业务与新六场景。动态文件实际响应 status=206、发出 256 字节后父上下文取消，读取租约释放；客户端关闭在三秒内返回、无私有副本残留。所有媒体预览请求均为 `/versions/1/download`，无 latest 下载。系统加载使用实际下载文件，并核对各文件 SHA-256。
- Go `make check test`、服务端/Agent 的 Darwin/Linux × arm64/amd64 八个构建、Universal Release/双架构/严格签名和仓库检查通过。后续仅视图布局改变时，Core/Go 证据保留原指纹，Release/GUI 使用新指纹。

首轮 Swift 并发编译触及嵌套 task-group 辅助函数的隔离检查限制，改为明确 Sendable 请求闭包与有界调度后通过。新回归先复现离页后迟到绑定回执重新填充旧状态、解绑后离页刷新，再补请求代号隔离，原失败日志保留。首次 Release 还发现系统组件属性名应为 isMuted，按 SDK 修正。

媒体辅助工具首次未提供占位图时，本机返回的非降级 PHLivePhoto 对象尺寸为零，严格验证失败。相同 JPEG/MOV 的 UUID/定时元数据核对后，提供其已解码静态占位图再测试，返回完整正值对象；没有降低尺寸断言。JPEG 冒充视频被系统明确拒绝（PHPhotosErrorDomain 3302）；不同 UUID 的两资源也未通过完整加载。这里只记录本机观察，不断言所有 macOS 都有同一行为。

首轮 GUI 已实际完成两张缩略图、PNG 预览/字节核验/关闭清理，以及原生 JPEG/MOV 选择/确认绑定，但 Live Photo 表格被裁剪，AX 有记录而实际不能点击，故整轮 passed=false。滚动未恢复可视区域，正常退出/私有服务退出 192.989s、端口关闭。随后仅修正视图的有界几何尺寸及可压缩表格/滚动区域，并按完整服务器/账户身份重建照片页。最终 GUI、远端 CI 与原始报告另行记录，不将首轮局部成功改写为完整验收通过。

[最终 GUI](MACOS-PHOTOS-GUI-FINAL-20261127.json)在新私有服务与新源码指纹下通过：照片分页/Live Photo表头与行完整可见；原生PNG预览2254字节与SHA一致，JPEG/MOV关联写入、系统LIVE标记、播放开始/移动画面/播放结束均实际观察。两资源3587/5634字节分别回读校验，关闭后无副本。原生解绑清空列表且两源保留；读取者仅图片可读时列表为空，另行API授予视频读取后原生刷新/播放成功，原生解绑403且旧页清理；API撤权后刷新0项。深浅色、Private名称筛选和正常退出通过，服务356.537s结束、端口关闭。API为读取者阶段重建pair2与调整权限均明确标为准备，不冒充原生动作。五个GUI下载请求全部使用固定版本1。72个App/Core文件前后不变。首轮失败见[原始报告](MACOS-PHOTOS-GUI-INITIAL-20261127.json)，源码与日志哈希见[验证记录](MACOS-PHOTOS-VALIDATION-20261127.json)；照片批次远端CI发布后单独核对。

提交 `886a0ee98e7b89e6c5c5852ac59773eff3aa0e3c` 的[双平台原生 CI](https://github.com/Rainhouse-Xingyu/cloudnest-app/actions/runs/45950367260)与[后端 CI](https://github.com/Rainhouse-Xingyu/cloudnest-app/actions/runs/45950367402)均已全部通过，逐项读取实际日志：macOS 26 完整原生 164.955s、非 UTC 12.133s、照片 25.29s；Xcode 27 完整 202.188s、非 UTC 9.597s、照片 55.84s。两平台均实际得到完整 320×240 PHLivePhoto、六角色通过、206/256 字节后父取消，固定版本请求计数 4/3/1 且没有 latest。Release、签名及独立 UI harness 构建均通过；CI 没有代替上面的真实 GUI 观察。

后端完整集成 498.072s、真实传输网络 97.893s、AI 故障回归 58.746s；新增媒体/旧类型权限分页 9.56s、Live Photo 保留历史迁移 1.96s，Linux HEIF、Agent/systemd、Keychain 和漏洞检查全部通过。最初扫描 PDF 的故障状态不符用例本次 1.06s 通过，原失败原因仍未确认，不把后续通过改写成已定位的产品修复。

复现：

```sh
CLOUDNEST_TEST_KEYCHAIN=1 swift test --package-path cloudnest_macos
GOFLAGS=-race python3 scripts/test-macos-backend.py --filter '^TestMacOSClientPhotos$|^TestMediaSearch|^TestLivePhoto'
GOFLAGS=-race python3 scripts/test-macos-backend.py
```

私有 GUI 设置 `CLOUDNEST_NATIVE_UI_PHOTOS=1` 与新的 `CLOUDNEST_NATIVE_UI_FIXTURE_DIR`。生产环境不读取这些 integration 开关。完整 VoiceOver、文字缩放/多显示器、恶意媒体/解码峰值、多窗口聚合、突然掉电/多卷、损坏历史配对的可管理性仍待 G10；本批不冒充扩展 AI 质量验收，AI/文件体验其余范围及所有 G10/G11 继续。
