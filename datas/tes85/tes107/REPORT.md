固定集成候选已交付：`4fc4d1ff2e9ab0688de33732f6c682ff6209cb64`。
本项工程产物供独立审查；不签署 P0/P1、原生链路或发布通过。`apply_allowed=false`。

## 完成范围

R7 不含 callback 修复。已从独立 bundle 恢复 R7，在隔离仓库 cherry-pick
`f932e6eea7b82631acf6cf02cbef33e2662e7e28` 的三文件增量；原始与合入 diff
字节一致，R6/R7 测试全部保留。随后只修复任务明确授权的两项 Windows 失败：

- `server/internal/handler/agent_work_dir_test.go`：比较前对 daemon 路径使用
  `filepath.ToSlash`，继续断言完整目录及稳定 ID 后缀。
- `server/internal/handler/skill_import_archive.go`：对已规范化 ZIP 路径增加
  `path.IsAbs` 拒绝。Windows 的 `filepath.IsAbs` 要求卷名，原代码漏拒绝
  `/abs/SKILL.md`、`\abs\SKILL.md`、UNC 路径。原安全用例和其他拒绝断言未删改。

修复前在专用数据库复现两个顶层失败（14 通过、2 失败）；修复后完整 handler
2177 通过、0 失败、47 跳过。新候选比 R7 仅五文件变化；详细逐文件 diff、源码
blob、历史映射、bundle 前置对象及二进制摘要均见 `CANDIDATE.json`。

## 最终候选上的实际验证

环境：Windows amd64；模块内 Go 1.26.6（PATH 默认 Go 1.26.4 自动选择模块工具链）；
GCC/MinGW-w64 15.2.0；Clang 22.1.7 已存在，未安装系统依赖。数据库为本机连接的
PostgreSQL 17.10，四个唯一命名 `tes85_p0_r7_tes107_*` 临时库已全部删除，末次
`pg_database` 查询再次确认不存在。没有使用默认产品库执行用例或迁移。

| 验证 | 实际结果 |
| --- | --- |
| `go build ./...` / `go vet ./...` | 均通过 |
| 聚焦 handler/service/router/workflow 回归 | 123 顶层通过，零失败/跳过 |
| callback 控制边界 | 674 子用例通过，六类业务行零新增断言保留 |
| 完整 `go test ./internal/handler -count=1 -json` | 2177 通过、0 失败、47 跳过 |
| 标准全量中的完整 workflow | 111 通过、0 失败/跳过 |
| 标准全量中的完整 internal/migrations | 18 通过、0 失败/跳过 |
| 专用库冻结及 TestTES85 回归 | 13 顶层通过：3 冻结/lint + 10 既有迁移回归 |
| 最终标准 regular cohort | 6312 通过、308 失败、176 跳过；9 个包失败 |
| 最终标准 agent cohort | 818 通过、177 失败、101 跳过；1 个包失败 |
| `go test -race` 两个 cohort | 65 测试包链接失败；零测试用例执行，不能计通过 |
| 已安装 lld 替代链接探针 | runtime/cgo 失败，未生成/执行 race 测试程序 |
| server/CLI/migrate 构建 | 三者均为最终 SHA、Go 1.26.6、vcs.modified=false |
| 操作员 PowerShell 脚本 | Parser 语法检查通过；未启动外部验收服务/daemon |

标准全量遵循 `scripts/test-go.sh` 的两组包划分：regular 为 `go list ./...`
排除 `pkg/agent/...`；agent 使用 `-p 2 -parallel 2 ./pkg/agent/...`，增加
`-count=1 -json -timeout=5m` 以记录逐项结果。Windows 上以本机 EXE 哨兵替代
仓库 Bash 哨兵，覆盖全部 `scripts/agent-cli-command-names.txt`，阻止意外执行
真实代理 CLI。两轮各拦截两次 codex 调用，共四条记录；没有把被拦截的调用当成功。
未将 Bash `make test` 启动器或其默认数据库环境冒充已执行。

所有数字是已报告完成的顶层测试事件；包中未运行/未终结的测试不能由总数推定通过。
`TEST-RESULTS.json`、`INVENTORY.json` 和各命令 JSON/log 列完整失败、跳过原因及
未终结条目（最终 regular 274 个已开始事件未收到终结结果）。47 项 handler 跳过仍保留，不能计入完整功能通过。

## R6 事务、互斥、恢复证据

最终候选实际运行以下用例，全部通过（具体事件在 focused.log / handler.log）：

- `TestR6WorkspaceDeleteRemovesSevenWorkflowTablesAndPreservesNeighbor`：七表清空，邻居数据保留。
- `TestR6WorkspaceDeleteFailureRollsBackWorkflowRows`：提交失败时七表和 workspace 全保留。
- `TestR6TemplateCreateLosesToWorkspaceDeletion`：等待删除锁后重新检查存在性，404 且不留孤儿。
- `TestR6TemplateMutationsWaitForWorkspaceDeleteFence`：publish/save/copy/seed 等待相同围栏。
- `TestR6EngineCommandsWaitForWorkspaceDeleteFence`：cancel/reconcile/start 等待围栏，删除回滚后恢复。
- `TestR6WorkspaceDeleteWaitsForWorkflowWriterAndSweepsItsCommit`：删除等待写者，随后清理其提交。
- R5 前置校验/批量原子性/提交回滚、issue 锁等待期间新激活任务、完成先赢、停止失败后
  重建 engine/reconciler 的恢复用例均通过。后者是服务内重建证据，不是进程重启证据。

