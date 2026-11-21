# 原生权限变化、会话撤销与真实响应中断验收

2026-11-21 UTC。产品 App/Core 和 Go 业务代码保持 `620e4b197c9b6f138a8c1f7ba9a90421bff18106`；本批增加独立验收夹具、真实网络故障回归和证据，不新增产品权限接口或修改生产数据。

## 方法与可重复入口

在 macOS 的终端 A 使用既有隔离 MySQL/Redis 运行器，保持前台进程运行：

```sh
fixture_dir="$(mktemp -d /tmp/cloudnest-native-ui-permissions-XXXXXXXX)"
printf '%s\n' "$fixture_dir"
CLOUDNEST_NATIVE_UI_FIXTURE_DIR="$fixture_dir" \
CLOUDNEST_NATIVE_UI_PERMISSION_ADMIN=1 CLOUDNEST_NATIVE_UI_SLOW_TRANSFERS=1 \
python3 scripts/test-macos-backend.py
```

日志显示 manifest 就绪后，在终端 B 构建验收应用；将下列参数替换为终端 A 输出的实际 fixture.json 完整路径：

```sh
python3 scripts/build-macos-native-ui.py "/tmp/cloudnest-native-ui-permissions-实际后缀/fixture.json"
```

夹具仍严格限定 loopback 的 disposable cloudnest_test MySQL/Redis；20 分钟到期停止，stop 文件可提前结束。跨时段工作须先确认原进程仍运行，不能仅凭清单文件存在继续使用。额外管理员仅是该临时数据库中的第二个合成账户，用户名和目标普通用户 ID 写入权限为 0600 的私有 manifest，不把 token 放入清单、日志或报告。没有创建产品绕过接口，普通账户始终通过真实路由鉴权。

用合成管理员登录取得的临时会话，调用正式 `PUT /api/admin/users/<permission_user_id>/permissions` 设置允许列表；发送 `permissions: []` 通过原接口恢复默认策略。本次 8 次设置/恢复均返回 200。另由普通账户独立会话调用正式 `POST /api/auth/logout-all`，第 9 张回执为会话撤销成功。目标、策略及 UTC 回执保留于[原始报告](MACOS-PERMISSION-NETWORK-GUI-20261121.json)，不包含认证 token。

新建空文件 trace-downloads 只开启私有夹具的下载元数据观察，记录实际 Range/If-Range/ETag、状态和写出字节；记录在 handler 的 defer 中追加，即使 `http.ErrAbortHandler` 中止也不遗漏。新建 interrupt-next-download 使下一次下载在实际写出 5 MiB 后返回写入错误；正式下载 handler 仍执行授权、强身份、读取和正常错误终止流程。应在目标下载有权限时创建该一次性标记，并核对 interrupted=true，不把一个被拒绝的请求误计为网络故障。

这些控制仅存在于 integration/darwin 的 `_test.go` 和手动夹具，生产服务不识别相关环境变量或文件。ResponseController 通过 Unwrap 保留真实网络连接的期限/刷新能力。新增独立真实 HTTP 测试声明 8 KiB 的 206，线上实际只发送 4 KiB，客户端读到 `io.ErrUnexpectedEOF`；race 回归通过（1.690s）。默认 macOS 运行器现在同时运行该测试和 TestMacOSClientSession，自定义 --filter、手动 UI 和 opt-in GiB 选择保持独立。

## 实际窗口结果

验收入口使用真实产品视图/Core、固定私有 TLS 证书和内存会话；不使用旧测试钥匙串。macOS 27.2.1 arm64。客户端产品代码与 620e4b1 无差异，报告保存全部 App/Core、夹具和运行器 SHA-256，以及实际验收二进制摘要。

