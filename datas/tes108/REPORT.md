TES-108 已完成本地复现、race 链接归因和阻断交付；标准全量仍不通过，P1 与发布不放行，`apply_allowed=false`。

固定源码仍为 `4fc4d1ff2e9ab0688de33732f6c682ff6209cb64`。没有修改产品代码、测试断言、迁移或身份校验，也没有制造新的空提交候选。当前证据没有建立“由此候选引入且可在本项授权内最小修复”的源码缺陷；按任务允许的阻断交付分支，保留原候选，交付环境修正与后续工程清单。包内的零字节 `candidate-to-itself.patch` 明确表示源码零增量；原 upstream→candidate 精确补丁和增量 bundle 一并附上。新交付包由独立 SHA256 锁定，不覆盖 TES-107 材料。

## 本轮实际结果

Windows amd64，Go 1.26.6，CGO=1，GCC 15.2.0，GNU ld 2.46，Git 2.54.0.windows.1，PostgreSQL 17.10。原始命令、开始/结束时间、退出码、逐测试记录均在 `evidence/*/results.json` 与 `.log` 中。标准组包划分与 `scripts/test-go.sh` 相同，增加 `-count=1 -json -timeout=5m`；agent 使用 `-p 2 -parallel 2`。未声称执行了 Bash 启动器。

| 执行 | 通过 | 失败 | 跳过 | 未终结事件 |
|---|---:|---:|---:|---:|
| 最终 build / vet | 两命令退出 0 | 0 | 不适用 | 0 |
| 最终完整 handler | 2177 | 0 | 47 | 0 |
| 最终 standard regular | 6306 | 315 | 175 | 274 |
| 最终 standard agent | 818 | 177 | 101 | 0 |
| 最终 race regular | 6307 | 314 | 175 | 274 |
| 最终 race agent | 818 | 177 | 101 | 0 |

数字为顶层测试终结事件，未终结和跳过绝不计通过。race 已执行用例，不再是链接失败；这不代表完整 race 通过。regular/race 差一项是 `TestHub_ClientRegistration`：标准失败、race 通过，属于时序波动，未据此豁免。

本轮保留三轮，避免混淆环境变化：

| 轮次 | regular 通过/失败/跳过 | agent 通过/失败/跳过 | race |
|---|---|---|---|
| baseline：原 PATH，长隔离 TEMP | 6260/349/187 | 818/177/101 | 64+1 包链接失败，0 用例 |
| corrected：MinGW bin 优先，较短隔离 TEMP | 6297/324/175 | 818/177/101 | regular 6297/324/175，agent 818/177/101 |
| final：另加隔离 HOME 内 Git longpaths | 6306/315/175 | 818/177/101 | 上表最终结果 |

三轮 regular 均有相同 274 个未终结事件。历史 TES-107 的 6312/308/176 和 agent 818/177/101 只作为输入比较，不冒充本轮运行。`evidence/analysis/*-delta.json` 逐项列出历史与本轮差异。最终 315 与历史 308 的差异为 7 项 repocache 测试；不是把历史 308 原样重报。

## 65 包 race 链接失败：已精确归因并本地解除

原 PATH 的 GCC 调用 `mingw64/x86_64-w64-mingw32/bin/ld.exe`，该程序连 `--version` 都以 `0xc0000130` 退出；Go 包显示 `collect2.exe: error: ld returned 48 exit status`。同机另一个 `mingw64/bin/ld.exe` 文件 SHA256 完全相同，却可启动。

DLL 搜索发现较早的 GnuWin32 目录含旧 `zlib1.dll`，而链接器导入该名称。旧 DLL 的 PE 签名实际位于 126，DOS 头声明位置为 128，实际 machine 为 0x14c；MinGW 同名 DLL 为 amd64。仅从探针 PATH 移除该旧目录，链接器立即退出 0；仅将已安装 GCC 所在 bin 前置，也恢复正常。独立于项目的 C 链接和 Go race 用例，原 PATH 都失败，前置 MinGW 后都通过。

因此这是本机 DLL 搜索冲突，未建立项目依赖或 Go 源码问题。已将修正限定到测试子进程 PATH，随后完整两组 race 实际执行。未安装、替换或卸载系统工具链，未改系统 PATH。证据：`evidence/toolchain/`，包括原始错误、最小 C/Go 源码、DLL 摘要和单目录排除对照。复现最低要求是现有 GCC 及其配套 DLL 优先于旧 GnuWin32 目录；无需笼统要求安装另一套编译器。

## 274 个事件及源码归因

274 个事件全部属于 `internal/daemon/execenv`，且全部已经 `run` 后 `pause`、没有 `cont`。它们没有真正恢复执行，不是 274 个已完成测试，也不是 274 个独立死锁。

触发中断的 `TestPrepareOpenclawConfigExpandsTilde` 只把 HOME 指向测试目录；Windows home 解析仍取 USERPROFILE，导致 `$include` 不存在，测试对 nil 直接断言为 `[]any` 后 panic。固定上游和候选均复现。诊断 overlay 仅补上 `t.Setenv("USERPROFILE", fakeHome)`，原断言完全保留，该单用例通过。此 overlay 只证明触发原因，未应用到候选，不能用来声称 274 项已经执行。下一项应全面审计 HOME/USERPROFILE 测试夹具，再运行完整 execenv 包。

固定上游 `9d18186e6e8cfa168d6053d33d652e86fadfc12b` 上，本轮完整 agent 为 **817/177/101**；177 个失败名称与候选 **逐项完全相同**。候选新增 `TestCodexOutputSchemaIsAnObjectOnTheWire` 单独通过，对应候选多出的一个通过项。见 `evidence/diagnostic-agent/comparison.json`，不是仅凭数量相同归因。

