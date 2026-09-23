# TES-85 r5 固定候选与证据

本轮完成已授权的受限 intake 与 issue 取消协调修订；交付新隔离候选供审查。**P1 未通过完整装配验收，不可上线；apply_allowed=false。** TES-85 保持 in_progress。真实 8 条仍按原归档约束处理，本轮不执行激活、迁移、生产切换或恢复操作；P0 独立复验、真实原始键/身份恢复库验证、P2/P3 和原生验收门槛不变。

## 固定来源

- 最终 HEAD：`e6865d16a9a8110c6c06db8fa9b0cfff94437308`，分支 `tes85-r5`，独立仓库干净。
- 前一 P1：`122a8fd72e1252316aa1939a6ecc44b95212de81`。P0 `0ca4c63c0dd02e00c2fdacc9d8c4f480e291f815` 是独立候选分支，不是本分支祖先；其修复已由前一 P1 转入。本轮核验最终 HEAD 的 `server/cmd/migrate` 与 `server/migrations` 对该 P0 的 Git diff 为空，本轮也未修改这些文件。
- 标准完整测试执行点：`d13d864cf5dccd88095a401a87bb63583fa04afc`；之后修正校验/并发边界及普通批量通知顺序，最终 HEAD 另跑全 handler、全 workflow、聚焦集及全包静态检查。
- Bundle 包含上游 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 之后的完整候选链；导入需要该上游对象，已执行 `git bundle verify`。
- `R5-from-P1.patch` 为本轮差异，`P1-from-upstream.patch` 为累计差异，`source/` 是本轮 15 个改动文件的固定 Git blob，`files-changed.txt` 列出范围。
- 匹配 Windows amd64 server/CLI/migrate 使用最终 HEAD 构建，版本 `tes85-r5`，三者均 `vcs.modified=false`。二进制在单独附件；摘要及 Go build metadata 可核验。构建成功不等于原生生命周期验收。

## 已实施差异与目的

| 文件/区域 | 实际修订 |
|---|---|
| `handler/workflow_intake.go` | 只适配认证成员与已发布模板；验证 path/context 租户；拒绝无法保留 task 来源的机器身份；拒绝显式版本及顶层 callback/instance/debug 配置；检查编码/hash 错误；保留幂等命名空间和回执。source_url 只存储。 |
| `service/workflow_router.go` | external run 根据持久化 issue 创建者恢复发起成员；每次路由同时校验发起者与负责人，覆盖 explicit/capability/previous/fallback。共享调用权限谓词本身未改变。 |
| `handler/issue.go` | 先纯校验；复用上游权限、目录、CAS、文本合并及附件规则；workflow 取消加入 issue 写入事务。批量先收集/校验，稳定顺序锁定全部有效身份，包括字段失败而跳过的目标；任一 workflow 目标失败则回滚；通知保持请求顺序。非状态重复项保留原处理方式。 |
| `workflow/commands.go`, `engine.go` | 抽出原有取消内核，提供 `CancelRunInTx` 及提交后 effects；统一 Issue→Run→Step 锁序。完成先赢时拒绝旧快照覆盖；当前发布版解析锁 template，重放保留原版本。直接 run cancel 仍不投影 issue cancelled。 |
| `workflow/engine.go`, `reconciler.go` | 取消提交后枚举关联活动任务，经同一 TaskService 停止；查询/停止错误返回或记录，重启后扫描 cancelled run 的未停任务重试。仅处理 step→run 关联任务。 |
| `handler/workflow_run.go`, `cmd/server/router.go` | 直接取消先检查必要服务，提交后停止任务；真实路由注册 intake，Engine 持有同一 TaskService。 |
| `queries/workflow.sql` 与 sqlc 生成物 | 仅增加 cancelled-run 恢复扫描及 issue 历史 run 存在性查询；无新增表、迁移或 callback 查询。 |
| 测试与内置平台说明 | 新增 handler 故障/并发、真实 middleware/router、跨负责人权限测试；同提交补充入口/取消行为说明。 |

`PLAN.md` 记录实施前的修订设计。必要事务支持差异位于原 P1 文件集内；本轮未修改 TaskService 实现、CLI 身份边界、provider/daemon 进程管理或普通 issue 全局自动停止规则。

