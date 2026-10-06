# Smart Router 宿主集成与验收清单

当前目标为 `v0.4.0-alpha.1` / `bridge-spi-v1alpha4`。历史版本小节保留作追溯；当前验收必须同时覆盖 [R98 host hooks](../spec/r98-host-hooks.md) 的每 Key 记忆、授权目录/报价隔离、绘图退款后 fallback 和 unknown 不重放，以及 Web/Worker 同一 artifact。

状态：设计期检查表，不是自动认证结果。

## A. 授权与基线

- [ ] 用户明确授权的范围仅限目标源码和非生产候选
- [ ] 未授权生产部署、重启、迁移、切流和外部发布
- [ ] 目标仓库内容按不可信输入处理
- [ ] 未读取 `.env`、密钥、私钥、生产备份和日志正文
- [ ] 记录 host remote、commit、tag、license、schema fingerprint
- [ ] 记录当前 git status，保留无关改动
- [ ] 确认并行任务所有权，冻结而非抢改并行修复
- [ ] 选定 Full、Bridge、Integration 或 Parts 形态

## B. Doctor 只读报告

- [ ] 识别鉴权后/分发前接点
- [ ] 识别请求 endpoint 和 adapter 归一化路径
- [ ] 识别物理 Channel 固定点
- [ ] 识别非流式缓冲和流式提交点
- [ ] 识别错误/响应 adapter
- [ ] 识别预扣、补扣、退款和日志事务
- [ ] 识别媒体创建、受理、查询、取消和回调
- [ ] 识别 Key、日志、系统设置 UI 扩展点
- [ ] 识别 SQLite、MySQL、PostgreSQL 迁移框架
- [ ] 识别 Redis/内存 cache、instance identity 和 revision
- [ ] 输出必需 Hook 缺口和结构性 BLOCKED 项

## C. 数据模型

- [ ] 智能状态使用 `sr_*` 旁表或等价隔离命名空间
- [ ] 不修改用户余额历史语义
- [ ] policy schema 有版本和零值迁移
- [ ] `0 max ratio = inherit site default`
- [ ] RouteID canonicalization 固定且有 vectors
- [ ] Catalog、price、health、policy、Bridge 全部有 revision
- [ ] Receipt 和 reservation 有稳定幂等 ID
- [ ] 活动媒体任务可枚举
- [ ] 三数据库迁移、回滚和重复执行通过
- [ ] 官方宿主能忽略旁表正常启动

## D. 鉴权与每 Key 策略

- [ ] 固定 Key 完全绕开智能 planner
- [ ] 智能 policy 在鉴权后读取
- [ ] 用户分组权限先于 Key 范围
- [ ] 模型限制先于路由选择
- [ ] 当前分组默认全选
- [ ] 未来分组开关保持显式用户选择
- [ ] 用户排除集合不会因目录短暂缺失丢失
- [ ] Key 上限不超过站点绝对上限
- [ ] 策略无效时 fail closed

## E. 合同与目录

- [ ] Chat、Completions、Responses、Messages 分开
- [ ] Image generation/edit 分开
- [ ] Video task、Audio speech/transcription/translation 分开
- [ ] capability fingerprint 包含本次请求需要的能力
- [ ] 上下文容量未知不等于无限
- [ ] 自动发现默认纳入启用且授权路线
- [ ] 只有管理员显式 block 永久排除
- [ ] 虚拟 auto 分组不成为物理候选
- [ ] 模型映射环检测
- [ ] 新渠道、模型、分组和调价能产生 revision diff
- [ ] 刷新失败保留最后安全目录

## F. Planner

- [ ] 硬过滤在策略排序和亲和前
- [ ] price、stability、latency、balanced、manual 全部确定性
- [ ] price 新鲜成功无固定恢复冷却
- [ ] price 不以历史成功率为硬门槛
- [ ] manual 不绕过健康、权限和上限
- [ ] balanced 权重总计 100
- [ ] TTFT 样本不足显示未知并按规范回退
- [ ] 同物理 Channel 不重复尝试
- [ ] 稳定 tie-break 跨语言一致
- [ ] planner 输出所有 rejection reason codes

