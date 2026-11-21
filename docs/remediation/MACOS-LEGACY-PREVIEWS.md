# 原生旧预览目录与历史任务保留

2026-11-21 UTC，G08 进行中。本批不删除历史文件、不改数据库或网络契约，不读取延后的 G01/G02 正式 Review。

## 真实兼容问题

历史提交 `153a8c9^` 的 enqueuePreview 创建 `<transfer store>/previews/<UUID>`，随后调用普通 enqueueDownload。因此旧 journal 的 direction 是 download，没有独立预览标记。新版受管 Quick Look 使用 `preview-cache-v1`，不能把旧目录也当作可清理缓存。

修复前的真实文件系统回归失败：恢复将 queued/running 旧任务改为 paused 并写回 journal；继续会重新访问网络、截断 part，取消会删除 part。两个初始用例共 9 条失败断言，日志 `/tmp/cloudnest-native-legacy-before.log`。这些都只发生在测试创建的私有目录，不是生产或用户历史目录。

## 行为与边界

新增 LegacyPreviewDirectory 以路径组件边界识别保留命名空间。只做 lstat 和路径/书签位置解析，不枚举旧目录、不读取文件正文、不认定其中每项都归应用所有。父目录和候选路径的标准形式/实际路径用于处理别名，`previews-backup` 不会被前缀误判。未知条目、符号链接与已有结果均保留。

TransferManager 在修改旧中断状态前，先对下载书签做只读归类。解析在 TransferDisk actor 执行，使用 withoutUI/withoutMounting，不申请安全作用域授权；可解析的陈旧位置仅用于识别保留区域，不能据此获得写入权限。正常读写仍使用原来的严格安全作用域及陈旧检查，没有把正常传输改为宽松解析。

能明确定位到该保留区域的下载任务，不论 queued/running/paused/failed/completed，原始 journal 字节均不改写，不自动调度，继续/暂停/取消也不进入写入或删除流程。窗口统一标记“旧版预览 · 已保留”，隐藏这些动作，不计入活动任务；提供旧目录的 Finder 显示入口。需要文件时，从文件列表重新预览或下载到其他位置。普通/历史版本新下载拒绝保留目录及其已识别别名，工作任务打开文件前还有同一检查。

这是按保留命名空间处理下载，并不猜测旧文件的所有权。位置无法解析或已经离开该区域的书签，不能据此证明是旧预览；它们仍遵守原有检查点错误路径。没有凭缺少标记自动归档、导入受管缓存或删除未知内容。

旧字节继续占用磁盘，**不计入也不冒充新版 512 MiB 预算已经覆盖的内容**。本批停止已识别旧任务继续增长，完成只读兼容；历史空间盘点/受控回收、旧客户端降级后的行为和所有本地目录替换竞态仍不由本批解决。旧版本客户端仍可能执行原行为，应保持同一任务目录只有一个新版本进程；既有进程锁保留。

## 窗口发现的第二层问题

第一轮单测通过后，实际独立应用未归类跨签名进程生成的书签，五行仍出现继续/取消。没有点击这些动作，但恢复阶段已规范化两个 journal；part、结果和未知文件均保留。[原始失败报告](MACOS-LEGACY-PREVIEW-GUI-20261121-FAILURE.json) 保持 passed=false，绑定当时二进制及源码指纹。

诊断发现同一书签在另一可执行文件用 withSecurityScope 解析报 `NSCocoaErrorDomain Code=259`，无安全作用域的只读位置解析成功。归类因此独立于写入授权；增加不带安全作用域的回归夹具，改动前再次失败（旧 ID 集合及两份原 journal，共 3 条断言，`/tmp/cloudnest-native-legacy-unscoped-before.log`）。修复没有改动 TransferDisk.resolve 对正常写入的约束。

中间通过的 GUI 结果也保留于 [第一次成功报告](MACOS-LEGACY-PREVIEW-GUI-20261121.json)。随后把解析放回磁盘 actor，并在全新私有后端/目录以最终构建重验，见 [最终报告](MACOS-LEGACY-PREVIEW-GUI-FINAL-20261121.json)。最初失败的两个测试 journal 只在专用夹具中按原 SHA-256 校验后恢复；最终验收用另一组新文件，没有以修复前改写后的值冒充原值。

## 最终验证

本机 macOS 27.2.1 / Xcode 27.2 / Swift 6.4.2：

- 普通 Swift 注册 42 项，实际 30 项通过、12 项条件跳过（10 项真实后端、1 项大文件 opt-in、1 项 Keychain），0.094s。四个新用例覆盖五种旧状态的原字节保留/零下载请求、正常相邻目录继续取消、路径边界/别名/替换符号链接，以及普通/版本新下载拒绝进入旧区域。
- 完整真实 Go/TLS/MySQL/Redis 默认账户 10 项与受限会话 1 项通过（其余 9 项跳过）；Go 包 70.424s，日志 `/tmp/cloudnest-native-legacy-all-real-v3.log`。包括单响应下载、上传/下载恢复、预览预算/关闭、固定历史下载、版本上传和双账户隔离。中间版本的 69.853s/72.683s 不是最终证据。
- 最终应用构建/签名通过，五个旧 journal 状态在实际窗口均标为已保留、没有继续/取消按钮和活动任务计数。CLI 对 5 份 journal、5 个 part、5 个结果和 1 个未知文件逐个核对原 SHA-256/大小，16 项全部不变。
- 同窗口从真实文件列表按空格，Quick Look 显示 78 字节的真实后端正文；私有服务只记录这 1 次新版预览下载。关闭后新版受管目录仅剩格式标记，旧 16 项仍不变；预览不进入持久任务历史。所有专用应用、私有服务正常退出，未接触旧钥匙串或系统外观设置。

普通测试运行 `swift test --package-path cloudnest_macos --filter LegacyPreviewTests`；真实套件仍用 `python3 scripts/test-macos-backend.py`。GUI 报告记录基线 c6db8b9 和实际 App/Core 源码 SHA-256，因为本批测试时工作树尚未提交；不能仅用基线 SHA 声称包含新修复。

基线 c6db8b9 的原生 CI 44898806151、后端 CI 44898806139 已全通过。本批源码的精确新 CI 在发布后单独核对，不借用基线结果。G08 权限变化/中断 GUI、文字缩放/多显示器、断电/多卷，以及 G09–G11/TIME-001 等继续开放；整个 Goal 不因本子项完成。

发布后已核实产品提交 620e4b1 的[原生 44900586921](https://github.com/Rainhouse-Xingyu/cloudnest-app/actions/runs/44900586921)、[后端 44900586925](https://github.com/Rainhouse-Xingyu/cloudnest-app/actions/runs/44900586925)全部通过。双平台各实际 31 项基础/Keychain、10 项默认真实后端及受限 1 项，包含四项旧预览回归、非 UTC 时间、Universal Release/双架构/签名和 UI 入口。后端普通 474.461s、网络 98.597s、AI 59.427s，Agent/systemd、HEIF、Keychain、漏洞检查全通过；本机构建亦通过。本次后续权限/中断 GUI 不改这份产品 App/Core，具体证据另见 [报告](MACOS-PERMISSION-NETWORK.md)，其他稳定性工作仍开放。
