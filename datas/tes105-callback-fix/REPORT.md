TES-105 callback 拒绝边界已在隔离候选中修复，等待独立复验；本次不是原 R5 或整体发布验收通过。

- 原候选：`e6865d16a9a8110c6c06db8fa9b0cfff94437308`，保持不变。
- 修复提交：`f932e6eea7b82631acf6cf02cbef33e2662e7e28`，直接以原候选为父提交。
- 范围：仅 intake 校验、回归测试及内置平台技能文档，共 3 文件；没有将当前主工作区的其他改动混入候选。

## 修复

`server/internal/handler/workflow_intake.go` 对已通过 JSON 语法/类型校验的请求逐个读取顶层键值，不再用会覆盖重复键的 map 做禁止项校验。使用 `strings.EqualFold` 与 JSON 结构体解码的大小写识别保持一致，覆盖 Unicode 等价大小写及 JSON 转义键。

七项禁止控制字段的每次出现都会检查。非空配置即拒绝，后续同名或不同大小写的 null/空字符串不能掩盖前面的禁止值。仅有 null/空字符串仍视为未设置，嵌套 payload 键仍作为普通业务数据。

## 验证证据

全部数据库测试显式使用本轮新建的 `tes85_fix105_callback_20260916`；从固定候选迁移成功，测试串行执行，结束后该库已删除。没有修改 CLI 身份、启动常驻 server/daemon、写产品库、发布或切换。

| 检查 | 结果 |
| --- | --- |
| 新增回归在原候选代码上执行 | 153 个子用例通过、521 个失败，确认原缺陷可复现 |
| 相同回归在修复后执行 | 674 个子用例全部通过，0 跳过 |
| 原始审查 overlay 探针 | 小写、全大写、混合大小写均 HTTP 400，新增 run 均为 0 |
| `go build ./...` | 退出 0 |
| `go vet ./...` | 退出 0 |
| `go test -p 1 ./internal/handler ./internal/service ./cmd/server -run 'TestR5\|TestWorkflow\|TestAgentInvoke\|TestBatchChildDone\|TestBatchUpdate' -count=1 -json` | 顶层测试 handler 64、service 34、router 1 全部通过，0 跳过 |
| `go test ./internal/workflow -count=1 -json` | 111 个顶层测试通过，0 跳过 |
| `git diff --check`、bundle verify | 通过；修复候选 tracked 工作区干净 |

新增回归使用真实 handler 与数据库，检查 issue、workflow_run、workflow_step_instance、workflow_event、agent_task_queue、issue_subscriber 六类业务行计数不变。合法 intake、嵌套 payload 保留、稳定 receipt 和幂等重放同时通过。计数包含表驱动子用例，不与聚焦套件重复相加。

首次修复后编译因缺少 bytes 导入失败，补齐后上述检查通过；不把该失败算作通过结果。完整 handler、标准全量、race 和原生端到端本轮未重跑。

## 交付与复现

附件包含本报告、单提交 patch、源码 bundle、REPRO.ps1、原审查探针、修复前后及相关回归日志和 SHA256 清单。日志已去除本机绝对工作区路径及数据库连接串。

源码 bundle 依赖上游对象 `9d18186e6e8cfa168d6053d33d652e86fadfc12b`（与原 R5 bundle 相同）。在拥有该对象的独立仓库中 fetch bundle 的 `refs/heads/tes105-callback-fix` 后 checkout 固定修复提交。也可仅将 patch 应用到原 R5 候选的新分支。

准备全新一次性 `tes85_*` 数据库，显式设置 `DATABASE_URL`，先在候选 `server` 下执行 `go run ./cmd/migrate up`，再运行附件 `REPRO.ps1 -CandidateRoot <候选目录>`。脚本检查修复 SHA 和隔离库名称，执行构建、vet、聚焦回归、原探针和 workflow 套件；不会回退到默认数据库。

Multica 关联 PR 列表为空；`gh auth status` 确认未登录，无法创建 PR，未验证远端 CI。附件是可审阅的本地候选交付，尚未合并或部署。

## 保留门槛

本次按用户“解决阻断项”指令提交修复，需由独立审查者复验新候选，不自签独立验收通过。TES-105 转入 `in_review`；父项 TES-85 不变。

此前 P1 问题不在本次修复范围：完整 handler 的路径后缀、unsafe SKILL.md 路径、七表删除契约三项失败，迁移编号重复，race 未执行成功，以及合法 CLI→server→daemon/进程树退出和原生验收缺失。这些门槛未解除。真实 8 条继续归档，`apply_allowed=false`，不批准 P1、发布或生产切换。
