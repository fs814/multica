建议将两文件的最小权限抽取及聚焦测试纳入隔离候选续办范围；静态对比支持“保持上游判定语义”，尚不构成测试通过、P1 完成或落地批准。最终范围由队长协调。本轮未应用补丁、运行服务、执行数据库测试或修改工单状态，不复验 P0 或 TES-88。

**证据与审查边界**

- 实际附件：TES-85-P0-P1-20260916.zip，附件 ID `01a0a873-56c6-7bfd-b8cd-6fa78257229f`，SHA256 `857c5b6cc69dad16566640edcdd975c33fde3d38025c4d90aadb7483088cecbb`；内层 SHA256SUMS.json 的 49 项全部匹配。
- 固定上游 `9d18186e6e8cfa168d6053d33d652e86fadfc12b`；P0 候选 `8e24b7f4afbeac7cc79678288582a94b867a35c1`；P1 未完成探针 `d69546e8437bf2bd7d687661e26b42b5d909f11f`。本轮以附件补丁和本地已有固定 Git 对象作静态比对，未导入 bundle、未将当前工作树冒充 P1 候选。
- 原 minimum-contract-source-files.json 实际为 162 项，SHA256 `0fd93f813f9d5dc6c0aedc2de1f9b65f181f1879cc3b6b266f8260fbe2b3146c`，两路径均未列入。
- 未应用提案补丁 SHA256 `db2884095d2a1af65167de4b01f8c39e7ec44a5451b51fb56c9bd8561abc18f3`。新增 service 文件与来源 e44b6a8d7 的 blob `c1ef1d6de6005debe77775d339c0d747d4ea9fb2` 内容一致；来源 handler blob `70537326450781175e7fec7d9da35fba5356eb9f` 只作来源定位，不准整份覆盖上游。
- agent_access.go.proposed 去除新增 import 与 invokeAgentDecision 函数体替换后，其余内容与固定上游一致；.proposed 文件本身未 gofmt，工程应采用补丁表达的格式并提交格式检查。
- 附件服务构建日志确实缺失 InvokeActor、invokeActorForRun、AgentInvokePermitted。111 PASS / 0 FAIL / 0 SKIP 是工程交付的 workflow 核心测试记录，本轮未重跑，也未扩展为权限、handler、daemon 或完整服务通过。

**逐文件建议允许范围**

| 路径 | 允许的增量 | 必须保留 / 禁止扩大 |
|---|---|---|
| server/internal/service/agent_invoke.go | 新增 InvokeActor、MemberInvokeActor、SystemInvokeActor、AgentInvokePermitted、invokeActorForRun；共享判定接受调用者传入的 *db.Queries | 保持 owner、private/unknown、public_to workspace/member/team 的原有分支顺序；不得新增管理员通行、A2A 私有通行或全局 Queries 回退；不得在此读取 HTTP 头、替换认证或添加 SQL/schema |
| server/internal/handler/agent_access.go | 仅增加 service import，将 invokeAgentDecision 替换为完整字段透传的共享调用；必要注释说明抽取边界 | 原 canInvokeAgent 拒绝日志、通用对外原因、所有 view/list/aggregation 权限、originator 提取、terminal-task 防护、source-task 记录及 squad 入口均保留；不得整文件覆盖 |
| server/internal/service/agent_invoke_test.go（新增建议） | 判定矩阵、actor 映射、错误分支与事务 Queries 测试 | 预期值来自下面的契约表；不能仅比较两个均调用新 helper 的结果而宣称证明抽取等价 |
| server/internal/handler/agent_access_test.go（现有扩展） | handler 透传与拒绝日志、查看/触发分离、伪造头与终态来源回归 | 保留上游既有权限测试；不为过测修改认证、成员关系或权限规则 |
| server/internal/service/workflow_router_test.go（原 P1 范围） | 四类路由、跨 workspace、事务读、撤权与 readiness 的接线验收 | explicit、previous_step、capability、fallback 都过相同 gate，不允许后备路由绕过 |
| server/internal/service/autopilot_invoke_parity_test.go（新增建议） | 同 package 验证现有成员入口与共享 helper 的成员分支等价，及触发身份一致性 | 不改 canMemberInvokeAgent、autopilotAdmitInvoke 或 ResolveAutopilotTriggerPrincipal 的生产逻辑；本轮不扩成 Autopilot 权限重构 |

原 P1 已授权的 handler、service、daemon、cmd/server 装配和相应测试仍按原逐文件清单续办，不因本次新增重复申请整批范围。若需要上述清单外生产文件、改权限语义或增加数据库依赖，应先提交新的具体差异。不得借此引入 callback/debug/Issue Pool 全套实现、P2/P3、身份迁移或生产切换。

**静态等价结论及必须纠正的表述**

