TES-106 独立复验通过，仅覆盖 R7 迁移冻结子项。审查日期：2026-09-16。

固定 R7：`38aad73c26fbb2064d32ff4a9631f3e96d474faf`。
直接父提交 R6：`fa698dcfc5f143a5c5c372944dbafb2b2ae2577b`。
`apply_allowed=false`；本轮未修改候选、产品库或共享 daemon。

## 来源与范围

从父项指定评论通过 Multica CLI 下载报告、补丁、源码证据和摘要。3 个外层 SHA256 全部匹配；ZIP 内 MANIFEST 的 31 个文件全部匹配。R7 bundle 在具备指定上游对象的独立仓库通过 verify，恢复的提交直接父节点为固定 R6。

附件补丁与实际 `git diff R6 R7` 字节一致，两份源码附件与 Git blob 字节一致。实际差异仅为 `server/internal/migrations/migrations_lint_test.go` 和新增 `migrations_freeze_test.go`。原有文件扫描、方向配对和 129 起唯一性边界保留，没有用旧附件覆盖较新测试。复验前后候选 `git status --porcelain` 均为空；主工作区原有 AGENTS.md 修改未动。

R6/R7 不变树：

- `server/migrations`：`44eff2b1cb922eb66e95ae825e97abd6a416b42c`。
- `server/cmd/migrate`：`fbdd1e7d97f7e1a8b37e3ec0b66379db530d2d85`。

因此 SQL、迁移文件名/排序、runner 和账本契约完全未变。本项及父项的 `multica issue pull-requests` 均返回空列表；没有关联 PR/CI 可验收，本轮不修改产品代码、不创建 PR。

## 独立验证结果

直接从 R6 Git 对象枚举所有 >=129 的重复编号，独立得到 232–255、284 共 25 组、63 个完整 stem；与 R7 冻结表及附件碰撞清单逐项一致。

审计实现确认：冻结表逐组遍历，整组消失也会失败；排序后的实际集合必须精确相等，拒绝替换、删除及追加；其他 >=129 编号仍拒绝重复。前导零别名按数值归组后仍被拒绝。方向配对检查保留。

在固定 R7 原样源码执行：

| 检查 | 本轮结果 |
| --- | --- |
| `go test ./internal/migrations -run 'TestMigration|TestHistoricalMigration' -count=1 -json` | 3 顶层 PASS、272 子测试 PASS，零失败/跳过 |
| 专用库执行 `go test ./internal/migrations ./cmd/migrate -run 'TestMigration|TestHistoricalMigration|TestTES85' -count=1 -json` | lint 3 + 既有 TES85 10 顶层 PASS、286 子测试 PASS，零失败/跳过 |
| `go vet ./internal/migrations` | 退出码 0 |
| `git diff --check R6 R7` | 退出码 0 |
| 固定 R7 migrate 构建 | 成功；VCS revision 匹配，vcs.modified=false |

272 个子测试事件包含分组节点，不等同于 272 个互相独立的断言案例。冻结回归实际逐项检查 63 次删除、63 次替换、25 次整组删除、25 次追加，并检查顺序、边界、新编号与数值别名。

## 真实临时 PostgreSQL 复验

本轮审计后复用交付脚本，只改本地目录及本轮专属数据库名前缀；改后脚本也在证据包中。PostgreSQL 17.10，system_identifier `7667197984617005089`。使用同一服务中的新建隔离数据库，并非独立 PostgreSQL 服务。`postgres` 管理连接仅用于服务器身份、创建/删除本轮库和最终清理核查，产品库未连接。

旧账本由下载的固定 R6 二进制生成；ZIP 摘要匹配 R6 清单，二进制 SHA256 为 `e6dbc4953e57b3f9e914c1eb52651477a005f35c34995ab2271f32891989b05e`，VCS 为 clean R6。R7 二进制本轮重新构建。

- 截至 255 的 R6 部分账本：333→545 条；原 version/applied_at 和植入的合成业务字段保留，63 个冻结 stem 全部入账。
- 完整 R6 账本：545→545 条；升级前后 public 表行摘要、账本、关系目录不变。
- 全新 R7 安装：545 条，63 个冻结 stem 全部入账。
- 三种场景再次 up，均保持 public 表行摘要、账本和关系目录不变。

创建的三个 `tes85_r7_tes106_*` 库和一个 `tes85_p0_r7_tes106_*` 回归库均已删除；最终再次查询 pg_database，确认这四个精确库名均不存在。数据库结果与清理结果见 `results/database-results.json`、`results/migration-packages-final.json`、`results/review-summary.json`。

## 原始证据的失败与限制

原始完整迁移包日志确有 3 PASS、15 SKIP；跳过的 15 项未计为通过，也未被本次聚焦命令覆盖。原探针因后续合法增列而失败、旧回滚回归因数据库前缀不符而失败的记录均保留；修订为比较升级前已有业务字段并使用合法专用库后，本轮重新通过，不依赖失败命令的退出码作成功证明。

上述数据库验证只覆盖合成数据、指定迁移历史与重跑行为；行摘要及关系目录比较不等于任意产品数据或完整 schema 等价证明。

P1 完整装配、合法 CLI→server→daemon 的 claim/取消/父子进程退出/恢复、完整 Go/handler/workflow/race、跨平台原生验收、真实 8 条和生产切换均未在本轮复验或放行。R6 已披露的两项 Windows handler 失败仍未由本轮解决。父项 TES-85 保持 blocked，`apply_allowed=false` 保持。

## 可审计材料

附带证据 ZIP 包含本轮原始测试/迁移日志、Git/摘要核对 JSON、数据库场景与清理结果、构建元数据，以及审查和复现脚本；SHA256SUMS.txt 可核对包内文件。脚本依赖本 Issue 已指定的 R6/R7 附件及本地 Go/PostgreSQL 环境，不包含凭据或产品数据。
