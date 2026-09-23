TES-104 最终集成候选独立复验，2026-09-16

结论：固定候选 `4fc4d1ff2e9ab0688de33732f6c682ff6209cb64` 的本项 P0 限定审查及 R1/R2/R3 复验通过，TES-104 可推进 done。P1 标准全量、race 和整项发布仍不通过；本结论不授权发布、生产迁移、真实八条应用或 P2→P3 推进，`apply_allowed=false`。旧 P0 整体不通过的历史结论保留。

审查者 Review Agent（1168947c-cacd-499f-89a5-083e266eebbc）未参与候选实现。本轮未修改候选、未创建 PR、未推送或合并。TES-104/TES-107 的实时关联 PR 查询均为空，没有 CI 通过声明。

## 固定对象与范围

通过 Multica CLI 下载实际附件；源包 SHA256 为 `5ac03e42c8133694b96ca077afb9f6b7873c73eddc17ad1f16dc885c49fc51aa`，Windows 包为 `d397bcb9b085e6f75484bcd20f088d455bf4bfa5ec42a622f44713d570cd6d56`，均匹配送审值。源包 87 项、Windows 清单 1097 项逐文件核验通过，Windows 包的 1090 个 SQL 文件与最终 Git blob 一致（明确归一化 CRLF/LF）。

在独立本地 clone 导入最终 bundle，verify 通过且前置对象为 `9d18186e6e8cfa168d6053d33d652e86fadfc12b`；另导入旧 P0 和原 callback bundle。实际 Git diff 与包内五份精确 patch/文件清单一致：上游→最终 171 文件，旧 P0→最终 85 文件，R5→最终 19 文件，R6→最终 7 文件，R7→最终 5 文件。没有用末五文件差异继承旧 P0 的全服务结论。

原 callback `f932e6ee…` 与集成 `5154e560…` 的三文件补丁字节一致。三个程序的大小、SHA256 和构建元数据均匹配 CANDIDATE：完整 revision 为最终 SHA，`vcs.modified=false`、Go 1.26.6。

- migrate：`ce437a149e2456e2587118be77510fcc80e373f04978efa9de1fe1f4b9bd60f8`
- CLI：`82c5676e0033f11e662f602e59917c04149bcfa4119bedd3073007757ef1c178`
- server：`f0d298842464716ed4f3451f061593c8469ec5a0af519c9b52964b7f083757a4`

独立核实旧 P0/R5/R6/R7/最终的 `server/migrations` tree 均为 `44eff2b1cb922eb66e95ae825e97abd6a416b42c`，`server/cmd/migrate` 均为 `fbdd1e7d97f7e1a8b37e3ec0b66379db530d2d85`。最终 `server/internal/migrations` tree 与 R7 同为 `a36269b8283b5c69e8e661ba32ca2e00a7e8aaf1`，对旧 P0 的变化只有测试。go.mod/go.sum 与迁移器直接依赖的 attributionbackfill、chatoriginbackfill、dbstartup、logger、taskusagebackfill 也逐对象相等。完整映射见 provenance.json、final-checks.json。

## 本轮独立执行

Windows/amd64，Go 1.26.6，sqlc 1.31.1，PostgreSQL 17.10；使用同一 PostgreSQL 服务中的本轮专用新库，不宣称独立 PostgreSQL 服务。服务 system_identifier 为 `7667197984617005089`。测试子进程隔离 HOME、USERPROFILE、APPDATA、LOCALAPPDATA、XDG 配置/缓存与 Multica 配置目录，保留全部受管身份标记。没有启动或借用宿主 daemon。

| 检查 | 本轮结果 |
|---|---|
| go build ./... / go vet ./... | 退出 0 / 0 |
| handler/service/router 聚焦回归 | 121 顶层 PASS，802 子测试 PASS；无失败/跳过 |
| 完整 workflow 包 | 111 顶层 PASS；无失败/跳过 |
| 冻结/lint + TestTES85 专用回归 | 13 顶层、286 子测试 PASS；无失败/跳过 |
| 完整 internal/migrations 包 | 18 顶层 PASS；无失败/跳过 |
| 历史独立 R1/R2 与 callback overlay 探针 | 3 顶层、1118 子测试 PASS；无失败/跳过 |
| 六个错误前缀、一个字面正例 | 7 顶层 PASS；无失败/跳过 |
| builtin 模板、daemon 提示/输出、agent schema 补充回归 | 16 顶层 PASS；无失败/跳过 |
| 完整 handler 包 | 2177 PASS、0 FAIL、47 SKIP |
| 新库最终迁移器 up / 重复 up | 均退出 0；账本保持 545 条 |
| sqlc 在隔离副本重复生成 | 59 个生成文件匹配候选；加上原有 2 个手写辅助文件后，61 文件整体重复字节稳定 |
| 候选 git status / diff --check | 干净 / 退出 0 |

