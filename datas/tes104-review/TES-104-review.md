TES-104：固定 P0 R1/R2 独立复验报告（2026-09-16）

结论：R1、R2 在本次固定候选的隔离范围内关闭；P0 整体不放行。新增阻断 R3：迁移编号唯一性静态测试失败。TES-104 应退回 in_progress；父项 TES-85 保持 in_progress。本报告供父项汇总，不将 P1/R5 或完整切换回退计为通过。

审查对象与证据真实性

- P0：0ca4c63c0dd02e00c2fdacc9d8c4f480e291f815。与修复提交 361dfb2f2 的差异仅 server/cmd/migrate/README.md。
- 上游：9d18186e6e8cfa168d6053d33d652e86fadfc12b；完整旧 fork：e44b6a8d70b826ff368390cab62de5bbffb7167e；归档：1ef4de983ea4f0220a363583f20bef3f24152fcc。
- 实际 source ZIP SHA256：7136870f384a973d2f47a60dc05d8e2ce4fe83102785032fb79a890b9d8e468c；Windows ZIP：6c2645498e55d79e5f31c4884125be2a88bd0ee7940003d93bdad161a3f1824c；两者均与归档 Git blob 一致。旧包摘要 857c5b6cc69dad16566640edcdd975c33fde3d38025c4d90aadb7483088cecbb 匹配。
- source 内层68项、旧包49项逐文件校验通过；两个 bundle verify 通过；P0 修复补丁及10文件范围与固定对象一致；1090个迁移文件摘要匹配（明确处理仓库 LF / Windows CRLF）。
- 五个附件程序的大小、SHA256、完整 VCS revision 和 vcs.modified=false 全部核验。本轮实际执行的 P0 迁移器摘要 b36aef3919ed2e9a3b46e0b687b9c868c75ff3486ff8018e32ea34962c9bf35d；旧 server 摘要 5804e7a1bd8d8486567894633e201daa22ab3591264502c80666154e8b46f283。P1 三程序仅核验元数据，没有执行或验收。
- 本轮 Go 1.26.4 windows/amd64、sqlc 1.31.1、PostgreSQL 17.10。附件程序元数据显示 Go 1.26.6；不宣称本机按同一工具链字节复建。
- TES-104 与 TES-85 的 multica issue pull-requests 均返回空列表。gh 无登录；没有 PR/CI 通过或合入证明。本轮未修改产品实现、无需实现 PR。

R1：关闭（隔离回退来源边界）

原 TestReviewerPreexistingUniqueIndexOwnership 已原样独立重跑通过。七组 workflow_template、workflow_template_version、workflow_run、workflow_step_instance、workflow_submission、workflow_acceptance、workflow_event 的既有同名等价唯一索引均保留原 OID，附着不获得本批所有权；显式 down 被拒，catalog/ledger 不变。

另新增独立 TestReview104UnmarkedCrashAndLegacyMarkers：七组各跑 CREATE 后未写标记、附着后未写 ledger、索引只有旧 v1 标记三场景（21个叶子用例），确认不会倒推所有权；均拒绝 down 且 catalog/ledger 不变。TestReview104ExplicitSwitchAndOwnershipLoss 覆盖开关未设/true/0、索引标记丢失、约束标记丢失、两者均旧 v1；全部拒绝且无改动。

候选原有用例独立执行通过，覆盖本批新建的 up/down/up、丢失 ledger 后重试、由本次 CREATE 唯一冲突留下并标记的 invalid index 恢复、未知 invalid index 保留、已有主键/列和错误形状拒绝。未知 invalid index 的 up 保留断言只覆盖被检查对象；本报告不把整个失败 up 批次宣称为事务性零写入。零改动结论对应有快照断言的受保护 down。

依据：server/cmd/migrate/main.go 的 tes85CreateIndex/tes85IndexHook/tes85EmptyPrimaryRollback，以及七个255主键附着文件。新 v2 来源同时检查索引和约束，旧约束注释不足以授权删除。未写标记的崩溃窗口保守保留对象，需要人工核实，不能伪造标记。

R2：关闭（字面库名前缀）

原 TestReviewerLiteralDatabasePrefix 在精确库名 tes85Xp0Xreview0916 上原样通过；候选实际使用 starts_with(current_database(), 'tes85_p0_')。另实测 tes85Xp0_tes104、tes85_p0Xtes104、tes85p0_tes104、tes85_p0tes104、ordinary_tes104 均拒绝，catalog/ledger 不变；tes85_p0_tes104_review 正例允许已拥有空 schema 的 key/hash down。

七个库均在本轮独立临时 PostgreSQL 容器中建立；原共享容器已有同名复现库，未读取或修改它。精确错误名上两个顶层测试通过，其余五个负例各一项；正例运行9个候选顶层测试、1个原 R1 复现和2个新增审查测试，共12通过、0失败、0跳过。不同库的重复执行不计为19个唯一测试。

独立迁移、生成与旧版业务验证

