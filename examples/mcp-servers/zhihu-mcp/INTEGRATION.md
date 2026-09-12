# zhihu-mcp（知乎 MCP 服务）

Vendored third-party MCP server. Lets an agent search, read and publish Zhihu
content by driving a browser that holds a scanned-in Zhihu session.

- 上游代码库: https://github.com/Douyh123/zhihu-mcp
- LobeHub: https://lobehub.com/mcp/douyh123-zhihu-mcp

归档在 `examples/` 而不是 `packages/`：`examples/` 在 pnpm workspace 的
`packages:` 匹配范围之外，不会被主仓库的 install / turbo build / typecheck /
test 拉进去。它是 Python 项目，与 Node 工作区完全无关。

## 与 wecom-doc-mcp 的关键差异

| | wecom-doc-mcp | zhihu-mcp |
|---|---|---|
| 语言 | Node | Python 3.10+ |
| 传输 | **stdio**（由 agent 拉起进程） | **streamable HTTP**（需常驻服务） |
| Multica 配置 | `{"command":"node","args":[...]}` | `{"type":"http","url":"http://127.0.0.1:18060/mcp"}` |
| 需要先启动吗 | 否 | **是** |

zhihu-mcp 是 HTTP 服务，**必须先跑起来**，Multica 才能连上。

## 在一台新机器上启用

```powershell
# 装依赖 + 起服务（幂等，可重复执行）
.\scripts\setup-zhihu-mcp.ps1 -Start

# 首次需要扫码登录（会弹浏览器）
.\scripts\setup-zhihu-mcp.ps1 -Login
```

登录 cookie 存在 `examples/mcp-servers/zhihu-mcp/cookies/cookies.json`，
已被 `.gitignore` 排除，不会进仓库。

### 接入 Multica

1. **设置 → MCP** 新增服务器，配置：
   ```json
   { "type": "http", "url": "http://127.0.0.1:18060/mcp" }
   ```
2. 到目标智能体的 **MCP** 标签页把它分配给该智能体。库里的条目
   **不会自动下发**给任何智能体 —— 这是平台刻意的两步设计。

## 提供的工具（11 个）

| 工具 | 说明 |
|---|---|
| `check_login_status` | 检查登录状态 |
| `get_login_qrcode` | 获取登录二维码（Base64） |
| `delete_cookies` | 删除 cookie，重置登录 |
| `search_content` | 搜索知乎内容（结果存为 md） |
| `get_recommend_list` | 首页推荐列表 |
| `get_feed_detail` | 帖子详情（正文 + 评论） |
| `get_user_profile` | 用户主页 |
| `publish_article` | **发布图文文章** |
| `publish_video` | **发布视频** |
| `post_comment` | **发表评论** |
| `reply_comment` | **回复评论** |

## 本 fork 相对上游的改动

`browser/manager.py` 新增 `ZHIHU_MCP_BROWSER_CHANNEL` 环境变量（带
`Multica fork` 注释）。上游只用 Playwright 自带的 Chromium，而该二进制需要
`playwright install chromium` 下载并解压 ~170MB —— 在装了腾讯电脑管家一类
端点安全软件的机器上，解压会被静默拦截（稳定停在
`chrome-win64\D3DCompiler_47.dll` 之后），安装器仍返回 0，症状只体现为后续的
`Executable doesn't exist at ...\chromium-<rev>\chrome.exe`。

设 `ZHIHU_MCP_BROWSER_CHANNEL=chrome|msedge` 即可直接驱动系统已装的浏览器，
完全绕开这次下载。不设则保持上游行为。setup 脚本默认自动探测并启用。

## 风险（未修改，使用前请知悉）

- **有发布权限。** `publish_article` / `publish_video` / `post_comment` /
  `reply_comment` 会**以你的知乎账号真实发内容**。没有 dry-run、没有二次确认。
  分配给智能体前请想清楚：凡是能调用该智能体的人，都能通过它用你的账号发帖。
- **cookie 是明文**存在 `cookies/cookies.json`，等同于知乎账号凭据。
- **Chromium sandbox 被关闭**（`--no-sandbox`），与上游一致。
- 服务默认只监听 `127.0.0.1`，不对外暴露 —— 不要改成 `0.0.0.0`，否则同网段
  任何人都能用你的知乎账号发帖。
- 上游仓库无 LICENSE 文件。
