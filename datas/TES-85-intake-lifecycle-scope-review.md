建议将受认证 intake 适配器及 workflow 所属 issue 的生命周期保护，作为两个明确的新增范围交队长核定；两份现有补丁均不建议原样落地。intake 是新增产品入口，不能以“补测试类型”自动批准；issue 补丁有校验前取消、批量预检写入、状态语义及并发遗漏，须先修订。以下是允许范围建议与验收设计，不是落地授权或 P1 通过证明。

**证据边界**

- 实际读取附件 `TES-85-r4-source-evidence.zip`（ID `01a0a8fa-e949-7671-af16-2c3c988fb0c5`），SHA256 `7136870f384a973d2f47a60dc05d8e2ce4fe83102785032fb79a890b9d8e468c`；内层 manifest 的 68 项摘要全部匹配。这只证明包内一致性，不等于执行结果复验。
- 审查对象为 `proposal/NOT-APPLIED-intake.patch`（SHA256 `3c08ab863b495838bd4e1b70cbb5c13f6e33250b50d794d93c4350d7979a0bb4`）及 `proposal/NOT-APPLIED-issue-lifecycle.patch`（`1a3595e3d85f8d87b95c1c966c4d44de22c2adeb2cdee754642f560e4239228e`）。结合包内 `P1-from-upstream.patch` 和固定上游 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 的 issue 实现分析。
- 包中 P1 标识为 `122a8fd72e1252316aa1939a6ecc44b95212de81`，P0 为 `0ca4c63c0dd02e00c2fdacc9d8c4f480e291f815`。本地无该 P1 Git 对象，本轮没有导入 bundle；不把当前包含更多能力的工作树当作此候选。
- `p1-compile-all.log` 确有缺失 WorkflowIntake / WorkflowIntakeResponse 的编译错误；issue 生命周期缺口仍是静态发现。本轮未应用补丁、编译、运行数据库/服务或重演失败用例，不复验 P0、TES-88、162 项盘点或权限抽取。

**逐文件最小范围建议**

| 文件 | 可纳入的最小差异 | 不随之纳入 |
|---|---|---|
| `server/internal/handler/workflow_intake.go` | 请求/响应类型、输入校验、已发布模板解析、受认证调用主体、既有 StartRun 适配、稳定回执及幂等重放；明确拒绝被排除配置；补齐编码/hash 错误检查和 gofmt | 匿名 webhook、签名密钥、第三方连接器、URL 抓取、独立 receipt 表、实例/debug/callback 管理与发送 |
| `server/internal/handler/issue.go` | 仅 UpdateIssue / BatchUpdateIssues 的 workflow 状态判定与取消协调，必要 import/helper，复用既有权限、目录状态、CAS、文本合并和提交后通知；修订下述事务与并发语义 | Issue Pool、列表/筛选、删除/指派重构、普通 issue 的全局自动停任务规则；禁止整份覆盖来源文件 |
| `server/cmd/server/router.go`（既有 P1 范围） | 如果队长接受 intake 能力，只新增 `POST /api/workspaces/{id}/workflow-intake` 的受认证接线，复用实际 workspace/任务身份中间件；验证路径 workspace 与上下文一致 | 新匿名入口、绕过 task-token 限制的路由或 CLI 特例 |
| `server/internal/handler/workflow_run_test.go` 及聚焦 intake/issue 测试文件 | 保留已有稳定回执、跨入口重放和 issue 生命周期用例；补本报告矩阵 | 删除保留测试、空 handler、重建 callback 表来消除编译错误 |

issue 原子取消可能需要在原已授权的 `internal/workflow/commands.go`、`engine.go` 中抽出可参与调用方事务的取消内核，并由 handler 在提交后统一 flush effects；停止遗漏任务的修复可能涉及既有 `workflow_run.go`、`reconciler.go` 或 TaskService 调用点。它们是具体设计依赖，不是本报告自动批准的新补丁：工程须随 issue 修订提交实际函数级差异，交队长一起核定。不得在 issue.go 复制引擎状态机，也不能只为维持“两文件”表面范围而保留分离事务。现有查询若足够则复用；新增 SQL/生成物需说明必要性，本轮不需要新增表或迁移。

**intake 的产品与权限契约**

