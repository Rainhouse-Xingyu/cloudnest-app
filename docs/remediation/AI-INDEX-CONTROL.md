# 索引状态、历史回填与重建 API

这些接口操作 `ai_index_job` 持久队列，不同步等待模型，也不改变旧 `ai_task` 历史。所有接口须登录并通过现有 API 权限策略；任务状态不能代表整个 G04 已验收。

## 查看状态

`GET /api/ai/index?after_id=0&limit=50` 按 file_id 升序分页列出**当前用户拥有的**活动非目录文件，包括尚无任务的历史文件。limit 为 1–100，默认 50；响应为 `items`、`next_after_id`、`has_more`。没有任务时 generation=0、status=not_indexed。此游标是遍历边界，不是跨请求的数据库快照：并发删除可使行消失，新增高 ID 文件会在后续页出现。未返回他人的共享文件；已知共享文件可使用单项状态接口。

`GET /api/ai/index/:id` 支持所有者及显式 read/write 文件授权；不存在、已回收、目录、未授权统一返回 404，无管理员资源级旁路。

每项包含 file_id、filename、generation、status、attempts、error_code、available_at、updated_at、source_current、indexed_current、chunk_count、indexed_at。不返回租约 token、租约截止时间、源 hash、文件路径、正文或向量。

- `source_current` 表示任务的源 hash 和 pipeline 与当前文件一致。
- `indexed_current` 仅在任务 done 且 document 的源、代次和 pipeline 匹配时为 true。陈旧 document 不暴露块数或索引时间。
- status 是任务状态，不把 `done` 独立解释为当前可搜索；例如历史写入未更新任务时，可能为 done 但 source_current/indexed_current=false。
- 错误码使用队列的固定诊断码，如 model_unavailable、extraction_limit、unsupported_type、source_integrity、attempts_exhausted。重试遵循既有最多五次及有界退避；此接口不会隐藏失败或写入假结果。

## 重建与失败重试

`POST /api/ai/index/rebuild`，JSON 示例：

```json
{"items":[{"file_id":123,"generation":0},{"file_id":456,"generation":2}]}
```

items 必须为 1–100 个唯一、正 file_id；generation 为状态接口观察到的非负代次。只允许文件所有者重建，显式共享写权限不授予索引调度权。请求体上限 16 KiB，数据库操作期限 10 秒。

成功返回 202、status=queued 和每个 file_id 的新 generation。这表示入队成功，**不表示模型处理已经完成**。客户端随后轮询状态。不存在任务时以 generation=0 创建第一代；已存在任务时创建下一代，并清除尝试次数/错误/租约，使旧 worker 无法发布。此能力也用于 failed/retry 的手动重试和 running 的显式重建。

请求按 file_id 排序，在同一事务中先锁定并核验所有文件所有权，再按顺序锁任务、检查 generation、入队并写审计。任意无权/已删除文件返回 404；任意过期代次或不可用源返回 409，整批回滚。审计失败同样回滚，返回 503。非法请求返回 400。重复提交旧代次返回 409，不会反复推进代次；客户端须重新查询状态，不能盲目循环重试 409。

历史回填流程：按游标列出文件，选择 not_indexed 或 source_current=false 的项，使用返回的 generation 批量提交；等待或继续下一页。显式全面重建也使用同样流程，对所有所需项提交当前 generation。当前无需新迁移表，不提供单请求无限扫描全库或代替其他用户重建的接口。未知/不支持的文件格式仍由 worker 返回明确 unsupported_type，不以重建成功冒充格式支持。

## 验证范围

`scripts/test-ai-search.sh` 包含检索六项及索引控制五项专项，使用独立一次性 MySQL/Redis。控制测试覆盖未登录/跨用户/撤权、共享可读但不可重建、错误诊断、历史游标、过期代次及混合所有权整批回滚、八路并发仅一次成功、旧 worker 被隔离、审计失败回滚、请求边界、源变更后状态失效。

真实模型套件另行使用公开重建 API → 独立生产服务 → 实际 Ollama embedding → 状态 API，验证重建完成。实际执行结果见 [TESTING](TESTING.md)；CI 编译但不执行需要本机模型的套件。

后续已接入 [版本/复制/恢复/扩展名变化的增量入队](AI-INDEX-INCREMENTAL.md)。PDF/Office/图片提取、照片拍摄时间或完整检索效果评估继续纳入 G04。
