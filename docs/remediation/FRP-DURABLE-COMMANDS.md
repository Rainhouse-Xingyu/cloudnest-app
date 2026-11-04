# G05 持久命令与配置领取

新命令写入 MySQL `agent_command`，管理端和 Agent 均通过 HTTPS 访问。入队、领取和终态回执各自与审计在同一事务中提交。表中只保存动作、版本 ID、配置摘要和状态，不存配置正文或设备凭据。终态记录保留；每设备最多 32 条 pending/leased 命令。

## 管理端协议

管理端使用用户 JWT、路由权限和设备所有权检查，管理员仍不能用 JWT 调用 Agent 执行接口。设备及所有者必须有效，入队还要求设备已完成独立凭据注册。

| 接口 | 请求 | 响应 |
| --- | --- | --- |
| POST /api/frp/devices/:id/commands | request_id、action、config_id | item 和 command_id |
| POST /api/frp/devices/:id/deploy-config | JSON config_id，Idempotency-Key 头 | command_id、device_id、config_id |
| POST /api/frp/devices/:id/reload | JSON 空对象，Idempotency-Key 头 | command_id、device_id |
| GET /api/frp/devices/:id/commands | limit 默认 20、最大 100 | items，持久命令历史 |

request_id 和 Idempotency-Key 都是 32 位小写十六进制随机 ID，客户端在第一次发送前持久保存。同一设备以相同请求 ID 重试相同动作和原始 config_id，返回同一命令，不重新执行入队。换动作、换配置参数或换设备凭据代次后复用同一请求 ID 返回 409。终态或已过期命令的相同请求 ID 也不会自动生成新工作。

动作只接受 deploy_config、start、stop、reload、diagnose。只有 deploy_config 能指定非零 config_id；零值在首次入队时解析为当时的最新配置版本，之后重试仍返回原版本。入队前解密验证该版本并保存其 SHA-256，不能派发旧明文、坏密文、其他设备的配置或缺少密钥的配置。Agent 不能选择任意 shell、可执行路径或额外命令参数。

以上写接口只接受一个不超过 4 KiB 的 JSON 对象，拒绝未知字段和查询参数。响应带 Cache-Control: no-store。无效输入返回 400，缺少注册或幂等冲突返回 409，队列满返回 429，配置/数据库不可用返回 503。旧 deploy-config/reload 入口也已使用新表，需相应请求 ID；reload 需发送 `{}`。历史列表返回新状态结构，不再读取旧 Redis 命令历史。旧 Redis 记录保留原生命周期，未自动导入新协议。

## Agent 协议

所有 Agent 调用使用独立 Bearer 设备凭据。device_id 从当前凭据确定，请求体不能指定其他设备。请求 ID、命令 ID、claim_id、lease_id 都使用 32 位小写十六进制格式。

| 接口 | 请求字段 | 行为 |
| --- | --- | --- |
| POST /api/agent/commands/claim | claim_id | 返回 command 和 lease_id，无可领取工作时 204 |
| POST /api/agent/commands/:id/renew | lease_id | 返回 lease_expires_at |
| POST /api/agent/commands/:id/config | lease_id | 返回 config_id、config_text、sha256 |
| POST /api/agent/commands/:id/result | lease_id、success、code | 成功保存或相同回执重试时 204 |

设备按创建时间领取命令，每台设备同时只有一条活动租约。领取响应丢失时应使用相同 claim_id 重试，在租约有效期内返回同一个 lease_id 和同一次尝试。不同 claim_id 不会获得已经活动的租约。每条租约 60 秒，可在到期前续租；命令从入队起最多有效 24 小时，续租不能超过该期限。租约到期后允许重领同一命令，生成新 lease_id 并增加尝试次数。总计五次领取后仍未完成且租约到期，该命令转为 timed_out，后续命令可以继续。

旧租约、超期租约、其他设备或其他凭据代次不能读取配置、续租或提交结果。配置仅在 deploy_config 的活动租约内解密返回，并重新核对版本、设备与入队时的明文摘要；更换密钥但内容相同的重加密不影响领取。HTTPS、no-store 和有界正文同样适用。授权检查与凭据轮换/撤销串行，已经发送给设备的字节不能被撤回。

结果只接受固定代码。success=true 时 code 必须是 ok；失败代码为 config_invalid、start_failed、stop_failed、reload_failed、diagnostic_failed、internal_error。拒绝自由文本，避免 frpc 日志或凭据进入命令记录。相同 lease_id 和相同终态回执可以重复提交，审计只产生一次；相反的结果返回 409。终态相同回执重试不要求租约仍在有效期，但设备凭据必须仍有效。

## 并发、时间与失效

事务按所有者、设备、绑定、命令顺序锁定，并使用 READ COMMITTED。初版默认 REPEATABLE READ 在取得设备锁前建立了旧快照，使并发的相同请求 ID 触发唯一约束错误；已有真实失败用例验证并修复。独立服务实例共同使用数据库中的唯一请求索引、租约及队列容量，不依赖进程内缓存。

命令时间以数据库 UTC 时钟为准，新表的时间读写不受 MySQL DSN 的 loc 参数影响。已有业务表保留原时间约定。每个设备命令事务有五秒上下文期限，失败返回错误并回滚本次状态变化。

命令绑定发放时的 credential_id。注册轮换、撤销、设备/所有者停用或转移使旧身份立即不能继续使用命令；新注册不会继承旧命令。过期或凭据失效的活动记录在后续入队/领取时持久标记 timed_out/cancelled，管理列表也会按当前绑定和时间计算这些状态。状态可为 pending、leased、succeeded、failed、timed_out、cancelled。

部署需先停止旧写实例并执行显式 migrate，新增 agent_command 表及请求唯一索引、领取索引。普通启动检查必需列和索引，缺失时拒绝启动。本批没有修改四个凭据撤销触发器的动作。用户 JWT 的旧 status/report、commands/next、旧 result 仍仅为未登记设备保留兼容入口，对已登记设备继续拒绝。

## Agent 执行义务及验收边界

这是允许重试的投递协议，不能据此宣称外部进程动作恰好执行一次。Agent 需要单实例锁、按 command_id 保存的执行记录、受控配置文件、官方 frpc 校验、PID/配置摘要核对，以及执行完成后可重发的结果。每次外部动作前确认租约仍有效；断线后应按命令 ID 对账，不能收到重领命令就无条件再启动一个进程。claim_id 用于当前领取响应的恢复，command_id 才是跨租约执行去重的身份。

服务端已用实际 TLS 服务和隔离 MySQL 验证配置领取/回执，多管理器并发验证幂等、容量和租约唯一性，并覆盖重领、旧回执拒绝、代次隔离、超时、审计回滚、索引缺失及跨连接时区。2026-11-04 Go Agent 已接入本地执行记录和白名单配置，实际 CLI 管理的 TCP/HTTP/HTTPS、正常重启及 frps 重连在隔离环境通过。真实崩溃自动接管和更长时间的断线/休眠恢复仍待验收，详见 [Agent 运行与边界](FRP-AGENT.md)。旧配置加密并不等于内容已通过 Agent 的配置白名单检查。