固定上游 handler.go:949 的 getWorkspaceMember 只是 ParseUUID(userID/workspaceID) 后调用 GetMemberByUserAndWorkspace。提案在 service 内执行同样解析和查询；handler 的 uuidToString 本身委托 util.UUIDToString。因此，所见替换没有改变 owner 比较、成员查找或 allow-list 判定。

但源码注释“Any lookup error is a denial”不准确：owner 在查询前通过；成员查询失败只使 isWorkspaceMember=false，仍可能由精确 member target 或内部 agent/system 的 workspace target 通过。只有 ListAgentInvocationTargets 失败在到达该分支时立即拒绝。这是上游既有行为，本次不得悄悄收紧或放宽；建议修正文档表述并用测试锁定。若希望“任意查询错误一律拒绝”，应另列权限行为变更。

AgentInvokePermitted 也不是完整的租户边界：它不比较 agent.WorkspaceID 与 actor.WorkspaceID，不自行验证 workspace target 的 TargetID，也不负责 archived/readiness。固定 P1 的 routeExplicit 使用 GetAgentInWorkspace，capability 使用按 workspace 列表，eligible 先检查 readiness，再调用 helper。必须以完整调用链验证边界，不能把单测传入任意 agent 行后得到 true 当作 API 授权依据。

invokeActorForRun 的 NULL accountable → system 映射只适用于确实获准的无归属工作流来源。固定上游 Autopilot 已要求：手动按点击者，定时等自动触发按 ResolveAutopilotTriggerPrincipal 解析的人，解析失败拒绝；不能因为新 helper 存在就退化为 system 或使用 autopilot 创建者。新文件和路由测试中把“autopilot schedule”笼统举作无归属例子的注释应改为“获准且确实无可归属人的来源”，避免工程照此接错。

**共享权限等价测试矩阵（下列为验收要求，尚未执行）**

将允许/拒绝作为独立预期值；共享函数运行完整矩阵，handler 运行字段透传和具名回归。可在迁移前后分别对固定上游与候选跑相同夹具，记录差异；不得在生产增加第二份旧判定器。

| 夹具 / 输入 | 必须观察的结果 |
|---|---|
| 真实 agent owner；private、未知 mode、public_to | owner 均允许；没有 owner 则不能通过 owner 分支。无效 UUID 不 panic |
| 非 owner 的 member、workspace owner/admin；private/未知 mode | 全拒绝；管理/查看权限不等于调用权限 |
| public_to + workspace target；普通有效成员 / 非成员 / 无效身份 / 成员查询失败 | 成员允许，其余 member 拒绝；角色不提供额外通行 |
| public_to + member target；匹配人 / 不匹配人 | 精确匹配允许，否则拒绝；即使成员查询失败，匹配 target 的原函数仍允许，须明确记录；HTTP 租户成员准入另测 |
| public_to + team、未知 target、空名单；非 owner | 拒绝；混合名单按已有任一有效匹配行为判断 |
| 读取 invocation targets 失败；非 owner | 拒绝；owner 短路不依赖该查询，不伪称所有查询故障均拒绝 |
| member ActorID=A、Originator=B | 按 A；不得借 B 获取权限 |
| agent/system ActorID=A、Originator=B | 按 B；A 自身 ID 或所有者地位不得替代 originator；B 为 owner/精确名单人分别允许 |
| 无 originator 的 agent/system | private、member-only、team-only 拒绝；同 workspace 内部身份且 public_to workspace 允许；非法 actor 类型不能冒充该例外 |
| accountable 有效 / NULL 的 run | 分别映射为 member / system，workspace 原样透传；非法来源应由入口拒绝，不能靠空 accountable 升格 |
| handler 非 member 且无 originator 被拒绝 | 保留既有一次 warning 与字段；对外仍为通用权限原因，不暴露名单；允许或普通 member 拒绝不产生该 warning |
| 管理/查看、名单聚合及历史读取 | 上游行为不变；“可查看但不可调用”管理员样本必须保留 |
| 伪造 X-Agent-ID/X-Task-ID、跨 workspace、终态 task | 真实 middleware + handler 不能借其他任务/租户的人获得新调用权；终态来源不得延续人的调用权限 |

路由与事务验收还须覆盖：

1. 四种主路由及 fallback 都拒绝异 workspace agent（包括同一 owner 跨 workspace）、归档 agent、离线/无效 runtime、查询失败与撤权目标；断言没有新增执行队列，不能只看错误文本。不同 fallback 若本身合法可选中，但不能继续使用被拒目标。
2. 在同一事务内修改调用名单或 runtime 可用性，路由必须通过传入的 q 读到本事务状态；对比 pool 连接仍是旧状态，防止错误使用 r.Queries/h.Queries。回滚后应恢复。事务共享不等于自动解决所有并发撤权竞态，测试必须写明隔离级别和“下一次激活前已提交撤权”的边界。
3. 第一步完成后、下一步激活前撤权，previous_step 和 reconcile/retry 重新判定，不能沿用旧授权缓存；取消后不入队下一步。运行终态通知重复到达不重复任务。
4. Autopilot：手动点击者与创建者不同、trigger.created_by 与 autopilot 创建者不同、触发主体无法解析/已失效、跨租户 trigger、系统来源都要测。既有成员准入与 run/task 的 accountable/originator 保持一致。失败不能 fallback 为 system 以命中 workspace allow-list；原 create_issue/run_only 路径行为保留。