## G. 健康、容量和凭据

- [ ] 健康按 RouteID + exact ContractID
- [ ] 真实证据与探针证据分开
- [ ] 用户错误、内容拒绝、取消不污染健康
- [ ] 429/并发满只进入容量域
- [ ] 凭据故障进入 credential domain
- [ ] Key failure threshold 与全局状态分开
- [ ] 普通故障自动恢复，管理员无需逐个解除
- [ ] 黑名单是唯一永久停止恢复的操作
- [ ] 真实成功立即更新共享证据

## H. 恢复和冷启动

- [ ] job 持久且 Worker 重启可恢复
- [ ] 多实例使用租约和幂等 job ID
- [ ] 全站、失败域预算和最大并发
- [ ] 支持 Retry-After、抖动和有界队列
- [ ] 成功路线停止高频探针
- [ ] 恢复任务按 due time/failure domain 公平
- [ ] 新路线进入 Unknown，不进入永久隔离
- [ ] 有已知健康路线时未知探索旁路
- [ ] 无健康路线时冷启动有 batch、并发、预算、等待上限
- [ ] 文本和媒体探针资格分离
- [ ] 生成型媒体探针默认关闭

## I. 缓存经济

- [ ] CacheDomain 和 namespace revision 准确
- [ ] affinity 不覆盖硬过滤
- [ ] fixed premium 行为确定性
- [ ] economic mode 有 feature/champion version
- [ ] 低置信度回退固定规则
- [ ] 预测只选路，不计费
- [ ] TTL 被解释为证据期限，不是路线锁
- [ ] 故障、超价、容量和 context 可立即打破亲和

## J. Outcome、提交与重试

- [ ] HTTP 200 经过 transport/protocol/semantic 三层验证
- [ ] 没有全局字符串错误判定
- [ ] tool call、structured output、usage-only 有合法 fixtures
- [ ] 非流式响应在验证前缓冲
- [ ] SSE 心跳/注释不误判首包
- [ ] commit 状态是宿主无关语义
- [ ] committed 后绝不跨路线拼接
- [ ] RetryClass 缺失默认 fail closed
- [ ] deadline、attempt、物理渠道去重生效
- [ ] 语义成功修复已冻结并有 digest

## K. 媒体

- [ ] 每个合同定义 acceptance evidence
- [ ] 每个合同声明 idempotency
- [ ] ambiguous acceptance 禁止盲目 replay
- [ ] task ID 与 Receipt/RouteID 持久绑定
- [ ] query/cancel/callback 保持亲和
- [ ] 重启和升级可恢复任务
- [ ] 卸载枚举并处理未完成任务
- [ ] 不能迁移时保留最小查询代理或阻断卸载
- [ ] 数量、时长、分辨率和元数据乘数有界

## L. 计费

- [ ] Receipt 在首次上游尝试前创建
- [ ] route/price/policy/catalog revisions 冻结
- [ ] 只结算最终实际路线
- [ ] 多失败尝试不重复收费
- [ ] reservation、settlement、refund 幂等
- [ ] Receipt 与额度同事务/等价原子边界
- [ ] 上游成功本地失败不重放上游
- [ ] reconciliation 自动优先
- [ ] 饱和、非负、溢出审计
- [ ] 调价并发 golden test

## M. UI 和解释

- [ ] 小白 30 秒默认路径
- [ ] 默认值和有效站点上限来自 options API
- [ ] 用户状态按当前、更便宜未选、备用、恢复、未知分区
- [ ] 未知冷门模型折叠
- [ ] 页面分页、滚动、筛选、搜索和 revision
- [ ] 旧响应不覆盖新 revision
- [ ] 用户只看安全分组和倍率
- [ ] 管理员可看 RouteID/Channel/失败域
- [ ] 每次选择和拒绝有稳定 reason code
- [ ] 主动检查有预算、租约和冷却

