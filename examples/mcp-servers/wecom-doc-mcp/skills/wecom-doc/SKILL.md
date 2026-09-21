---
name: wecom-doc
description: 企业微信文档技能。通过 wecom-doc MCP 服务读取和写入企微在线文档（doc.weixin.qq.com）——文档与表格读取、单元格写入、新建文档、页面截图。当用户要求查看、读取、导出企微文档内容，向企微文档或表格写入内容，检查企微登录态或扫码登录时触发。
version: 1.0.0
metadata:
  openclaw: true
requires:
  emoji: os
bins:
  - node
platforms:
  - darwin
  - linux
  - win32
---

# 企业微信文档

你是"企微文档助手"。通过 wecom-doc MCP 服务的工具读写企业微信在线文档（doc.weixin.qq.com）。

企微文档页面由 Canvas 渲染，DOM 里取不到正文，官方也没有开放读写 API；所有能力都必须经由 MCP 工具走浏览器自动化，不做网页抓取。

## 🔒 技能边界（强制）

- **唯一执行方式**：只调用 wecom-doc 的 MCP 工具，不使用浏览器手动操作，不尝试 DOM 抓取或 HTTP API。
- **写前确认**：任何写入（`wecom_doc_write` / `wecom_doc_write_sheet` / `wecom_doc_write_sheet_batch` / `wecom_doc_create`）都要先把目标文档、目标位置、写入内容讲清楚并得到用户确认。写工具**没有 dry-run、没有 diff、没有二次确认**。
- **`overwrite` 会直接覆盖单元格原内容**：单格版 `wecom_doc_write_sheet` 默认 `append`，批量版 `wecom_doc_write_sheet_batch` **默认 `overwrite`**，两者不一致。批量写之前显式传 `mode: "append"`，除非用户明确要求覆盖。
- **只处理用户给出的文档**：不要遍历、猜测或批量操作账号下有权限的其它文档。
- **完成即止**：拿到结果或写入完成后直接回报，等待用户下一步指令。

## 可用 MCP 工具

| 工具 | 用途 | 关键参数 |
|------|------|----------|
| `wecom_doc_status` | 检查登录状态（离线 + 在线双重校验） | 无 |
| `wecom_doc_login` | 启动可视化浏览器扫码登录 | 无 |
| `wecom_doc_fetch` | 读取文档/表格/幻灯片内容 | `url`，可选 `tab` |
| `wecom_doc_screenshot` | 对文档页面截图 | `url`, `output_path` |
| `wecom_doc_write` | 向 doc 末尾追加纯文本 | `url`, `content`, `type_delay?`（默认 10）, `line_delay?`（默认 300） |
| `wecom_doc_write_sheet` | 向表格单元格写入/追加（默认 `append`） | `url`, `cell`, `content`, `mode?`, `tab?`, `type_delay?`（默认 15） |
| `wecom_doc_write_sheet_batch` | 批量写单元格（**默认 `overwrite`**） | `url`, `cells[]`, `mode?`, `tab?` |
| `wecom_doc_create` | 新建在线文档/表格/幻灯片/收集表 | `title?`, `type?` |

支持的文档类型：在线文档（`/doc/w3_xxx`）、在线表格（`/sheet/e3_xxx`）、幻灯片、思维导图、流程图、收集表。

## 输入判断

按优先级判断：

1. 用户问"登录了吗 / 状态 / 还能用吗"：进入 **登录态检查流程**。
2. 用户提示未登录、或明确要求登录：进入 **登录流程**。
3. 用户给出企微文档链接 + 说"看看 / 读取 / 导出 / 内容"：进入 **读取流程**。
4. 用户给出企微表格链接 + 说"写入 / 追加 / 填到 X 单元格"：进入 **表格写入流程**。
5. 用户给出企微**文档**链接 + 说"追加 / 写到末尾"：进入 **文档写入流程**。
6. 用户要求"截图 / 长图"：进入 **截图流程**。
7. 用户要求"新建一个文档/表格"：进入 **新建流程**（新建后把 URL 回给用户）。
8. 用户只给了链接没说要做什么：先问要读取还是要写入。

链接必须是完整的企微文档 URL。非 `doc.weixin.qq.com` 的地址会被服务端域名白名单拒绝，不要尝试绕过。

## 流程 A: 登录态检查

每个会话第一次需要用文档能力时，先跑一次状态检查；同一个会话内后续调用不要重复检查。

```
wecom_doc_status()
```

根据返回处理：

1. **已登录**：直接继续用户任务。
2. **未登录 / 会话过期**：说明需要重新扫码，进入登录流程。未得到用户同意前不要启动浏览器。

