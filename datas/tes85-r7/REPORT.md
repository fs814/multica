固定隔离候选：`38aad73c26fbb2064d32ff4a9631f3e96d474faf`，直接父提交为 R6 `fa698dcfc5f143a5c5c372944dbafb2b2ae2577b`。`apply_allowed=false`。

本轮完成已核定的迁移冻结规则；不是 P1 或整项发布验收。当前主工作区的源码历史与 R6 不同，因此从 R6 bundle 恢复了独立仓库，未把旧附件测试覆盖到主工作区。

## 变更与不变性

仅修改 `server/internal/migrations/migrations_lint_test.go`，新增 `migrations_freeze_test.go`，共 134 行新增、9 行删除。保留 R6 既有方向配对检查、文件扫描和 129 起唯一性边界，只对经核对的 232–255、284 精确集合加冻结例外。

- 25 组、63 个完整 stem 必须全部存在，且不能替换、删除或追加；整组消失同样失败。
- 129 及以上不在冻结表中的编号必须唯一。前导零数字别名不能绕过冻结。
- 新回归逐一覆盖全部 63 个 stem 的删除与替换、全部 25 组的追加与整组删除，另覆盖顺序、唯一新编号、重复新编号和旧边界。
- `server/migrations` 的 R6/R7 Git tree 同为 `44eff2b1cb922eb66e95ae825e97abd6a416b42c`；`server/cmd/migrate` 同为 `fbdd1e7d97f7e1a8b37e3ec0b66379db530d2d85`。SQL 内容、文件名、排序和 runner（含既有测试）完全未改。生产账本未读写。

`CANDIDATE.json`、精确补丁和源码 blob 可交叉核验；bundle 需要已有上游 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 对象，包含从该上游至 R7 的候选链，verify 通过。R7 仓库为 clean。

## 实际验证

| 检查 | 结果 |
| --- | --- |
| R6 原始编号 lint | 复现 25 组/63 stem 冲突失败 |
| R7 迁移 lint / 冻结回归 | 3 个顶层测试、272 个子测试事件 PASS |
| 聚焦迁移包复验 | internal/migrations 3 PASS；cmd/migrate 既有 TES85 10 PASS；零失败/跳过，合计 286 个子测试 PASS |
| go vet ./internal/migrations | 通过 |
| git diff --check | 通过 |
| R7 migrate 构建 | 通过；Windows amd64，Go 1.26.6，revision 与固定候选相同，vcs.modified=false |

另运行过未设置 DATABASE_URL 的完整 internal/migrations 包：3 PASS、15 SKIP；15 个数据库集成测试未计入通过。本轮没有运行完整 Go/handler/workflow、race 或原生 daemon 验收；R6 已披露的全量与原生阻断不能据此消除。

## 既有账本 upgrade / re-run

全部使用新建、明确命名的本机临时数据库，通过 DATABASE_URL 连接并记录 PostgreSQL 身份。没有使用产品库或默认 multica 数据库。旧库由 R6 已交付 migrate.exe 和原样 SQL 构建；该二进制 SHA256 与 R6 清单一致，并核对 clean R6 VCS 信息。

1. **截至 255 的旧账本升级**：R6 先执行原样 001–255 文件形成 333 条账本，植入合成 workspace/archived template；R7 执行完整原样迁移至 545 条。333 条既有 version 与 applied_at 全部保留，既有业务字段保留；63 个冻结 stem 全部入账。再次 up 后所有 public 表行摘要、完整账本与关系目录完全不变。
2. **完整 R6 账本升级**：R6 形成 545 条账本并植入合成数据；R7 up 和再次 up 均不改变所有 public 表行摘要、完整账本及关系目录。
3. **全新 R7 库**：完整 up 形成 545 条账本，63 个冻结 stem 全部存在；重跑后同样全部不变。

这证明本轮 lint 规则没有改变原 runner 的账本身份和升级行为。pending 场景只比较既有业务字段，允许后续历史迁移正常增列；全部完成后的重跑则比较完整行。关系目录检查包括 OID、名称、类型和注释，不应被理解为对任意历史产品数据的全面恢复验收。

所有本轮新建库均已删除，记录在 evidence JSON；未启动、停止或改动共享 daemon，未操作真实 8 条工作流。

## 已保留的调试失败与修正

- 初次部分升级探针直接比较整行 JSON，因后续合法增列导致断言失败；已改为比较升级前存在的业务字段，重新完成三种场景。首次日志保存在 `evidence/initial-probe/`。
- 三种场景全部通过后，附加旧 TES85 回滚回归在 `tes85_r7_*` 库运行时有 2 项被既有安全门拒绝：要求专用 `tes85_p0_*` 库。没有修改安全门或旧测试；在新建 `tes85_p0_r7_tests_*` 库重跑，10 项全部通过且清理成功。`database-results.json` 保留该组合命令的失败状态，三种升级场景的结果仍在其中；最终回归结果以 `migration-packages-final.json` / `.jsonl` 为准。
- 发布复现脚本将三种升级场景与专用回滚回归分开，以避免复现错误的库命名。复现需使用同一 R6 二进制附件；不含凭据，连接配置取候选测试中既有的本地开发默认值。

## 剩余门槛

迁移策略核定与实现阻断已解除；本轮交付可供独立审查。P1 完整装配、合法 CLI→server→daemon 的 claim/取消/父子进程退出/重启恢复证据仍缺独立操作员环境。R6 的两项 Windows handler 失败、标准全量/race 和跨平台原生验收未在本轮修订或复验。P0 独立审查也不由本轮代替。

`apply_allowed=false`、真实 8 条归档状态、原生验收和生产切换门槛全部保持。TES-85 整项仍因上述环境与验收缺口 blocked；不将迁移 lint 修复当作发布授权。
