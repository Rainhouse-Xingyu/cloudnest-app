# CloudNest for macOS

CloudNest 使用 SwiftUI 原生分栏、窗口、工具栏和设置场景，跟随系统外观。G07 提供登录、会话恢复和退出、账户信息及服务器状态。文件和传输界面在 G08 接入，AI 检索、FRP 和协作界面在 G09 接入。

## 构建与测试

应用部署目标为 macOS 26.0，Swift Package 测试要求 Swift 6.2 或更新版本。在仓库根目录运行以下命令，或在 Xcode 中打开 `cloudnest_macos/CloudNest.xcodeproj` 并选择 CloudNest scheme。

```sh
CLOUDNEST_TEST_KEYCHAIN=1 swift test --package-path cloudnest_macos
xcodebuild -project cloudnest_macos/CloudNest.xcodeproj -scheme CloudNest \
  -configuration Release -destination 'generic/platform=macOS' build
```

设置 `CLOUDNEST_TEST_KEYCHAIN=1` 会用随机服务名对登录 Keychain 做一轮写入、更新、读取和删除。GitHub Actions 分别在 macOS 26 和 Xcode 27 runner 构建并执行测试。工程使用临时签名，供本机和 CI 验证。

## 连接后端

普通下载采用连续 Range 响应，在有背压的磁盘块边界记录恢复检查点，重新打开应用后仍核对前缀和强 ETag。独立 opt-in GiB 测试、实际窗口恢复和内存测量方法见 [原生大文件验收](../docs/remediation/MACOS-LARGE-FILES.md)。

打开应用，输入 CloudNest API 的 HTTPS 源地址，例如 `https://cloudnest.example:8443`，以及现有账户的用户名和密码。服务证书需要受 macOS 信任，证书名称需要匹配该地址。服务应将该源下的 `/api/auth/login`、`/api/auth/me`、`/api/auth/logout` 和 `/api/resource/health` 路由到 Go 后端。

登录后，应用将会话保存在 Keychain，在恢复会话时向服务查询账户状态。服务器状态单独检查资源权限，自定义权限账户仍可恢复会话和退出。服务器不可达时可在窗口工具栏重试，或按 Command-R。重新登录时可输入另一台服务器。

文件页提供版本历史、所选历史版本下载、确认后追加恢复，以及选择本地文件上传为新版本。上传捕获确认基线，冲突后需取消并重新确认，不覆盖未见的新版本。传输页分别显示已确认进度、最近五秒速度及校验/发布阶段，暂停或重新开始时重置速度。协议和验证边界见 [G08](../docs/remediation/G08-MACOS-FILES.md)、[版本上传](../docs/remediation/VERSION-UPLOADS.md)、[历史版本](../docs/remediation/MACOS-VERSIONS.md) 和 [速度](../docs/remediation/MACOS-TRANSFER-SPEED.md)。

AI 页支持真实自然语言检索、原文片段、来源、索引状态与确认后重建；远程连接页支持设备/映射/配置历史/运行状态、五种命令确认和持久原请求重试。入队回执与 Agent 执行成功分别显示，应用重开不会自动重放远程操作。边界和验收见 [G09](../docs/remediation/G09-MACOS-EXTENSIONS.md) 与[命令恢复](../docs/remediation/MACOS-FRP-COMMANDS.md)。设备/映射编辑及注册、协作、分享和扩展设置继续实施。

服务器分段已接入[登记与编辑](../docs/remediation/MACOS-FRP-SERVERS.md)、每页 50 项、版本冲突核对和保留/替换/清除 token。服务资料保存、Agent 部署和正在运行的隧道分别处理；设备/映射编辑及注册等剩余界面继续实施。
