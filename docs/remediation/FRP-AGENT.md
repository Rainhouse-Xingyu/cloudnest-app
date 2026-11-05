# G05 前台 Go Agent

`cloudnest_go/cmd/agent` 已接入独立注册、HTTPS 心跳、持久命令、官方 frpc 启停及本地执行记录。Agent 只接受 deploy_config、start、stop、reload、diagnose。身份/执行记录已加密，macOS 默认用 Keychain 保管密钥。可在终端运行，也可由当前 macOS 登录用户的 launchd 启动。G05 尚未整体验收。

## 运行与本机授权

先按 [命令协议](FRP-DURABLE-COMMANDS.md) 迁移后端，配置 HTTPS 和 FRP 配置密钥。管理员配置 frps 证书、token 和允许的映射端口；用设备所有者账号申请一次性注册码。Agent 不保存用户 JWT 或密码。

执行 `make -C cloudnest_go build` 会生成服务端和 `dist/cloudnest-agent`。`build-macos`、`build-linux` 分别构建 amd64/arm64。只支持这四个平台的官方 frpc 0.71.1；自行重编译、其他版本或冒充相同版本号的二进制不会通过摘要检查。

下面的域名、目录和端口仅为操作示例，需替换为部署实际值。注册码通过 stdin 从秘密管理器或受保护文件输入，不放在参数、环境变量或 shell 历史中。

```sh
cloudnest-agent enroll \
  --state /absolute/private/cloudnest-agent \
  --server https://cloudnest.example.com \
  --frpc /absolute/official/frpc \
  --frps-host frps.example.com --frps-port 7000 \
  --frps-ca /absolute/frps-ca.pem \
  --local-ports 8080,8443 < /absolute/private/enrollment-input

cloudnest-agent run --state /absolute/private/cloudnest-agent
```

`--ca` 可指定 CloudNest 私有 CA，默认系统信任库；不能关闭证书校验。控制端只允许 HTTPS origin，不允许用户名、路径前缀、查询或重定向，也不继承系统代理。`--frps-ca` 是独立的 FRP 控制连接信任，非字面量回环地址必须提供；证书名与连接地址不同时可指定 `--frps-name`。FRP CA 复制进状态目录，CloudNest CA 随身份保存，运行时不依赖原始 CA 文件。

状态目录必须是当前用户拥有、权限 0700 的绝对路径，父目录也必须由操作人员保护。Agent 文件为 0600，已验证 frpc 副本为 0700。拒绝最终路径符号链接、硬链接、非普通文件、其他所有者或组/其他人可访问的状态文件。文件通过同目录临时写入、fsync、rename 和目录 fsync 提交；独占 flock 防止重复运行。不要用 root 运行，按需要授权 `--local-ports`，不要默认允许所有本地服务。

身份、原始/规范化配置与执行记录现以 AES-GCM 加密保存。macOS 默认用当前用户默认 Keychain，Linux 注册必须另加 `--key-file` 指向状态目录外的私有 32 字节密钥；没有明文或后端失败回退。旧状态需显式 `protect-state`，见 [本地加密与迁移](FRP-AGENT-VAULT.md)。frpc 运行配置移除认证 token，由继承的匿名管道供给固定本机 token 源；地址等运行参数仍为私有文件。历史快照/备份不会因此消失，SEC-010 仍开放。注册只接受尚无身份的状态目录；凭据轮换前停止旧 Agent，使用新目录完成注册并保留旧目录供操作人员处置。兑换响应丢失或身份无法落盘时重新申请注册码，不能重用已消费注册码。

## 配置和执行语义

收到配置后先核对服务端记录的 SHA-256，再解析至严格字段白名单。禁止未知字段、重复键、YAML 锚点/别名/标签、多文档、模板、includes、plugins、visitors、管理端口、远程日志路径和文件凭据加载。配置中的 frps 地址/端口必须与本机授权完全一致，上游只能是字面量回环 IP 和授权端口。代理只允许 TCP/HTTP/HTTPS。

Agent 将白名单值重新编码成 JSON，增加本机指定的 TLS 信任、自动重连和仅控制台日志配置，再调用固定的 `frpc verify -c <私有文件>`。不直接执行下发的 YAML，不接受可执行路径、shell 或额外参数。启动前再次校验已安装 frpc 的摘要；子进程不继承宿主代理和其他环境，原始 stdout/stderr 不入日志。现阶段只记录固定本地错误或 HTTP 状态，服务端回执也只有固定代码。