新增数据库测试采用仓库 testutil 夹具，使用隔离 PostgreSQL，不能因无数据库跳过后计通过。默认测试不得解析或运行用户安装的 agent CLI；daemon 整链使用显式测试可执行文件/确定性协议夹具，记录其覆盖限制。

**P1 完整构建、装配与验收门槛**

| 门槛 | 必须交付的证据 | 不足以替代的证据 |
|---|---|---|
| 固定范围 | 新 P1 HEAD、父 P0 SHA、最终路径/函数差异、来源 blob、迁移及生成物摘要；两文件实际应用和聚焦测试单独可审 | 本提案、git apply --check、原111项结果 |
| 完整编译 | 在新固定候选的 server 下完成 go build ./...、go vet ./...；按该候选 Makefile 的 build 目标构建 server/multica/migrate，记录匹配版本、commit、vcs.modified=false | 仅 internal/workflow 或 generated/migrate 构建；未知版本的 health 200 |
| 全套测试 | 指向自建隔离数据库，按候选 make test / scripts/test-go.sh 的真实流程跑 Go 测试及支持环境的 race；另提供权限矩阵、service/handler/daemon/CLI/router 的结果，列出任何失败和跳过原因 | 仅 go test -run 某个已通过集合；为编译删测试或用 stub；数据库缺失跳过 |
| 实际装配 | cmd/server/router.go 装配同一 Engine、WorkflowRouter、Notifier，注入 Handler、AutopilotService.WorkflowEngine、TaskService.WorkflowTerminal；main 启动并受 context 管理的 Reconciler；授权路由及 middleware 实际注册 | 在测试中手工给 Handler 填一个 engine 后通过；生产路由仍404/503 |
| 运行闭环 | 真 server/匹配 CLI 的认证请求到达已发布模板入口；固定 version → run/step/task → 测试 daemon 结构化输出 → terminal observer → 后续步骤/终态；失败、取消、重启和 lost-terminal reconcile 均有持久化证据 | 仅健康检查或直接调用 engine；把确定性 fixture 等同于所有真实 provider/原生主机通过 |
| 权限与原子性 | 上述矩阵；run/event/task/receipt 的事务失败回滚，提交后通知；同 key/同载荷并发只一条 run/issue/task，同 key/异载荷冲突，workspace 隔离，固定 version 与旧 NULL hash 的兼容边界 | 单线程重放；把旧 NULL hash 当作已校验载荷一致 |
| 耦合清理 | 未支持的 callback/debug/instance 来源在已纳入 API 明确拒绝且零副作用；published 只查已纳入 schema；API、service、测试夹具、reconcile 和装配中均不依赖被排除的表 | 仅 core 移除 callback；JSON 字段静默忽略，随后正常执行 |
| 既有能力保持 | 上游权限、Autopilot 旧模式、进程所有权和停止确认回归；新独立审查确认最终集成差异；P0 独立结论和任何遗留条件分别引用 | 从现有完整 fork 裁掉功能后替换现有服务；把阶段成功作为生产放行 |

具体未完成项：P1-probe.patch 新增的 server/internal/handler/workflow_run_test.go:83–84 仍清理 workflow_callback_delivery/destination，:476 插入 callback_destination，:537 查询 callback_delivery。这些测试尚未按裁剪契约改完；工程需保留原子性/重放目标并改成未支持 callback 的拒绝和零写入断言，不能建回排除的表来过测，也不能直接删除整段测试。这是本次静态发现的 P1 闭包缺口，不影响已报告111项核心包结果的原有限定范围。

P1 的“匹配 CLI”门槛指当前阶段已有 CLI 和 server 二进制完整构建及既有入口兼容。保存实例、新 workflow action/MCP 及其 CLI 注册仍属 P2；不能提前声称全部产品 CLI 可用。下一阶段放行还要满足队长既定的 P0 独立审查顺序。

本报告可以供队长核定最小新增范围并安排工程续办，不能自动执行提案或批准生产。八条历史实例继续归档，apply_allowed=false；P0 三类迁移和完整回退由独立审查给结论，真实原始键、P2/P3、Mac/Linux 原生验收和生产切换门槛均保持原状。TES-85 当前仍为 in_progress，本轮不改状态。
