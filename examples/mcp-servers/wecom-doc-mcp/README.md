# 企业微信文档 MCP 服务

> 通过 Playwright 浏览器自动化，让 CodeBuddy 能够读取企业微信在线文档（doc.weixin.qq.com）的内容。

## 功能特性

| 工具 | 说明 |
|------|------|
| `wecom_doc_login` | 启动浏览器扫码登录企微文档 |
| `wecom_doc_status` | 检查登录状态 |
| `wecom_doc_fetch` | 获取文档/表格/幻灯片内容 |
| `wecom_doc_screenshot` | 对文档页面截图 |
| `wecom_doc_write` | 向在线文档(doc)末尾追加文本 |
| `wecom_doc_write_sheet` | 向在线表格(sheet)指定单元格写入/追加内容 |

## 快速开始

### 1. 安装依赖

```bash
cd wecom-doc-mcp
npm install
npx playwright install chromium
```

### 2. 首次登录

运行登录脚本，会打开浏览器窗口：

```bash
npm run login
```

在浏览器中使用企业微信扫码登录。登录成功后 Cookie 会自动保存到 `~/.wecom-doc-mcp/state.json`。

### 3. 配置到 CodeBuddy

在 `~/.codebuddy/mcp.json` 中添加：

```json
{
  "mcpServers": {
    "wecom-doc": {
      "command": "node",
      "args": ["/绝对路径/wecom-doc-mcp/src/index.js"],
      "timeout": 60000
    }
  }
}
```

### 4. 在 CodeBuddy 中使用

直接发送企微文档链接，AI 会自动调用工具获取内容：

```
帮我获取这个文档的内容：https://doc.weixin.qq.com/sheet/e3_xxx
```

## 支持的文档类型

- ✅ 在线表格 (sheet)
- ✅ 在线文档 (doc)
- ✅ 幻灯片 (slide)
- ✅ 其他类型（通用提取）

## Cookie 管理

- 登录状态保存在：`~/.wecom-doc-mcp/state.json`
- Cookie 过期后需重新运行 `npm run login`
- 支持自动检测登录状态

## 文档内容写入

支持向在线文档末尾追加文本内容：

```
帮我在这个文档末尾追加内容：https://doc.weixin.qq.com/doc/w3_xxx
内容为：这是新追加的一段文字
```

参数说明：
- `content`: 要追加的文本内容，支持 `\n` 换行
- `type_delay`: 可选，每字符输入延时(ms)，默认 10
- `line_delay`: 可选，每行间等待时间(ms)，默认 300

> 注意：仅支持 doc 类型文档的纯文本追加，不支持富文本格式。

## 表格单元格写入

支持向在线表格的指定单元格写入内容：

```
帮我在这个表格的 N1 单元格追加内容：https://doc.weixin.qq.com/sheet/e3_xxx
内容为：4、这是第四点关键信息
```

参数说明：
- `cell`: 单元格地址，如 `A1`, `N1`, `B3`
- `mode`: `append`（追加到末尾，默认）或 `overwrite`（覆盖替换）
- `tab`: 可选，指定工作表的 tab 标识

## 注意事项

1. 首次使用需要安装 Playwright 浏览器：`npx playwright install chromium`
2. 企微文档大量使用 Canvas 渲染，部分表格内容可能需要通过截图+OCR获取
3. 登录状态一般可保持数天，过期后需重新登录