cmd/multica、internal/cli、execenv、repocache、wecom、storage、realtime 七个目录的 Git tree 与固定上游完全相同。上游最小探针另外复现了 tilde、POSIX 权限、打开文件删除、Git 长路径错误。全部最终失败测试所在的测试源文件未在候选相对上游增量中改变；这只是辅助证据，不能单独证明受修改代码调用影响的测试无回归。完整模块对象对照在 `evidence/source-parity.json`。

## 保留阻断与建议的下一子项

每个失败名称、源文件、原始断言、分类及每项后续建议见 `FAILURES.csv` 和 `evidence/analysis/final-*.json`。分类中的“unresolved”明确表示未建立精确根因，不作为环境豁免。

| 阻断 | 最终标准失败数 | 证据与下一项 |
|---|---:|---|
| cmd/multica | 182 | 受管身份、配置路径和输出/隔离断言混合；单独小集合在另一隔离夹具下可通过。需独立操作员在无宿主任务标记的专用测试账户运行完整包并审计顺序依赖；本项不删除标记、不借用凭据。 |
| internal/cli | 6 | HOME 与 Windows USERPROFILE 不一致及 POSIX mode 断言；实现 Windows 夹具隔离/等价 ACL 验证，保留安全要求。 |
| internal/daemon | 58 | agent 可执行夹具、任务配置路径、Git cache 依赖等；拆出 Windows 原生假程序及配置隔离专项，按逐项证据处理。 |
| internal/daemon/execenv | 11，另274未终结 | HOME-only panic、权限、并发 rename 等；先修隔离夹具并恢复完整包执行，再分别评估 Windows 文件替换缺陷。不能删断言或直接全部 skip。 |
| internal/daemon/repocache | 46 | 长路径、fetch refspec/not-in-git-directory 等；longpaths 仅消除部分失败，不能把剩余当作已修复。需 Windows 原生路径规范化和独立 checkout 夹具专项。 |
| internal/integrations/wecom | 6 | 打开文件后 unlink 在 Windows 失败，是既有产品可移植性缺陷；另项设计保持私密性与可靠清理的 Windows 等价实现。 |
| internal/storage | 1 | 世界可读文件 mode 断言在 Windows 不成立；需要平台等价访问权限验证，不弱化安全断言。 |
| internal/service | 4 | CRLF 嵌入技能资源破坏 frontmatter；Git 原始 LF 资源 overlay 下四项全通过。后续固定发布构建的换行约束，或另审解析规则。 |
| internal/realtime | 1 | 50ms sleep 后注册数为0，race轮通过；需事件同步测试替代时间假设，保留注册行为断言。 |
| pkg/agent | 177 | 169 项包含 Windows 可执行/夹具路径错误，其余8项包括进程树、secret-bearing config mode、模型目录和路径格式；全部与上游同名失败。建议原生假进程夹具专项，进程树和权限单列安全验收。 |

换行说明：仅设置 `core.autocrlf=false` 未能在 Windows `text=auto` 下保证 LF，本轮最终全量的实际工作树仍为 CRLF；绝未把它称为 LF 全量通过。四项 LF 成功属于单独嵌入资源 overlay，记录在 `evidence/lineendings/lf-skills.log`；初次漏含 legacy 资源的失败探针也保留。若独立操作员需要 Git 原始换行检出，应同时设置仓库局部 `core.eol=lf` 并验证字节，再另行完整复验。

## 隔离、清理与边界

- HOME、USERPROFILE、APPDATA、LOCALAPPDATA、XDG config/cache、task config root、TEMP/TMP、数据库和保留的 loopback 端口均为本轮隔离资源。保留任务/agent ID 和工作目录身份标记；只给测试子进程不可用测试 token 和隔离端点。真实 agent CLI 名称由 EXE 哨兵拦截，调用日志不含参数；未把哨兵失败计成功。
- 三个唯一命名 `tes85_p0_r7_tes108_*` 测试库全部删除，末次 pg_database 查询为0。三轮宿主默认配置的时间/长度/摘要前后均相同。只读确认宿主 daemon PID 56568 仍运行，未启动、停止或重启共享 daemon。
- TES-104 的 P0/R1/R2/R3 独立通过限定范围保留；本轮源码零变化、完整 handler 通过。未冒充重演其独立数据库升级矩阵、旧应用 HTTP、备份恢复或 sqlc 验证。
- 合法 CLI→server→daemon、真实八条、Mac/Linux 原生、生产迁移/切换、P2/P3、发布：**未执行/未放行**。本轮未运行前端测试、未创建或核验 PR/CI。

## 输入与复现

已通过 CLI 下载并验证 TES-107 源证据 ZIP SHA256 `5ac03e42c8133694b96ca077afb9f6b7873c73eddc17ad1f16dc885c49fc51aa`、87项内层清单；TES-104 独立证据 ZIP `33b298c4c9c56d2913362d2c93608863a91945c9404c0d06c84fff884fc2b275` 和 REVIEW 摘要也匹配。bundle prerequisite 校验通过，原候选被恢复到独立克隆。

附件 ZIP 解压后运行 `REPRO.ps1 -UpstreamRepo <含固定上游对象的本地仓库>`。它重建原固定候选，并执行本轮最终环境策略下的 build/vet、完整 handler、标准两组和 race 两组；Go测试非零结果写入日志，脚本返回并不代表通过。原始记录位于 `evidence/`，新执行落在 `final/`。需要可访问的本机开发 PostgreSQL及一次性库创建/删除权限；不连接产品数据库运行测试。
