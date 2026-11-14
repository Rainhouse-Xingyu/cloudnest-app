# FRP 服务器登记与配置版本

本批为 G05 增加稳定的服务器身份及不可变策略版本。它管理 CloudNest 保存和下发的 frpc 配置；不会远程修改 frps、验证公网服务器可达性，或自动解除历史全局映射预留。真实隧道验收使用固定官方二进制和隔离测试服务，生产服务器接入仍须单独验收。

## 接口与归属

以下接口均要求用户 JWT、路径权限、实际 HTTPS，并返回 `Cache-Control: no-store`：

| 接口 | 行为 |
| --- | --- |
| POST /api/frp/servers | 新建归当前用户所有的服务器 |
| PUT /api/frp/servers/:id | 提交新版本，必须提供当前 `expected_revision_id` |
| GET /api/frp/servers/:id | 返回当前安全元数据，不返回 token 或密文 |
| GET /api/frp/servers?after_id=0&limit=50 | 按 ID 游标查询，limit 为 1–100 |

所有者可读写自己的服务器；管理员可以管理现有服务器。生成配置时，服务器和设备必须属于同一用户，管理员也不能混用两人的凭据。列表只含当前用户记录，管理员列表除外。不存在删除或转移服务器的 API；停用用于保留版本和审计。

创建示例：

```json
{
  "name": "我的 FRP 服务器",
  "server_addr": "frp.example.com",
  "server_port": 7000,
  "token": "由部署方配置的凭据",
  "tcp_start": 20000,
  "tcp_end": 20999,
  "http_port": 80,
  "https_port": 443,
  "enabled": true
}
```

输入最多 4 KiB，拒绝未知字段和尾随 JSON。创建时必须显式提供 token，允许显式空字符串；更新时省略 token 或传 null 表示保留当前秘密，空字符串表示清除。更新提交完整名称、地址及端口策略；省略端口/范围会采用下述默认值，不是局部 PATCH。响应仅有 `token_configured`，没有秘密读回接口。

地址规范化为小写 ASCII DNS 主机名或规范 IP，去掉单个 DNS 尾点；IPv4-mapped IPv6 归一为 IPv4。不进行 DNS 解析，也不声称识别别名是否指向同一实际服务器。`address + port` 全局唯一，端点创建后不可更改；迁往其他端点须创建新服务器。端口默认 7000；TCP 范围两端都省略/为零时默认 20000–20999；HTTP/HTTPS 的零表示未配置。控制、HTTP、HTTPS 非零监听端口不得相同，也不得落在 TCP 范围内。拒绝控制字符、FRP 模板、非法主机名、过长名称/token，以及 token 的首尾空白（与 Agent 文件 token 读取语义一致）。

## 生成、回滚与部署

`POST /api/frp/configs/preview` 和 `/save` 新增输入：

```json
{"device_id": 12, "server_id": 3, "server_revision_id": 8}
```

调用必须固定当前服务器版本，不能混入旧 `server_addr/server_port/token` 参数。版本已变或服务器停用返回 409。服务器读取、当前活动映射快照、配置插入及审计位于同一数据库事务，使用现有 FRP 写入锁。TCP 映射须在该版本范围内；HTTP/HTTPS 映射须有对应监听端口。生成器与现有 Agent 使用相同 YAML 格式，最多 512 条映射及 32 KiB。预览遮蔽 token；保存/历史/回滚只返回安全元数据，并含服务器和版本 ID。

历史回滚生成新配置 ID，保留原服务器版本关联并重新加密。回滚和新部署都会核对设备所有者仍拥有该服务器、服务器启用、历史版本存在。允许明确回滚到较早版本，不能把版本更新描述为旧 token 已撤销。设备转移后，新所有者不能下发或回滚原所有者的托管服务器配置。

停用服务器阻止新生成、回滚及新部署。它不会撤销已排队的命令、停止已运行隧道、修改 frps token 或释放端口。重启/恢复原有 Agent 意图仍遵循既有协议；需要停机时须使用已有停止/设备凭据撤销流程，并验证实际结果。历史旧 API 仍可提交原始端点/token，保存为未关联配置；它不是托管服务器策略的强制替代入口，后续客户端迁移及统一策略仍待处理。

## 数据、加密与升级

显式 `migrate` 新增 `frp_server`、`frp_server_revision` 及 `frp_config.server_id/server_revision_id`。历史配置两列保持零，不从旧明文猜测服务器或归属，不改写历史正文。启动只读核对 InnoDB、主键、端点完整唯一索引及列约束；缺件/漂移阻止启动，不在普通启动时修库。

服务器修订使用独立 `cnfrps:1:` AES-GCM 信封，AAD 绑定所有者、服务器、修订和密钥 ID。托管配置使用 `cnfrp:2:`，绑定设备、配置、服务器、修订及密钥 ID；删除或替换关联列不会把它降级为旧配置。既有未关联加密配置仍使用 `cnfrp:1:`。两种正文都不从用户接口或模型 JSON 暴露。

先停旧写入实例、备份并用维护账号显式迁移，再运行新后端。旧后端不认识 v2 配置，不能与新实例混跑或直接降级读取新数据。Agent 接收解密后的原 YAML，协议和 Agent 版本本批不变。未操作生产库或生产凭据。

现有 `audit-frp-configs` / `encrypt-frp-configs` 维护命令现在同时检查/轮换配置和全部服务器修订，新增 `server_scanned/server_current/server_old_key/server_rewritten` 计数。dry-run 同样认证密文与策略；失败停止，已提交行可续跑。每条轮换和审计原子提交。旧密钥必须保留到两类历史及备份都按部署要求处理完毕，不能只看原配置计数就删除旧密钥。

## 尚未完成的端口生命周期

映射现支持按服务器划分的端口和域名，历史 server_id=0 保持与所有服务器重叠，见 [映射协议](FRP-MAPPING-RESERVATIONS.md)。禁用/退役不自动释放。后续已接入[运行配置占用及成功清除回执](FRP-RUNTIME-RESERVATIONS.md)；历史占用对账、离线设备释放确认、DNS 别名和外部 frps 占用探测仍未完成，不能把保存成功视为远程端口可用。专项、真实 Agent 和完整回归证据记录在 [测试记录](TESTING.md)；G05 和总 Goal 保持进行中。
