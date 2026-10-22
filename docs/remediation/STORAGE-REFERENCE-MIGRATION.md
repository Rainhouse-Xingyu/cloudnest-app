# 存储对象引用审计与约束

2026-10-22；G02 的回收前置工作。这里只安装数据库保留约束，不执行物理 GC，也不把“没有引用”解释为可删除文件。

## 引用边界

当前模型只有两类持久 storage_id 引用：

| 引用 | 保留规则 |
|---|---|
| file.storage_id | 所有非 NULL 指针；包括活动文件、回收站和异常的目录指针 |
| file_version.storage_id | 全部版本；包括 source_verified=false、当前没有对应 file 的历史记录 |

来源未验证影响读取授权，不取消保留责任。分片临时路径、旧修复路径、正在读取的对象没有因此获得完整生命周期管理；版本到 file 的结构完整性也不在本次约束范围。

两条外键均为 ON DELETE RESTRICT / ON UPDATE RESTRICT，不使用 CASCADE 或 SET NULL。直接 SQL 也不能向不存在的 storage.id 写引用，或删除/改号仍被任一引用持有的对象。父记录删除和子引用插入竞争时，由 InnoDB 外键检查及行锁决定先后；这不是物理文件系统事务。[MySQL 外键规则](https://dev.mysql.com/doc/refman/8.0/en/create-table-foreign-keys.html)、[外键检查的共享记录锁](https://dev.mysql.com/doc/refman/8.0/en/innodb-locks-set.html)。

## 只读审计

```sh
./cloudnest audit-storage-references
```

仅需 MYSQL_DSN，不依赖 JWT、Redis、HTTP 或 DATA_DIR。在只读 REPEATABLE READ 事务中统计 storage 对象数、file/version 的全部非 NULL 引用数、悬空引用数和无引用对象数。输出 JSON：悬空引用最多 100 条，按来源及行 ID 排序，保留完整总数和 truncated 标记；不输出文件名、路径、正文。

悬空引用产生非零退出码；查询失败同样非零，不输出伪造的空报告。仅有 unreferenced_objects 时成功退出，这个数字只是元数据快照统计，**不是 GC 候选清单或删盘授权**。审计不打开磁盘对象、不安装约束、不改指针、不删除任何记录。普通文件 NULL 指针、文件尺寸、路径和内容损坏、版本到文件关系等需要其他审计，本命令不能替代完整健康检查。

大表全量统计可能超过两分钟 CLI 超时；尚未完成生产数据规模的 EXPLAIN、锁等待和耗时验收。超时应保留失败并在副本评估，不能当作干净结果。

## 显式升级

仅在备份、停旧写实例、确认真实库结构及副本演练后，由操作者执行：

```sh
./cloudnest migrate
./cloudnest audit-file-names
./cloudnest migrate-file-names
./cloudnest audit-storage-references
./cloudnest migrate-storage-references
```

migrate 增加基础模型字段，仍不自动安装本约束；普通服务启动只读核对全部名称、版本元数据和引用约束，缺失或漂移时退出。未升级库不能直接启动新版。已有其他名称的同类外键不会被静默替换，必须审核结构差异。

迁移要求 MySQL 8 家族至少 8.0.16、三个表均为 InnoDB、storage.id 为单列主键、引用列为预期 signed BIGINT/nullability。会话及全局 foreign_key_checks 必须均为 1；程序不替操作者打开检查，也不使用 FOREIGN_KEY_CHECKS=0 加速升级。重新开启检查不会追溯校验关闭期间写入的旧行，因此迁移重试仍先审计。

| 独立步骤 | 原子 DDL | 版本 |
|---|---|---|
| file 引用 | 添加 idx_cn_file_storage_v1 单列可见普通索引及 cn_file_storage_v1 外键 | 20261022_02_file_storage_refs |
| file_version 引用 | 添加 idx_cn_version_storage_v1 单列可见普通索引及 cn_version_storage_v1 外键 | 20261022_03_version_storage_refs |

每张表一条原子 ALTER，成功后分别写 cloudnest_schema_migration 的 SHA-256 checksum；**两张表的 DDL 不构成一个原子事务**。使用固定数据库连接和数据库范围 GET_LOCK 串行化同类迁移，取消时尝试释放锁；获取/释放结果不确定的连接不回池。

迁移在任何 DDL 前检查所有已有步骤的索引、外键目标库/表/列、列顺序、DELETE/UPDATE 行为及 checksum。约束缺失但已有记录、部分索引、级联行为、额外 storage_id 外键、不同目标或错误 checksum 均拒绝自动修复。遇到悬空引用保留原记录并退出，不自动删除、改写或将未知历史标成可信。

## 中断与恢复

- 第二步失败时，第一步及其版本记录可能已经完成；启动检查拒绝这种部分升级。备份、审查错误和实际结构后重跑同一命令，精确已完成步骤不重复执行。
- 某步 ALTER 成功但 ledger 未写入时，只允许在索引/外键定义精确匹配后补记该版本。
- 审计后另一个写入者制造悬空引用时，该步启用外键检查的 ALTER 会失败，整个该步的索引/约束不落下，历史坏引用仍保留。应停止旧写入者并审核数据，不能关闭外键强行通过。
- 不提供自动 down；不能为回滚旧二进制而删除数据或约束。旧程序与新程序混跑未验收，需停服处理。

应用及维护写入必须保持外键检查开启。拥有 DDL/禁用外键权限的外部操作者仍能破坏约束；启动检查不是持续监控，更不是对生产库完整性的证明。后续 GC 必须在自己的事务中核对状态、引用、读取租约和迁移契约，再以持久回收日志处理磁盘/数据库中断；本批没有删盘命令。

## 验证

新增 11 项 disposable MySQL/Redis 集成用例：直接 SQL 插入/更新拒绝、回收站/未知旧版本保留、两种并发发布/移除顺序、无磁盘访问的全引用统计、101+1 条悬空引用截断及实际 CLI、历史记录不变、跨表中断/ledger 间隙/重试、缺失/级联/错误目标/部分索引/checksum 漂移拒绝、额外外键拒绝、六操作者并发、审计后晚到坏行的原子 DDL 失败、取消释放锁、关闭检查拒绝、数据库单独配置的真实 CLI 和未迁移启动拒绝。完整结果见 [测试记录](TESTING.md)。

首轮测试暴露夹具问题：GORM 失败 Update 仍修改 Go 结构字段，指针别名使后续断言查询了错误 ID；已改为独立 Model/WHERE。漂移夹具在默认 ALTER 算法下同语句 DROP/ADD 同名外键失败，改为临时库两条 DDL。两项均是测试构造问题，未将失败计为通过。所有建库、改约束与删除测试记录都只在带 disposable 防护的容器中进行；没有连接或修改项目真实数据库。

后续已接入 [物理读取租约](STORAGE-READ-LEASES.md)，storage_read_lease 也用 RESTRICT 外键保留父对象；此报告的两步迁移仍只管理 file/file_version。活动租约/过期清理/物理回收的运行协议不能由前述无引用统计替代。
