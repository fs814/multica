# TES-85 R6：workspace 删除修复与未闭合门槛

固定候选：`fa698dcfc5f143a5c5c372944dbafb2b2ae2577b`；基线 R5：`e6865d16a9a8110c6c06db8fa9b0cfff94437308`。只完成 workspace 删除契约修复，不代表完整 P1 通过。`apply_allowed=false`；未应用到产品数据库、现有 daemon 或真实 8 条工作流。

## 具体可审计差异

- `R6-from-R5.patch`：12 个文件，376 行新增、7 行删除。七张 workflow 表纳入 workspace ownership manifest；删除事务在任务清理后删除 workflow 行，失败随父事务回滚。sqlc 生成两份对应查询文件。
- 新增 `LockWorkspaceForWorkflowWrite`（FOR KEY SHARE），与原 `LockWorkspaceForDelete`（FOR UPDATE）互斥。生产事务先锁 workspace，再进入原有 template 或 Issue→Run→Step 锁序。覆盖模板创建、内置模板复制/播种、保存/发布、StartRun、提交/验收/取消、终态推进、reconciler，以及借用事务的单个/批量 issue 取消。
- TaskService 终态回调原本在提交后运行，因此无需修改 TaskService 或 daemon 实现。模板 archive 只更新已有行，不新增子行，沿用原行锁竞争。未改变 CLI 身份规则、权限策略、业务状态投影。
- `source/` 是本次变更的精确 Git blob；bundle 包含上游 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 之后的候选链，导入需要该上游对象。独立仓库干净，bundle verify 通过。

## 迁移编号：已复现，策略尚未核定

`proposal/migration-collisions.json` 精确列出 25 个冲突前缀、63 个完整 stem：232–255、284。`TestMigrationNumericPrefixesAreUnique` 实际失败，日志附于 evidence。Runner 的账本主键是完整 stem，不是数字；当前迁移运行本身能在新隔离库完成。这是交付编号约束/历史兼容问题，不能把 lint 失败解释成所有迁移都无法执行。

R5 的改名草案没有应用。本轮提出更小的 `proposal/NOT-APPLIED-preserve-ledger-keys.patch`：只冻结这 25 组现存精确 stem 集合；任意新增重复、第三个 stem、历史 stem 被替换/删除均继续失败；129 以上的其他编号仍强制唯一。`git apply --check` 通过；未应用、未执行该提案，不把提案描述为已通过修复。

此提案会调整 R5 当前严格的编号唯一性约束，需队长核定。选择重编号则必须另交账本映射、旧库恢复/重放与条件迁移兼容方案，不能仅移动文件。候选 R6 对 `server/migrations` 和 `server/cmd/migrate` 的 Git diff 均为空；SHA256 清单附后。未修改历史账本，也未用改测试掩盖失败。

## 可复现验证

所有数据库操作显式使用本轮新建 `tes85_r6_assembly_0916`，未落到默认 `multica` 库。最终交付有 `REPRO.ps1`，要求调用者先准备显式命名的独立测试库。

| 验证 | 实际结果 |
| --- | --- |
| 隔离库 migrate up | 成功 |
| 原有 workspace 删除及 manifest | 12 PASS |
| 聚焦 handler（R5/R6/workflow/workspace） | 63 PASS，零失败/跳过 |
| 完整 workflow | 111 PASS，零失败/跳过 |
| 完整 handler | 2173 PASS、2 FAIL、47 SKIP |
| 最终 R6 新增回归 | 6 PASS（子例另计），零失败/跳过 |
| CLI 身份边界测试 | 2 PASS |
| go build ./... / go vet ./... | 成功 |
| 匹配 Windows server/CLI/migrate | 构建成功，三者 revision 等于固定候选，vcs.modified=false |

完整 handler 与全包 vet 在最后添加第六个“写入先赢”测试之前执行；生产代码与最终候选相同。之后六个 R6 回归全部执行通过，三份二进制从最终 clean commit 构建。未重跑标准完整 Go 脚本、race 或跨平台原生验收，不宣称全量绿色。

两项 handler 失败仍为 `TestStableIDSuffixMatchesDaemon`、`TestParseSkillArchive_RejectsUnsafeSkillMdPath`。R5 的 `TestWorkspaceDeletionManifestCoversPublicSchema` 失败已消除。这两项呈现 Windows 路径差异，未在本范围修订。

新增回归验证：七表数据清理及相邻租户保留；提交失败完整回滚；删除先赢时等待中的模板创建返回 404 且不留孤儿；写入先赢时删除等待并清扫刚提交数据；StartRun/取消/reconcile 等待删除锁；模板发布/保存/复制/播种等待删除锁。测试使用真实 PostgreSQL 和真实 handler/engine，任务进程仍不是原生 daemon 验收。

初次新测试因夹具缺 trace_position、工作区请求头指向默认夹具、StartRun 缺必填输入/字段名写错而失败；修正后重新执行。初次 sqlc 因草案 CTE 的 workspace_id 歧义失败，已限定表名并成功生成。上述调试失败未计入通过。交付 JSONL 去掉 Output 正文以避免夹具凭据/隐私信息，保留测试事件及计数；原始日志摘要独立保存，不能把这些脱敏事件流视为逐字原始 stdout。

## 原生 CLI→server→daemon/进程退出：仍阻断

已从最终候选执行原生 CLI 只读 auth status，退出 0，访问现有服务；这不是匹配候选 server 的端到端验证。未复制或替换凭据。匹配 CLI 在保留注入的任务身份/标记时，执行 `daemon start --foreground --no-auto-update --no-auto-reload` 返回：

> daemon start is not available inside a daemon-managed task

退出码 1，见 `native-daemon-boundary-final.log`。此前尝试带 `--no-task-claims` 返回 unknown flag；这个 R5/R6 CLI 不支持该参数，该次不是身份拒绝证据，日志保留以免混淆。没有任何新 daemon 成功启动，宿主 daemon PID 56568 保持运行。

需要独立操作员在合法的人类会话中运行隔离匹配 server/CLI/daemon，并交回：同一候选与数据库证明、原生任务 claim、父/子进程 PID/启动时间、匹配 CLI 取消关联 issue 的命令/退出码、server→daemon 取消事件、进程树消失、任务及 run 的持久终态、重启恢复与迟到完成不复活证据。现有 `issue status` 可用于取消入口；候选没有 `workflow` CLI 命令，不能交付虚构的 `multica workflow start` 步骤。工作流准备应走该候选支持的 UI/公开入口，由操作员在隔离环境完成。

本轮不删除 task marker、不清空身份变量、不读取人类 PAT、不改 CLI 限制，也不停止共享 daemon。CLI 边界回归通过只说明拒绝有效，不等于正常原生链路通过。

## 剩余阻断与交接

1. 队长核定迁移兼容策略；随附明确未应用的补丁，后续仍需升级/恢复独立复验。
2. 安排合法独立原生执行环境，补齐 daemon/进程退出证据。当前任务身份不能自行完成此步骤。
3. 完整 handler 两项失败、47 项 skip；标准全量和 race 未闭合。
4. R5 P0 独立审查仍属 TES-105 范围；本轮只读检查其状态仍为 in_review，未宣称已独立通过。真实 8 条归档、生产切换及原生验收门槛保持不变。

这是可审查的部分修复候选，P1/整项未完成。TES-85 置 blocked，等待上述必要决策和环境。
