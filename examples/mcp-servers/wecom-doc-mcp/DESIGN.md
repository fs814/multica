# 企业微信文档 MCP 服务 — 架构与设计方案

> 最后更新：2026-04-22

---

## 一、项目概述

### 1.1 背景与目标

企业微信文档（doc.weixin.qq.com）大量使用 **Canvas 渲染**，页面 DOM 中几乎没有可直接提取的文本内容，且企微官方尚未开放文档读写 API。这意味着传统的网页抓取方案（DOM 解析、HTTP API 调用）均不可用。

本项目通过 **Playwright 浏览器自动化** 实现了一套企微文档的 MCP（Model Context Protocol）服务，让 AI 系统（CodeBuddy / WorkBuddy 等）能够直接读写企微在线文档，填补了这一能力空白。

### 1.2 核心能力

| 能力 | 工具名 | 说明 |
|------|--------|------|
| 登录认证 | `wecom_doc_login` | 启动可视化浏览器，用户扫码登录，自动持久化 Cookie |
| 状态检查 | `wecom_doc_status` | 离线 + 在线双重校验登录态是否有效 |
| 文档读取 | `wecom_doc_fetch` | 读取 doc/sheet/slide/flowchart/mindmap 等类型内容 |
| 页面截图 | `wecom_doc_screenshot` | 对文档页面全页截图 |
| 文档写入 | `wecom_doc_write` | 向 doc 文档末尾追加纯文本 |
| 表格写入 | `wecom_doc_write_sheet` | 向 sheet 指定单元格写入/追加内容 |

### 1.3 技术栈

- **运行时**：Node.js（ESM）
- **MCP SDK**：`@modelcontextprotocol/sdk` — 提供 Stdio 传输层和工具注册框架
- **浏览器自动化**：Playwright（Chromium headless）
- **Excel 解析**：SheetJS（xlsx 库）
- **PPTX 解析**：JSZip — 解压 PPTX 文件并提取 slide XML 中的文本内容
- **通信协议**：MCP over JSON-RPC（stdin/stdout），日志走 stderr

---

## 二、项目结构

```
wecom-doc-mcp/
├── package.json          # 项目配置与依赖声明
├── README.md             # 用户使用文档
├── DESIGN.md             # 本文档（架构设计）
└── src/
    ├── index.js           # MCP 服务入口 — 工具注册、请求路由、输出格式化
    ├── browser.js         # 浏览器管理模块 — 实例生命周期、Cookie 持久化
    ├── login.js           # 登录脚本 — 可视化浏览器扫码登录
    └── fetcher.js         # 内容获取模块 — 读取/写入文档的核心策略实现
```

### 模块依赖关系

```
index.js（MCP 服务入口）
  ├── browser.js（浏览器管理）
  │     └── Playwright chromium
  ├── fetcher.js（内容获取/写入）
  │     ├── browser.js（launchBrowser / saveState）
  │     ├── xlsx（Excel 解析）
  │     └── jszip（PPTX 解析）
  └── login.js（独立进程，spawn 调用）
        └── browser.js（openLoginBrowser）
```

---

## 三、模块详细设计

### 3.1 `browser.js` — 浏览器管理模块

**职责**：管理 Playwright 浏览器实例的完整生命周期，包括创建、Cookie 持久化、热更新和销毁。

#### 关键设计

| 机制 | 说明 |
|------|------|
| **Cookie 持久化** | 登录态保存在 `~/.wecom-doc-mcp/state.json`，使用 Playwright 的 `storageState` API |
| **Session Cookie 修复** | 企微登录产生大量 `expires=-1` 的 session cookie，Playwright 新上下文会忽略它们。`loadState()` 将其 expires 修正为未来 30 天 |
| **过期 Cookie 过滤** | 加载时自动过滤已过期的非 session cookie |
| **sameSite 兼容** | 修正非标准的 sameSite 值为 `Lax` |
| **热更新检测** | 通过 `state.json` 文件的 mtime 检测外部更新（如 login.js 进程写入了新 Cookie），自动重建浏览器上下文 |
| **反检测** | 注入 `navigator.webdriver = false`，伪装 User-Agent |
| **实例复用** | 单例模式管理 browser/context，多次调用复用同一实例 |