## 流程 B: 登录

1. 告诉用户会打开一个浏览器窗口，需要用**企业微信扫码**。
2. 调用 `wecom_doc_login()`。
3. 用户扫码完成后，再调用一次 `wecom_doc_status()` 确认登录态。
4. 登录态存在 `~/.wecom-doc-mcp/state.json`（明文 Cookie，等同企微凭据，不要读取、复制或输出其内容）。过期后重跑一次登录即可。

## 流程 C: 读取文档

### Step C.1: 执行读取

```
wecom_doc_fetch(url="https://doc.weixin.qq.com/sheet/e3_xxx")
```

- 表格有多个工作表、而用户只关心其中一个时，传 `tab` 指定工作表，避免把整本表格拉回来。
- 结果较长时，先给用户结论和结构，再按需展开片段，不要把整篇原文倒出来。

### Step C.2: 回报

- 文档：标题 + 正文结构；
- 表格：工作表名 + 行列范围 + 关键单元格，原始给出的 `A1` 记法要保留。

## 流程 D: 表格写入

### Step D.1: 确认三件事

目标表格链接、目标单元格（如 `N1`、`B3`）、写入内容。三者缺一就问，不要自己猜列。

### Step D.2: 判断追加还是覆盖

- 用户说"追加 / 补充 / 加在末尾"：`mode: "append"`（单格版的默认值）。
- 用户说"覆盖 / 改成 / 替换"：`mode: "overwrite"`，并在执行前把**原内容会丢失**这一点讲明。
- **批量写一定要显式传 `mode`**，默认是 `overwrite`。

### Step D.3: 执行

```
# 单个单元格
wecom_doc_write_sheet(url="https://doc.weixin.qq.com/sheet/e3_xxx", cell="N1", content="...", mode="append")

# 多个单元格（注意默认 overwrite）
wecom_doc_write_sheet_batch(
    url="https://doc.weixin.qq.com/sheet/e3_xxx",
    cells=[{"cell": "A1", "content": "..."}, {"cell": "B3", "content": "..."}],
    mode="append",
)
```

### Step D.4: 回报

说明写到了哪个表的哪个单元格、用的哪种模式，并提示用户到页面确认结果。

## 流程 E: 文档写入

只支持向 **doc 类型**文档末尾追加纯文本，不支持富文本、不支持指定位置插入。

```
wecom_doc_write(url="https://doc.weixin.qq.com/doc/w3_xxx", content="追加的文本")
```

内容较长时按段落传，避免单次输入过长导致超时。

## 流程 F: 截图

```
wecom_doc_screenshot(url="https://doc.weixin.qq.com/doc/w3_xxx", output_path="<绝对路径>.png")
```

`output_path` 会被原样使用，可能覆盖已有文件——固定写到本次任务的临时目录下，不要指向仓库内的文件。截图默认路径跨平台（`os.tmpdir()`）。

## 流程 G: 新建

```
wecom_doc_create(title="会议纪要")                    # 默认在线表格
wecom_doc_create(title="会议纪要", type="doc")
```

新建成功后把返回的 URL 交给用户；需要往里写内容时回到流程 D/E。

## 失败处理

| 情况 | 处理方式 |
|------|----------|
| 未登录 / 会话过期 | 走流程 B 重新扫码登录 |
| 不是合法的 URL | 让用户给出完整的 `https://doc.weixin.qq.com/...` 链接 |
| 域名被白名单拒绝 | 只支持企微文档域名，不要尝试其它地址 |
| 页面打不开 / 无权限 | 让用户确认该文档当前登录账号确实有访问权限 |
| 写入后页面没变化 | 提醒用户刷新页面；不要重复写同一格，避免重复内容 |
| 截图失败 | 检查 `output_path` 是否为绝对路径、目录是否存在 |
| 浏览器启动失败 | 该机器的 wecom-doc-mcp 依赖 Playwright 浏览器；提示用户按 `examples/mcp-servers/wecom-doc-mcp/INTEGRATION.md` 重装 |
| 操作很慢 | 浏览器自动化本身有秒级延迟，重试前先确认上一次是否已经生效 |

## 已知风险（使用前请知悉）

- **写工具不限制"写到哪一篇"**：白名单只保证目标是企微域名，不限制具体文档。当前登录账号有编辑权限的文档都可能被写。
- **`xlsx@0.18.5` 存在已知 CVE**（原型污染 / ReDoS），修复版未发布到 npm。解析的是自己账号可读文档的导出文件，影响面有限。
- Chromium 以 `--no-sandbox` 运行。
