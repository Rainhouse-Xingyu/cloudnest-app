# 独立公网 FRP 验收

公网验收使用操作者明确准备的测试 frps 实例，保留远端现有服务和 Mac 现有 frpc。测试只创建自己的临时上游、Agent 状态、密钥和 disposable MySQL/Redis；不连接生产数据库。它通过正式 HTTPS 接口登记服务器、创建带 server_id 的三种映射、保存配置并下发持久命令。

## 准备与运行

在 Linux x86_64 服务器运行 `scripts/prepare-public-frps-test.sh PUBLIC_IPV4`。默认独立端口为控制 17000、HTTP 18080、HTTPS 18443、TCP 映射 20080，也可显式指定四个端口。脚本先检查监听冲突，再下载并验证固定 FRP 0.71.1 SHA-256，生成私有目录、随机 token、七天有效且绑定公网 IP 的测试证书以及前台 run.sh。它不修改防火墙、系统服务或信任库。操作者允许测试来源访问这四个端口，并在独立终端运行生成的 run.sh。20080 只在代理成功注册后监听。

安全复制 ca.pem 和 token 到 Mac 的绝对路径私有目录（目录 0700、文件 0600），server.key 留在服务器；不要把 token 放到聊天、命令参数或 Git 中。Mac 执行：

```sh
./scripts/test-agent-public.sh PUBLIC_IPV4 /absolute/private/credential-directory
```

非默认端口时在末尾依次追加控制、HTTP、HTTPS、TCP 端口。脚本下载并核验本平台官方 frpc，测试结束删除下载目录；凭据目录由操作者保留。远端前台实例由操作者 Ctrl-C 停止。测试不会 SSH 或重启远端服务。

## 验收证据

先通过私有 CA 严格验证远端控制端口的 TLS 身份，然后部署。每种隧道必须返回临时上游的随机 nonce；TCP 与 HTTP 使用服务器 IP 作为 Host，HTTP 映射的 customDomains 也设为该 IP；HTTPS 使用 example.com 作为测试 SNI 并验证临时上游证书。探测直接连接显式公网 IP，不依赖公共 DNS，不接受重定向或外部网页作为成功。若本机有 TUN/透明代理，需对测试 IP 使用 DIRECT；仅取消 shell 的 HTTP_PROXY 不能绕过系统 TUN。

验收包含三隧道、注册计数、stop/start、Agent 重启恢复、持久 forget、重启后 start/reload 拒绝旧配置，以及重新部署/再次清除。stop 必须保留三条运行资源占用，成功 forget 必须清空对应代次占用。停止探测要求随机上游响应消失，同时控制端口 TLS 仍可认证，避免把整台服务器断网当作停止成功。

裸 TCP Dial 成功不能证明远端已有监听：TUN 可能在远端连接成功前接受本机 socket。因此不以它拒绝测试或声称端口已关闭；远端资源占用由实际 frpc 注册结果及随机响应证明。固定测试端口的部署若冲突会失败，不会停止或抢占已有远端监听。

这套公网验收覆盖 IP 虚拟主机 HTTP 和域名 SNI HTTPS，不等于真实 DNS 配置或所有本机代理规则已验收。example.com 纯 HTTP Host 路径在本机曾超时，而相同端口的 IP Host 与 HTTPS SNI 对照可达；原因尚未确定，保留在测试记录中，不将换 Host 表述为已修复域名路径。

结果以 [TESTING](TESTING.md) 的实际运行记录为准。准备脚本成功、端口放行、控制 TLS 成功或操作者手工连通，均不能替代完整 Agent 验收。

## 2026-11-13 复测记录

北京时间 10:33 的早期 Agent 诊断原文为 `public command diagnose: failed/diagnostic_failed, want ok`。服务器在北京时间 10:33:51.738（UTC 02:33:51.738）记录 `new proxy [device_1_map_1] type [tcp] error: port already used`。两次测试的控制目标均为 `129.146.81.189:17000`，手工 frpc 配置通过 `auth.tokenSource` 读取与 Agent 相同的 token 文件，TLS 也信任同一份 CA。手工 frpc 0.71.0 直接注册单条 TCP 20080 并在服务器回环地址验证，Agent 则从管理 API 接收部署，调用官方 frpc 0.71.1 注册 TCP/HTTP/HTTPS 后从 Mac 探测公网数据路径。

端口释放后，提交 `f83e4b641ddee819596b49266a1ade4e92016f7a` 于北京时间 19:42:30 启动的公网复测 **通过（测试 87.60s，包 90.382s）**，日志 `/tmp/cloudnest-g05-f83e4b6-public-20261113.log`。三种代理全部注册，验证随机响应、启停、Agent 重启、持久清除与重新部署。HTTP 使用公网 IP 作为 Host，HTTPS 使用 example.com 作为 SNI。该提交的 PR CI 和 push CI 均通过。