197 行提案新增了按 source/event_id 接收事件、按 template_key 定位流程、创建 issue/run、指定负责人并返回 receipt 的能力。相对本轮 P1 候选，这是额外的可调用 API；相对完整旧来源，是裁剪后的入口恢复。建议仅纳入“已认证工作区成员的已发布流程适配器”，保留已有测试要求的跨入口重放，不称为已完成外部系统集成或签名 webhook。若队长不接受该入口，须显式调整 P1 契约并将 intake 专属测试迁往后续阶段；不能无声删测后宣布原范围通过。

1. 路径 workspace、模板、负责人、run/issue 都必须同租户。登录与 membership 是前置；任意 source/event_id/source_url 都只是输入，不是可信身份。source_url 只存储、不访问。发布权限仍沿用现有规则，运行限已发布版本，草稿/归档/无当前版本不能新开 run。
2. 提案默认负责人为认证成员；普通成员只能指定自己，owner/admin 可指派同工作区有效成员。这里的“可指派”不应被解释为可以借被指派人的私有 agent 权限。提案把 ownerID 直接写为 AccountableUserID，路由又以它判定调用权限，因此必须验证“管理员自己不能调用、目标负责人能调用”的案例。建议在支持代办授权证据前，对跨人指派同时满足实际发起者与负责人的现有调用准入；不能仅换 owner_user_id 就扩大权限。此项是新入口授权修订，须在差异中明示，不修改既有共享谓词规则。
3. `requireWorkspaceMember` 不是“真人专用”判定：上游 mat_ 会映射到所属人的 userID，提案却将 CreatorType/ActorType/创建事件全部硬编码 member。应复用可信认证上下文并保持实际 actor/source-task/originator 可追溯；不扩大既有机器身份可访问范围，也不接受客户端自报身份。若现有 StartRun 契约不足以正确承载，应先拒绝无法正确归因的身份并列为限制，不能冒充 member，更不能改 CLI 环境/凭据绕过限制。
4. 保留现有规范化及 key 契约：默认 `external:<规范化source>:SHA256(trim(event_id))`；显式 key 复用 `workflow-start:v1:`，限长沿用当前规则。hash 复用既有算法及规范化业务输入；不能随意加入 source/event 导致已有跨入口重放测试失效。相同 key/相同语义返回原 run/issue；不同模板、负责人或业务输入导致 hash 不同则 409；数据库竞争分支一致。重复请求不改原 source/provenance、不新增通知或任务。旧 NULL hash 仍只证明兼容，不能宣称验证了历史输入一致性。
5. 当前 intake 没有显式版本参数，应明确只支持首次 StartRun 内固定的当前发布版，重放保持原版本。不得静默接受 `X-Workflow-Template-Version-ID` 却忽略它：本次最小做法为有值就明确拒绝；若需要支持，另列同模板、同租户、published 校验和版本参与 hash 的具体差异。并发发布/归档与重放的顺序须测，不能把 handler 预读的 CurrentVersion 当作已固定版本。
6. callback 只保留请求字段用于明确拒绝，非空配置返回 `400 unsupported_workflow_configuration`，零持久化副作用。不导入 callback 查询、engine 参数、表、worker、发送器或指标。应与已发布 run 入口对齐空值规则，避免本提案 trim 后放过空白字符串而另一入口拒绝。intake 还必须识别并拒绝非空顶层 `input_instance_id/input_instance_revision/input_source/execution_mode/debug_session_id` 等已排除控制字段；目前 json.Unmarshal 会忽略这些未知字段，不能把“被忽略后正常执行”算作拒绝。业务 payload 与顶层控制参数分开，不把 payload 中同名普通字段偷偷升级成执行配置。

**issue 补丁的静态阻断项及修订契约**