deploy_config 保存目标配置并启动 frpc；start 启动已保存配置；stop 停止受管进程；reload 通过停止和重新启动应用当前配置，会短暂中断连接。诊断目前只确认受管进程存活。`ok` 代表本地进程操作完成，**不代表每条映射已在 frps 注册成功**；连接级状态和脱敏诊断仍待补充。

执行记录按 command_id 保存，领取 claim_id 在请求前落盘。先持久保存结果，再提交回执；网络丢失或租约重领时重发已保存结果，不重复执行同一已完成命令。记录保留 48 小时，超过服务端 24 小时命令期限；8192 条上限到达后停止，避免无限写盘。动作前续租；不足十秒的剩余租约拒绝执行，设备时钟需同步。失去未完成命令的租约时关闭不确定运行意图，待新领取授权后对账。仍是允许重试的执行协议，未宣称外部副作用恰好一次。

正常退出停止并等待子进程；再次运行先通过认证心跳，再恢复已提交目标状态。frpc 使用官方自动重连，Agent 在后续成功心跳时也检查进程是否意外退出。运行中的控制端网络中断不主动切断已有隧道；重启后无法认证则不启动隧道。收到明确的凭据失效响应会退出并停止子进程。真实测试已终止并重新启动独立后端进程：停机时已有三种隧道仍可用，数据库中待处理命令保持 pending；恢复后领取、执行并确认。另在隧道运行时撤销测试设备凭据，确认 Agent 报错退出、TCP 映射不再监听且状态锁释放。这里验证的是短时停机，不代表长期离线或系统休眠验收。

Agent 通过自身可执行文件的内部入口创建独立监督进程，传入已打开的私有目录、独占锁、存活管道和就绪管道和 token 读管道。监督进程核对目录/锁 inode、所有者、权限与管道类型，再启动固定 frpc；下一级继承锁和固定 token 读端，不继承存活/就绪管道。设备注册凭据不通过这些参数或管道传递，FRP token 不出现在参数或环境中。启动只有在监督进程确认 frpc 存活后才完成。

存活管道唯一写端属于 Agent。正常停止或 Agent 被 SIGKILL 后，内核关闭写端，监督进程停止并等待其直接创建的 frpc；正常退出超时后，只对该子进程句柄执行强制终止。释放所有继承锁后，可重新运行 Agent，从已保存状态恢复。**Agent SIGKILL 后清理与重启已通过真实进程/隧道测试**。前台模式需手动重启 Agent；使用下面的用户 LaunchAgent 时，由 launchd 自动拉起。

若监督进程自身被 SIGKILL，Agent 不能证明 frpc 已回收，会明确退出并拒绝继续启动。仍存活的 frpc 持有锁，新 Agent 同样被拒绝。真实测试已验证此双故障边界。只有监督进程明确回收了子进程的退出状态才允许自动重启；不从持久 PID 文件接管，不根据可能复用的 PID 发送管理信号。此类残留进程仍需操作人员核对并停止，不能删除 lock 文件绕过保护，也不能使用 killall。掉电、系统休眠和整组进程被强制终止的恢复尚未验收。

## macOS 登录用户的后台服务

先从固定的、仅受信任用户可修改的绝对路径运行已构建的 Agent，按上文注册，并确认前台 `run` 可以连接。停止前台实例后，在该用户的 macOS 图形登录会话中执行以下首次安装示例（替换二进制和状态路径）。不使用 sudo。生成命令只输出 plist，不启动服务；plist 只含固定标签、可执行文件、`run --state` 和 launchd 设置，不含凭据、token 或密钥。

```bash
(
  set -eu
  set -C # 首次安装拒绝覆盖已有 plist
  umask 077
  agent="/absolute/path/cloudnest-agent"
  state="$HOME/Library/Application Support/CloudNest/agent"
  jobs="$HOME/Library/LaunchAgents"
  plist="$jobs/com.cloudnest.agent.plist"
  mkdir -p "$jobs"
  "$agent" launch-agent --state "$state" > "$plist"
  /usr/bin/plutil -lint "$plist"
  /bin/launchctl bootstrap "gui/$(id -u)" "$plist"
)
/bin/launchctl print "gui/$(id -u)/com.cloudnest.agent"
```

