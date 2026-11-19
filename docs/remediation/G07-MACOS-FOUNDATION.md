# macOS 客户端骨架与会话联调

G07 在 `cloudnest_macos/` 提供独立 SwiftUI 工程、分层 API/状态/Keychain 服务、登录与服务器状态、原生分栏/工具栏/窗口/设置场景。应用跟随系统深浅外观。部署目标为 macOS 26.0，包测试要求 Swift 6.2。

会话通过 `/api/auth/login`、`/api/auth/me`、`/api/auth/logout` 与服务端交换。服务器地址要求 HTTPS 源地址，密码在提交后从输入框清空，已验证源地址可保存在 UserDefaults，会话 token 保存于独立 Keychain 条目。恢复时查询真实账户状态，健康检查 403 单独显示为受限状态。远端注销暂时失败时保留本地会话，供用户重试；远端确认撤销或已失效后清除 Keychain。

所有元数据请求使用临时 URLSession、有界响应和显式任务 delegate。`URLSession.bytes(for:)` 的任务 delegate 负责系统证书信任与禁止重定向，避免会话级 delegate 未参与该请求路径。实际 307 登录重定向回归确认凭据请求未被转发。

## 已执行证据

提交 `5ff84a3f0cf0e7053cc45316abec2fa85f295c46` 的 [GitHub Actions 44639630078](https://github.com/Rainhouse-Xingyu/cloudnest-app/actions/runs/44639630078) 在以下两台 runner 全部通过。

| 环境 | 基础回归 | 真实 Go 会话 | Release |
|---|---|---|---|
| macOS 26.7.2 / Xcode 26.6 | HTTPS 地址约束、403、401、503 及真实独立 Keychain 条目 | 默认/自定义权限两种场景通过 | Universal 构建、临时签名和 arm64/x86_64 校验通过 |
| macOS 27.0.1 / Xcode 27.1 | 同上 | 默认/自定义权限两种场景通过 | 同上 |

真实联调启动临时 MySQL 8.4、Redis 和 HTTPS Go 路由，由 Swift 客户端验证错误密码、登录、Keychain 恢复、刷新、远端注销、旧 token 拒绝及另一个窗口观察撤销。测试客户端仅以该 Go 服务器证书作为本次 URLSession 的信任锚。运行入口为 `python3 scripts/test-macos-backend.py`，需要 Homebrew 的 `mysql@8.4`、`redis` 以及项目要求的 Go/Swift 工具链。

首轮 CI 因架构校验命令参数失败，现读取 `lipo -archs` 并明确检查两种架构。真实联调还发现测试宏、临时 MySQL 触发器权限以及 async bytes 任务 delegate 问题，已修正后取得上述结果。早期本机 macOS 26.3.1 / Xcode 26.3 的 Debug、Release 构建及五项基础测试也通过，其范围限于当时客户端骨架。

原生 GUI 的实际运行/可用性、G08 文件与传输、G09 AI/FRP/协作及最终签名发布继续按里程碑验收。G07 代码完成待审，整体 Goal 保持进行中。