| 所见问题 | 依据与后果 | 必要修订 |
|---|---|---|
| 单项取消早于全部验证 | 新 hook 在 status 解析处；固定上游其后才校验 priority、assignee、日期、parent/project、附件，并在写入处校验 expected_revision/文本冲突。CancelRun 自行提交，之后可返回 400/403/409/500 | 所有纯校验先完成；CAS/文本及目录竞态在同一事务中验证，不能出现请求拒绝而 run/step/task 已取消 |
| 批量“预检”实际写入 | 循环直接调用有副作用的 guard；后续 issue 查询/取消失败或逐项字段校验 skip，会保留前面的取消。查询失败还被 continue 吞掉 | 第一遍只读检查、去重、校验，不得取消；workflow 状态命令需在统一事务中复核目标并原子提交。普通批量更新既有计数/skip 行为不顺带重构 |
| 单项/批量 no-op 不一致 | 单项比较解析 key 与 prevIssue.Status；批量无此比较，对原状态重发仍可报 workflow_run_active | 单项与批量按锁内实际当前值判定无状态变化；不得仅用请求开始时的旧快照放行 |
| 自定义取消状态被误拒绝 | `resolveIssueStatusKeyKind` 返回 entry.Key 和“是否自定义”，不是有效内置状态；如键 `aborted` 继承 cancelled，`target != "cancelled"` 仍成立 | 用既有状态目录解析 effective/category 判定取消语义，保存规范化自定义 key；保留目录共享锁和归档竞态检查 |
| 查询与写入存在竞态 | ListActiveWorkflowRunsForIssue 无锁，issue 写入和每次 CancelRun 分事务；StartRun 会锁 issue，运行推进会锁 run 并投影 issue | 状态判断、issue 更新和 run 取消共享序列化边界，锁内重读 active run；锁顺序必须与 StartRun/终态投影统一，不能简单套 issue→run 锁后忽略现有 run→issue 路径 |
| 停任务可能遗漏 | taskIDs 在 CancelRun 前查询且错误被 `_` 丢弃；该查询与取消间可激活新任务；TaskService=nil 仍可继续成功 | 不吞错误；在禁止后续激活之后再覆盖查询关联活动任务，并有重试/恢复机制。仅停止 step→run 关联任务，不按整个 issue 扫停无关任务 |
| 幂等冲突被当成功 | guard 特判忽略 IsIdempotencyConflict，但当前 CancelRun 对已终态返回成功/no-op，不需要用冲突代表幂等 | 仅确认的既有终态 no-op 视作幂等；其余错误按契约返回，不继续覆写 issue |

建议保持以下可验收行为：

- 无活动 run 的 issue 沿用上游行为。活动集合沿用 SQL 的 pending/running/waiting_acceptance/blocked；完成/失败/取消的历史 run 不永久锁住 issue。
- 活动 run 拥有状态时，非取消的真实状态变更返回 `409 workflow_run_active`，附同租户 run ID；非状态字段仍按上游权限/CAS 更新。规范化后相同状态是 no-op，不触发 run 或任务副作用。
- 取消必须先完成授权和全部请求校验，再在可回滚数据库事务中取消该 issue 的活动 run、非终态 step 和 pending acceptance，并提交 issue 请求的取消状态；事件也在该事务中，通知在提交后。`commands.go` 的 CancelRun 已负责这些工作流行，必须复用它的状态机。
- 特别保留：当前 `IssueStatusForRun` 对 cancelled 返回“不投影”。直接 run cancel 与 issue cancel 不是同一个 issue 状态承诺；issue 入口必须自己完成 issue 更新，不能假定引擎会写 cancelled。不要顺带改变直接 run cancel 的既有状态投影契约。
- 完成先赢得并发时，取消不得把 completed run 改成 cancelled 或由旧快照覆盖新完成状态；重读后按已终态/no-op或冲突处理。取消先赢时，不再激活下一步；迟到完成、重复终态通知不得恢复运行或产生重试任务。
- 数据库原子提交不等于 OS 进程已停止。提交取消意图后，通过 TaskService 停止任务；失败要保留可恢复证据并能重复执行，不能用“终态结果会被丢弃”替代停止。进程退出/子进程树确认由 daemon 验收，不能把一般 200/已 cancelled 当作进程已停证明。缺少必要服务装配时在副作用前拒绝。
- 批量 workflow 状态命令建议全批预检、统一提交；任一受控目标验证失败则整批不写。若工程选择保留部分成功协议，须单独给出逐项结果与产品契约，不能沿用现补丁“全批预检原子”的注释掩盖部分取消。

**必要回归（均为待执行要求）**

