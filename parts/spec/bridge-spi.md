# NewAPI Smart Router Bridge SPI

状态：`Normative design contract`  
版本：`bridge-spi-v1-design`  
运行边界：本文定义宿主接点，不证明任何宿主已经实现或认证  

## 1. 目的

Bridge 是宿主进程中的极薄适配层。它把鉴权、精确物理路由、响应提交、账务事务、媒体任务和原生日志的生命周期事件交给版本无关的智能路由核心。

Bridge 不承载目录刷新、探针调度、在线学习或复杂 UI。控制面故障时，数据面使用版本化本地快照或明确降级。

## 2. 协议版本协商

启动时交换：

```text
host_commit
host_schema_fingerprint
bridge_protocol_version
core_protocol_version
policy_schema_versions
route_id_schema_versions
receipt_schema_versions
supported_hooks
supported_endpoint_contracts
supported_replay_classes
```

规则：

- 未知主版本 fail closed；
- 缺少强制 Hook 时不能声明 Full Parity；
- 旧实例不能发布低于当前持久 revision 的快照；
- 兼容矩阵使用精确 commit/digest，不接受浮动 `main` 或 `latest`；
- Bridge off 时固定 Key 必须恢复宿主原生行为。

## 3. 数据面阶段

```text
AUTHENTICATED
  -> CONTRACT_NORMALIZED
  -> PLAN_FROZEN
  -> QUOTA_RESERVED
  -> ATTEMPT_STARTED
  -> SEMANTICALLY_VALIDATED
  -> RESPONSE_COMMITTED
  -> SETTLED
```

失败可以进入 `RETRY_ELIGIBLE`、`RECONCILIATION_REQUIRED` 或 `TERMINAL`。每个迁移都带 request ID、Receipt ID、revision 和幂等键。

## 4. Hook 清单

### HOOK-AUTH-001: Post-auth Key policy

调用时机：宿主已经验证 Key、用户、IP、模型限制和额度身份后，任何渠道选择前。

输入：

```text
request_id
user_id
token_id
token_group
token_routing_policy_raw
authorized_groups
model_limits
quota_identity
viewer_role
```

输出：

```text
mode: fixed | smart
normalized_policy
effective_site_limits
policy_hash
```

约束：

- 不向 core 提供 API Key 正文；
- v4 policy 的 `max_effective_ratio_ppm=0` 在这里解析为站点默认有效值；
- Key 上限不能超过站点绝对上限；
- 固定 Key 不进入智能 planner；
- 策略解析失败时拒绝智能请求，不悄悄扩大范围。

### HOOK-CONTRACT-001: Request contract normalization

调用时机：鉴权后、读取足够请求元数据后、任何候选发现前。

输入：路由路径、HTTP 方法、规范化模型、stream、tools、vision、structured output、reasoning、媒体模式、上下文需求、参考资产和 adapter 类型。

输出：符合 `route-contract.schema.json` 的合同、ReplayClass 和最小能力位。

约束：

- Chat、Responses、Messages、Image、Video 和 Audio 分开；
- 不通过模型名猜 endpoint；
- 未识别合同 fail closed；
- 请求 body 只保留规划必需元数据，敏感正文不进入控制面；
- 上下文需求和媒体乘数先做边界验证。

### HOOK-ROUTE-001: Exact physical route injection

调用时机：planner 已返回候选、每次上游尝试开始前。

输入：RouteID、ChannelID、channel generation、Group、upstream model、exact endpoint、capability revision、attempt index。

输出：宿主 relay context 已固定到该物理路线。

约束：

- 原分发器不得再次随机选 Channel；
- billing/logging 读取同一固定对象；
- 本请求记录已尝试 ChannelID/credential domain；
- 路线 generation 与当前渠道不一致时拒绝执行并重新计划新请求；
- 不把内部路线返回普通用户。

### HOOK-COMMIT-001: Semantic response commit

目标：暴露“是否仍可安全换路”，而不是某个 Web 框架是否写过字节。

状态：

```text
buffered
semantically_validated
committed
terminal
```

宿主必须提供：

- 非流式结果在完整验证前保持内部缓冲；
- 流式结果在第一个合法业务事件前允许终止当前尝试；
- 心跳、SSE 注释、usage-only 块不自动视为语义提交；
- 一旦客户端不可撤销地看到业务内容，状态变为 committed；
- committed 后的错误只结束当前流，不跨路线拼接。

### HOOK-OUTCOME-001: Adapter outcome validation

