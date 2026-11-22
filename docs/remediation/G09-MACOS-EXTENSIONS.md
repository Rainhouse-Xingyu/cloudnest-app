# macOS 扩展功能

G09 在 `codex/g09-macos-extensions` 上继续 G08，保持独立[草稿 PR #10](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/10)，不合并前置分支。当前完成 AI 首批功能、[FRP 管理概览](MACOS-FRP.md)和[五种命令/原请求回执恢复](MACOS-FRP-COMMANDS.md)。整个 G09、G10、G11 仍进行中/待做；FRP 编辑与注册、协作、分享、设置的真实原生界面继续实施。

## AI 检索与索引

原生侧栏新增 AI 检索，使用 `POST /api/ai/search/nl`，显示实际原文片段、关键词/语义/重排等匹配来源，保留服务器相关性顺序。显式提交，每页 20 项，最多访问当前 200 项候选窗口，不累积无限结果；分页说明并发文件/权限变化可能改变结果。修改输入但尚未提交时，续页仍使用已提交查询。空输入及超过 4096 UTF-8 字节的查询拒绝发送。

请求替换和离页取消会取消原任务，并以本地请求代号拒绝迟到结果。会话封装沿用 G08 的原 token 校验，旧请求不能污染新登录；状态在服务器/账户变更后拒绝执行。403、网络错误或坏响应清空旧片段，权限拒绝保留登录。`keyword_fallback`/`embedding_unavailable` 明确提示语义模型不可用，不将关键词结果冒充语义检索；重排失败保留来源说明。

搜索 API 不提供当前版本号，因此 `AISearchHit` 保持独立模型，不伪造 `FileItem.currentVersion`。已有下载队列改为接受仅含 ID/名称/大小/目录标志的 `DownloadableFile`；后端混合查询限定非目录，搜索结果可使用同一实际下载、完整性校验、持久任务和身份边界。保存面板打开时捕获身份，确认后再次核验。

索引页调用 `GET /api/ai/index`，每页 50 项、替换当前页，提供下一页/返回第一页/刷新。该列表只含自己拥有的文件；`indexed_current` 决定是否显示当前可检索，不能仅凭 done 判成功。显示实际排队、处理中、重试、格式/提取/模型等错误。手动重建先确认具体文件，再将已观察 generation 提交至 `POST /api/ai/index/rebuild`。202 仅表示入队，随后刷新看状态；409 重新读取并要求核对，禁止盲目重试。游标进展、去重、ID/尺寸/代次和响应体均有界校验。

网络层为重建接口显式接受 202，其余接口继续要求原有成功状态；未泛化为任意 2xx。模型推理、索引、权限和 API 均使用已有真实 Go 实现，未新增模型假响应或放宽产品权限。

界面采用原生 SplitView、List、分段选择、文本输入、系统保存面板和确认框。依据 Apple 的[搜索设计指南](https://developer.apple.com/design/human-interface-guidelines/searching)明确全局检索范围，并与文件页的当前目录筛选区分。行内动作包含文件名专属辅助功能标签，片段支持选择复制；CUA 的 SwiftUI List 树仍聚合为行，不宣称完成全部 VoiceOver 审计。

## 2026-11-21 验证

- 最终普通 Swift 注册 47 项，实际 33 项通过、14 项条件跳过，0.084s；包含新检索取消/替换、提交查询分页、旧服务器拒绝、403 清空、重复 ID 坏响应及多字节查询边界。URLProtocol 仅用于这些客户端竞态/协议测试，不作为模型能力证明。
- 完整真实 TLS/Go/MySQL/Redis 回归通过，Go 包 74.722s：默认账户 11 项实际通过、1 项真实模型 opt-in 跳过；受限账户 2 项通过、10 项跳过。新用例验证文件名降级检索、跨用户隔离、受限路径 403 保留登录、真实 202 入队，以及另一个客户端推进代次后，本窗口 409 刷新/核对/再次提交。
- 独立 `TestMacOSClientLiveAISearch` 使用实际本机 Ollama 0.40.17、`qwen3-embedding:0.6b`，digest `ac6da0dfba84a81fdbfbaf330198c33cd77c4cdfc53e8bc50eb581914a15621d`、1024 维；三份真实上传正文经生产 worker 持久索引，Swift 通过 TLS 检索只返回本用户迁移文档，实际来源含 semantic、原文片段正确，并通过同一队列下载回读全文。Go 场景 8.78s，Swift 0.632s。此小型合成场景不替代 G04 检索效果评估。
- [首轮 GUI 原始报告](MACOS-AI-GUI-INITIAL-20261121.json)：实际语义查询、重建至第 2 代、135 字节下载回读、深浅色与 640×492 点窗口。发现 AX 合并行未包含完整片段后补充标签/容器设置。
- [最终 GUI 原始报告](MACOS-AI-GUI-FINAL-20261121.json)：绑定最终 App/Core 指纹和独立二进制，实际查询/片段与文件名动作标签、再次下载/完成、重建至第 3 代/当前索引均通过；最终下载 SHA-256 为 `75a9135c6ba7921daab37757b64c073393ee82ae54ea8acede514121ce40e4de`。最终构建为 900 点宽浅色复验；首轮深色/窄窗证据保留原指纹，不改写其来源。两个应用均正常退出，私有测试服务收到 stop 后退出。
- 最终正式 Universal Release 构建、x86_64/arm64 与签名检查通过；仓库卫生及 diff 检查通过。精确提交的远端 CI 发布后单独核对。

首轮联调失败保留：新增测试最初有 Swift Testing 宏嵌套/缺少 try 的编译错误；修正后，过宽的测试查询还命中含 Native 的既有夹具，改用独立查询词验证隔离，并未收紧产品检索。真实重建已入队但客户端将 202 当成坏响应，修复成功状态契约后回归通过。未忽略错误或降低最终断言。

复现入口（独立临时 MySQL/Redis，不接触生产库）：

```sh
swift test --package-path cloudnest_macos
python3 scripts/test-macos-backend.py
CLOUDNEST_TEST_AI_MODEL=1 python3 scripts/test-macos-backend.py --filter '^TestMacOSClientLiveAISearch$'
```

真实模型场景要求已安装 Ollama 与上述 embedding 模型。私有 GUI 夹具可额外设置 `CLOUDNEST_NATIVE_UI_AI=1`，先实际处理队列，再运行同一生产 worker，终止时取消并等待退出。该选项仅存在于 integration 测试；产品行为不读取它。没有可用模型时明确失败，不跳过后声称通过。

## 后续范围

继续接入 FRP 设备/映射/服务器编辑、凭据注册、配置预览/生成/回滚，协作空间/成员和文件授权、分享创建/限制/撤销、用户/系统设置。AI 照片/视觉端到端、多页并发变化、真实模型中断及更多辅助功能/文字缩放验收继续补齐。G10 保留历史迁移、备份恢复、磁盘治理、异常/压力和发布门槛，G11 保留全部交付材料。指定 G01/G02 正式 Review 仍按用户要求留到整个 Goal 完成后读取。

2026-11-21 后续：af4f3ef 的 macOS 26/Xcode 27 与完整后端 CI 全部通过。已新增原生命令持久意图、确认和原键重试；实际 Agent/官方 frpc/frps 的三隧道与五操作 GUI、真实 TLS 丢回执恢复均有独立证据，见[命令说明](MACOS-FRP-COMMANDS.md)。同日恢复并实际评估视觉模型，2B 的两组合成图片场景通过，4B 失败保留，见[模型资源复测](MACOS-FRP.md#ai-模型资源复测)。新提交 CI 另按精确 SHA 核对，未完成范围不变。

后续命令提交 da24d87 的双平台原生及完整后端 CI 全通过，两平台日志确认实际执行丢回执恢复、非 UTC、Universal Release/签名及独立 UI 构建。新的[服务器原生管理](MACOS-FRP-SERVERS.md)已接入分页、登记/编辑、版本 CAS 与三种 token 处理方式，真实接口和 GUI 通过；设备/映射 CRUD、enrollment、配置生成/预览/回滚及其余 G09–G11 工作继续。

2026-11-22：服务器提交 04539b7 精确双平台与完整后端 CI 全通过。后续[配置流程](MACOS-FRP-CONFIGS.md)已完成脱敏预览、确认后追加保存/复制历史、分别部署配置 2/3 的真实三隧道及停止 GUI；首轮夹具过期保留失败，最终新夹具正常通过退出。设备/映射 CRUD、enrollment、协作/分享/设置和扩展 AI 继续实施，G10/G11 范围不变。