| 组 | 必须包含的用例及断言 |
|---|---|
| intake 真路由与权限 | 真实 middleware 下成员、自身/跨人负责人、非成员、跨 workspace/template、无效身份、agent task/终态 task、伪造身份头；管理员代指派不能借目标私有调用权。拒绝时 issue/run/step/task/event/subscriber 均不新增 |
| intake 数据契约 | body 限制、非法 JSON/非对象 payload、长度/空白/无效 URL、编码失败；callback 与其他排除字段的 null/空/非空/错误类型；显式版本输入不得被忽略；draft/archive/current-version 竞争 |
| intake 幂等 | 默认 event key、显式 key、并发同载荷唯一 run/issue/task，异载荷409；manual↔intake 两个方向同 key 重放；模板发布新版本后重放保留原版本和来源；跨工作区隔离；旧 NULL hash 限定兼容；事务故障零残留、通知不早于提交 |
| issue 常规状态 | 无 run、各活动状态、各终态；单项和批量 no-op；大小写/空白别名、自定义 cancelled category、其他自定义状态；非状态更新保持上游行为；目录归档竞态 |
| issue 拒绝零副作用 | `status=cancelled` 搭配非法 priority/date/assignee/parent/附件、过期 expected_revision、文本冲突、DB 故障；批量后项失败、重复 ID、查询错误。比较完整业务行/revision、run/step/acceptance/event/task及通知计数 |
| issue 并发 | start vs update/cancel，next-step activation vs cancel，completion vs cancel，两个取消并发，多个目标稳定锁序；验证不死锁、不遗漏任务、不重写终态、不多发事件 |
| 停止与恢复 | 查询 taskIDs 失败、TaskService=nil、CancelTask 失败、提交后崩溃、重启、迟到结果、丢失 terminal；最终无关联活动任务且进程退出，无关 issue 任务不被停止 |

保留 `TestWorkflowIntakeReturnsStableReceiptAndRejectsConflictingReplay`、跨入口同 key 用例、`TestWorkflowOwnedIssueStatusRequiresRunCancellation`，但它们不足以覆盖上述风险。用真实隔离 PostgreSQL 和仓库 testutil；故障注入与并发 barrier 要精确落在“校验后/提交前、任务枚举后/取消前”。不靠随机 sleep 或删测得到绿色；无数据库跳过不能计通过。

**P1 装配验收条件**

1. 队长先确认 intake 是受限新增入口，并核定修订后的两文件及必要事务支持差异；工程交新固定 P1 HEAD、与 P0 的关系、逐文件差异/摘要及实际包。本报告与原补丁 apply-check 都不是授权执行或通过证据。
2. 在该固定候选执行全包 build/vet、测试编译及实际 `make test/scripts/test-go.sh` 流程；交真实 DB 测试、支持环境下 race 结果与任何跳过/失败。构建匹配 server/CLI/migrate，版本和 vcs.modified=false 可核验；旧111项和定向权限通过不能替代全服务。
3. 真 router 注册 intake 和原运行/取消入口，Engine/Router/Notifier/Terminal/Reconciler 使用同一装配。用允许的身份通过匹配 CLI/server 执行一次完整调用，拒绝的 CLI 请求只计边界通过；不得移除任务上下文、改用人类 PAT 或伪造 mat_ 绕过。本轮不增加 P2 action/MCP/实例 CLI。
4. 真 server→持久化 run/step/task→隔离 daemon→结构化输出→terminal observer→下一步/终态闭环，覆盖上述取消/恢复/撤权及 intake 拒绝。确定性测试可执行文件须显式指定，说明其不等于真实 provider 或 Mac/Linux 原生通过。当前 end-node HTTP 演练和手工注入 terminal 的单测均不充分。
5. callback/debug/instance 相关排除表在 P1 数据库中不存在也能完成测试；配置拒绝且零副作用，不能加入 273/274–281 或签名密钥。既有权限、普通 issue、Autopilot 旧模式、daemon 进程所有权保持；新的独立审查确认本轮闭包。

P0 独立复验及完整切换回退由原审查职责给结论，本报告不覆盖。真实8条继续归档，`apply_allowed=false`；真实原始键与身份恢复库验证、P2/P3、原生验收、生产版本及切换门槛全部保留。TES-85 仍为 in_progress。本轮只读分析与设计交付，没有修改生产实现或工单状态。
