# 活动文件名唯一约束迁移

版本：`20261022_01_file_active_names`。命令：`cloudnest migrate-file-names`。这是 G02 的首个独立版本化迁移；基础 `migrate` 仍是显式 GORM AutoMigrate，其他模型的版本化升级尚待后续阶段。

本轮只在 disposable MySQL 8.0/Redis 和临时文件上执行。真实数据库迁移、历史冲突修复及上线需单独审核与授权，本轮未执行。

## 约束与一致性

| 对象 | 作用 |
|---|---|
| `cn_active_scope_v1` | 活动条目的虚拟生成列：根目录为负 user_id，非根目录为正 parent_id；已删除条目为 NULL |
| `cn_active_name_v1` | 活动 filename 的虚拟生成列；沿用原列字符集和排序规则，已删除时 NULL |
| `uq_file_active_name_v1` | 两个生成列上的完整唯一索引；根按所有者隔离，非根按父目录统一判重，回收站名称不占活动名 |
| `cn_file_scalar_v1` | 强制正 user_id、非负 parent_id、非空 filename、0/1 folder/deleted 状态，显式拒绝这些字段为 NULL |
| `cloudnest_schema_migration` | 保存版本、DDL SHA-256 和应用时间；checksum 覆盖该库实际字符集/排序规则，不是文件内容校验和 |

选择虚拟生成列与索引，依据 [MySQL 生成列与索引支持](https://dev.mysql.com/doc/refman/8.0/en/create-table-generated-columns.html)。正负作用域依赖上述标量 CHECK，避免无效 owner/parent 或 NULL 绕开唯一性。[MySQL CHECK 约束](https://dev.mysql.com/doc/refman/8.0/en/create-table-check-constraints.html) 的 FALSE/UNKNOWN 区别要求显式拒绝 NULL，不能只写比较条件。

应用原有目录范围锁和名称分配仍保留，数据库索引用于阻止绕过应用协议的重名写入。已知索引的冲突返回 API 409；其他 SQL 错误不冒充重名冲突。普通 file JSON 模型不新增生成列。

## 升级步骤与拒绝条件

使用 MySQL 8 家族且至少 8.0.16、现有 InnoDB file 表；已实测 MySQL 8.0。运行环境通过 MYSQL_DSN 提供连接，此维护命令不需要 JWT/Redis，也不启动 HTTP 或读写磁盘内容。

1. 停止所有业务写实例，备份数据库及用户文件，并在备份副本演练。大表 DDL 的耗时和锁定应专项测量，本迁移不承诺在线零阻塞。
2. 用 `./cloudnest audit-file-names` 运行[只读预检](FILE-NAME-AUDIT.md)，审查完整结果。任何历史重名或标量异常都会使迁移拒绝继续，不自动改名、移动、删除或“修复”数据。
3. 需要基础结构升级时显式运行 `./cloudnest migrate`，再审计一次。基础 AutoMigrate 仍需检查真实结构差异。
4. 显式运行 `./cloudnest migrate-file-names`。新库也需要基础 migrate 后再执行本命令。
5. 重新运行只读审计并启动新程序。启动只验证结构和版本，不自动迁移；缺少/篡改约束或 ledger 时在创建数据目录、连接 Redis和监听 HTTP 前退出。

命令总上下文超时十分钟；维护操作按数据库哈希取得固定连接上的 GET_LOCK，等待上限十秒。并发操作者串行执行。结束时用独立短上下文释放锁；无法确定已释放时丢弃连接，避免把仍持有锁的会话放回连接池。

## 原子性、重试与恢复

所有生成列、CHECK、唯一索引通过一条 InnoDB ALTER 安装。[MySQL 原子 DDL](https://dev.mysql.com/doc/refman/8.0/en/atomic-ddl.html) 只保证单条 DDL，DDL 会隐式提交，不能把它和后续版本记录当作同一个事务。

- 审计发现问题：不执行本迁移 DDL、不写成功版本记录。既有 file/user/storage 行保持原样。
- 审计后又有绕过维护停写要求的新冲突：ALTER 失败；不会自动处理冲突条目。错误信息不输出冲突文件名。
- ALTER 成功、ledger 写入前中断：下次执行逐项验证完整定义；仅在所有定义精确匹配且 ledger 缺失时补记版本，不重新修改 file 表。
- 已完成迁移：重复执行验证定义与 checksum 后返回，不重复 DDL或覆盖历史。
- 部分生成列/索引、非唯一替换、CHECK 禁用、表达式/排序规则变化、checksum 不符、ledger 存在而对象丢失：拒绝自动修复。保留现场，由操作者在副本中核查并批准处理。

启动检查包括生成表达式、虚拟列类型/有符号范围/可空性、原列字符集与排序规则、完整可见唯一索引、启用的 CHECK、版本与 checksum，不以“同名索引存在”或版本号存在推断迁移正确。

没有提供自动降级删除约束的产品命令。代码回滚需停服评审，保留附加列、索引和迁移记录；不能通过移除保护重新允许历史重复。不在本命令中执行数据回填/修复或删除。

## 验证范围与遗留

真实数据库回归覆盖：12 路直接 SQL 同名竞争、根/共享作用域、已删除名称释放/恢复冲突、NULL 和非法标量拒绝、API 409 与事务回滚、历史异常拒绝且零行修改、重复迁移/ledger 间隙恢复、checksum/索引/CHECK 漂移、六个并发操作者、部分结构拒绝、实际 CLI 与旧库启动拒绝、上下文取消释放锁、审计后新冲突导致原子 DDL 失败、保留原有 binary 排序规则。

所有默认 API 夹具安装并验证此约束后运行，旧格式损坏数据测试才在已验证的临时库内显式移除/恢复约束。大库升级/崩溃恢复、真实备份恢复、MySQL 其他 8.x 小版本、完整版本化迁移框架仍需专项验收。

本迁移不验证父项存在/树无环、权限继承、文件路径合法字符、磁盘字节、引用/GC 或配额；这些由业务路径或后续阶段处理。数据库唯一性也不能证明上传完成已具备幂等性。
