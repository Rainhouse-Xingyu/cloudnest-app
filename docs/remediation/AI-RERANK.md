# 有界重排与验证边界

2026-10-29：v1 六类实际评估中，电脑发票的 Recall@5 只有 0.75，采购合同和评测可排在已付款票据之前。新增可选重排阶段；完整质量验收仍在进行，不能据协议测试宣布 G04 完成。

## 实现

`AI_RERANK_MODEL` 默认为空，保留原混合顺序。显式配置已安装的 Qwen 兼容二分类模型后，成功取得查询向量且候选超过一条时，对已授权、已复核的前 20 个文件逐对评分。模型看到实际文件名和选中块正文；日期已由 SQL 过滤的套话使用与查询向量相同的分离规则。未评分尾部保留原顺序，200 条候选窗口及分页总数不因重排而缩小。

模型调用要求真实 yes/no 两个 token 的 logprob；缺少任一项、非法字节/分数、错误模型身份、未完成或多 token 输出均拒绝。使用稳定二项归一化，仅作为当前查询内的排序分数，不是经过校准的相关概率。输入控制 token 转义；不把文件内容或模型输出作为授权指令。

重排整批共六秒，与 OCR/图片分析共享两个无队列推理名额，嵌入原搜索二十秒总期限。成功后确认模型 digest；任意失败不发布部分分数，保持原 RRF 顺序并返回 `rerank_unavailable` 与有限的失败原因。请求自身取消/超时返回错误。无论成功还是回退，推理后再次复核所有候选的权限、文件身份、源及索引代次。

公开响应保留 `retrieval.mode=hybrid` 和原 RRF `score`。新增 `retrieval.rerank`：status（disabled/skipped/applied/fallback）、model、model_digest、evaluated_count、window_size、window_limited、failure_reason；仅成功评分的项新增 `rerank_score` 和 `match_sources=rerank`。没有统一 0.5 阈值，也没有移除低分结果。

## 已执行与未执行

- Go provider/service/config race 单元通过；覆盖严格协议、身份变化、整批失败、共享名额、请求取消、20 条排序和尾部保留、再次验证。完整构建/vet/race 单元通过，日志 `/tmp/cloudnest-rerank-check.log`。
- 真实一次性 MySQL/Redis 检索专项通过，包 51.615s，日志 `/tmp/cloudnest-rerank-protocol.log`。新增模型响应期间撤权、撤权后模型失败、重命名、源替换、索引代次变化五个子用例，均走公开 API 与实际 SQL；模型分数是协议夹具，不能当作效果证据。
- 全标签评估器编译及分页契约通过（0.726s）。实际质量报告会保留每条查询的重排元数据；配置了重排却发生 fallback 时，质量运行失败，不能以基础检索成功冒充重排验收。
- [首轮 18 对文本预检](AI-RERANK-OLLAMA-PREFLIGHT-20261029.json) 与 [固定当前提示/16384 context 的第二轮](AI-RERANK-OLLAMA-PREFLIGHT-V2-20261029.json) 使用实际 qwen3-vl:4b。第二轮 completed=true，18 对总推理 17.025s；首对冷调用 6.590s，说明六秒预算可能回退。合同正例分数约 0.446/0.160，不能统一用 0.5 过滤。仅三条开发查询×六段文本，不覆盖图片、完整候选或 API 效果；首轮提示脚本在运行后修改过转义规则，第二轮记录最终脚本哈希。
- 第二轮预检启动前曾因本地 Ollama 不在监听且 urllib 继承系统代理而得到 503，未进入评分；确认监听缺失后重启本地服务，预检固定禁用 loopback 代理，再完整执行。没有修改系统代理。
- 官方 Qwen3-Reranker-0.6B 固定 revision 的权重下载曾因连接中断终止，已从缓存断点恢复。尚未完成官方权重推理，不能把已存在的预检脚本称为已验证的专用模型结果。

## 复现与剩余工作

完整实际 API 评估在原 `scripts/test-ai-retrieval-eval.sh` 基础上增加 `CLOUDNEST_AI_EVAL_RERANK_MODEL=已安装模型名`；仍需独立新报告路径、原生 Poppler 和本地模型。它重新真实上传并索引 30 个文件；不改变标签/门槛，v1 保留集早已暴露，只证明回归。

预检脚本 `scripts/evaluate-ollama-rerank-preflight.py --output /absolute/new.json` 只访问本机；`evaluate-reranker-preflight.py` 为待执行的官方 Safetensors 本地实验，固定 revision 和权重 SHA，拒绝远程代码及截断。都不操作生产数据。

待完成：实际全量重排效果/延迟、专用模型评估、复杂语义与低相关项处理、全新独立语料、格式覆盖和资源压力。重排只看到每文件融合选择的一块，不能声称是完整长文交叉编码；候选召回之外的文件也不能由重排找回。

协议依据：[Qwen 官方模型](https://huggingface.co/Qwen/Qwen3-Reranker-0.6B)、[Ollama 0.31.1 API 类型](https://github.com/ollama/ollama/blob/v0.31.1/api/types.go)。实际兼容性与质量以报告为准。