不同命令之间有重复覆盖，顶层和子测试也不能相加为独立案例数。674 个作者 callback 子用例包含在聚焦及 handler 命令中；历史独立 callback 矩阵的 1091 个子用例本轮通过 overlay 再执行，其余 27 个子用例为独立索引/开关边界。overlay 仅加入审查测试，不覆盖生产实现或既有测试。

47 个 handler 跳过项及原始输出见 tests.json/handler.log；涉及 Redis 条件测试和外部 Skills.sh 集成，未计通过。本轮没有执行标准全量或 race。

## 逐项判断

R1 通过。七组既有同名等价索引保留 OID，不因附着、旧 v1 标记或丢失 ledger 倒推本批所有权。候选十项迁移回归覆盖崩溃重试、受管无效索引恢复、未知无效索引保留；独立 overlay 又覆盖七组 CREATE 后未写标记、附着后未入账、旧索引标记，以及索引/约束标记丢失。被拒绝 down 的 catalog/ledger 快照保持相等。

R2 通过。独立复验未设置/true/0 开关均拒绝；合法隔离前缀与显式开关的正例通过。六类错误前缀分别为 `tes85Xp0Xreview0916`、`tes85Xp0_tes104`、`tes85p0_tes104`、`tes85_p0Xtes104`、`tes85_p0tes104`、`ordinary_tes104`，本轮均追加 `_independent_134845` 后建库；拒绝时 catalog/ledger 零改动。精确旧名称 `tes85Xp0Xreview0916` 已存在，首次保护检查在连接该库之前停止，未复用或删除它。该精确名称本身的成功拒绝沿用旧 TES-104 独立证据，不冒充本轮执行。

R3 通过。从固定 R6 Git SQL 文件独立枚举得到 25 组/63 完整 stem，与最终冻结表精确相等；最终对象也等于 TES-106 验收的 R7。核阅冻结代码及本轮回归，删除、替换、整组删除、追加均被拒绝；≥129 的其他编号仍保持唯一性，方向配对及数值别名保护保留。遵循已核定的精确冻结方案，没有要求改名或改写历史 ledger。

P0/service 增量通过本项限定复验。按 85 文件清单覆盖服务装配、共享权限判定、发布模板/intake、issue 取消及批量更新、workflow engine/reconciler、工作区七表删除、daemon 提示/输出、生成 SQL 与协议增量。重点核查和执行：

- handler 共用 AgentInvokePermitted；workflow 四类路由均校验可用性/授权，external run 同时核查发起者和负责人；未发布版本、身份/workspace 不匹配与禁用控制字段拒绝。
- 取消前置参数、附件、CAS、文本校验及批量原子性保留；事务提交失败回滚、等待 issue 锁期间新激活任务、完成先赢、无操作重放均通过。
- 工作区 KEY SHARE 围栏先于 issue→run→step；删除获取相冲突围栏并在同一事务清理七表。真实锁等待测试覆盖先删/先写、publish/save/copy/seed、engine start/cancel/reconcile、删除失败回滚及邻居数据保留。
- TaskService 停止发生在事务提交之后；停止失败保留取消意图，重建服务/reconciler 后恢复；这是数据库与服务级恢复，不是操作系统进程重启验收。
- callback 大小写、Unicode 折叠、转义及重复键拒绝和六类业务行零新增断言通过；合法嵌套数据、空控制及幂等身份稳定通过。
- Windows 路径比较保留完整目录/稳定 ID 后缀；ZIP 绝对路径增加 path.IsAbs 拒绝，原有安全测试及拒绝断言仍存在并通过。

## 历史证据沿用与补验界限

以下不是本轮重新执行。沿用前已核对 Git 对象、依赖、原报告及原始 JSON；R3 历史证据包内 29 项摘要也核验通过。

