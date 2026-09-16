TES-105 独立复验通过，结论仅适用于固定 callback 修复及本项 P0 回归范围。原 R5 不通过结论保留；不批准 P1、R7 后继集成、发布或生产。

审查者为 Review Agent `1168947c-cacd-499f-89a5-083e266eebbc`，与修复作者不同。候选为 `f932e6eea7b82631acf6cf02cbef33e2662e7e28`，直接父提交为 `e6865d16a9a8110c6c06db8fa9b0cfff94437308`。本轮没有修改候选代码。

## 来源核验

- 已读取当前 Issue、全部线程摘要及原审查/修复交付线程。附件均通过 multica attachment download 获取。
- 外层 SHA256 清单 3 项、包内清单 19 项全部匹配，包括源码 bundle、patch、三个源码文件及作者日志。附 verified-hashes.txt。
- bundle verify 通过；依赖对象 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 在本地可用。导入隔离副本，HEAD 精确匹配候选，工作树干净。
- 最终差异仅 3 文件：intake 实现、边界测试、内置技能文档。包内源码与 checkout 内容一致（排除 Windows checkout 的换行转换）。单独 patch 与提交的 stable patch-id 均为 `b94ab109e780fd869e4a4f16b4e7362106467304`。
- 原 handler 文件 Git blob 精确匹配父提交：`1e86b8ac18278a2c15969410e377220468de31b3`。
- Multica 关联 PR 返回空列表；GitHub CLI 未登录，无法核实远端 PR/CI。本轮为本地固定候选审查，没有创建 PR、合并或部署。

## 独立执行

Windows/amd64，显式使用新建隔离库 `tes85_review105_independent_20260916_1168947c`，由候选迁移成功。各测试命令依次运行；聚焦命令使用 `-p 1`，不使用默认产品库。结束后专用库已删除。

| 验证 | 本轮独立结果 |
| --- | --- |
| go build ./... | 退出 0 |
| go vet ./... | 退出 0 |
| 聚焦 handler / service / router | 顶层 64 / 34 / 1 PASS，0 FAIL，0 SKIP |
| workflow 包 | 顶层 111 PASS，0 FAIL，0 SKIP |
| 作者新增边界矩阵 | 674 子用例全部 PASS，0 SKIP；本轮实际执行，非仅核阅作者日志 |
| 原三种键名探针，修复候选 | 全部 HTTP 400，各新增 run=0 |
| 本轮独立扩展矩阵 | 1,091 子用例 PASS：1,088 拒绝探针、3 合法/重放场景，0 FAIL，0 SKIP |
| 原 R5 handler overlay 失败对照 | 预期退出 1：小写 400/run+0，全大写及混合大小写各 201/run+1 |

计数不跨层重复相加：674 已包含在聚焦 handler 的一个顶层测试内。作者的修复前 521 失败/153 通过日志仅核阅，未重跑整组；本轮另行执行上述原三探针失败对照。原生产 handler 通过 Go overlay 恢复，其他生产代码与原 R5 相同，候选 tracked 文件未改动。

扩展探针对七种控制字段逐字母改写大小写，覆盖 Unicode `ſ` 和 `K`、逐字符 JSON 转义、同名及不同大小写重复键、后续 null/空字符串掩盖、非空 NUL 字符串。每个拒绝均验证 HTTP 400，且 issue、workflow_run、workflow_step_instance、workflow_event、agent_task_queue、issue_subscriber 六类 workspace 业务行计数不变。合法空控制字段、嵌套 payload 和数组数据通过，幂等重放返回 200，receipt/run/issue 身份稳定、六类行无新增。

## P0 审查范围

`server/internal/handler/workflow_intake.go:87` 开始逐项读取顶层键，EqualFold 匹配解码后的键名；前置 json.Unmarshal 已校验整个 JSON，逐项检查避免后续重复键掩盖禁止值，嵌套值整体读取而不作为控制字段处理。本轮未发现该修复的阻断问题。

原 P0 实现未被此提交更改。独立复跑并核阅关键断言：

- 真实 router/Auth 拒绝未认证、机器身份、workspace 不匹配，保留发布版本与稳定 receipt 语义；四种 agent 路由策略均覆盖权限撤销和发起者/负责人准入。
- issue 取消保留参数、附件、CAS、文本前置校验；共享事务覆盖 run/issue/step/acceptance/event，拒绝及 commit 失败回滚断言通过。
- `engine.go:1369` 的 issue→run 锁序、step 后续锁定，与批量 issue 排序锁定一致。竞争场景、后继任务激活、完成先赢及幂等/no-op 回归通过。
- `CancelRunInTx` 使用调用者事务；TaskService 停止在提交后执行。停止失败保留 cancelled run 与关联活动任务，重建 reconciler 后收敛，不停止同 issue 无关任务。此为数据库/服务级证据，不等同于 daemon/操作系统进程退出验收。

## 复现与保留门槛

证据包包含测试动作日志、完整独立探针日志、原反例日志、退出码、清理证明、overlay 探针源码与 REPRO-INDEPENDENT.ps1。聚焦/workflow 日志只保留测试动作，去除应用输出；其他日志已去除本机路径及数据库连接串。准备全新一次性 tes85_* 数据库并显式设置 DATABASE_URL，在固定候选 server 下运行 `go run ./cmd/migrate up`，再执行脚本 `-CandidateRoot <候选目录>`。脚本尾部负对照应失败，须同时核对三项 HTTP/run 计数；结束后删除自己创建的数据库。

完整 handler、标准全量、race、原生 CLI→server→daemon/进程树退出，本轮均未执行。历史 P1 三项 handler 失败（路径后缀、unsafe SKILL.md 路径、七表删除契约）、迁移编号重复、race 链接失败及原生证据缺失仍未解除；不将历史或作者日志计作本轮通过。没有审查新的 R7 集成候选或生产环境。

最初 DB helper 从仓库根目录执行因缺少 go.mod 失败，未连接数据库；改为候选 server 模块目录后创建、迁移及测试均成功。Windows 沙箱进程启动错误 1385 后使用获准的沙箱外执行；没有清除 CLI 身份标记、修改共享 daemon 或产品数据。

TES-105 可推进 done，仅代表本固定修复通过独立复验。父项 TES-85 保持实施中，真实 8 条继续归档，`apply_allowed=false`。
