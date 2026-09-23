本次核定：沿用既有 P0→P3 隔离候选路线，接受 request_hash 单列扩展、callback 契约剥离和七个主键拆分。以下是工程允许范围及验收设计，不是实现通过证明。8 条历史实例继续归档并保留去重身份；apply_allowed=false，生产切换门槛不变。

证据基线：原方案 TES-85-prerequisite-scope.md；固定来源 e44b6a8d7；工程探针基线 9d18186e6e8cfa168d6053d33d652e86fadfc12b；本次 CONTINUATION.md、identity-audit.json、request-hash-proposal.sql。已只读核对来源 SQL/engine、当前 CLI/handler 和迁移 runner。归档时间/revision 来自本次工程证据包，未重新直读生产数据库；历史保存回执不证明当前存储键。本次不重做 162 项盘点，不整批纳入 273。

### 1. request_hash 与 callback：逐文件范围

路径均相对产品仓库；新文件名为本方案建议名称，尚未创建。工程须在固定集成树再次检查完整文件名冲突，并记录实际名称、摘要和排序；不可机械重编号既有迁移或补写 ledger。

| 文件 | 允许的增量 |
|---|---|
| server/migrations/254_tes85_workflow_run_request_hash.up.sql | 仅向 workflow_run 增加 nullable TEXT request_hash，使用 IF NOT EXISTS；不加默认值、NOT NULL、索引或回填。迁移前校验已存在同名列确为兼容 TEXT；名字相同不能视为类型正确。 |
| 对应 .down.sql | 保守回退：默认拒绝删除已有数据/来源不明的列；仅在隔离空库、证明本批创建且无消费者/数据时允许撤销。完整 fork 上即使本次 up 跳过，也不得删除原273创建的列。应用回退优先保留加法 schema。 |
| server/pkg/db/queries/workflow.sql | CreateWorkflowRun 保留 request_hash，移除 callback_destination_id 的列和参数。按阶段拆开尚未纳入的 P2 instance 快照字段及 debug 查询；到 P2 再与491–499同步引入，不能为让 P0 生成通过提前整批引入 P2。 |
| server/pkg/db/generated/workflow.sql.go、models.go、querier.go（若当前生成配置产出） | 仅从已审 SQL/schema 重生成；连带生成差异逐项解释。不得选用来源整份模型或手删字段。 |
| server/internal/workflow/engine.go（P1） | 保留 RequestHash 的存储及正常/竞争重放冲突检查；移除 StartRunInput/CreateWorkflowRun 的 CallbackDestinationID、recordEvent 的 callback 入队、callbackEventType 及仅服务回调的指标接口。保留事务事件、txEffects 和提交后的通知，不删除整个 recordEvent。 |
| server/internal/handler/workflow_run.go（P1） | 保留现有规范化 hash、显式版本参与 hash、幂等错误码及兼容旧客户端路径，不更换算法或字段集合。 |
| server/internal/handler/workflow_intake.go（若既有 P1 选择使用它） | 去除 callback 查询及 engine 参数；请求携带非空 callback 配置必须明确拒绝，不能接受后忽略。不得为了剥离 callback 一并移除已授权的普通运行入口。 |
| server/cmd/server/router.go、main.go；server/internal/metrics/workflow.go（仅实际装配涉及处） | 只移除本候选新提取部分的 callback 路由/worker/指标装配；保留上游原有业务及工作流运行装配。若上游或目标已有 callback 能力，禁止借此删减，转入独立兼容范围。 |
| server/internal/workflow/engine_integration_test.go；server/internal/handler/workflow_run_test.go、workflow_run_version_test.go；server/cmd/migrate/workflow_migration_compat_test.go | 保留幂等、事务和版本断言，解除测试对回调夹具的耦合；补本批迁移/重放边界验证。不得以删除整条原子性测试缩小依赖。 |

不纳入 server/migrations/273_workflow_external_handoff.*、274–281 的 callback 迁移、workflow_callback.sql/生成物、workflow_callback.go/测试及回调发送组件。本次也不顺带加入 autopilot_trigger.signing_secret_encrypted；后续 TES-85 签名 webhook 必须在对应产品批次按已授权契约单独解决，不能声称该能力已由 request_hash 补齐。