## 逐项证据与剩余范围

以下均是已执行自动化测试的边界，不能替代真实 daemon/OS 证据。测试函数位于 `workflow_lifecycle_r5_test.go`，真实路由测试位于 `cmd/server/workflow_intake_r5_test.go`。

| 审查要求 | 已有证据 | 仍待补齐/限制 |
|---|---|---|
| intake 真路由与身份 | `TestR5PublishedIntakeActualRouter`使用真实 NewRouter/Auth；匿名、伪造身份头、workspace 不匹配、测试 task 凭据拒绝、成员创建和重放 | 这是测试凭据，不是许可的匹配 CLI→server 调用；终态 task/全部跨租户组合尚未逐项实装验证 |
| 管理员代指派不可借私有 agent | `workflow_router_permission_test.go` 新例覆盖四策略，只有负责人可调用时拒绝；双方准入时允许；事务内撤权重新拒绝 | 路由级证据；完整管理员 HTTP→任务执行链待补 |
| callback/instance/debug/版本拒绝 | `TestR5IntakeExcludedControlsAndIdentity` 42 个非空/错误类型组合、显式版本头、机器 actor；按测试 workspace 比较 issue/run/step/event/task/subscriber 行数不变 | 所有 null/空值、body 上限、编码故障、URL 边界的完整新增矩阵尚未完成 |
| 幂等与 provenance | 保留原稳定回执、冲突、manual→intake；新增 `TestR5IntakeThenManualReplayPreservesProvenance`；引擎并发 replay 单次 materialization 测试通过 | intake 真 HTTP 多请求并发、publish/archive 精确 barrier 尚待；旧 NULL hash 仍仅兼容 |
| issue 拒绝零持久化副作用 | `TestR5IssueCancellationRejectedRequestsAreAtomic`：priority/date/assignee/parent/附件/过期 revision/文本冲突；比较 issue/run/step/acceptance/event/task 完整业务快照；commit 故障测试回滚 | 每种 SQL 查询故障与 WS 通知计数注入尚不完整 |
| 批量原子、重复与 no-op | `TestR5IssueBatchCancellationIsAtomicAndNoopIsConsistent`，`TestR5SkippedWorkflowTargetPreventsPartialBatch`；无效后项/反向顺序/重复/同状态，单次取消事件 | 多批同时反向锁序压力与所有 query 错误注入待补 |
| 自定义取消与装配缺失 | `TestR5CustomCancelledCategoryAndMissingServices`；closed 分类自定义 key 保留，nil 停止服务在写入前 503 | 目录归档与取消精确并发尚待 |
| next-step activation 与取消 | `TestR5CancellationIncludesTaskActivatedWhileWaitingForIssueLock` 使用 Router/Tx 查询 barrier；TaskService 完成先激活后继，取消等待 issue 锁后覆盖新任务；迟到 terminal 不复活 | fixture 将任务推进 running，不是 daemon claim/OS 执行证明 |
| 完成先赢 | `TestR5CompletionWinningBeforeCancellationPreservesTerminalState` 在初读后暂停取消 Begin；经 TaskService 完成各步、引擎接受后，旧取消 409 且终态快照不变 | start-vs-cancel、两个取消竞争等矩阵仍需扩展 |
| 停止失败/重启 | `TestR5CancelledRunRecoversTaskServiceFailureAfterRestart` 注入停止失败，重建 reconciler，经真实 TaskService 收敛；同 issue 无关任务保留 queued | 真实 server→daemon→子进程树退出、任务查询故障、丢失通知、真实进程重启尚未验收 |
| 排除表不存在 | `schema-inventory.txt` 仅七个 workflow 核心表；包含合法 `workflow_step_instance`，无 input-instance/debug/callback 表 | 不等于真实恢复库/历史身份验证 |

## 实际验证结果

计数为顶层测试；各组重叠，不相加宣称总通过量。详见 `evidence/test-summary.json`，包含具体失败及 47 个 skip 名称。

