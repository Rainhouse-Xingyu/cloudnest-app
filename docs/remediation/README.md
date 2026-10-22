# CloudNest 整改工作台

基线：`cloudnest` / `cbad48baded392fd214150545c5ef861cecc030d`，审查日期 2026-10-21（Asia/Shanghai）。

用户粘贴的任务正文是执行要求。Downloads 中两份参考文档未找到，因此未作为代码事实或额外授权依据。保留 Go 分层单体与现有数据，iOS 仅作为历史参考；macOS 在独立目录开发。

- [里程碑与验收](MILESTONES.md)
- [问题登记表](ISSUES.md)
- [基线验证报告](BASELINE.md)
- [测试执行方法](TESTING.md)
- [G01 安全整改与升级边界](G01-SECURITY.md)
- [现有客户端 API 权限兼容性](CLIENT-API-COMPATIBILITY.md)
- [设备身份边界设计（G05 待实现）](DEVICE-IDENTITY-DESIGN.md)
- [受控账号恢复与会话撤销](ACCOUNT-RECOVERY.md)
- [G02 文件一致性与递归安全](G02-FILE-CONSISTENCY.md)
- [只读文件名称约束预检](FILE-NAME-AUDIT.md)

状态定义：待做、进行中、代码完成待审、验收完成、外部阻塞。代码存在、构建通过、单项测试通过均不等同于业务验收完成。每个后续 PR 必须更新问题、测试结果及本表，保留失败和未执行项。

G00 已提交 [PR #1](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/1)，G01 持续更新 [PR #2](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/2)，#2 的 base 是 #1 的分支，两者仍为未合并草稿。已核实 #1 的原始漏洞基线失败、#2 首批代码 `cee6db8` 、审查修复 `c61f337`、登录限流 `38416ad` 及账号恢复 `588a187` 的构建/测试/漏洞 CI 全通过；后续提交以对应 SHA 的 PR checks 为准。

G01 本次补齐自定义权限下的会话查询/注销、随机 JTI、客户端 API 契约测试及 Redis 原子登录限流。受控密码恢复/会话撤销已实现，17 项本地集成及迁移回归通过，G01 代码完成待审，最终提交 CI 单独核验；设备身份边界设计已记录，G05 实现前仍保留风险项；下一阶段按依赖推进 G02 文件一致性。G02 已实现命名隔离/事务锁及递归权限整改，37 项本地集成通过，已提交独立 [PR #3](https://github.com/Rainhouse-Xingyu/cloudnest-app/pull/3)，base 为 PR #2 分支；存储引用、GC、配额、目录继承及数据库最终约束仍在推进。G02–G11 未完成，整体目标保持进行中。所有生产数据迁移、旧凭据轮换、真实 FRP 服务器访问和最终发布必须在有相应凭据/授权后执行。
