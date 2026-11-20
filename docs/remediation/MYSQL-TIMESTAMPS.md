# MySQL 时间协议与历史值边界

TIME-001 的直接证据来自 2026-11-20 原生 UI 验收使用的独立临时库。MySQL 为 `time_zone=SYSTEM`、`system_time_zone=MST`，`NOW()` 比 `UTC_TIMESTAMP()` 少 7 小时；DSN 却是 `loc=UTC`。数据库 `CURRENT_TIMESTAMP` 写入的朴素 DATETIME 被当成 UTC 返回，客户端再次转换为本地时间，列表比原生面板少 7 小时。没有据此推断生产库的原时区。

## 新连接与新默认值

`store.NewMySQL` 保留 DSN 的 `loc`，要求解析时间，并由驱动在每一条物理连接建立时设置对应的数字 `time_zone`。UTC 对应 `'+00:00'`；明确固定为东八区的连接对应 `'+08:00'`。重连和池扩容也经过同一过程，无需数据库时区表，也不修改全局 MySQL 配置、表结构或历史单元格。

显式 `time_zone` 必须使用与 loc 对应的带单引号数字偏移；冲突值、SYSTEM、不同大小写的同名参数和存在未来偏移切换的地区拒绝启动。后者包括使用夏令时的地区：朴素 DATETIME 不能区分秋季重复的一小时，启动时固定当前偏移也会在下次切换后失效。UTC、当前及未来固定的 Phoenix/Shanghai 等地区可以保留既有连接约定。错误不输出原 DSN 或密码。

新安装示例改用 UTC。已有库不能直接套用新示例：驱动 loc 的改变会重新解释全部历史 DATETIME，即使没有执行 UPDATE。升级前记录实际原配置和写入来源；所有服务、worker、维护凭据应采用相同的业务时间约定。拥有未来偏移切换的旧配置须先制定历史计划，不能为绕过启动检查擅自换 loc。受支持时区只说明新连接可保持这个约定，不证明旧数据一直遵守该约定。

此次覆盖数据库生成的插入及 ON UPDATE 默认值。既有 `CommandTime` 仍独立以 UTC 编解码 Agent 命令/租约，不依赖业务 loc；其跨 UTC/Shanghai 交接回归保留。其他显式 UTC 的调度/到期字段需按各自协议核对，不能把本修复视为全库所有字段已完成时间治理。

## 历史迁移的准入与回滚要求

可先在已授权的副本使用 `cloudnest audit-mysql-time` 生成只读 JSON 盘点。命令仅需现有 MYSQL_DSN，不依赖 Redis、JWT、DATA_DIR，不运行业务 schema 检查或自动迁移；在只读事务中记录服务器 UUID、库名、当前 session/global/system 时区、同一查询的本地/UTC 时钟，以及当前凭据可见的 DATETIME/TIMESTAMP 字段类型、精度、NULL、默认值和 ON UPDATE 定义。不读用户行值或输出 DSN。按表/字段排序，最多输出 1000 列；超过上限输出带 truncated 标记的部分报告并非零退出，不能作为完整盘点使用。

盘点只反映当前凭据能看到的元数据：没有可见列不代表库没有时间字段。元数据不构成历史一致性快照，需在 DDL 静止时核对；当前时区和默认值不能证明旧数据的来源。实际历史计划仍需下面的来源证据、原值和逐值 CAS。本批仅在临时测试库运行此命令。

TIME-001 的历史部分保持开放。已有只读盘点和逐值规划，尚未提供执行/回滚命令，也没有运行生产迁移。不能根据当前服务器 SYSTEM、用户界面地区或一条样本，为所有行整体加减 7 小时。

## 只读逐值计划

`cloudnest plan-mysql-time` 从非终端 stdin 读取一个严格 JSON 请求，最多 1 MiB、200 个不重复的明确单元格。每项要求表、列、正整数行 ID、原时区和证据引用；没有按范围或“全库平移”的入口。示例仅描述格式，ID、原时区和证据必须来自已核对的目标副本：

```json
{
  "version": 1,
  "cells": [
    {"table":"file","column":"create_time","row_id":42,
     "source_zone":"America/Phoenix","evidence":"CHG-42/mysql-host-history"},
    {"table":"file","column":"update_time","row_id":42,
     "source_zone":"America/New_York","source_offset":"-04:00",
     "evidence":"CHG-42/writer-log-with-offset"}
  ]
}
```

在私有目录准备请求并保留输出，例如先设置 `umask 077`，再执行 `cloudnest plan-mysql-time < request.json > plan.json`。同一批中不同字段可有不同来源。证据引用最多 200 字节，不能是空白或控制字符，不应存放凭据。未知 JSON 字段、多文档、重复选择、未知列、非整数主键、非 InnoDB 表和 TIMESTAMP 列均拒绝；缺失行不会被当成 NULL。

