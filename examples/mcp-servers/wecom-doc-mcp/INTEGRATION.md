# wecom-doc-mcp（企业微信文档 MCP 服务）

Vendored third-party MCP server. Lets an agent read and write WeCom online
documents (`doc.weixin.qq.com`) — docs, sheets, slides — by driving a headless
Chromium that holds a scanned-in WeCom session.

- 上游代码库: https://git.woa.com/baojiantang/wecom-doc-mcp
- knot MCP 市场: https://knot.woa.com/mcp/detail/7797
- 使用教程: https://km.woa.com/articles/show/655368

归档在 `examples/` 而不是 `packages/`，因为 `examples/` 在 pnpm workspace 的
`packages:` 匹配范围之外 —— 它不会被主仓库的 install / turbo build / typecheck
/ test 拉进去，主构建不受影响。

## 在一台新机器上启用

```bash
cd examples/mcp-servers/wecom-doc-mcp
npm ci                              # 用 ci 而不是 install：锁定 package-lock.json 的版本
npx playwright install chromium     # 必须，约 150MB；playwright 包本身不含浏览器
node src/login.js                   # 打开浏览器，用企业微信扫码登录
```

登录态存到 `~/.wecom-doc-mcp/state.json`（**不在**仓库里）。过期后重跑
`node src/login.js` 即可。

### 接入 Multica

Multica 已内置 MCP 支持，不需要改代码：

1. **设置 → MCP** 新增一个服务器（工作区级「服务器库」），配置：
   ```json
   {
     "command": "node",
     "args": ["<仓库绝对路径>/examples/mcp-servers/wecom-doc-mcp/src/index.js"]
   }
   ```
   路径必须是该机器上的绝对路径 —— 库里存的是启动命令，不是相对仓库的位置。
2. 到目标智能体的 **MCP** 标签页把它分配给该智能体。这一步是刻意分开的：加进
   库里的服务器**不会自动给任何智能体**，和「工作区技能」同一套逻辑。

### 接入 CodeBuddy / 其他 MCP 客户端

写进客户端自己的 MCP 配置（CodeBuddy 是 `~/.codebuddy/mcp.json`）：

```json
{
  "mcpServers": {
    "wecom-doc": {
      "command": "node",
      "args": ["<仓库绝对路径>/examples/mcp-servers/wecom-doc-mcp/src/index.js"],
      "timeout": 60000
    }
  }
}
```

## 提供的工具

| 工具 | 说明 |
|---|---|
| `wecom_doc_login` | 启动浏览器扫码登录 |
| `wecom_doc_status` | 检查登录状态 |
| `wecom_doc_fetch` | 读取文档/表格/幻灯片内容 |
| `wecom_doc_screenshot` | 页面截图 |
| `wecom_doc_write` | 向 doc 末尾追加纯文本 |
| `wecom_doc_write_sheet` | 向表格单元格写入（`append` 或 `overwrite`） |
| `wecom_doc_write_sheet_batch` | 批量写单元格（**默认 `overwrite`**） |
| `wecom_doc_create` | 新建在线文档/表格/PPT |

## 本 fork 相对上游的改动

代码已做安全审查。**没有发现恶意行为** —— 唯一的出站域名是
`doc.weixin.qq.com`，无遥测、无第三方上报、无混淆。但有几处"加固不足"，
已在此 fork 修掉。改动都带 `Multica fork` 注释，便于日后与上游同步时识别：

1. **`src/fetcher.js` — 新增域名白名单（最重要）。**
   上游把调用方给的 `url` 直接交给 `page.goto`，而那个浏览器上下文带着已登录
   的企微会话；唯一的检查 `detectDocType()` 对无法识别的地址返回 `'unknown'`，
   写入路径又显式放行 `'unknown'`，等于没有检查。后果是
   `file:///C:/Users/<you>/.ssh/id_rsa` 或 `http://169.254.169.254/`（云元数据）
   都能被打开，页面文本还会原样回传给模型 —— 在 URL 可能来自提示注入的
   AI 场景下，这是本地文件读取 + SSRF 原语。
   现在 5 个入口（fetch / screenshot / write / write_sheet / write_sheet_batch）
   统一调用 `assertAllowedDocUrl()`，按解析后的 `hostname` 比对而非子串匹配
   （子串匹配会被 `https://evil.com/?x=doc.weixin.qq.com` 绕过）。
2. **`src/browser.js` — 凭据文件权限。** `state.json` 存的是可直接复用的企微
   会话 Cookie。上游用默认权限落盘（Linux/mac 下 0644，同机其他用户可读）。
   现在写文件带 `mode: 0o600`，建目录带 `mode: 0o700`。
3. **`src/index.js` — 截图默认路径跨平台。** 上游硬编码 `/tmp/...`，Windows 上
   解析成 `C:\tmp\`（通常不存在）导致截图失败；改用 `os.tmpdir()`。

## 仍然存在的风险（未修，使用前请知悉）

- **写工具没有"写到哪个文档"的限制。** 白名单只保证目标是企微域名，不限制
  是哪一篇。凡是当前登录账号有编辑权限的文档，agent 都能写。
  `wecom_doc_write_sheet` 的 `overwrite` 模式会**直接覆盖**单元格原内容，且
  `wecom_doc_write_sheet_batch` **默认就是 `overwrite`**（单格版默认是
  `append`，两者不一致，容易踩）。没有 dry-run、没有 diff、没有二次确认。
  如果你的企微账号有较大范围的文档权限，agent 也就有。
- **`wecom_doc_screenshot` 的 `output_path` 未校验**，可以把 PNG 写到任意可写
  路径（包括覆盖仓库内文件）。
- **`xlsx@0.18.5`（SheetJS）是可达代码路径**（`fetcher.js` 解析导出的 .xlsx）。
  该版本有 CVE-2023-30533（原型污染）和 CVE-2024-22363（ReDoS），而修复版
  （0.19.3+）**从未发布到 npm** —— SheetJS 已迁到自有 CDN，所以 `^0.18.5`
  这个 caret 永远解析不到已修复版本。要彻底解决只能改成 CDN tarball 依赖或
  换 `exceljs`。缓解因素：被解析的文件是你自己账号能读到的文档的导出。
- **Chromium sandbox 被关闭**（`--no-sandbox --disable-setuid-sandbox`）。
- User-Agent 硬编码为 macOS Chrome 131，在所有平台上都一样。
