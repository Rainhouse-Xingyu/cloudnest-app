# 六类检索评估

G04 的真实链路冒烟检查不能证明检索质量。本基准通过真实上传、持久索引 worker、实际 Ollama 和公开搜索 API，对预先标注的 30 个合成文件执行 24 条查询；数据定义见 `cloudnest_go/integration/testdata/retrieval/README.md`。目前新增评估器已通过指标单元测试和带标签编译，首次真实基线尚未执行，不填写效果数值。

## 执行

需要本地 Ollama 的 qwen3-embedding:0.6b、qwen3-vl:4b，以及原生 Poppler。运行只创建全新受守卫检查的 MySQL/Redis 容器，不读取 .env，不操作生产库。

```sh
CLOUDNEST_TEST_POPPLER_BIN=/absolute/native/poppler/bin \
CLOUDNEST_AI_EVAL_REPORT=/absolute/new-report.json \
./scripts/test-ai-retrieval-eval.sh
```

报告路径必须为绝对路径且不存在，避免覆盖既有失败证据。整体 15 分钟上限与原模型冒烟套件的六分钟上限分开；没有放宽生产请求、worker 或解析资源期限。报告逐文件/逐查询保存实际源哈希、模型 digest、Ollama/Go 版本、Git 提交/工作区差异哈希、清单哈希、真实排序/分数/来源/原文片段和耗时。未完成执行标记 completed=false；质量门槛不达标返回失败且保留报告。

正例分别报告宏平均 Recall@1/3/5、Precision@5（分母五）、MRR@10；无答案问题单独报告空结果率，不混入正例宏平均。dev 与 heldout 分别执行预先门槛；所有标签在首次执行前固定。保留集问题对实现者可见，不能称为盲测。后续使用保留集结果修改系统必须明确披露，不能继续把它称为独立验证。

所有返回结果检查用户权限；额外检查共享撤销、文件回收和模型返回 503 时的真实索引文本降级。以上边界检查单独记录，不与正常相关性指标混算。模型身份或相对日历跨月发生变化时本次执行失败。

CI 仅编译 `integration,aimodel,aieval`，不具有本地模型，不能将 CI 绿色解释为质量门槛通过。模型不可用、未支持格式、质量不足均须保留为未完成，G04/G00–G11 不因新增评估器而完成。