#### 登录状态检查（`checkLoginStatus`）

采用**两阶段校验**：
1. **离线校验**：检查 `state.json` 中是否存在关键 Cookie（TOK/uid/uid_key），以及是否大部分已过期
2. **在线校验**：使用独立浏览器实例访问 `doc.weixin.qq.com`，检查是否被重定向到登录页

---

### 3.2 `login.js` — 登录脚本

**职责**：以可视化模式启动 Chromium，引导用户完成扫码登录。

#### 流程

```
启动 headless=false 浏览器
   → 打开 doc.weixin.qq.com
   → 轮询检测（每 2s）：
       ├── URL 是否离开登录页？
       ├── 是否存在 TOK + uid Cookie？
       └── Cookie 总数 ≥ 10？
   → 登录成功后：
       ├── 等待 5s 让异步 Cookie 稳定
       ├── 再访问一次首页触发额外 Cookie
       └── saveState() 持久化到 state.json
   → 3s 后关闭浏览器
```

#### MCP 中的调用方式

`index.js` 中 `wecom_doc_login` 工具通过 `spawn` 以**独立进程**启动 `login.js`，原因：
- 登录需要 `headless: false`（弹出可视化窗口），而 MCP 服务本身运行在 headless 模式
- 登录进程的 stdout 不能干扰 MCP 的 JSON-RPC 通信，因此只继承 stderr

---

### 3.3 `fetcher.js` — 内容获取与写入模块

这是项目最核心的模块（约 2000 行），实现了多种策略来应对企微文档 Canvas 渲染带来的内容提取难题。

#### 3.3.1 文档类型检测

通过 URL 路径判断文档类型：

| URL 路径 | 类型 | 提取策略 |
|----------|------|----------|
| `/sheet/` | 在线表格 | `extractSheetContent` — 五重策略 |
| `/smartsheet/` | 智能表格 | 同上 |
| `/doc/` | 在线文档 | `extractDocContent` — iframe 检测 + DOM + 剪贴板 |
| `/slide/` | 幻灯片 | `extractSlideContent` — 三策略组合（API拦截+PPTX导出+逐页截图） |
| `/mindmap/` | 思维导图 | `extractMindmapContent` — API拦截+JS内存扫描+截图 |
| `/flowchart/` 等 | 通用 | `extractGenericContent` — 全选复制 + body.innerText |

#### 3.3.2 表格内容读取 — 五重策略降级

企微表格是最复杂的文档类型，内容完全由 Canvas 渲染，需要多种策略组合提取：

```
策略 A：JS 运行时 API 扫描（快速，但只能读当前 tab）
   ↓ 如果失败
策略 B：拦截 opendoc API 响应（刷新页面 + 网络拦截）
   ↓
策略 C：解析 opendoc 数据（提取 sheet 列表 + 解压 workbook）
   ↓ 如果无数据或只有占位符
策略 E：UI 菜单导出 Excel（最可靠，获取所有 sheet 完整数据）
   ↓ 如果导出失败
策略 D：JS 引擎深度搜索（遍历 window 对象树，查找 getCellValue 等方法）
   ↓ 所有策略都失败
兜底：body.innerText
```

**各策略详解：**