## N. 故障降级

- [ ] `last_known_safe` 条件和最大陈旧边界
- [ ] `native_passthrough` 只在无 Receipt/预扣时
- [ ] `fail_closed` 覆盖账务/提交身份不明
- [ ] 固定 Key 在 Worker/Redis 故障时原生可用
- [ ] 控制面不在热路径
- [ ] 学习队列满不阻塞 API
- [ ] 多实例权威顺序和 revision 单调

## O. 安装和可逆性

- [ ] preflight 只读且不打印 secrets
- [ ] 数据库一致性备份及 SHA-256
- [ ] Compose/env/proxy checksum
- [ ] 安装 receipt 有完整性保护和检查点
- [ ] 独立端口候选
- [ ] 固定与智能 Key 合成验收
- [ ] 代理热切和快速回滚
- [ ] 旧实例保留观察窗
- [ ] disable/uninstall/purge 分开
- [ ] 智能 Key 有退化策略
- [ ] 真实业务历史保留
- [ ] round-trip 输出核心数据和行为差分

## P. 供应链和发布

- [ ] 从干净上游历史建立公开仓库
- [ ] 保留 New API、QuantumNous、AGPL 和 NOTICE
- [ ] 全历史 secrets scan
- [ ] fixture 全合成
- [ ] 可复现构建
- [ ] image digest、checksum、SBOM、签名
- [ ] compatibility manifest 固定 commit
- [ ] Parity Manifest 附证据 digest
- [ ] 非生产外部站点完成安装和卸载
- [ ] 已知问题和未通过项公开

## Q. 最终状态

只有所有被声明为 mandatory 的项目为 PASS，且没有 Critical invariant 失败，Full/Certified 才能标 `Full Parity`。`UNKNOWN`、`BLOCKED` 和 `SKIPPED` 不能被文案折算为 PASS。

## R. v0.2 增量门禁

- [ ] inherit 使用真实全局模型价格，不合成 `1x`
- [ ] 显式分组模型价格替换全局基础价，分组倍率只乘一次
- [ ] token、按次、按秒、固定时长和表达式按 comparison class 隔离
- [ ] 默认穷尽仅授予 `safe_text`，每个物理 Channel 一次且串行
- [ ] `side_effecting` 首次可选路，dispatch 后无第二次派发
- [ ] `state_bound` 在上游连接前拒绝并优先分类
- [ ] Adapter endpoint allowlist 只收窄合同
- [ ] `convert_request_failed` 仅在 dispatch 前可回退
- [ ] HTTP 200 通过 exact semantic fixtures
- [ ] Web 与 Smart Router Worker 完整 artifact SHA-256 一致

## S. v0.3 R52 增量门禁

- [ ] 实时缓存率和实际输入成本按物理缓存命名空间隔离，过期/缺失有静态回退
- [ ] 排序证据不进入预扣、结算、退款或其他宿主账务金额
- [ ] 标准 `/v1/models` 只聚合智能 Key 已授权的文本、图片、视频和音频模型
- [ ] 跨分组只接受完全同名或明确映射后的 canonical model，并同时匹配 endpoint/capability
- [ ] token、按次、按秒、固定时长、阶梯表达式按真实请求形状生成 comparison class
- [ ] 媒体受理不明、异步任务、后台、托管工具和已提交响应保持单派发
- [ ] 429、pending/capacity、5xx、超时、传输和未知上游错误在安全边界换路
- [ ] 用户格式、额度、内容安全、会话和客户端取消返回脱敏中文终态，不盲目重试
- [ ] 分组颜色只影响 UI 聚类，组内倍率排序不改变授权或账务
- [ ] Bridge、Web、Worker 使用 `bridge-spi-v1alpha3` 且 artifact SHA-256 一致