- 上游新库、已完成固定上游迁移且含合成业务行的库、完整固定 fork schema 的自建库备份恢复：三类 up、重复 up、默认整批 down 拒绝通过。已有业务表行摘要保持，重复 up 包括 ledger 保持；拒绝 down 的全表行摘要/约束保持。fork 是完整 schema 的合成库备份，绝非真实生产备份。
- 三类均做 pg_dump/pg_restore 数据复验；fresh/existing 额外验证恢复后的重复 up。fork 同一备份另恢复到固定上游对照库，候选与未修改上游最终约束完全相同。
- 两处状态约束确实改变：category 扩展及系统状态映射改为包含 legacy/category 对应关系并带 NOT VALID，与固定上游478迁移一致。不得宣称升级前后约束字节不变。
- fresh/existing 的 pg_dump 恢复将同一 CHECK 的嵌套 OR 重排为扁平 OR。逐项值/顺序/NOT VALID 保持，完整前后字符串已保存；数据摘要无差异。本轮最初过严的字符串相等断言失败，定位后只对该已核对表达式允许括号规范化，其余约束仍精确比较。
- sqlc 实际生成与重复生成通过，61个输出文件重复字节稳定；初次相对 checkout 只有 CRLF→LF 变化，git diff --ignore-space-at-eol 为空。generated DB / migrate 构建及 vet 实际通过，命令记录见 commands.jsonl。
- 固定旧版实际启动三次，每次 health 完整 commit 与二进制 metadata 对应；验证依赖真实认证 HTTP，而非只看 health。迁移前创建/发布 end-node 模板、执行完成 run、读取历史；停进程后 P0 up，再启动同一旧版，读取旧模板/run、验证 issue description 写入结果、新 run 完成；再次停进程后验证默认 down 非零且全部表行摘要和 catalog 不变。恢复迁移前备份后全表摘要与备份相同，旧版重新读取历史 run，issue 原 description 精确恢复。
- 8条合成归档输入实例与1条暂停模板绑定，在迁移、旧版业务执行及恢复各阶段逐行 JSON 保持。旧版没有后续 autopilot instance-binding 列，本轮不声称这些列、历史实例运行快照或真实8条身份通过。
- 旧版启动后的实际业务写入涉及 issue、workflow_run/step/event、inbox、activity、subscriber、workspace、PAT 使用记录；old-app-results.json 给出各表前后摘要。首次启动日志同时记录 rollup_task_usage_hourly 调度完成，rows_affected=0；这不表示维护记账零写入。本轮迁移快照取在首次旧服务停止后，迁移阶段已有表仅 ledger 变化；后续短时重启没有重现旧包两张维护记账表再次变化。旧包维护表差异仅作为工程历史证据核阅，不提升为本轮逐字段因果复现，不报整库不变。

R3：保留，P0 放行阻断（迁移编号冲突）

独立命令：在固定 P0 server 下运行 go test ./internal/migrations -count=1 -json。
实际结果：TestMigrationNumericPrefixesAreUnique FAIL，38条冲突；匹配方向测试通过，15个需额外DB开关的无关测试跳过。固定上游同命令2通过、0失败、15跳过，证明该静态回归属于候选增量，而非上游既有失败。

位置：server/internal/migrations/migrations_lint_test.go:35；server/migrations/232_workflow_template.up.sql:1；254_tes85_workflow_* 与255_tes85_workflow_*；284_workflow_template_revision.up.sql:1。
实例：232_workflow_template 与232_channel_media_pending_object_due_index；254_tes85_workflow_template_id_index 与254_runtime_profile_add_reasonix；255_tes85_workflow_template_pkey 与255_agent_task_queue_chat_pending_deferred_v3。冲突涉及232–255、284，其中254/255各承载多项新迁移。

影响：直接违反仓库129起编号唯一性测试，迁移静态检查/完整测试门禁失败，并使按编号定位迁移有歧义。三类数据库 up 成功不能抵消此失败；这不是重新打开已验证的 R1/R2，也不声称本轮已经观察到数据损坏。父项 R5 工程说明也提到继承编号问题；本结论是对本次固定 P0 的独立实测，未应用 R5 的提案。

修复要求：按允许范围提供唯一编号的新固定候选，同步 migration hooks/conditions/测试/sqlc 输入；为已经写入旧完整 stem 的 ledger 明确兼容/升级策略，不得简单删 ledger 或重命名后盲目重复 DDL，也不得放宽/跳过 lint 来掩盖冲突。补齐上游、已应用旧 P0 和完整 fork 恢复库的重复执行/保护回退验证，再送审。审查者本轮不代做范围外迁移重构。

默认 cmd/migrate 包另独立运行32通过、0失败、12跳过；数据库专用12项零跳过结果另列，不混为“全部测试零跳过”。没有全服务测试通过结论。

完整切换回退仍缺的条件

1. 关闭 R3，重新固定源包、迁移器与摘要，确认新编号对既有 ledger 的处理。
2. 完整匹配新 server/CLI 与合法任务身份的实际 daemon 消费、任务失败/取消/重启/丢失终态恢复，以及新服务切换到旧服务的演练。本轮仅旧版本兼容及备份恢复，不是完整新服务切换。
3. 真实8条原始键、归档/输入/历史快照/绑定身份，在匹配只读能力和恢复库中验收；随后按原门槛决定零迁移或逐ID CAS。合成8条不能替代；apply_allowed=false，真实8条继续归档。
4. callback/debug 旧功能回退、Mac/Linux原生及生产切换门槛仍按原要求保留。TES-105/R5 的权限/取消验收属另一固定候选，本轮不覆盖或批准。

清理及复现

本轮最终矩阵7库、旧版2库已清理，精确前缀测试的7库及临时容器已清理，3个旧服务子进程已停止并 wait。未替换现役服务，未执行生产迁移或绕过 CLI 身份限制。沙箱账户报 Windows1385，使用获准的执行通道完成操作。

附件 ZIP 收录本轮测试源码、脚本、实际日志、结果与清单；工程附件保留为输入，不混入本轮通过数。诊断过程中出现一次库名已存在、一次误写包路径 ./pkg/migrations 和过严 CHECK 文本断言，均明确定位和修正；没有把这些失败计为通过。主工作区保留原有 AGENTS.md 修改；仅增加本次审查资料。