兼容风险：旧 run 的 request_hash=NULL 继续保留；来源代码对 NULL 不执行 hash 冲突比较，只能称为旧幂等兼容，不能宣称旧记录也有载荷一致性证明。不得从当前输入反算并批量回填历史 hash。新请求必须写入 hash，同键同语义返回同 run，同键不同语义拒绝，事务竞争分支规则一致。未纳入 callback 的候选不能替换已使用 callback 的部署。未来真正引入273时需另做共存适配，否则原始 ADD COLUMN 会与本次列冲突。

### 2. 七个主键的文件拆分

仅调整候选中以下三个来源 up 文件的 id 定义：PRIMARY KEY 改为 NOT NULL，保留 UUID、DEFAULT gen_random_uuid() 和其余字段/检查；不增外键或级联。已应用旧建表迁移的恢复库不重跑、不改 ledger、不主动拆旧主键。

以下新增文件均位于 server/migrations/，每行两份 up 各有对应 down；索引文件严格一条 CREATE UNIQUE INDEX CONCURRENTLY，主键附着在另一文件进行。254/255 是建议前缀，完整排序须保证建表→索引→附着，应用接流量前七个主键全部存在。

| 来源 up | 表 | 新索引 up | 新附着 up |
|---|---|---|---|
| 232_workflow_template.up.sql | workflow_template | 254_tes85_workflow_template_id_index.up.sql | 255_tes85_workflow_template_pkey.up.sql |
| 同上 | workflow_template_version | 254_tes85_workflow_template_version_id_index.up.sql | 255_tes85_workflow_template_version_pkey.up.sql |
| 235_workflow_run.up.sql | workflow_run | 254_tes85_workflow_run_id_index.up.sql | 255_tes85_workflow_run_pkey.up.sql |
| 同上 | workflow_step_instance | 254_tes85_workflow_step_instance_id_index.up.sql | 255_tes85_workflow_step_instance_pkey.up.sql |
| 244_workflow_submission_acceptance_event.up.sql | workflow_submission | 254_tes85_workflow_submission_id_index.up.sql | 255_tes85_workflow_submission_pkey.up.sql |
| 同上 | workflow_acceptance | 254_tes85_workflow_acceptance_id_index.up.sql | 255_tes85_workflow_acceptance_pkey.up.sql |
| 同上 | workflow_event | 254_tes85_workflow_event_id_index.up.sql | 255_tes85_workflow_event_pkey.up.sql |

server/cmd/migrate/main.go 只增上述完整键的条件、无效索引清理及回退保护；测试放在上述 migration compatibility 测试及专门的七主键重试测试文件。沿用 runner 的条件机制，避免把并发 CREATE 藏进 DO 或多语句 migration。

恢复库已有正确 PRIMARY KEY(id) 时跳过建索引/附着并保留原约束；须检查表、列、唯一性、有效性及约束关联，不仅比较名字。发现异形主键、同名不同定义索引、NULL/重复 id 必须停止。清理只能作用于本批精确命名且未附着的无效索引，不得删除别人的有效索引或旧主键。跳过 SQL 也会写 ledger，故 ledger 不能证明本批拥有该约束；默认拒绝破坏性 down，不能仅凭迁移记录拆掉旧主键。