| 检查 | 最终结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 均退出 0 |
| `go test ./... -run '^$'`，显式隔离 DATABASE_URL、串行执行 | 退出 0；这是测试编译/初始化，不是完整测试通过 |
| 全 workflow PostgreSQL suite | 111 PASS / 0 FAIL / 0 SKIP |
| 聚焦 handler/service/真实 router | 63 / 34 / 1 PASS，均零 FAIL、零 SKIP |
| 完整 handler，最终 HEAD | 2167 PASS / 3 FAIL / 47 SKIP；普通批量通知顺序回归已修复 |
| 标准 `scripts/test-go.sh`（中间固定提交） | FAIL；常规包阶段失败，后续 pkg/agent 阶段未执行；CLI 包 10 分钟超时。未修改任务身份来获取绿色结果 |
| 聚焦 `-race` | 构建失败：本机 GCC 链接 `ld returned 48 exit status`；没有 race 用例执行，不能计通过 |
| 三个匹配二进制 | 构建成功；最终 revision、Windows amd64、clean VCS 证据齐备 |

最终完整 handler 的三个失败为：`TestStableIDSuffixMatchesDaemon`、`TestParseSkillArchive_RejectsUnsafeSkillMdPath`、`TestWorkspaceDeletionManifestCoversPublicSchema`。前两项呈现 Windows 路径差异；第三项确认七张 workflow 表尚未纳入 workspace 删除契约。

标准全量失败包：cmd/multica、internal/cli、internal/daemon、internal/daemon/execenv、internal/daemon/repocache、internal/handler、internal/integrations/wecom、internal/migrations、internal/realtime、internal/service、internal/storage。日志显示任务上下文身份限制、平台路径/权限及继承候选的迁移/删除契约缺口；其他失败未全部归因，不能统一断言与本轮无关。中间版本的普通批量通知顺序失败确为本轮回归，已在最终全 handler 复验中消除。未为这些范围外失败修改 CLI/daemon/存储实现。

## 操作偏差与证据可信度

- 一次聚焦复验误设 `TEST_DATABASE_URL`，handler 使用默认本机 `multica` 数据库，遇到 schema 不匹配等失败。该次 `focused-confirmed.*` 结果全部作废。另一次无用例编译/初始化命令未显式设置 DATABASE_URL，也走默认库；其成功不作为最终证据。
- 测试会运行 TestMain，因此不能把无用例命令描述成完全无数据库操作。已从代码核对相关 workflow 清理按生成的测试 workspace 限定；没有执行全库清理、修改默认库 schema 或主动恢复操作。未做默认库前后全量审计，不能声称该默认库整体字节不变。
- 最终聚焦、全 handler、全 workflow 和无用例编译/初始化均显式设置到 `tes85_r5_tests`。标准全量此前也使用隔离库。上述偏差保留记录，不混入成功计数。
- 日志输出可能包含测试凭据/验证码，因此附件是脱敏副本；JSON Action/Test 计数保留，原始字节摘要另存。日志可证明所列测试结果，不代表独立审查复现。

## 范围外具体差异（未应用）

1. `proposal/NOT-APPLIED-migration-number-allocation.patch`：继承候选有多个重复数字前缀（232–255、284，含 P0 254/255 家族）。给出按现有依赖顺序从 479 起分配唯一编号的重命名差异及映射。**仅供新隔离库方案评审，禁止对现有迁移历史直接改名**；须先制定 ledger 兼容/升级方案并由 P0 独立复验。
2. `proposal/NOT-APPLIED-workspace-teardown.patch`：给出七表 ownership、删除查询与 handler 调用点。此草案尚不覆盖 template/run 并发创建 fence；必须把 workspace 写入栅栏及锁图测试一起核定，不能只改 manifest 消除失败。未执行 sqlc、应用或运行此草案。
3. 完整装配需要允许的 CLI 身份/隔离 daemon 执行资源。本轮没有删除 task marker、替换人类 PAT、解除环境绑定或启动未经许可的原生守护进程。需要合法环境与独立审查资源，不能通过改 CLI 边界绕过。

请队长核定上述迁移 ledger 与 workspace teardown/fence 的独立修订范围，并安排 P0/P1 独立审查及允许的 native harness。当前交付是可定位、可导入的候选和真实失败证据，**不是 P1 验收通过或生产应用批准**。

隔离验证完成后，仅删除本轮创建的测试数据库 tes85_r5_tests；未启动本轮常驻 server/daemon，现有 PostgreSQL 容器保持运行。
