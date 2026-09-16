# Multica 本地运行失败复盘与修复方案

## 本次故障链

任务 TES-85 经历了多个独立故障，不能只看最后一条报错判断根因。

| 现象 | 已确认的原因 | 本次代码处理 |
| --- | --- | --- |
| `model_not_found_or_unavailable`，不自动重试 | 原始错误为 `Selected model is at capacity`，被宽泛的 `selected model` 规则误分类 | 优先识别容量错误；服务端修正旧 daemon 的错误分类；加入有限延迟重试 |
| 启动日志显示 terra，但运行仍用 astra | 智能体显式模型覆盖了启动环境的默认值 | 保留显式选择；诊断时核对智能体设置及运行记录，不自动换模型 |
| 恢复运行一直排队 | 对应运行时离线，daemon 状态为 stopped | 使用现有本地 daemon 启动助手；检查心跳和具体运行，不重复排队 |
| daemon 在线，运行仍停留 dispatched | 当时旧 bundled daemon 未启动工作进程；从本地源码重建后恢复 | 本地构建记录 checkout 版本；复用运行中 daemon 时提示版本差异。版本差异是线索，不宣称已证明协议层根因 |
| `EPERM unlink multica.exe` | 运行中的 Windows 进程占用 bundled CLI；无 Go 时仍复制旧 server/bin 文件 | 无 Go 且已有 bundle 时保持原文件；暂存后替换单个文件，失败保留原文件并给出处理提示 |
| `CreateProcessWithLogonW 1385` | Windows 拒绝沙箱账号所需的登录类型；模型仍能正常返回文字 | 模型轮次开始前执行沙箱命令检查，失败时直接报告运行失败，避免仅返回 blocked 文字却被视为正常完成 |

## 恢复与安全边界

容量错误按运行预算重试：默认首次运行加一次重试，30 秒后执行；允许第三次时再等 60 秒，硬上限三次。`max_attempts=1` 继续禁用重试。鉴权、额度、真实模型不存在和本机沙箱错误不自动重试；不自动换模型。

Windows 检查通过已初始化的同一个 Codex app-server 调用 `command/exec`，使用任务目录、既有配置和权限，执行 `cmd.exe /d /c exit 0`。命令超时 10 秒，RPC 总时限 15 秒；不读取用户文件，不加载 shell 配置，不发起模型请求。失败使用现有 `agent_error.process_failure` 分类，并给出明确诊断。任意 RPC 文本不写入持久错误，避免泄漏凭据。

本次恢复中，用户明确批准了两个指定智能体的 `unelevated` 覆盖。该模式保留文件系统限制，但账号和网络隔离较弱。代码不将此批准扩展到其他智能体，也不自动修改 Windows 安全策略。

离线和模型失败属于不同层。旧失败运行的“没有自动重试”不代表不存在新的协调运行。先检查最新运行及其执行者，再决定是否重新运行。一次运行的 `completed` 只代表该次正常结束，不能替代任务验收。

## 部署与验证

本次只修改源代码并执行回归测试，不重启正在执行 TES-85 的守护进程或后端。分类归一化与重试策略需要重建并重启后端；Windows 检查需要更新 daemon；打包与启动助手修改在下一次调用时生效。

本地 daemon 版本提示依据 Git describe（含 dirty 标记），不是工作目录内容的完整指纹；两个不同的未提交状态可能有相同版本标签。提示不会终止活动运行，也不会证明服务端与 daemon 协议不兼容。

回归覆盖：实际容量报错与旧分类、不可重试错误、次数预算与延迟、沙箱成功/失败/错误 1385/取消/非法响应、无 Go 保留 bundle、Windows 文件锁、替换失败保留旧文件、daemon 版本差异时不停止活动任务。

验证结果：

- Node 打包与 daemon 启动助手测试：18/18 通过。
- Go 分类、Codex 专项和服务端重试专项通过：`go test ./pkg/taskfailure ./pkg/agent ./internal/service -run 'Test(Classify|SelectedModelCapacity|NormalizeDaemonReason|Codex|CapacityRetryBudgetAndCooldown|TaskFailureClassifiers|ProviderNetworkRetry|Retry)' -count=1`。
- Windows 模拟 app-server 集成测试确认：错误 1385 后不会调用 `thread/start` 或 `turn/start`；已有超时和取消场景的进程树清理测试通过。
- 扩大运行整个 agent 包时仍有其他 Windows 用例失败，包括 Dim 子进程测试、ExplainExecError 的 POSIX 命令字符串预期、部分其他 CLI 的模拟可执行文件/参数文件缺失。本次没有扩大范围修复这些项目，也不宣称全量测试通过。
- 没有进行新代码的真实模型端到端验证，也没有部署到正在运行的 daemon。模拟测试不消耗模型额度。

仍需人工处理的情况：持续容量不足、真实模型不可用、缺失 Go/PATH、Windows 登录权限或文件锁不会被“自动修好”。容量重试耗尽后应明确失败；更换模型、修改登录策略或采用隔离较弱的 sandbox 模式仍须由用户选择。启动日志中的 `where` 不可用和重复 PATH 是环境问题，本次没有改写全局 PATH。

参考：[OpenAI Windows 沙箱故障排查](https://learn.chatgpt.com/docs/windows/windows-sandbox#troubleshooting-and-faq)、[Codex command/exec](https://learn.chatgpt.com/docs/app-server#command-execution)。