附着主键仍需要锁；NOT NULL 应在建表时保留。附着后索引由约束拥有，删除约束会同时删除该索引，因此 up/down/up 与“索引成功但 ledger 尚未写入”的崩溃恢复必须实际验证。[PostgreSQL 17 ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)、[并发索引及失败处理](https://www.postgresql.org/docs/17/sql-createindex.html)。

### 3. P0 的通过门槛

1. 固定候选 HEAD、基线、完整迁移文件名/摘要和逐文件差异；本次仅多出单列迁移、七组索引/附着及必要 runner/测试改动。没有 callback/debug/Issue Pool 的隐性新增。
2. 使用候选实际 schema/queries 执行 sqlc generate 成功；再次生成无漂移，生成包和 migrate 构建通过。P0 不以未纳入 P1/P2 的符号空实现骗取全服务编译；每阶段编译范围明确记录，完整 server/CLI 构建仍在后续集成门槛。
3. 真实 PostgreSQL 分别验证：上游 fresh DB、上游已有数据 DB、含旧232/235/244及273的 fork恢复 DB。后者只证明加法迁移兼容，不证明裁剪后的应用可替换完整 fork。
4. 核验 request_hash 类型/NULL语义、七个 NOT NULL id/有效唯一索引/主键和所有既有上游约束；数据、归档、历史快照、自动化绑定不因 P0 改变。上游原迁移行为不退化。
5. 第二次 up 无业务行变化；覆盖并发索引失败、非法同名索引、已有等价主键、索引已成而 ledger 未记、附着已成而 ledger 未记。条件跳过及重试有真实数据库断言，测试缺 DB 或 skip 不能报通过。
6. 隔离空库可撤销场景完成 up/down/up；有数据/旧列/旧主键的回退必须按设计拒绝，并验证匹配旧应用加保留 schema 的回退及备份恢复路径。只读夹具不算通过。

P0通过才进入P1；P1再证明 RequestHash 重放/并发冲突、事务事件、callback拒绝与真实服务构建。实例迁移、同步、CLI读取在P2及产品增量阶段验收，不可提前用其夹具替代P0数据库门槛。

### 4. 八实例保留与去重策略

本次证据包对应的8个ID如下；revision是证据采集时值，迁移前必须重新读取，不是可直接使用的CAS值。

| 项目/目标 | 实例ID | revision |
|---|---|---:|
| fmt / windows-native | d64110dc-927e-4f45-ad9c-068c055272c3 | 3 |
| fmt / macos-native | 0949017a-464e-4df9-8f93-0dc8518405e9 | 3 |
| nginx / linux-clone | 22c1cca3-8e2c-4c99-8379-c4018b3a3553 | 3 |
| mquickjs / linux-native | 2349f854-e84a-40bf-bde1-4ff405e4dd9d | 2 |
| mquickjs / windows-clone | 9066ef4f-cb42-4913-b142-7839a06bc823 | 2 |
| ripgrep / macos-native | 69902286-4e1a-4162-a83d-9d2d3a320204 | 2 |
| proxy / windows-native | 9978ce25-2f20-4382-855b-940d2ab7a3ad | 2 |
| mquickjs / macos-native | c6e82753-cf83-47a5-9a81-22fbb79acdc8 | 2 |

均继续归档；不恢复、不重置revision、不改输入/模板版本，不计活动覆盖。canonical键仍按原字节规则 buildcatalog:v1:SHA256(project_key + NUL + target_key)，不得额外大小写归一化、改路径或改前缀。

归档记录继续保留其(workspace_id,idempotency_key)唯一身份；现有497索引不排除归档行，应保持。同步须完整分页且 include_archived=true，按受证据支持的键映射识别 archived 并跳过写入。身份字段缺失时是“无法判断”，不是“不存在”；对这8项禁止fallback create，全局 apply_allowed 仍为false。相同键映射多ID、异模板、键占用或缺少页结果均停止，不能取字典最后一项。目录摘要变更可以报告冲突，但不得使归档项重新变成create/update。

journal只缓存证据，不是数据库真值；它应记录workspace、原ID、原始回执摘要、预期键和读取时revision。两台Mac的普通原生验收实例继续使用独立身份，不占用这8个canonical键，不计入活动受管覆盖。未来确有恢复/替换需求需单独明确决定，本次不预授权。

### 5. 原始存储键的受支持读取路径

已确认当前能力缺口：安装CLI的 workflow 只有call/mcp，当前cmd_workflow.go注册七项template/run动作，无实例原始键读取。当前 GetWorkflowInstance→GetWorkflowInputInstance 能在服务内部读取 archived行和存储idempotency_key，但 inputInstanceResponse 未输出该键；list也不输出。已审产品“managed_source/external_key投影”即使恢复，也不等于对任意旧键的原始值证明。不能捏造一个现成CLI命令，也不能以create重放探测或绕过CLI直查线上数据库。

推荐的最小新增只读路径，明确标记为“待实现/审查，不是现有命令”：在匹配产品CLI新增 instance.identity.get 动作，经 workspace受权HTTP详情子路由读取指定ID的存储身份。建议路由 /api/workflow-instances/{instanceID}/identity；CLI负责闭合schema，服务端按工作区与管理权限校验，复用现有精确ID查询，不提供任意SQL/批量扫描。返回同一行快照的workspace_id、id、template_id、template_version_id、revision、archived_at、nullable idempotency_key及投影schema版本；键必须来自存储，不得由输入合成。响应缺字段/不支持/403/404不得解释为键为NULL。普通实例列表无需扩大原始键暴露范围。

这一只读诊断增量的逐文件范围：server/cmd/multica/cmd_workflow.go及测试（动作/转接/响应）；server/internal/handler/workflow_instance_management.go及测试（授权/同一行读取）；server/cmd/server/router.go（GET装配）；内置multica-workflows技能的SKILL.md或对应实际reference（说明读取契约）。现有GetWorkflowInputInstance足够，无需为诊断增加数据库列/迁移。具体动作仍需进入固定候选的独立审查与正常部署流程，不能为读取8条记录擅自替换8081服务；此前缺口保持未通过，由队长协调平台能力提供方。

原始键只在受控证据中保存，不扩大到普通列表/日志。本轮不读取凭据、不提供数据库绕行。服务二进制须有固定提交/摘要及匹配schema证明；238a52b3 + vcs.modified=true不是已审版本证明。

### 6. 真实数据库迁移/回退验收

首先分流，不能预设“必需更新8行”：

- 原始键与逐ID回执证明的canonical键完全相同：数据迁移为no-op；只恢复匹配API投影。真实DB集成必须证明上线投影前后这8行完整值/revision不变，且两轮同步均识别archived、无create/update/run/trigger。
- 原始键缺失或不同：本次不默认认领。工程提交逐ID old→new清单、当前快照及来源证据，先在恢复库验证；缺少来源、键被占用、归档/revision/模板已变化时拒绝。真实迁移的生产执行仍须原有放行条件。

真实迁移候选需满足：同一事务按固定顺序锁定精确workspace+ID行；比较旧键、revision、归档和模板；验证canonical键唯一并保留唯一索引作竞争后盾。批次8项预检任一不符则整个批次不写。只更新确需变化的身份字段，每个实际变更revision单调+1并记录before/after及审计；输入、归档时间、名称、版本、历史运行/自动化绑定不变，不能自动重绑。若revision变更使旧自动化绑定过期，应明确显示失效，不能静默修复。

重复迁移只在数据库状态等于已记录after-state时为no-op；任何其他后续变化应拒绝。回退同样锁行并CAS核对after-state、键占用和revision：恢复旧身份字段，revision再+1，绝不倒退至旧revision或覆盖用户编辑。重复回退依据已记录rollback-state为no-op。归档始终保留。完整行比对需把预期revision/审计字段差异单独列明，不能声称写迁移后所有字节均相同。

验收记录必须来自与候选匹配的真实server/CLI/PostgreSQL恢复库，至少包括：首次迁移、重复迁移、两轮同步、成功回退、重复回退、迁移后并发编辑拒绝回退、其他ID占键、跨工作区拒绝、事务故障全回滚，以及全部8行和相关历史/绑定表前后对账。非目标行零变化；实际创建/更新/运行/触发次数有数据库及调用侧证据。备份还原后核对schema、ledger与业务行。协议夹具的8项archived/零写入只保留为规划器测试，不能顶替上述证据。

目前P0、原始键读取、真实身份迁移/回退均未通过；本报告只解除工程最小依赖的设计歧义。TES-85保持in_progress；最新独立审查已另报TES-88 R1关闭、R2仍待修订，本报告不复审或覆盖该结论。Mac/Linux原生验收、生产版本/恢复/维护窗口及原发布条件继续保留。