## 保留失败与环境要求

完整套件不通过，未扩修其余包：cmd/multica、internal/cli、daemon、execenv、
repocache、storage、wecom、service、realtime、pkg/agent。可见报错包括受管身份标记
导致人类命令拒绝、Windows HOME/权限位/打开文件删除差异、测试 Shell 可执行文件、
builtin SKILL frontmatter、realtime 注册时序。首次 cmd/multica 还达到五分钟包超时。
上述只是有日志支持的失败类别，不宣称所有失败均为环境问题或均不影响产品。
未对这些分支提出或应用范围外产品补丁；需后续按逐文件失败证据核定修订范围。

首次全量的宿主配置隔离偏差必须披露：既有测试仅设置 HOME，Windows 某些代码仍
解析 USERPROFILE；观察到宿主默认 `.multica/config.json` 修改时间为
2026-09-16T13:07:23Z，与该轮测试一致，现有 73 字节，键为 server_url/token。
未记录或输出配置值，事前没有备份，无法证明或恢复原始内容；其内容未完全匹配静态
测试字面量，不能断言所有字段来源。请操作员核对并从可信备份恢复该默认配置，勿用
本报告猜测凭据。命名 profile `desktop-localhost-8081` 的宿主 daemon 只读检查仍为
running（PID 56568），本轮没有停止或重启它。

已为第二轮测试子进程单独设置 USERPROFILE 与 task config root，保留所有受管身份
标记，并保持 Go 缓存路径。第二轮后宿主默认配置时间/长度未再变化；安全隔离后的
结果才是上表的最终全量结果。首次完整日志仍在 `evidence/initial-full/`，不隐藏该偏差。

race 的默认 GCC 链接输出为 `collect2.exe: error: ld returned 48 exit status`；
已安装 lld 探针也失败。最小外部要求是独立原生 Windows 测试账户、Go 1.26.6、
可通过最小 `go test -race` 探针的 GCC/MinGW-w64 工具链、独立 PostgreSQL 及隔离
用户目录。先证明工具链，再运行本包完整命令；不需要也不允许借用受管任务身份。
Linux/macOS 则需要本平台 Go/编译器及数据库原生运行，交叉构建不能替代。

## 固定版本和审查交接

R5 `e6865d16...` → R6 `fa698dcf...` → R7 `38aad73c...` →
callback 合入 `5154e560...` → 最终 `4fc4d1ff...`。callback 原分支为
`f932e6ee...`，直接基于 R5；原候选全部保留。

旧 P0 `0ca4c63c...` 不是最终候选祖先；两者精确差异为 85 文件。
但 `server/migrations` 和 `server/cmd/migrate` 的 Git tree 在旧 P0/R5/R6/R7/最终
完全相同。R1/R2/down 保护因此有逐对象沿用依据，本轮又实际执行十项专用回归。
最终 `internal/migrations` tree 等于 R7；TES-106 已独立通过的三类 ledger
升级/重跑矩阵只作为历史证据沿用，本轮没有重演那三类矩阵、旧应用 HTTP/备份恢复或 sqlc。

TES-105 在本轮期间已 done，独立评论 `01a0aa58-2236-76eb-9536-7aaf91ce0ec4`
接受 callback 原分支及其 P0 回归（674 作者子例、1091 新探针）；不等于集成 SHA 通过。
`REVIEW-HANDOFF.md` 给出 PM 重绑 TES-104 至最终 SHA、更新材料并送 in_review 的具体增量。
本轮不改 TES-104 状态，不替代独立审查。

| 门槛 | 当前状态 |
| --- | --- |
| R7 冻结、独立 callback 子项 | 原固定范围已独立通过；最终集成仍待审 |
| R6 七表/互斥/恢复、两项 Windows 失败 | 本候选工程回归通过，待独立接受 |
| P0 最终整体 | 待 PM 核对并重绑 TES-104，未放行 |
| 标准全量 / race | 失败 / 链接阻断，无全绿声明 |
| 合法 CLI→server→daemon、父子进程退出/重启恢复 | 操作单已交付，外部结果 NOT EXECUTED |
| Mac/Linux 原生、真实 8 条身份及历史/恢复库快照 | 未执行/未获输入；原门槛保留 |
| P2/P3、两个产品提交、集成测试、生产切换 | 不提前推进；不发布模板、不 apply、不生产迁移 |

附件包含增量 bundle/精确 patch、匹配 Windows 二进制、逐文件 SHA256、脱敏日志、
REPRO.ps1、外部 Operator.ps1/HoldTree.ps1 和操作单。GitHub CLI 未登录，未创建 PR、
未核验远端 CI；本轮仅交付隔离候选，未推送或合并到产品分支。
