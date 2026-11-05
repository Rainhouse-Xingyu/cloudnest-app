# Agent 本地密钥与状态加密

Agent 的 `identity.json` 和 `state.json` 现以 AES-256-GCM 保存，包括设备凭据、原始/规范化配置和执行记录。外层标识为 `CNAGENT1`，认证数据绑定版本、随机 vault ID 和文件名；每次写入使用随机 nonce。跨文件搬用、错误密钥、篡改和未知加密版本均拒绝，不把解密错误当成旧明文重试。

`vault.json` 只包含版本、vault ID、密钥后端及其位置，不含密钥。目录仍要求当前用户拥有的 0700，文件 0600，拒绝链接、其他所有者和不合规权限；原有单实例锁、原子写入和 fsync 保留。

## macOS Keychain

原生 macOS 构建默认使用当前用户的默认钥匙串。首次创建时把实际钥匙串路径保存到元数据，后续不会因用户切换默认钥匙串而改查另一个位置。也可在 `enroll` 或 `protect-state` 时用 `--keychain /absolute/existing.keychain` 指定已有钥匙串。

密钥通过 Security.framework 的 SecItemAdd/SecItemCopyMatching 在进程内读写，service 为 `com.cloudnest.agent.vault.v1`，account 为随机 vault ID。相同 account 不覆盖旧密钥。没有调用 `security` shell 命令，不把密钥或钥匙串密码放进 argv、环境变量或日志。没有自动创建空密码钥匙串、修改默认钥匙串或放宽条目访问控制。

后台 Agent 禁用 Keychain 交互提示。钥匙串锁定、条目缺失或访问拒绝时退出，不回退到文件密钥或明文。错误只给出固定消息及 OSStatus。已加载进内存的密钥不会因之后锁定钥匙串立即失效；重启/重新解锁存储时重新检查。正式分发仍需稳定代码签名及升级访问验证，替换未签名开发构建可能导致 Keychain 拒绝访问。