| 策略 | 原理 | 优点 | 局限 |
|------|------|------|------|
| **A** | 扫描 `window` 上的全局对象，查找暴露 `getCellValue` 方法的引擎实例 | 快速，无需刷新页面 | 只能读当前 tab，且依赖全局变量暴露 |
| **B** | 注册 `page.on('response')` 监听器，刷新页面后拦截 `opendoc` API 响应 | 能获取原始数据 | 需要刷新页面（耗时 ~8s） |
| **C** | 从 B 拦截到的 opendoc 数据中提取 sheet header（tab 名称/ID 映射）和 workbook 压缩数据，尝试用 pako/DecompressionStream 解压 | 能获取完整的 sheet 元信息 | 压缩数据不一定能成功解压 |
| **E** | 模拟 UI 操作：点击「文件操作」→ hover「导出」→ 点击「本地 Excel 表格 (.xlsx)」，下载后用 SheetJS 解析 | **最可靠**，获取所有 sheet 完整数据 | 耗时较长（需等待下载），依赖 UI 元素存在 |
| **D** | 从 `window` 上深度搜索（DFS，深度 ≤6）含有 `getCellText`/`getCellValue` 等方法的对象，逐个读取单元格 | 兜底能力 | 扫描范围有限（200 行 × 20 列） |

#### Sheet 名称校正

策略 A/D 只能提取当前展示的 tab，其 sheet name 会被标记为 `__current_tab__`。在所有策略执行完毕后，通过以下优先级校正：

1. **opendoc API 的 sheetList**（策略 C 提取）+ URL 中的 `tab` 参数匹配
2. **DOM 方式提取的 tabNames**（兜底）
3. 默认名 `Sheet1`/`Sheet2`/...

#### 3.3.3 文档内容读取（doc 类型）

```
策略 0：检测 iframe（企微文档编辑区可能在 iframe 中）
   → 遍历所有 frame，找到文本内容最多的那个
   ↓
策略 1：等待编辑器 DOM 选择器出现（ql-editor / contenteditable 等）
   ↓
策略 2：通过 DOM 选择器提取 innerText
   ↓ 如果为空（Canvas 渲染）
策略 3：模拟 Ctrl+A → Ctrl+C → 读取剪贴板
   ↓ 如果剪贴板也为空
策略 4：body.innerText 去噪（移除 header/toolbar/sidebar 等噪音元素）
```

#### 3.3.4 幻灯片内容提取（slide 类型 — `extractSlideContent`）

企微 PPT 使用 Canvas 渲染，无法直接从 DOM 提取文本。采用**三策略组合**：

```
策略 A（API 拦截 + JS 内存扫描）：
   → 注册 response 监听器，刷新页面
   → 拦截 opendoc/slide/presentation 等 API 响应
   → 从 JSON 数据中提取 slide 文本（按页分组）
   → 同时从 JS 内存（renderData.slideRenderData / 全局变量深度扫描）提取逐页文本
   ↓ 如果没有有效文本且非只读
策略 B（PPTX 导出 — exportAndParsePptx）：
   → 点击「文件」→ hover「导出」→ 点击 PPTX 选项
   → 等待下载完成
   → 使用 JSZip 解压 PPTX → 逐个 slide XML 提取 <a:t> 文本节点
   ↓ 始终执行
策略 C（逐页截图 — screenshotSlides）：
   → 检测侧边栏缩略图或使用键盘翻页
   → 截取主内容区域（排除工具栏/侧边栏）
   → JPEG 格式 + 低质量（quality=40）压缩，最多截 10 页
   → 返回截图路径列表
```

**截图输出优化**：
- 使用 `findSlideMainArea()` 定位主编辑区域裁剪框，避免截取无关 UI
- JPEG 格式 + quality=40 大幅压缩体积
- 最多内联 3 张 base64 图片（MAX_INLINE_IMAGES），单张上限 200KB
- 超出部分以文件路径形式返回

#### 3.3.5 思维导图内容提取（mindmap 类型 — `extractMindmapContent`）

企微思维导图使用 Canvas 渲染，DOM 中无文本。采用**双策略组合**：

