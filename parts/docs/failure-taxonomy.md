# Smart Router Failure Taxonomy

状态：规范设计词汇。Adapter 必须输出稳定分类，不能只返回一段错误字符串。

## 1. 分类维度

每次 attempt 至少回答：

- 传输是否完整；
- 协议是否有效；
- 业务语义是否成功；
- 错误归因于谁；
- 是否已经提交给客户端；
- 媒体是否已经受理；
- 当前 ReplayClass 是否允许换路；
- 是否更新健康、容量或凭据域；
- 是否进入账务协调。

## 2. 成功

### `success`

条件：transport、protocol、semantic 三层通过。

影响：

- 真实请求更新 real success；
- 探针只更新 probe evidence；
- 清理当前恢复周期；
- 最终成功路线结算；
- 不重试。

## 3. 路线基础设施故障

### `infrastructure_failure`

示例：连接失败、上游超时、网关 5xx、协议截断、合同判定的空 200/伪 200。

条件：可归因于精确路线/合同，而非用户、容量或客户端。

影响：更新路线 health；未提交且 ReplayClass 允许时可换下一物理渠道。

### `contract_not_supported`

示例：模型只支持 Chat，本次是 Responses；adapter 不支持图片编辑。

影响：目录/合同问题，不应把整个渠道全局判坏；触发目录诊断。

### `model_mapping_invalid`

示例：上游模型不存在、映射循环、能力元数据错误。

影响：Route/Contract 级失败；需要目录刷新或管理员修复。

## 4. 凭据故障

### `credential_failure`

示例：上游 Key 失效、授权被撤销、组织身份错误。

影响：更新 credential domain；共享该凭据的路线暂时阻断。不要污染无关凭据。

## 5. 容量

### `capacity_limited`

示例：429、并发槽满、上游明确 busy、站点 inflight 限制。

影响：只更新 capacity domain；可短等、同缓存域替代或升档；不降低健康可靠性。

### `queue_deadline_exceeded`

影响：结束等待并尝试下一合格路线；不污染健康。

## 6. 用户和策略

### `user_rejected`

示例：请求参数错误、模型限制、IP 限制、额度不足、倍率上限导致无候选。

影响：不污染路线健康。错误应向用户说明可行动原因。

### `content_rejected`

示例：上游按协议正常返回内容安全拒绝。

影响：通常不换路、不污染健康。若产品明确允许策略性再选，必须是单独合同而非伪装基础设施失败。

### `client_cancelled`

影响：释放容量和预留，保留已发生的合法 usage；不污染健康。

## 7. 提交和流式

### `failure_before_commit`

条件：尚未向客户端提交合法业务内容。

影响：结合 attribution 与 ReplayClass 判断是否换路。

### `failure_after_commit`

条件：已经 committed。

影响：禁止跨路线重放和拼接；记录终止，按已知 usage/账务合同处理。

### `protocol_terminated_without_semantic_result`

示例：只有心跳/usage，缺少合同要求的结果和合法终止。

影响：由 OutcomeValidator 决定是否属于 retryable infrastructure failure。

## 8. 媒体受理

### `media_not_accepted`

有明确拒绝证据。未提交且 ReplayClass 允许时可换路。

### `media_accepted`

有任务 ID、幂等确认或最终资产。禁止盲目重新创建；绑定查询亲和。

### `media_acceptance_ambiguous`

连接超时或响应损坏，无法证明是否创建任务。禁止跨路线重放，进入查询/协调。

### `media_duplicate_detected`

使用幂等键或上游 task identity 合并结果，不重复结算。

## 9. 宿主和账务

### `host_persistence_failure`

上游尚未开始：可以 fail closed 并释放预留。

上游已经成功：不得重放，写入 `reconciliation_required`。

### `billing_overflow_or_invalid_multiplier`

影响：fail closed、审计、不得产生负费用或信用。

### `reconciliation_required`

已知上游事实与本地账务事务尚未一致。自动协调优先，必要时 operator review。

## 10. 探针

### `probe_success`

证明当前一次可达，更新 probe evidence 和恢复状态，不计入 real SLA。

### `probe_failure`

更新 probe failure 和 next-attempt，不直接制造用户账单。仍受预算、租约和 Retry-After。

### `probe_ineligible`

媒体生成、副作用合同、缺少安全最小请求或上游禁止探测。保持未知，通过真实业务受控探索。

## 11. 决策映射表

| 分类 | 健康 | 容量 | 凭据 | 可换路 | 账务 |
|---|---|---|---|---|---|
| success real | 提升 | 可释放 | 可清理 | 否 | 结算 |
| success probe | 恢复证据 | 无 | 可清理 | 不适用 | 独立探针记录 |
| infrastructure failure | 降低 | 无 | 无 | 首包前按 ReplayClass | 释放/协调 |
| credential failure | 路线可见但凭据阻断 | 无 | 阻断 | 首包前可 | 释放/协调 |
| capacity limited | 不变 | 更新 | 无 | 可升档 | 释放当前尝试 |
| user/content/client | 不变 | 释放 | 无 | 通常否 | 按合同 |
| media ambiguous | 不擅自降为普通失败 | 释放 | 视证据 | 否 | 协调 |
| failure after commit | 按归因记录 | 释放 | 视归因 | 否 | 按已提交事实 |

## 12. 禁止实现

- 从 HTTP status 直接映射所有语义；
- 从错误 message 全局 substring 推断；
- 将 429 归为 infrastructure health failure；
- 将探针成功累计到 real reliability；
- 将客户端取消归为上游故障；
- 将媒体超时一律当作 not accepted；
- 在 committed 后返回到 planner 选择新路线。

## 13. v0.2 新增判定

### `convert_request_failed`

仅当请求转换在 upstream dispatch 前失败时，才可按 exact contract 选择另一路线。dispatch 已开始时不得借此 reason code 重放。

### `semantic_http_200_invalid`

HTTP 200 的 transport 可正常，但 protocol 或业务语义无有效结果。保持未提交时可按 ReplayClass 处理；已提交、已受理或受理不明时禁止跨路线。

### `state_bound_pre_dispatch_rejected`

请求含 `previous_response_id`、conversation 或上游绑定文件。连接上游前拒绝智能选路，不污染渠道健康。

### `side_effecting_dispatch_fenced`

`store:true`、`background:true` 或托管工具已开始派发。当前失败可以记录和结算/退款，但不能选择第二个渠道。

### `route_price_incomparable`

价格缺失或计费单位/表达式不同，无法可靠静态比较。不得用默认 `1x` 或数值大小猜测更便宜路线。

### `runtime_revision_mismatch`

Web 与 Smart Router Worker 二进制或协议 revision 不同。阻断发布/Worker 接管，避免旧合同写入新版共享状态。