此 CLI 使用 macOS 文件型钥匙串；SecItem 是键值接口，选择指定钥匙串、读取锁定状态和禁止交互使用 SDK 仍提供的 SecKeychain 兼容接口。这些文件型接口已被 Apple 标为 deprecated，代码将其限制在 Darwin/cgo 实现中；没有声称使用 Data Protection Keychain、Secure Enclave 或未来 macOS App 的访问组。G07 应对签名 App 的 Keychain 存储另行验收。[Apple SecItemAdd](https://developer.apple.com/documentation/security/secitemadd(_:_:))、[指定钥匙串](https://developer.apple.com/documentation/security/ksecusekeychain)。

macOS Agent 构建要求 cgo 和 Apple SDK，`make build-macos` 已明确启用 cgo 构建两个架构。禁用 cgo 的构建不能使用 Keychain，也不会隐式生成文件密钥。Linux 构建不链接 Apple 框架。

## 外部密钥文件

Linux 必须显式指定 `--key-file /absolute/private/agent.key`；macOS 也可由操作人员明确选择该后端。文件必须是当前用户拥有的私有普通文件、唯一硬链接、恰好 32 字节，并位于状态目录之外。不要用密码字符串直接充当密钥，不要覆盖已有密钥。

下面只演示新密钥生成；将路径替换为已存在的私有父目录。排他创建可防止覆盖现有密钥，命令不输出密钥。

```sh
python3 - <<'PY'
import os, secrets
fd = os.open('/absolute/private/agent.key', os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, 'wb') as f:
    f.write(secrets.token_bytes(32))
    f.flush()
    os.fsync(f.fileno())
PY
```

在原 [注册命令](FRP-AGENT.md) 上添加 `--key-file`。运行仍仅用 `run --state ...`，根据已保存元数据查找原密钥。不自动更换后端或再生缺失密钥。当前没有就地密钥轮换命令；密钥遗失不能通过生成新文件解密旧记录。备份必须妥善保管原密钥/钥匙串和元数据，不能只保存密文文件。

## 旧状态迁移

停止 Agent 并确认受管进程已退出，先备份状态及现有密钥。普通 `run` 不接受旧明文状态，需明确执行：

```sh
# macOS 默认钥匙串；也可以显式加 --keychain。
cloudnest-agent protect-state --state /absolute/private/cloudnest-agent

# Linux，或明确选择外部文件的 macOS 安装。
cloudnest-agent protect-state --state /absolute/private/cloudnest-agent \
  --key-file /absolute/private/agent.key
```

迁移逐个验证并原子替换身份和日志，已加密文件先验证认证标签，重复运行不改写有效密文。中途失败可能已转换前面的文件，修正问题后用相同元数据和密钥重试。已加密身份缺少元数据时不会创建新密钥。Keychain 条目创建后元数据落盘失败，可能留下未引用的条目；不自动删除，以免删掉已经提交但 fsync 报错的数据所需的密钥。

迁移不会删除历史备份、快照或系统日志，也不修改服务器设备凭据。旧备份中可能仍有明文，需按已有敏感备份流程处理。

## 运行配置边界

官方 frpc 0.71.1 会重复读取配置路径，当前仍用 0600 的 `candidate.json` / `active.json` 给固定程序验证和启动。candidate 在验证结束后删除；active 在监督进程停止并回收 frpc 后删除。重新启动 Agent 或完成迁移时清理遗留的这两个缓存，清理函数不允许删除身份、执行历史或操作人员的密钥文件。

运行文件只保存映射、连接和 TLS 参数；认证 token 被移除。Agent 重新验证规范化配置后，将最多 1024 字节的原始 token 写入匿名管道并关闭写端。验证进程直接继承读端；启动进程通过监督进程传给 frpc。唯一允许的 tokenSource 是本机生成的固定 `file.path=/dev/fd/4`，远端下发的文件加载器、exec 或自定义路径仍被拒绝。token 不出现在子进程参数、环境或运行配置中。

固定 0.71.1 的 [认证初始化](https://github.com/fatedier/frp/blob/v0.71.1/pkg/auth/auth.go) 解析一次 [文件 token 源](https://github.com/fatedier/frp/blob/v0.71.1/pkg/config/v1/value_source.go)，然后将值留在进程内用于重连。每次启动或验证使用新的管道；不要把管道用于整个配置路径，[配置加载](https://github.com/fatedier/frp/blob/v0.71.1/pkg/config/load.go) 会先探测旧格式再重新读取。已通过实际二进制认证、重连和重启测试，升级 FRP 时必须重新验证这个协议。

文件 token 源会裁剪首尾空白。Agent 对此类 token 返回 `config_invalid`，不静默修改凭据、不写明文回退。旧加密状态如含此类 token，启动后保存停止意图并报告固定错误，但继续认证和领取命令；需协调 frps 的认证配置并下发新的合法版本。内部空格、井号和 Unicode 均原样传递。服务端仍保留原配置与历史，不自动轮换生产 token。

旧明文文件的磁盘历史、备份和快照不因更新而消失；运行中的进程内存、映射地址也不是加密存储。SEC-010 因历史备份、生产迁移与部署保护继续保持开放，设备仍需受保护磁盘和受限备份。

## 验证

普通测试验证随机 nonce、逐字节篡改、跨文件搬用、明文拒绝、密钥权限/位置、丢失密钥不再生、迁移部分提交及重试、清理不删除持久状态。实际 Agent CLI/后端/三隧道测试使用独立测试密钥文件，覆盖加密状态下的正常重启、SIGKILL 恢复、错误密钥拒绝，以及停止后运行缓存消失。

`CGO_ENABLED=1 go test -tags=keychainnative -race -count=1 -v ./internal/frpagent -run '^TestDarwinKeychain'` 运行两项真实 macOS Keychain 测试：只创建并删除测试拥有的临时钥匙串，不写现有登录钥匙串。测试包含原生读写/锁定/解锁/重复条目拒绝，以及实际编译 CLI 跨进程迁移、幂等重试和锁定后失败。CI 新增独立 macOS 作业；具体提交结果和本地证据见 [测试记录](TESTING.md)。