```
策略 A（API 拦截 + JS 内存扫描）：
   → 注册 response 监听器，刷新页面
   → 拦截 opendoc/mindmap/mind 等 API 响应
   → 从 JSON 数据中提取思维导图节点结构（text + children 模式）
   → 辅助函数 extractMindmapNodes 递归搜索节点
   → 同时从 JS 内存（全局变量深度扫描）提取节点或文本
   ↓ 始终执行
策略 B（全页截图）：
   → 查找主内容区域（mind-canvas / canvas 等选择器）
   → JPEG 格式 + quality=60 截图
   → 以 base64 内联返回（≤500KB）或路径引用
```

**节点结构提取**：
- `extractMindmapNodes()` 递归搜索 `text/content/topic` + `children/sub/subTopics` 模式的对象
- 支持多种常见思维导图数据格式（树形结构、扁平节点列表）
- 结果以编号列表形式输出

#### 3.3.6 文档写入（doc 类型 — `writeDocContent`）

```
校验文档类型为 doc
   → 打开页面，等待 Canvas 渲染
   → 点击编辑区域激活
   → Ctrl+End 跳到文档末尾
   → 按两次 Enter 分隔
   → 逐行 keyboard.type() 输入文本
   → Ctrl+S 保存
```

#### 3.3.7 表格写入（sheet 类型 — `writeSheetCell`）

采用**三层策略降级**：

```
策略一（JS API 直写）：探测企微引擎的 setCellValue/setCellText 方法，直接写入内存
   ↓ 如果找不到 API
策略二（箭头键导航 + 剪贴板）：Ctrl+Home → ArrowDown/ArrowRight 导航 → 剪贴板粘贴
   ↓ 如果单元格距 A1 过远（步数 > 200）或失败
策略三（名称框定位 + 键盘输入）：点击名称框 → 输入单元格地址 → Enter 跳转 → 键盘输入
   ↓ 全部失败
返回错误

写入后：
   → Ctrl+S 保存
   → 通过 getCellValue 回读验证（endsWith 检查）
```

**写入前校验**：
- 单元格地址格式校验（正则 `^[A-Za-z]{1,3}\d{1,7}$`）
- 文档类型校验（仅允许 sheet/smartsheet）
- 写入模式校验（仅允许 append/overwrite）

---

### 3.4 `index.js` — MCP 服务入口

**职责**：工具注册、请求路由、参数校验、输出格式化。

#### MCP 协议层

```
StdioServerTransport（stdin/stdout JSON-RPC）
   → Server 实例
      → ListToolsRequestSchema → 返回 6 个工具定义
      → CallToolRequestSchema → switch-case 路由到具体处理逻辑
```

#### 输出格式化（fetch 结果）

`wecom_doc_fetch` 的结果会被格式化为 **Markdown**：

```markdown
📄 文档获取成功
类型: sheet
URL: https://...

## 标题
xxx

## 工作表列表（共 N 个）
1. **Sheet1** (tab=xxx)
2. **Sheet2** (tab=yyy, 隐藏)

## 表格数据
### Sheet1
| col1 | col2 | col3 |
| --- | --- | --- |
| data | data | data |
```

**关键处理**：
- 单元格中的 `|` 和 `\n` 字符会被转义，防止破坏 Markdown 表格结构
- 支持 `tab` 参数过滤：当用户指定 tab 时，只输出对应 sheet 的数据
- 幻灯片区分有文本和纯 Canvas 页面

---

## 四、数据流

### 4.1 读取流程

```
AI Agent → MCP (wecom_doc_fetch) → index.js
   → fetchDocContent(url, tab)
      → launchBrowser() — 复用/创建 Chromium 实例
      → page.goto(url) — 带 Cookie 访问企微文档
      → detectDocType() — 判断文档类型
      → extractXxxContent(page) — 执行对应提取策略
      → 返回结构化数据
   → 格式化为 Markdown
   → 返回给 AI Agent
```

### 4.2 写入流程

