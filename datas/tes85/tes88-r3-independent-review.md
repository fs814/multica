TES-88 r3 独立复审：R1 关闭，R2 文档交付仍需修订。

审查时间：2026-09-15（America/Los_Angeles）。只使用下载的实际附件、合成身份和离线构建；未导入、发布、部署或运行真实模型。TES-86/87 的既有通过范围不变。

## main 与工程交付

- 本地 main 与 fs814/multica 远端 main 均为 `9c3fa6c58380a0f0d71941138997b0e349b46838`。
- 已读取并 fetch multica-ai/multica 的 main：`9663b4a87b5d1c22817b81bfe6ec00dc62e88cbf`。
- 两者树中均无 `build_linux_cli.py`、`prepare_mac_routes.py`、`MAC-ROUTING.md`、`MAC-USER-GUIDE-r3.md`；对应 Python 特征检索无匹配。不能把 r3 附件说成 main 已实现或已合入。
- 工程候选确实存在：修复 `dbe9b85f8dd8359fd0eb0b93cf8adc0bf94a5ac0`，整合文档 `440a1849dd922687d4a26accbb9642b327f96069`。核对实际包中两个辅助脚本、两组测试和两份路由说明与候选 Git 字节相同。
- TES-88 与父任务 TES-85 的 `multica issue pull-requests` 均返回空列表；无关联 PR/CI 可验。本轮只做审查，没有创建代码 PR。
- 提交 bundle 摘要为 `11b954de7cb1084749ce4ef29fd8b03838a5c4868bd2bb0991058ed5028dc2bf`。在含基线的工程候选仓库中 `git bundle verify` 通过；在当前 main 仓库中缺少前置 `a51f770314486924bdc10e573997948daced03aa`，该 bundle 不是可独立恢复全部历史的全量仓库。

## R1：通过

通过 CLI 下载实际附件，外层 SHA256 一致：

- 源码：`7bdd511c78a089a7863539fe2ec3f1fd70c9d17903ffb54a785457b67510a108`。
- 交接包：`5bca7fd6b8b9cf99f1d9b6cb4d2521bc0ade176cd25df9842d8c51fbfc869310`。

ZIP 路径安全检查通过。SOURCE.json 的 3171 项摘要全部一致；除单独加入的辅助脚本外，完整文件集合及字节与产品提交 `79afa3cfd14cf0b559324140c881e2ae50ab27e0` 的 server、LICENSE、NOTICE 的 Git archive 一致。

从实际交接包运行 `python -m unittest discover -s <交接包解压根> -p 'test_*.py' -v`：12/12 通过，无跳过。其中新增输入测试有 13 个子场景，覆盖 Go/汇编/cgo/object/embed/隐藏资源、模块及 vendor 文件；另外验证缺失、篡改、路径安全、符号链接和包内输出拒绝。

独立向实际源码 ZIP 解压结果分别加入 `.go`、`.s`、普通资源、隐藏资源：4/4 返回非零与 `Source input set mismatch`，输出目录均未创建。测试后移除自建注入文件再执行构建。已有输出再次调用时拒绝为 `Output already exists`。

固定 Go 1.26.6、CGO=0、readonly 模块、离线暖缓存下，两个全新解压根分别构建 amd64 和 arm64，四次全部成功；同架构摘要相同，并与工程记录一致：

| 架构 | 两次 SHA256 | ELF machine |
| --- | --- | --- |
| amd64 | d6a8c5d9079e34235e8e2356cd57737b7653fdc2d6a2367a2faf1ddb4a7d18c3 | 62 |
| arm64 | 83b87874391125545deb08a0cbc112949d3fcae499817a6d9bbb46d8519764f6 | 183 |

构建命令形态：`python <解压根>/build_linux_cli.py --cross --offline --arch <架构> --output <新的包外目录>`。这是 Windows 交叉构建，未在 Linux 执行产物。首次无缓存下载、Linux 原生工具链与实际 Dev Cloud 架构仍未验证。外层 ZIP 摘要是清单可信来源；构建期间需保持源码目录不变。

## R2：原路径缺陷修复，但交付未整体通过

通过 CLI 重新下载实际 Mac arm64 v8 包，摘要为 `38e4e7ca62d1d7b03be9de2cf03a7f2016adce0cd6042c746575cf07a2c4da1b`，确认实际入口 `multica` 和 `settings/buildcatalog/templates/macos.json`。r3 操作单已改成这两个路径，并说明 v8 与交接包分别解压。

使用实际 v8 模板和合成配置执行生成器：两份请求均使用各自 explicit agent，无 capability fallback；agent/runtime 不同，返回 platform_writes=0、ready_for_platform_execution=false。普通实例键与 canonical 键分离，操作单说明 CAS、发布版本核对、重绑冻结及实际 task/daemon 落点证据。没有进行平台写入或验证真实 runtime 路由。原 PM 修订2与 provenance 副本逐字节相同。

仍需修复的两个文档问题：

1. **中：交接包 README 测试路径不可运行。** `README.md:18,34` 给出 `python -m unittest discover -s e2e/delivery ...`，但实际 ZIP 把测试放在包根，没有该目录。从解压包根运行第34行命令，退出1：`ImportError: Start directory is not importable: 'e2e/delivery'`。修复：包内命令用 `-s .`，或明确区分源码 checkout 和交接 ZIP 的命令；重新打包后从新的解压根照文档运行，确认发现并执行12项测试。
2. **中：当前操作单保留与同次交付冲突的线上状态。** `MAC-USER-GUIDE-r3.md:302` 仍称“当前线上确认目标/实例仍 8/8”；第189行也沿用历史8/8复用表述。同次工程交付的 `TES-85-continuation-evidence.zip/identity-audit.json` 明确记录 original_count=8、archived_count=8、所有 revision 已变化，父任务最新协调评论要求保留归档。这里独立确认的是交付材料内部冲突，未重新读取线上8条实例。修复：现行操作单标明8条是历史已归档记录、不能计活动覆盖；要求读取当前归档/revision/身份后对账，保留归档，不自动恢复、不以相似输入认领或重新初始化。provenance 历史文件可保持原样。

上述问题不否定 R1 与本地路由生成器的通过范围；但不满足可直接照实际包执行、保护用户修改状态的文档验收条件。TES-88 退回 in_progress，要求工程师修订当前说明、更新固定提交和交接 ZIP/SHA256、执行包根文档命令回归后重新提交 in_review。源码包无需为纯文档变更重做源代码修复；若改变源码/构建输入，须重新记录摘要与相关验证。

## 可继续的范围

固定 r3 源码和 Linux 构建辅助脚本通过限定离线审查，可用于后续用户 Linux 本机构建/环境回执；Mac v8 既有本地原生验收可继续。双 Mac 本地请求生成通过，线上导入、发布、实例执行仍须匹配服务器及现场权限/路由条件。平台操作需使用修订后说明。

没有 Linux/Mac 原生运行结论，没有不可变硬件绑定结论，没有正式 main 集成或发布结论。父任务继续 in_progress，apply_allowed=false，生产门槛不变。
