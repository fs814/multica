TES-105 独立复验结论：**阻断，退回 in_progress**。

审查对象固定为 `e6865d16a9a8110c6c06db8fa9b0cfff94437308`。本结论只针对 TES-105 的 P0 权限、取消语义与证据复验，不改变父项 TES-85，不批准 P1 或发布。`apply_allowed=false`，真实 8 条继续归档，原生验收与生产切换门槛不变。

## 必须修复：callback 拒绝存在大小写绕过

位置：`server/internal/handler/workflow_intake.go:82`、`:91`。

请求先解码为结构体，随后仅按精确小写键检查原始 JSON map。结构体能够识别大小写变体的 `callback_destination_id`，但拒绝逻辑没有检查解码后的 `CallbackDestinationID`，也没有归一化原始键。

在相同已认证成员、已发布模板及有效 intake 字段下，独立探针结果：

| callback 字段名，值为非空 UUID | HTTP | 新增 workflow_run |
| --- | --- | --- |
| `callback_destination_id` | 400 | 0 |
| `CALLBACK_DESTINATION_ID` | 201 | 1 |
| `Callback_Destination_Id` | 201 | 1 |

这会将本应拒绝的配置作为成功 intake 持久化，违反明确的 callback 拒绝边界。没有证据表明 callback 被实际投递，本发现也不声称外部回调执行或权限升级。

修复要求：使原始字段识别、结构体解码与禁止配置校验一致；对禁止控制项的大小写变体以及大小写/重复键组合增加拒绝、零新增业务行回归。保留合法 intake、幂等重放和普通 payload 行为。重新交付固定候选后复验。

复现采用附带 `review_boundary_test.go` 和 Go `-overlay`；未写入或修改候选源码。`TestReview105CallbackCaseBoundary` 的两个变体失败，标准小写对照通过。附件提供完整探针源码、运行脚本、日志与摘要。

## 来源与归属

- 已扫描 TES-105 评论（空），并扫描父项全部线程、展开固定交付所在的相关线程。TES-105 入口状态为 `in_review`。
- 下载的是题述父项评论 `01a0a955-546a-7738-b265-b41bbe61434d` 的四个指定附件。REPORT、source zip、Windows zip、bundle 的 SHA256 全部匹配；source 内部清单 59 项全部匹配。
- `git bundle verify` 通过，使用本地已有上游对象 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 导入独立副本。HEAD 精确匹配目标，tracked 工作区干净。
- 最终提交相对 `122a8fd72e1252316aa1939a6ecc44b95212de81` 的差异为报告所述 15 文件。本轮没有应用任何 proposal 补丁。
- 三个交付二进制的 Go metadata 均为 windows/amd64、目标 revision、`vcs.modified=false`；未启动这些 server/CLI/migrate 二进制。
- 未提供关联 PR 链接；`gh pr list --repo multica-ai/multica --state all --head tes85-r5 ...` 因未登录失败。PR/远端 CI 状态未能核实，不能声称不存在 PR 或 CI 通过。
- 独立 P0 提交 `0ca4c63...` 不在本地可用对象中，未能独立重复报告所述与它的空 diff；不能把提交者的关系说明当作已核实事实。

## 独立执行结果

新建专用数据库 `tes85_review105_r5`，固定候选 `go run ./cmd/migrate up` 退出 0。所有测试进程均显式设置指向此库的 `DATABASE_URL`，没有清除任何 CLI 身份标记。各命令顺序运行；全包初始化额外使用 `-p 1`。标准聚焦命令内部仍按 Go 默认并行调度三个 package，各自使用其 fixture。

| 检查 | 独立结果 |
| --- | --- |
| `go build ./...` | 退出 0 |
| `go vet ./...` | 退出 0 |
| `go test -p 1 ./... -run '^$'` | 退出 0；仅编译/初始化，非全量测试通过 |
| `go test ./internal/workflow -count=1 -json` | 111 PASS、0 FAIL、0 SKIP |
| `go test ./internal/handler ./internal/service ./cmd/server -run 'TestR5\|TestWorkflow\|TestAgentInvoke\|TestBatchChildDone\|TestBatchUpdate' -count=1 -json` | handler 63、service 34、router 1 PASS；0 FAIL/SKIP |
| `go test ./internal/handler -count=1 -json` | 2167 PASS、3 FAIL、47 SKIP；退出 1 |
| 独立 callback 探针 | 退出 1；两个大小写变体不符合拒绝要求 |
| `go test ./internal/migrations -run '^TestMigrationNumericPrefixesAreUnique$' -count=1 -v` | 退出 1；重复数字前缀确实存在 |
| `go test -race ./internal/workflow -run '^Test' -count=1 -json` | 链接失败，`ld returned 48 exit status`；0 个测试执行 |

计数为顶层测试，重叠套件不相加。提交者最终套件计数与本轮独立结果一致。提交者曾误用默认数据库的结果仍作废；本轮没有审计默认库的历史副作用。附件原标准 `scripts/test-go.sh` 是中间提交的失败结果，本轮没有重跑该标准全量，不能将其归属为最终 HEAD 全量通过。

## P0 已核实部分及边界

- 真 router/Auth 测试通过；入口要求成员身份、path/context workspace 一致，拒绝机器 actor 和显式版本头。当前发布版本在引擎事务中锁 template 后解析。source_url 仅持久化。
- external 路由从持久化 issue 恢复发起成员，在 explicit/capability/previous/fallback 四种选择中对发起者与负责人同时执行准入检查；事务内撤权测试通过。
- issue 单条和批量取消保留字段、CAS、文本、附件校验，issue/run/step/acceptance/event 写入共享事务；拒绝和 commit 失败测试证明回滚。批量稳定锁定 issue，写入/通知保持请求顺序。
- 引擎取消、完成及重试使用 issue→run→step 锁序；后继先激活后取消、完成先赢时拒绝旧快照、重复/no-op 和普通批量顺序回归均通过。
- 提交后通过同一 TaskService 停止 step→run 关联活动任务；停止失败保留 cancelled run 与活动任务作为恢复意图，重建 reconciler 后收敛；同 issue 无关任务不被停止。此为数据库/服务级验证，不是 daemon/OS 退出证明。
- 未新增 SQL 故障全矩阵、publish/archive 精确 barrier、反向批量压力或跨租户全部组合验证；callback 新增失败意味着这些通过项不能合成为 P0 整体通过。

## P1 与发布门槛保留

完整 handler 三个失败与提交者披露一致：

1. `TestStableIDSuffixMatchesDaemon`：Windows 分隔符与预期不同。
2. `TestParseSkillArchive_RejectsUnsafeSkillMdPath`：`/abs/SKILL.md`、`\abs\SKILL.md`、UNC 路径未被拒绝。不能仅因平台差异就将其视为无害。
3. `TestWorkspaceDeletionManifestCoversPublicSchema`：七张 workflow 表未纳入 workspace 删除契约。

迁移编号唯一性失败独立复现。新空库迁移成功不证明历史 ledger/升级恢复安全；不得据此批准迁移重编号。删除契约还需要 workspace 创建/删除并发栅栏验证。race 没有执行；合法 CLI→server→daemon→子进程树退出、真实重启恢复、原始键/身份恢复库及原生平台证据仍缺失。

本轮没有修改固定候选、写生产、启动常驻 server/daemon、发布或切换。测试库清理及最终状态见同附证据摘要。