```
AI Agent → MCP (wecom_doc_write_sheet) → index.js
   → 参数校验 (url, cell, content, mode)
   → writeSheetCell(url, cell, text, options)
      → 校验 cell 格式、doc 类型、mode 合法性
      → launchBrowser() + page.goto(url)
      → 策略一: JS API 直写 setCellValue
      → 策略二: Ctrl+Home + ArrowKey 导航 + 剪贴板粘贴
      → 策略三: 名称框输入地址 + 键盘输入
      → Ctrl+S 保存
      → getCellValue 回读验证
   → 返回写入结果
```

---

## 五、关键技术决策

### 5.1 为什么用 Playwright 而非 HTTP API？

企微文档没有开放的读写 API，且页面使用 Canvas 渲染，DOM 中无文本内容。Playwright 是能够同时处理以下需求的唯一方案：
- Cookie 持久化登录
- Canvas 渲染页面的内容读取（通过 JS 注入、剪贴板、文件导出）
- 模拟键盘/鼠标操作实现写入

### 5.2 为什么 Excel 导出（策略 E）是表格读取的核心策略？

| 对比维度 | JS API（策略 A/D） | API 拦截（策略 B/C） | Excel 导出（策略 E） |
|----------|-------------------|---------------------|---------------------|
| 数据完整性 | 仅当前 tab | 可能不完整 | **所有 sheet 完整数据** |
| 可靠性 | 依赖全局变量暴露 | 依赖 API 格式不变 | **依赖 UI 菜单存在** |
| 耗时 | 快（~1s） | 慢（需刷新 ~8s） | 中等（~10-15s） |

### 5.3 为什么 MCP 通信用 stderr 输出日志？

MCP 协议基于 stdin/stdout 的 JSON-RPC 通信。任何写入 stdout 的非 JSON-RPC 内容都会破坏协议。因此：
- 所有 `console.error()` → stderr（日志/调试）
- 所有 `console.log()` → 仅在 login.js 中使用（独立进程，不走 MCP 通信）

### 5.4 为什么登录用独立进程？

`wecom_doc_login` 需要弹出可视化浏览器窗口（`headless: false`），而 MCP 服务本身运行在 headless 模式。使用 `spawn` 启动独立进程可以：
- 避免 headless/headed 模式冲突
- 避免登录进程的 stdout 输出干扰 JSON-RPC 通信
- 登录进程崩溃不影响 MCP 服务

---

## 六、已知限制

| 限制 | 说明 |
|------|------|
| **Canvas 文档读取** | doc 类型文档如果完全使用 Canvas 渲染，剪贴板提取可能失败，需依赖截图 + OCR |
| **写入验证** | 策略二/三（键盘输入方式）无法 100% 验证写入是否成功（JS API 可能不可用） |
| **只读文档** | 写入操作在只读文档上会静默失败（模拟输入被忽略），无法预检测权限 |
| **并发** | 单例浏览器 context，不支持并发调用。多个工具调用会串行执行 |
| **表格大小** | 策略 A/D 的 JS 内存扫描限制为 200 行 × 20 列，更大的表格需依赖策略 E（Excel 导出） |
| **Cookie 有效期** | 登录态一般可持续数天，过期后需重新扫码登录 |
| **幻灯片截图** | 单次最多截取 10 页，内联返回最多 3 张（base64），单张上限 200KB，超出以文件路径返回 |
| **PPTX 导出** | 仅在非只读模式且 API 拦截无有效文本时尝试，依赖 JSZip 解析 slide XML |

---

## 七、配置与部署

### MCP 配置（`~/.codebuddy/mcp.json`）

```json
{
  "mcpServers": {
    "wecom-doc": {
      "command": "node",
      "args": ["/绝对路径/wecom-doc-mcp/src/index.js"],
      "timeout": 180000
    }
  }
}
```

### 数据存储

```
~/.wecom-doc-mcp/
└── state.json    # Playwright storageState（Cookie + localStorage）
```

### 临时文件

- Excel 导出时使用 `os.tmpdir()` 下的临时目录，用后立即清理（finally 块保证）
