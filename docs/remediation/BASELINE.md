# G00 基线验证报告

日期：2026-10-21（Asia/Shanghai）。源分支 `cloudnest`，源提交 `cbad48baded392fd214150545c5ef861cecc030d`。工作开始时无未提交改动，本地与 origin/cloudnest 一致。

## 环境与真实能力

Go 1.25.1 / darwin-arm64；go.mod 声明 Go 1.25.0；Xcode 26.3 (17C529)；Docker Server 29.4.1。已存在 MySQL 8.0 和 Redis 7.4 镜像，测试创建了全新容器，退出后已确认测试容器清理完毕。未连接生产数据库或本地现有 MySQL/Redis。

Go 后端有 Handler/Service/Repository 分层与接口契约，应保留。已有文件、协作、AI、FRP、Live Photo 等 API，但完整业务可靠性未验收。macOS 目录为空；iOS 为历史客户端，测试文件只有示例模板。

## 实际执行结果

| 检查 | 结果 | 解释 |
|---|---|---|
| 原始 `go test ./...` | 命令成功，但无测试 | 全部 11 个包均 `[no test files]`，不能算业务通过 |
| 原始 `go build ./...` | 通过 | 本机 Go 构建通过 |
| 原始 `go vet ./...` | 通过 | 静态工具无报错，不能证明安全 |
| 新增 `go test -race -count=1 ./...` | 通过 | JWT 正常/过期/签名错误/错误算法；路径授权匹配；未覆盖全业务 |
| 新增 `go build ./...` / `go vet ./...` | 通过 | 加入测试与脚本后复查 |
| `go mod verify` | 通过 | 模块缓存校验通过，非漏洞结论 |
| `scripts/test-integration.sh` | 通过 | MySQL/Redis/磁盘真实链路，race 开启，TestAPIFileLifecycle 通过 |
| `scripts/check-repository.py` | 通过 | 示例凭据为占位符；发布包/二进制/用户配置取消跟踪 |
| `git diff --check` | 通过 | 未发现补丁空白问题 |
| govulncheck v1.9.0 + Go 1.25.1 | **失败，退出 3** | 43 项可达漏洞；另有导入/依赖但未识别调用的告警，未视作已利用 |
| 首次 `go run ...govulncheck@latest` | **失败** | 工具自动切到 Go 1.26.10，报告 6 项可达依赖漏洞；不能代替旧 Go 标准库检查 |
| iOS Simulator build | **失败，退出 70** | 缺少所需 iOS 26.2 平台，destination 不可用；尚未进入完整 Swift 编译 |
| iOS 单元/UI 测试 | 未执行 | 构建平台阻塞，现有测试仅模板 |
| macOS 构建/运行 | 未执行 | 尚无工程 |
| AI 检索效果 / OCR / 真实视觉 | 未执行 | 无模型配置实测，不将后台任务终态冒充能力 |
| FRP 真实隧道 | 未执行 | 无 Agent 和专用 frps 测试部署 |
| 大文件/压力/并发/真实数据库迁移 | 未执行 | 另设阶段专项验收 |
| GitHub Actions | 已执行 | G00 PR #1 的 build-test 通过，vulnerabilities 失败，与基线一致；修复在 G01 |

API 集成测试覆盖：两用户 bcrypt 登录、未登录 401、非管理员 403、目录创建、MD5 分片上传、合并、原始二进制下载一致性、跨用户下载/重命名拒绝、他人列表隔离、公开分享下载、撤销后 404、删除/恢复与内容一致、注销后 401。数据为隔离测试夹具，不是产品 Mock 服务。

## 依赖扫描与整改依据

完整公告编号摘要见 [漏洞基线](VULNERABILITIES.md)。官方 [govulncheck](https://go.dev/doc/tutorial/govulncheck) 的可达性分析受构建标签/工具链影响，不能据条数推导实际攻击成功。

初次新工具链扫描识别到 x/net v0.51.0（修复版本 v0.60.0）、x/text v0.35.0（修复版本 v0.39.0）、quic-go v0.59.0（修复版本 v0.59.1）。参考官方公告：[GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617)、[GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970)、[GO-2026-5676](https://pkg.go.dev/vuln/GO-2026-5676)。G01 升级后必须再次执行全套测试及扫描。

为避免工具自动升级掩盖应用标准库风险，`make vuln` 先在临时目录编译固定 scanner，再用项目选择的 Go 工具链扫描。扫描输出不包含真实凭据。

## G00 交付范围

提供持续台账、测试/隔离环境/CI、仓库产物治理及泄露示例清理。不是最终项目验收。示例中的原凭据如果有效必须人工轮换，Git 历史仍包含旧值；没有改写历史、修改实际账号或执行真实数据库迁移。