| 场景 | 实际结果 |
|---|---|
| 打开改名面板后撤回写权限 | 提交得到 403，界面显示“当前账户没有权限执行此操作。”；原文件名及选择保留；恢复权限后重新确认改名成功，仍为原 ID 1 |
| 已有列表/选择后撤回列表读取 | 刷新得到 403，清空旧列表与选择、下载按钮禁用；恢复后点重试重新读取；整个过程没有重新登录 |
| 保存面板打开后撤回下载 | 原任务 failed，确认 0/78 字节，没有最终文件，会话保留；恢复后点同一任务继续，原 UUID 完成 78 字节，摘要与源一致 |
| 已暂停上传后撤回继续权限 | 已确认 4,194,304 字节、epoch=1；继续得到 403，任务 ID/upload ID/epoch/确认字节不变；恢复后同一任务 epoch=2，完成 9,437,357 字节，最终根目录只增加一个文件 ID 5 |
| 已发送响应正文后中断连接 | 实际 206 从 bytes=0- 发出 5,242,880 字节后中止；界面 failed，持久确认 4,194,304 字节、强 ETag 保留，没有最终文件；点继续后以原检查点恢复并完成 |
| 撤销已登录会话 | 正式 logout-all 返回 200；原窗口刷新得到 401，返回登录页、清空用户名/密码；同账户重新登录后文件和 3 条 completed 任务保留 |

上传源为 9 MiB + 173 字节的确定性 0x6e 数据。暂停期间未确认的第二块请求被取消；拒绝继续期间没有推进服务端代次。恢复后与源文件最终摘要一致，实际下载也校验了上传后的对象，而非仅核对列表大小。

网络续传的服务端原始观察为：

| 次序 | 状态 | Range | If-Range | 实际写出字节 |
|---|---:|---|---|---:|
| 首次 | 206 | bytes=0- | 无 | 5,242,880 |
| 显式继续 | 206 | bytes=4194304- | 原 sha256 强 ETag | 5,243,053 |

两次都对应文件 ID 5、相同强 ETag；首轮在途数据超过持久确认边界的 1 MiB 不能计入已确认进度。最终本地文件大小 9,437,357 字节，SHA-256 为 `3ff9e8cc78a48729be846a5417e5b7989aab01a4c82bf29248c11cf9cf1ec117`，与上传源相同，part 已移除。78 字节文件为 `da92a2dbbc61507ddcec740d14b019f98209a972ce40911d8d282e164a4f3811`。最终共 3 条 completed journal，远端根目录 5 项，原版本文件和目录保留。

## 失败、测试与范围

跨时段恢复工作后，前一组私有服务已按 20 分钟期限停止，旧地址 127.0.0.1:55645 无监听；登录界面显示连接错误。Go 手动夹具明确以 expired 退出（1200.49s），该轮没有执行权限验收。[环境失败报告](MACOS-PERMISSION-GUI-20261121-ENVIRONMENT-FAILURE.json) 保留 passed=false；确认同一进程句柄终态后，使用全新私有数据库/目录/端口重验，未凭观察超时重启服务。

最终完整默认账户 10 项真实 Swift/Go 联调、额外受限会话 1 项通过（其余 9 项条件跳过），加上新增真实断流 Go 测试，包 78.015s，日志 `/tmp/cloudnest-native-permissions-all-real.log`。独立 wire/race 日志为 `/tmp/cloudnest-native-ui-fault-helper-final.log`。专用 GUI 正常 Quit 并确认进程退出，私有后端收到 stop 后退出 0。

620e4b1 的原生 CI 44900586921 与后端 CI 44900586925 全通过。双平台各实际 31 项基础/Keychain、10 项默认真实后端与额外 1 项受限会话（9 项跳过），包含非 UTC 时间、Universal Release/双架构/签名和验收入口；后端普通 474.461s、网络 98.597s、AI 59.427s，Agent/systemd、HEIF、Keychain、漏洞检查全通过。本批新夹具提交的 checks 另核对，不借用基线 run。

本轮证明的是每请求路径权限变化、显式恢复/重试、真实响应中断和完成任务后的会话撤销。没有证明对已授权活动响应的即时撤权、强杀/突然断电持久性、多卷、HTTP/2 或聚合吞吐，也不把受控慢速夹具的耗时当作性能基准。相关稳定性与发布验证继续在完整 Goal 中推进；G01/G02 指定正式 Review 未提前读取，未合并 PR。

本轮之后，同一最新产品源码又独立通过四个真实进程的 1 GiB + 173 字节恢复/摘要验收，包 249.240s，观测进程高水位最高 82.625 MiB；原始报告、测量点及吞吐率范围见 [大文件复验](MACOS-LARGE-FILES.md)。这不是本轮慢速权限夹具的性能结果。
