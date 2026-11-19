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

打开应用，输入 CloudNest API 的 HTTPS 源地址，例如 `https://cloudnest.example:8443`，以及现有账户的用户名和密码。服务证书需要受 macOS 信任，证书名称需要匹配该地址。服务应将该源下的 `/api/auth/login`、`/api/auth/me`、`/api/auth/logout` 和 `/api/resource/health` 路由到 Go 后端。

登录后，应用将会话保存在 Keychain，在恢复会话时向服务查询账户状态。服务器状态单独检查资源权限，自定义权限账户仍可恢复会话和退出。服务器不可达时可在窗口工具栏重试，或按 Command-R。重新登录时可输入另一台服务器。