launchd 直接执行 Agent，无 shell 展开。KeepAlive 保持服务运行，异常退出后允许重试，快速失败的启动按 10 秒节流。钥匙串锁定、密钥丢失或状态损坏时，进程仍拒绝读取，不能靠重启绕过。服务仅在该用户登录时工作。临时停止：`launchctl bootout "gui/$(id -u)/com.cloudnest.agent"`；要恢复，重新执行 bootstrap。若需卸载，bootout 后移走这个 plist，保留状态、凭据和钥匙串。变更二进制或状态路径时，先 bootout，审阅并替换 plist 再重新加载。

本机验收使用随机标签、临时 plist 和单独的测试身份，未写入真实登录启动目录。试验比较 SIGKILL 前后的 launchd 进程标识，通过新命令回执与三种隧道请求验证恢复。此次采用外部测试密钥，签名升级、用户实际钥匙串的非交互访问、跨登录/休眠恢复需分别验收。这里提供独立 CLI 的服务方式；未来 macOS App 内的后台项应通过 [Apple Service Management](https://developer.apple.com/documentation/servicemanagement/) 注册，并尊重系统用户审批。

## 固定官方制品

来源：[FRP v0.71.1 官方发布](https://github.com/fatedier/frp/releases/tag/v0.71.1)。2026-11-04 获取 GitHub release asset digest，逐个验证归档后读取其中 frpc 的 SHA-256。运行代码固定的是提取后的二进制摘要，脚本固定的是归档摘要；不会仅信任 `--version` 输出。

| 平台 | 归档 SHA-256 | frpc SHA-256 |
| --- | --- | --- |
| darwin/amd64 | 8acb59b6fb998cc8d209d1373d90bf70fa5ec3efeb4dfbc7bda6ce3abd78cb45 | 01cb3d59aba860a7097e1343ce04f9b5d8164b7488e21f5c551a34e62406c4c6 |
| darwin/arm64 | d15d0324b99e14619c9aa46e37f6db588c06a09be4bc6f7948890b6b61077842 | 7aaaba058dbeeedc86be876c44912c4f33756ab3e6e7a085f8d7af7814a073c1 |
| linux/amd64 | 1d9875210972a5d1a62b2dc4c48df45ab0af05d3ca1b5e494a014428259df803 | 35c6054d4549bbcf2f02875543895fd9e44c2aae2925f61cd8ff5a693703c28b |
| linux/arm64 | 8edf590b0c396fa42f9c147121a76af83cfc2002b08f5aef0ea1008c43482019 | 651de3428bdc7f37b4bad76ed3236b221939a989c433cd7daee79d2817519f8b |

配置格式与可选能力参照 [官方配置格式](https://gofrp.org/en/docs/features/common/configure/) 和 [客户端配置参考](https://gofrp.org/en/docs/reference/client-configures/)。CloudNest 只开放其中经过上述本机授权的子集。

## 验证与剩余验收

`scripts/test-agent-native.sh` 下载并校验本平台官方归档，运行 race Agent 单元/原生进程测试，再通过现有隔离脚本创建一次性 MySQL/Redis，启动实际 TLS 后端和实际编译 Agent CLI。测试执行注册、三种真实隧道访问、reload、stop/start、正常 Agent 重启、frps 断线重连、恶意配置拒绝及日志秘密检查。测试只使用临时配置、临时证书和回环端口。Linux CI 新增独立 agent-native 作业，结果需核对具体提交。

普通测试还覆盖 TLS 信任/重定向/响应边界、回执网络丢失后从磁盘恢复、同命令新租约不再执行、未完成租约失效关闭意图、状态配置重新验证、文件访问边界和假冒二进制。原生进程测试用实际官方 frpc 验证关闭父进程描述符后锁仍保留，子进程退出后释放。

剩余：运行配置缓存与备份治理、Keychain 签名升级访问验收、监督进程丢失后的安全恢复、长时间离线及系统休眠验收、连接级状态/诊断、受限日志、正式签名发布与 Linux 服务部署、远程端口并发约束验收与服务器作用域、旧 JWT 设备迁移。STCP/XTCP 继续按里程碑评估。未部署到真实公网服务器，未操作现有设备或生产数据库。具体本地测试和失败记录见 [测试记录](TESTING.md)。
