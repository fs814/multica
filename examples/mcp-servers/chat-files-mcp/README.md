# chat-files-mcp（微信 / 企业微信聊天文件整理）

列出微信和企业微信聊天记录里收到的文档（pdf、ppt/pptx、doc/docx、xls/xlsx、caj、
epub、WPS/iWork 格式……），按类别整理，然后：

- 导入 **Zotero**：每个类别一个 collection（`论文/LLM` 这样的写法会建子 collection），
  文件作为独立附件，带 `chat-files` 和 `wechat` / `wecom` 标签；
- 或复制到 **分类文件夹**：`目标目录/类别/文件名`；
- 最后 **核对**：重新校验归档副本，列出哪些原件已经安全归档，再去微信 / 企业微信里清理聊天记录。

本项目是本 fork 自己写的（不是第三方 vendored），stdio 传输，由 agent 拉起进程，
不需要常驻服务。核心逻辑只用 Python 标准库；`mcp` SDK 只有 MCP 入口需要。
**它从不删除或移动聊天文件。**

## 启用

```bash
scripts/setup-chat-files-mcp.sh --test        # macOS / Linux
.\scripts\setup-chat-files-mcp.ps1 -Test      # Windows
```

脚本创建 `.venv`、安装 `mcp`，并打印 Multica 用的配置：

1. **设置 → MCP** 新增服务器：
   ```json
   { "command": "<仓库>/examples/mcp-servers/chat-files-mcp/.venv/bin/python",
     "args": ["<仓库>/examples/mcp-servers/chat-files-mcp/server.py"] }
   ```
   Windows 上 python 在 `.venv\Scripts\python.exe`。路径必须是该机器上的绝对路径。
2. 到目标智能体的 **MCP** 标签页把它分配给该智能体。
3. 可选：把 `skills/chat-files/SKILL.md` 加为技能，agent 会按"扫描 → 分类 → 预览计划 → 归档 → 核对"的流程走。

## 文件在哪里

| 应用 | 系统 | 目录（`*` 是账号） |
|---|---|---|
| 企业微信 | macOS | `~/Library/Containers/com.tencent.WeWorkMac/Data/Documents/Profiles/*/Caches/Files` |
| 企业微信 | Windows | `文档\WXWork\*\Cache\File` |
| 微信 4.x | macOS | `~/Library/Containers/com.tencent.xinWeChat/Data/Documents/xwechat_files/*/msg/file` |
| 微信 3.x | macOS | `…/com.tencent.xinWeChat/Data/Library/Application Support/com.tencent.xinWeChat/*/*/Message/MessageTemp/*/File` |
| 微信 4.x | Windows / Linux | `文档\xwechat_files\*\msg\file`（Linux 也查 `~/xwechat_files`） |
| 微信 3.x | Windows | `文档\WeChat Files\*\FileStorage\File`；自定义位置从 `%APPDATA%\Tencent\WeChat\All Users\config\3ebffe94.ini` 读取 |

改过存放位置（比如放到 D 盘）时，用 `CHAT_FILES_ROOTS` 补充，多个用系统路径分隔符
（Windows `;`，macOS/Linux `:`）隔开，每项是 `app=路径`，路径可含通配符：

```powershell
$env:CHAT_FILES_ROOTS = 'wechat=D:\xwechat_files\*\msg\file;wecom=D:\WXWork\*\Cache\File'
```

聊天文件只能看出账号和月份（`YYYY-MM` 目录），看不出是哪个聊天发来的：那个信息在
加密的聊天数据库里，本工具不去解密。

## 命令行用法（不需要 agent）

