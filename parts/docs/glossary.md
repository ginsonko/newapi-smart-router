# Smart Router Short Glossary

| ID | 术语 | 稳定含义 |
|---|---|---|
| TERM-POLICY | Policy | 每 Key 的路由范围、策略和约束 |
| TERM-CONTRACT | Contract | canonical model、exact endpoint、能力和 ReplayClass 的协议身份 |
| TERM-ROUTE | Route | 真实 channel generation、group、upstream model 和 contract 的组合 |
| TERM-ROUTE-ID | RouteID | 规范化路线身份哈希，贯穿所有阶段 |
| TERM-CATALOG | Catalog | 带 revision 的合同与路线集合 |
| TERM-PRICE | Price snapshot | 带 version 的 RouteID 到有效倍率 PPM 映射 |
| TERM-QUALITY | Quality | 精确路线/合同的共享可用性证据 |
| TERM-CAPACITY | Capacity | 并发、429 和队列状态，不等于健康 |
| TERM-CREDENTIAL | Credential domain | 共享上游授权失败的边界 |
| TERM-FAILURE | Failure domain | 预计共同故障、用于恢复去重的边界 |
| TERM-CACHE | CacheDomain | Prompt Cache 可以共享或延续的真实边界 |
| TERM-AFFINITY | Affinity | 在硬约束通过时保留当前 CacheDomain |
| TERM-TTFT | TTFT | Time To First Token |
| TERM-REPLAY | ReplayClass | 请求在何种提交/受理状态下可以重放 |
| TERM-OUTCOME | Outcome | transport、protocol、semantic、attribution 和 retry 的结构化结果 |
| TERM-COMMIT | Commit state | 响应是否已不可撤销地交给客户端 |
| TERM-RECEIPT | Route Receipt | 冻结实际路线、价格、策略、尝试和账务的收据 |
| TERM-PROBE | Probe | 后台验证未知/故障路线的受控请求 |
| TERM-RECOVERY | Recovery Worker | 持久调度探针、租约、预算和状态推进的进程 |
| TERM-COLD | Cold-start wave | 没有已知可用路线时的有界并发探索 |
| TERM-REVISION | Revision | 单调版本，防止多实例旧状态覆盖新状态 |
| TERM-PARITY | Parity Manifest | 某发行/宿主实际通过能力的机器证据清单 |
| TERM-BRIDGE | Bridge | 宿主进程内的极薄生命周期接入层 |
| TERM-PARTS | Agent Parts Kit | 不可直接运行的开发零件、规格和测试集合 |
| TERM-LITE | Sidecar Lite | 只能提供有限全局代理能力的实验形态 |
| TERM-RECON | Reconciliation | 上游事实已知、账务待幂等协调的状态 |

## 状态词

| 状态 | 含义 |
|---|---|
| `Implemented in reference fork` | 内部参考实现存在，仍需抽离与公开证据 |
| `Release target` | 公开发行必须完成 |
| `Experimental` | 不作为 Stable 承诺 |
| `Not available in this form` | 该形态结构上不能提供 |
| `PASS` | 有当前测试证据 |
| `FAIL` | 行为违反合同 |
| `BLOCKED` | 缺宿主 Hook 或环境条件 |
| `UNKNOWN` | 没有证据，不能当 PASS |