输入：精确合同、传输状态、协议事件、终止原因、工具调用、结构化结果、任务受理和脱敏错误。

输出：符合 `outcome.schema.json` 的结构。

约束：

- HTTP 200 不是完整成功；
- 不使用一个全局关键词判断所有协议；
- 用户错误、内容拒绝、容量和客户端取消正确归因；
- probe 标志与真实请求分离；
- adapter evidence 脱敏、有界且可测试；
- 语义成功公开前必须冻结 golden fixtures。

### HOOK-BILL-001: Atomic route receipt billing

调用时机：首次尝试前预留，最终结果后结算/释放。

操作：

```text
Reserve(receipt, estimate)
MarkUpstreamStarted(receipt_id, attempt)
RecordOutcome(receipt_id, outcome, usage)
Settle(receipt_id, actual_usage)
Refund(receipt_id, amount_or_reservation)
MarkReconciliationRequired(receipt_id, reason)
```

约束：

- Receipt 和额度变更在宿主主数据库同一事务或经证明等价的原子边界；
- 所有操作幂等；
- 冻结 price version 和 effective ratio；
- 上游成功而本地事务失败时不重放上游；
- 溢出、NaN、Inf、负数 fail closed 并审计；
- 协调 Worker 优先自动修复已知结果。

### HOOK-LOG-001: Role-safe log enrichment

输入：plan、rejections、attempts、Outcome、Receipt、viewer role。

用户字段：模型、用户可见分组、实际倍率、策略、切换次数、友好原因、证据时间。

管理员字段：RouteID、合同、失败域、revision、内部 ChannelID、探针/真实证据、协调状态。

约束：用户永远看不到上游凭据、私有 URL、管理员注释或完整错误 body。

### HOOK-TASK-001: Media acceptance and affinity

适用：图片、视频、音频和其他有副作用/异步任务。

宿主必须提供：

- 幂等键能力；
- acceptance state：not accepted / accepted / unknown；
- 上游 task ID 与 Receipt/RouteID 持久映射；
- query/cancel/callback affinity；
- 升级和实例切换后可恢复；
- 卸载前活动任务枚举；
- 无法迁移时的最小查询代理。

受理不明时禁止盲目跨路线重放。

### HOOK-UI-001: Native entry points

宿主 UI 只需提供：

- Key 创建/编辑中的固定与智能模式；
- 用户日志里的实际路线入口；
- 用户状态页入口；
- 管理员目录、健康和配置入口。

复杂页面可以独立部署，但会话、RBAC、CSRF 和主题跳转必须正确。

## 5. 快照接口

Bridge 热路径消费不可变快照：

```text
CatalogSnapshot(version, source_revision)
PriceSnapshot(version, user_group)
QualitySnapshot(version)
CapacitySnapshot(version_or_epoch)
CredentialSnapshot(version)
PolicySnapshot(version, hash)
CacheEconomySnapshot(feature_version, champion_version)
```

规则：

- 快照在请求开始时冻结；
- 新 revision 只影响后续请求；
- 本地读取不发控制面网络 RPC；
- 刷新失败保留最后安全版本；
- 账务身份不明时不使用过期快照猜测。

## 6. 降级接口

Bridge 暴露：

```text
last_known_safe
native_passthrough
fail_closed
```

`native_passthrough` 只在没有 Receipt、没有预扣且原生路径不改变账务语义时允许。活动媒体、已预扣或已开始上游请求不能无条件 passthrough。

## 7. Bridge off 差分

必须证明：

- 固定 Key 的渠道选择与官方宿主一致；
- 用户、额度、渠道、Options 和日志无额外变更；
- 智能 UI 隐藏或按退化策略处理；
- Worker 停止不会影响固定 API；
- `sr_*` 旁表可被官方镜像忽略；
- 不存在遗留 middleware 改写固定请求。

## 8. 测试接点

每个 Hook 至少提供：

- contract test；
- failure injection；
- idempotency test；
- multi-instance/revision test；
- role redaction test；
- bridge-off differential test；
- SQLite、MySQL、PostgreSQL 适用测试。

## 9. 不兼容判定

出现以下任一项时 doctor 报 `BLOCKED`：

- 无法在鉴权后、分发前读取 Key policy；
- 无法固定物理 Channel；
- 无法判断语义提交；
- 无法把 Receipt 和额度置于原子边界；
- 媒体受理和任务亲和不可观察；
- 宿主版本未知且核心路径已魔改；
- 迁移不能保证三数据库或安全回滚。

可以降级为部分能力，但不能继续声称 Full Parity。