| 历史场景 | 沿用依据与本轮增量 |
|---|---|
| TES-106 旧账本 333→545 | 相同 SQL/runner/依赖和完整 stem，原 entries/applied_at、合成数据保留及重复 up 快照相等；本轮重跑冻结与迁移回归 |
| TES-106 完整 R6 545→545 | 相同迁移生产对象，原 public 行/ledger/catalog 保持及重跑相等；本轮不把该矩阵计为新执行 |
| TES-106 新库与重复 up | 沿用原全表/ledger/catalog 相等证据；本轮另用最终匹配 migrate 建新库并重复 up，确认 545 条 |
| 旧 TES-104 上游新库/已有库/完整 fork 备份恢复库 | SQL、runner、生产依赖不变；原迁移/备份恢复/业务行及受保护 down 证据可用于相同数据库迁移边界 |
| 固定旧应用 e44b6a8d… 实际 HTTP 读写、历史读取、停启、恢复 | 旧程序固定不变，迁移的 SQL/执行语义不变；原 old-app-results.json 包含业务操作与停进程结果，非仅 health；仅沿用旧应用数据库兼容边界 |
| sqlc 与最终新服务行为 | queries/generated 有增量，不能照搬旧结论；本轮重新生成两次并执行 build/vet、完整 handler/workflow 与聚焦服务回归 |

两处 issue_status 约束的 category 扩展和系统状态映射（含 NOT VALID）来源于固定上游 478，最终该 SQL 与上游逐字相等。历史 pg_dump 恢复还发生同一 CHECK 表达式括号规范化；不得据此宣称所有 schema 字节不变。旧应用运行中的业务/维护写入按历史报告披露，迁移阶段快照只出现 ledger 变化，不延伸为运行服务期间整库无写入。

上述沿用不证明最终新服务→旧服务完整切换、任意真实数据恢复或 daemon 消费链路。旧 P0 到最终新增的服务行为由本轮回归覆盖其限定范围，没有继承旧候选的全服务通过结论。

## 保留的失败、未执行项与下一阶段要求

工程标准 regular 日志仍为 6312 PASS / 308 FAIL / 176 SKIP，另有 274 个开始但未终结事件；agent 为 818 PASS / 177 FAIL / 101 SKIP。race 65 包链接失败、零用例执行。以上仅核阅工程证据，未本轮重跑，也没有把全部失败统一解释为环境问题。P1 需要逐项归因/修复后复验标准全量，并先证明原生 race 工具链可用。

合法 CLI→server→daemon claim/取消/父子进程退出/进程重启、Mac/Linux 原生、真实八条身份与历史/恢复快照、workflow 合成已发布 fixture/明确实例 ID、完整切换回退、生产授权仍未完成。这些是后续 P1/整项发布门槛，不在本项 P0 限定通过范围内；不能因此提前推进 P2/P3、两个产品提交和集成测试。

工程此前宿主默认配置可能被改写且无原始备份的问题仍须独立操作员核对可信备份；本轮不猜测或复写凭据。本轮主回归前后宿主默认配置的长度、mtime 和摘要均相等，全部受管身份标记保留。本轮创建的九个数据库均删除并核验不存在，候选 Git 工作树仍干净，主工作区原有 AGENTS.md 修改未动。

## 证据与复现

附件证据包包含本轮 provenance.json、final-checks.json、tests.json、supplement.json、实际命令日志、审查脚本与原样 overlay 探针；history-reference 中的文件明确标记为历史沿用，未计入本轮测试数。日志的本机路径和可能的连接串/凭据作脱敏，测试动作与错误保留。

还原 Issue 指定源包至 source、Windows 包至 bin、三个 Git bundle 至脚本所用的 history 目录，按 audit.py 校验固定对象；在本轮相同目录结构准备 candidate 后执行 run_review.py、supplement.py、final_checks.py。脚本继承受管身份，测试仅使用显式新建库；若指定名称已存在则停止，不允许复用。数据库登录取仓库既有测试默认配置，证据不包含产品凭据。

诊断记录保留：首个 bundle fetch 未指定 ref 而失败，补上真实 ref 后核验通过；精确旧负例名占用触发保护并改用唯一名称；sqlc 首次新目录缺两份手写辅助文件，确认后复制原文件，59 个生成文件与候选一致、61 文件两次重复稳定。这些准备/对照问题没有作为产品测试通过计数。
