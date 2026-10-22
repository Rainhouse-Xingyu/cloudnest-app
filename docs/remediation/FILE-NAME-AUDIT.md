# 文件命名唯一约束预检

`cloudnest audit-file-names` 是只读维护命令，用于检查 G02 命名规则下的历史活动重名与结构标量异常。它不会启动 HTTP、连接 Redis、访问磁盘内容、创建命名锁、迁移、改名或删除记录；只需 MYSQL_DSN，不需要 JWT_SECRET。当前轮次仅在 disposable 测试库执行。

## 执行与结果

由有相应读取权限的运维环境设置 MYSQL_DSN，使用编译后的服务端二进制：

```sh
umask 077
./cloudnest audit-file-names > file-name-audit.json
```

报告包含文件名和 ID，应按用户元数据保存。命令采用只读 REPEATABLE READ 事务、两分钟总超时。大库可能超时；查询失败会返回失败，不会生成“零冲突”的假报告。

- 退出 0：本次检查的两个范围没有发现问题。
- 非零且有完整 JSON、conflict_groups 或 invalid_scalar_rows 非零：审计完成且有待处理问题，stderr 明确说明未修改数据。
- 非零且无完整 JSON：连接、查询、超时或输出失败，不能据此判断数据没有冲突。

JSON 字段：

| 字段 | 含义 |
|---|---|
| filename_collation | 当前 file.filename 的真实数据库排序规则 |
| total_files / active_files | 快照中的全部/活动 file 行数 |
| conflict_groups / conflicting_files | 合法标量行中，活动重名组总数/这些组的行数 |
| conflicts | 最多 100 组示例，按父目录、根所有者、最小文件 ID 排序；example_name、entry_count、first_file_id/last_file_id 用于定位，不是完整 ID 清单 |
| root_owner_id | 根目录为所属 user_id；非根为 0，整个 parent_id 共用名称集合 |
| invalid_scalar_rows | user_id 非正或 NULL、parent_id 负或 NULL、is_deleted/is_folder 非 0/1 或 NULL、filename 空或 NULL 的行数 |
| sample_limit / truncated | 示例上限和是否截断；截断不影响两个冲突总数 |

不同所有者的相同根文件名不冲突；同一非根目录即使子项所有者不同仍会冲突。已删除条目不占活动名称。分组直接使用 filename 的实际排序规则，因此数据库认为相等的大小写、重音、尾空格等也按该列规则判断，未用 Go 的字节比较代替。异常标量行单独计数，不把 NULL parent_id 偷换成根目录。

## 已验证与未覆盖

真实 CLI 用例验证：无 JWT/Redis 配置可运行、JSON 与退出状态一致、根隔离、共享父目录、当前排序规则、已删除项排除、NULL 旧字段、重复读取无文件/结构变化。另有 101 组冲突测试，证明 100 组示例截断后总数仍完整。

这是唯一命名约束的预检，不是完整文件系统健康报告。它不检查目录环、缺失父项、逐项 ACL、逻辑名称非法字符、磁盘内容、存储/版本引用、配额，也不承诺可在两分钟内扫描任意规模的库。目录遍历的安全限制在业务路径单独实现。

报告不自动生成修复 SQL，也不授权处理历史数据。出现问题时需要备份、用只读查询定位完整冲突 ID（尤其 truncated=true 时）、审查并批准具体修复，再进行升级。已有独立 [活动文件名约束迁移](FILE-NAME-MIGRATION.md)，必须在审核后显式执行；审计成功本身不会安装约束，不能据此判断数据库已经强制唯一。