原时区可用明确 IANA 名称或带符号的 HH:MM/HH:MM:SS 固定偏移，拒绝 Local/SYSTEM。规划枚举原时区的实际偏移区间，验证回转字段一致；缺失时间拒绝，重复时间必须以 `source_offset` 指定实际原偏移，偏移不符同样拒绝。原值为 NULL 时保留 NULL；零日期、非法日期和转换越界不生成计划。微秒保留到字段定义中的精度，不对时间增加浮点换算。

目标约定来自本次实际使用的最终 MYSQL_DSN loc，并与只读事务的 session time_zone 核对；目标 loc 必须是明确名称，不能是随主机变化的 Local。不能为运行规划而猜测更换旧部署的 loc。目标历史时刻若产生新的重复墙钟时间也拒绝，避免再次存入无法唯一解读的 DATETIME。

计划在只读 Repeatable Read 事务内读取指定原始墙钟字符串，输出库 UUID/名称、目标约定、每项 before/after、对应 UTC 时刻、解析出的原偏移、证据和字段定义；不读取文件内容、账号凭据或修改行/结构/审计。按表、行 ID、列排序，SHA-256 绑定完整 JSON 结构（计算时 sha256 字段为空字符串）。摘要供完整性核对使用；执行工具仍须校验库身份、字段定义、来源和原值，不能只信任摘要。

目前只允许下列旧业务元数据列。它们的写入来源仍必须逐项提供证据，列名不代表数据已被证明有误：

| 表 | 列 |
|---|---|
| user、file | create_time、update_time |
| storage、workspace、upload_chunk、upload_task、user_path_permission、file_version | create_time |
| live_photo、device、frp_mapping、frp_config、ai_metadata、ai_task、share、operation_log | create_time |

Agent UTC 命令/租约、AI 拍摄本地时间、到期字段及容量账本时钟不进入这个旧默认值转换协议。当前命令只输出计划，**不执行修复或回滚**；历史修复完成条件仍包含下面的事务、CAS、回执、派生索引和恢复演练。

后续迁移应先在副本演练，并形成以下可审阅材料：

1. 记录服务器 UUID、数据库、表/字段类型、原始 DSN loc、数据库与应用主机历史时区、部署/导入时间区间及其证据。仅对证据覆盖的行 ID 和字段生成计划；DATETIME、TIMESTAMP、显式 UTC 协议字段和 EXIF 拍摄时间分别核对。
2. 在只读一致性快照中导出原始字符串，明确原时区与目标约定，逐字段列出转换前后值。零值、NULL、DST 缺失/重复时间及证据不足项停止或保留人工待核；重复时间必须提供可验证的原偏移。不得借驱动 loc 在读取时先做一次隐式转换。
3. 计划绑定具体库身份、行 ID、字段和值摘要。应用时确认停写边界与备份可恢复性，用事务及原值 CAS 拒绝陈旧计划；重复应用不得二次平移。更新选定字段时显式保留其他 ON UPDATE 时间值，审计和修改原子提交。
4. 生成不可变回执，保存 before/after、证据与执行身份。回滚同样逐值 CAS，只撤回本次值；并发业务修改后拒绝覆盖。需要派生时间索引的文件须按现有索引代次协议重建，不能只改展示列。
5. 验收覆盖混合来源、跨日/跨年、正负/非整小时偏移、DST 边界、重复运行、陈旧计划、审计失败事务回滚及备份恢复。真实库操作仍遵守用户禁止擅自修改生产数据的要求。

## 本批验证

macOS 本机使用独立 MySQL 8.4.13/Redis 8.10.3。下面命令只创建并销毁临时服务，可通过现有两个依赖路径环境变量指定私有安装：

```sh
python3 scripts/test-macos-backend.py \
  --filter '^TestMySQLTime|^TestAgentCommandUTCLeasesAcrossConnectionTimezones$' \
  --mysql-time-zone=-07:00
```

回归持有六条池连接并再建连接、通过真实创建/列表/改名 API 核对默认时间、确认重连不改历史字符串、验证显式固定东八区，以及既有 Agent 跨时区命令领取/续租/完成。将 NewMySQL 用 Go overlay 切回修复前实现时，前三项按预期失败：会话偏移 -25200 秒、列表时间差 7 小时、固定东八区仍得到 -07:00。修复后的最终结果及精确提交 CI 在 G08/PR 中记录。CI 两个 macOS job 均显式运行非 UTC 数据库回归，避免托管机器默认 UTC 掩盖此问题。