```bash
cd examples/mcp-servers/chat-files-mcp
python3 -m chat_files sources                 # 找到的目录
python3 -m chat_files scan                    # 建索引；--ext '*' 收录所有文件类型
python3 -m chat_files list --uncategorized
python3 -m chat_files preview <id>            # 文档开头的文字
python3 -m chat_files set-category '论文/LLM' <id> <id>
cp categories.example.json ~/.chat-files-mcp/categories.json   # 按需修改
python3 -m chat_files auto                    # 看规则会怎么分；--apply 才写入
python3 -m chat_files zotero-auth             # 一次性：在 Zotero 弹窗里点"始终允许"
python3 -m chat_files zotero-import           # 看计划；--apply 才导入
python3 -m chat_files export ~/Documents/聊天文件   # 看计划；--apply 才复制
python3 -m chat_files verify --check-zotero
python3 -m chat_files report ~/Desktop/chat-files.csv
```

`list` / `export` / `zotero-import` / `report` 都接受同一组筛选：`--id`、`--app`、`--ext`、
`--category`、`--uncategorized`、`-q 关键字`、`--since/--until YYYY-MM`、`--archived/--not-archived`。

## Zotero

> Multica 桌面版的 **Zotero** 页面（侧边栏 Web 下面）提供同样的“选文件 → 选分类 → 转入”操作，另外还列出“下载”文件夹。两者共用 `~/.chat-files-mcp/zotero.json` 里的本地 key，授权一次即可。

默认用 **本地桌面 Zotero**（7.1 起，本机是 10.x）的 local API：文件直接进本地库，不经网络。

1. Zotero 设置 → 高级 → 勾选 **"允许此计算机上的其他应用程序与 Zotero 通讯"**。
2. `python3 -m chat_files zotero-auth`（或 agent 调 `chat_files_zotero_authorize`），在 Zotero
   弹窗里点 **始终允许**。key 存在 `~/.chat-files-mcp/zotero.json`（仅本人可读）；点"允许"是一次性 key。
3. 导入后 PDF 可以在 Zotero 里右键 → **检索元数据**（Retrieve Metadata），从 PDF 内容生成带作者、标题的父条目。

也可以用 **zotero.org web API**：设 `ZOTERO_BACKEND=web`、`ZOTERO_API_KEY`、`ZOTERO_USER_ID`
（群组库用 `ZOTERO_GROUP_ID`）。注意文件会占用 zotero.org 的存储配额（免费 300MB）。

已导入同一个库的文件会跳过（`--force` 强制再导）；每导入一个就写一次索引，中断后重跑不会重复上传。
导入后会再读一次附件的 md5，与原件一致才算成功。

## 清理聊天记录

`verify` 会重新校验每个归档副本（文件夹副本重算 sha256；`--check-zotero` 时到 Zotero 核对附件 md5），
然后在 `~/.chat-files-mcp/` 写出：

- `safe-to-delete.txt`：内容已归档的原件路径；
- `not-archived.csv`：还没归档的文档。

清理请在应用里做：

- **微信**：设置 → 通用 → 存储空间（或"文件管理"），按聊天清理；也可以删除聊天记录。
- **企业微信**：设置 → 通用 → 存储空间管理 / 清理缓存。

清理后再跑一次 `scan`，被清掉的文件会标记为"不在磁盘上"，归档记录保留。

## 数据和安全

- 索引在 `~/.chat-files-mcp/manifest.json`（`CHAT_FILES_HOME` 可改），权限 0600：里面是文件名和路径。
- 同一内容只记一条（sha256 去重），记下所有副本路径；微信的 `xxx(1).pdf` 副本会合并。
- 预览不跑完整解析器：Office 文件从 zip 里按 4MB 上限读 XML 再用正则取文字；PDF 用
  `pdftotext`（poppler），没有时用可选的 `pypdf`；macOS 上 `.doc` 用系统自带 `textutil`。
- 分类名里的 `..`、`\`、`:` 等会被清理，导出不会写出目标目录之外。

## 测试

```bash
.venv/bin/python -m unittest discover -s tests
```

测试用临时目录模拟微信 / 企业微信目录，并起一个假的 Zotero local API，走完整的授权 → 建
collection → 建附件 → 上传 → 注册 → 核对流程，不需要真的微信、企业微信或 Zotero。
