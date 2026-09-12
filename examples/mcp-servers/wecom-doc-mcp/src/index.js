#!/usr/bin/env node
/**
 * 企业微信文档 MCP 服务
 *
 * 提供以下工具：
 * 1. wecom_doc_login       - 启动浏览器登录企微文档
 * 2. wecom_doc_status      - 检查登录状态
 * 3. wecom_doc_fetch       - 获取企微文档/表格内容
 * 4. wecom_doc_screenshot  - 截图企微文档
 * 5. wecom_doc_write       - 向在线文档末尾追加文本
 * 6. wecom_doc_write_sheet - 向在线表格指定单元格写入/追加内容
 * 7. wecom_doc_create      - 创建新的企微在线文档/表格/PPT
 */
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import { checkLoginStatus, closeBrowser } from './browser.js';
import { fetchDocContent, screenshotDoc, writeDocContent, writeSheetCell, writeSheetCells, createDoc } from './fetcher.js';
import { spawn } from 'child_process';
import path from 'path';
import os from 'os';
import fs from 'fs';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// 创建 MCP 服务器
const server = new Server(
  {
    name: 'wecom-doc-mcp',
    version: '1.0.0',
  },
  {
    capabilities: {
      tools: {},
    },
  }
);

/**
 * 注册可用工具列表
 */
server.setRequestHandler(ListToolsRequestSchema, async () => {
  return {
    tools: [
      {
        name: 'wecom_doc_login',
        description:
          '启动可视化浏览器窗口，让用户扫码登录企业微信文档。登录成功后会保存 Cookie 供后续使用。注意：此操作会打开一个浏览器窗口，用户需要手动扫码。',
        inputSchema: {
          type: 'object',
          properties: {},
          required: [],
        },
      },
      {
        name: 'wecom_doc_status',
        description:
          '检查当前企业微信文档的登录状态，确认 Cookie 是否有效。',
        inputSchema: {
          type: 'object',
          properties: {},
          required: [],
        },
      },
      {
        name: 'wecom_doc_fetch',
        description:
          '获取企业微信文档（doc.weixin.qq.com）的内容。支持在线文档、在线表格、幻灯片等类型。需要先登录（使用 wecom_doc_login）。',
        inputSchema: {
          type: 'object',
          properties: {
            url: {
              type: 'string',
              description:
                '企业微信文档的完整 URL，例如：https://doc.weixin.qq.com/sheet/e3_xxx 或 https://doc.weixin.qq.com/doc/w3_xxx',
            },
            tab: {
              type: 'string',
              description:
                '（可选）表格的 tab/sheet 标识，用于获取指定工作表的内容',
            },
          },
          required: ['url'],
        },
      },
      {
        name: 'wecom_doc_screenshot',
        description:
          '对企业微信文档页面进行截图，返回截图文件路径。需要先登录（使用 wecom_doc_login）。',
        inputSchema: {
          type: 'object',
          properties: {
            url: {
              type: 'string',
              description: '企业微信文档的完整 URL',
            },
            output_path: {
              type: 'string',
              description:
                '截图保存路径（包含文件名），例如：/tmp/doc-screenshot.png',
            },
          },
          required: ['url'],
        },
      },
      {
        name: 'wecom_doc_write',
        description:
          '向企业微信在线文档（doc 类型）的末尾追加文本内容。通过模拟键盘输入实现，适用于 canvas 渲染的企微文档。需要先登录（使用 wecom_doc_login）。注意：只支持纯文本追加，不支持富文本格式。',
        inputSchema: {
          type: 'object',
          properties: {
            url: {
              type: 'string',
              description:
                '企业微信文档的完整 URL，例如：https://doc.weixin.qq.com/doc/w3_xxx',
            },
            content: {
              type: 'string',
              description:
                '要追加到文档末尾的文本内容，支持换行符(\\n)分隔多行',
            },
            type_delay: {
              type: 'number',
              description:
                '（可选）每个字符的输入延时(毫秒)，默认 10。设置更大的值可提高稳定性但会更慢',
            },
            line_delay: {
              type: 'number',
              description:
                '（可选）每行之间的等待时间(毫秒)，默认 300。较长的文档建议增加此值',
            },
          },
          required: ['url', 'content'],
        },
      },
      {
        name: 'wecom_doc_write_sheet',
        description:
          '向企业微信在线表格（sheet 类型）的指定单元格写入或追加内容。通过模拟名称框导航和键盘输入实现。需要先登录（使用 wecom_doc_login）。支持追加模式（在现有内容后追加）和覆盖模式（替换单元格内容）。',
        inputSchema: {
          type: 'object',
          properties: {
            url: {
              type: 'string',
              description:
                '企业微信表格的完整 URL，例如：https://doc.weixin.qq.com/sheet/e3_xxx',
            },
            cell: {
              type: 'string',
              description:
                '目标单元格地址，如 "A1", "N1", "B3", "C10"。列用字母表示，行用数字表示',
            },
            content: {
              type: 'string',
              description: '要写入的文本内容',
            },
            mode: {
              type: 'string',
              enum: ['append', 'overwrite'],
              description:
                '（可选）写入模式："append" 追加到现有内容末尾（默认），"overwrite" 覆盖替换现有内容',
            },
            tab: {
              type: 'string',
              description:
                '（可选）工作表的 tab 标识，用于指定写入哪个工作表',
            },
            type_delay: {
              type: 'number',
              description:
                '（可选）每个字符的输入延时(毫秒)，默认 15',
            },
          },
          required: ['url', 'cell', 'content'],
        },
      },
      {
        name: 'wecom_doc_write_sheet_batch',
        description:
          '批量向企业微信在线表格写入多个单元格。相比逐个调用 wecom_doc_write_sheet，批量写入只打开一次页面，大幅减少总耗时（4个单元格从约80s降至约25s）。适合需要同时写入多个单元格的场景。',
        inputSchema: {
          type: 'object',
          properties: {
            url: {
              type: 'string',
              description: '企业微信表格的完整 URL',
            },
            cells: {
              type: 'array',
              items: {
                type: 'object',
                properties: {
                  cell: { type: 'string', description: '单元格地址，如 "A1", "B3"' },
                  content: { type: 'string', description: '要写入的内容' },
                },
                required: ['cell', 'content'],
              },
              description: '要写入的单元格数组，每项包含 cell（地址）和 content（内容）',
            },
            mode: {
              type: 'string',
              enum: ['append', 'overwrite'],
              description: '（可选）写入模式，默认 "overwrite"',
            },
            tab: {
              type: 'string',
              description: '（可选）工作表的 tab 标识',
            },
          },
          required: ['url', 'cells'],
        },
      },
      {
        name: 'wecom_doc_create',
        description:
          '创建一个新的企业微信在线文档（支持在线文档、在线表格、幻灯片）。通过模拟浏览器操作在企微文档首页新建文档，返回新文档的 URL。需要先登录（使用 wecom_doc_login）。',
        inputSchema: {
          type: 'object',
          properties: {
            type: {
              type: 'string',
              enum: ['doc', 'sheet', 'slide', 'mindmap', 'flowchart', 'form'],
              description:
                '（可选）要创建的文档类型："doc" 在线文档，"sheet" 在线表格（默认），"slide" 幻灯片，"mindmap" 思维导图，"flowchart" 流程图，"form" 收集表',
            },
            title: {
              type: 'string',
              description:
                '（可选）文档标题，不指定则使用企微默认标题',
            },
          },
          required: [],
        },
      },
    ],
  };
});

/**
 * 处理工具调用
 */
server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args = {} } = request.params;

  try {
    switch (name) {
      case 'wecom_doc_login': {
        // 启动登录脚本（独立进程，因为需要可视化浏览器）
        const loginScript = path.join(__dirname, 'login.js');
        const child = spawn('node', [loginScript], {
          stdio: ['ignore', 'ignore', 'inherit'], // 只继承 stderr，避免 stdout 干扰 MCP 的 JSON-RPC 通信
          detached: true,
        });
        child.unref();

        return {
          content: [
            {
              type: 'text',
              text: [
                '🚀 已启动登录浏览器窗口！',
                '',
                '请在弹出的浏览器中完成登录：',
                '1. 使用企业微信扫码登录',
                '2. 或使用账号密码登录',
                '3. 登录成功后会自动保存状态',
                '',
                '登录完成后，可以使用 wecom_doc_fetch 工具获取文档内容。',
                '',
                '⚠️ 如果没有看到浏览器窗口，请在终端手动运行：',
                `cd ${path.join(__dirname, '..')} && npm run login`,
              ].join('\n'),
            },
          ],
        };
      }

      case 'wecom_doc_status': {
        const status = await checkLoginStatus();
        return {
          content: [
            {
              type: 'text',
              text: status.loggedIn
                ? `✅ 已登录企业微信文档\n状态: ${status.reason}`
                : `❌ 未登录或登录已过期\n原因: ${status.reason}\n\n请使用 wecom_doc_login 工具登录。`,
            },
          ],
        };
      }

      case 'wecom_doc_fetch': {
        const { url, tab } = args;
        if (!url) {
          return {
            content: [
              {
                type: 'text',
                text: '❌ 请提供文档 URL',
              },
            ],
          };
        }

        console.error(`[wecom-doc-mcp] 正在获取文档: ${url}`);
        const result = await fetchDocContent(url, tab);

        if (!result.success) {
          return {
            content: [
              {
                type: 'text',
                text: `❌ 获取文档失败\n错误: ${result.error}\nURL: ${result.url}`,
              },
            ],
          };
        }

        // 格式化输出
        let output = `📄 文档获取成功\n`;
        output += `类型: ${result.docType}\n`;
        output += `URL: ${result.url}\n\n`;

        if (result.title) {
          output += `## 标题\n${result.title}\n\n`;
        }

        // 输出权威的工作表列表（来自 opendoc API 或 Excel 导出解析）
        // 这是唯一可信的 sheet 列表来源，避免将 sheet 内容误判为 sheet 名称
        if (result.sheetList && result.sheetList.length > 0) {
          output += `## 工作表列表（共 ${result.sheetList.length} 个）\n`;
          result.sheetList.forEach((sh, i) => {
            const extra = [];
            if (sh.id && !sh.id.startsWith('sheet_') && !sh.id.startsWith('tab_')) {
              extra.push(`tab=${sh.id}`);
            }
            if (sh.hidden) extra.push('隐藏');
            const suffix = extra.length > 0 ? ` (${extra.join(', ')})` : '';
            output += `${i + 1}. **${sh.name}**${suffix}\n`;
          });
          output += '\n';
        }

        if (result.sheets && result.sheets.length > 0) {
          // 如果用户指定了 tab 参数，只输出对应的 sheet 数据
          // 解决策略 E（Excel 导出）返回全部 sheet 导致目标 sheet 被截断的问题
          let sheetsToOutput = result.sheets;
          
          // 确定用户请求的 tab：优先 MCP 参数 > fetchDocContent 携带 > URL 中解析
          let requestedTab = result.requestedTab || tab;
          if (!requestedTab && url) {
            try {
              const urlObj = new URL(url);
              requestedTab = urlObj.searchParams.get('tab');
            } catch { /* ignore */ }
          }
          
          if (requestedTab && result.sheets.length > 1) {
            let filtered = [];
            let matchedName = null;
            
            if (result.sheetList && result.sheetList.length > 0) {
              // 方式1：通过 sheetList 找到 tab ID 对应的 sheet 名称，再按名称过滤
              const matchedMeta = result.sheetList.find(s => s.id === requestedTab);
              if (matchedMeta) {
                matchedName = matchedMeta.name;
                filtered = result.sheets.filter(s => s.name === matchedMeta.name);
              }
            }
            
            if (filtered.length === 0) {
              // 方式2（兜底）：通过 sheetList 中的索引位置匹配 sheets 中的对应位置
              if (result.sheetList && result.sheetList.length > 0) {
                const idx = result.sheetList.findIndex(s => s.id === requestedTab);
                if (idx >= 0 && idx < result.sheets.length) {
                  filtered = [result.sheets[idx]];
                  matchedName = result.sheets[idx].name;
                }
              }
            }
            
            if (filtered.length > 0) {
              sheetsToOutput = filtered;
              output += `> 已筛选工作表: **${matchedName}** (tab=${requestedTab})\n\n`;
            } else {
              output += `> ⚠️ 未找到 tab=${requestedTab} 对应的工作表，已输出全部数据\n\n`;
            }
          }

          output += `## 表格数据\n`;
          sheetsToOutput.forEach((sheet) => {
            output += `### ${sheet.name}\n`;
            sheet.rows.forEach((row, rowIndex) => {
              // 转义单元格中的管道符和换行符，防止破坏 Markdown 表格结构
              const escapedRow = row.map(cell => cell.replace(/\|/g, '\\|').replace(/\n/g, ' '));
              const docRowNumber = rowIndex + 1; // 文档行号从 1 开始
              output += `| ${escapedRow.join(' | ')} |<!-- row:${docRowNumber} -->\n`;
              // 在第一行（表头）后添加 Markdown 表格分隔行
              if (rowIndex === 0) {
                output += `| ${row.map(() => '---').join(' | ')} |\n`;
              }
            });
            output += '\n';
          });
        }

        if (result.content) {
          output += `## 文档内容\n${result.content}\n\n`;
        }

        if (result.workbookData) {
          output += `## 解压后的工作簿数据\n${result.workbookData}\n\n`;
        }

        if (result.workbookInfo) {
          output += `## 工作簿信息\n`;
          output += `- 压缩状态: ${result.workbookInfo.compressed ? '已压缩' : '未压缩'}\n`;
          output += `- 压缩数据长度: ${result.workbookInfo.length} 字符\n`;
          output += `- 表格尺寸: ${result.workbookInfo.maxRow} 行 × ${result.workbookInfo.maxCol} 列\n`;
          output += `- 数据预览: ${result.workbookInfo.preview}...\n\n`;
        }

        if (result.protobufStrings && result.protobufStrings.length > 0) {
          output += `## Protobuf提取的字符串 (${result.protobufStrings.length}个)\n`;
          output += result.protobufStrings.join('\n');
          output += '\n\n';
        }

        if (result.protobufHint) {
          output += `## 表格尺寸提示\n${result.protobufHint}\n\n`;
        }

        if (result.exportApiPaths && result.exportApiPaths.length > 0) {
          output += `## 导出API路径探测\n`;
          result.exportApiPaths.forEach((api, i) => {
            output += `${i + 1}. ${api.method} ${api.url}\n`;
          });
          output += '\n';
        }

        if (result.rawApiData) {
          output += `## 原始API数据\n${result.rawApiData}\n\n`;
        }

        if (result.clipboardText) {
          output += `## 选区文本\n${result.clipboardText}\n\n`;
        }

        if (result.rawText && !result.content && !result.clipboardText && !result.rawApiData
            && !(result.nodes && result.nodes.length > 0)) {
          output += `## 页面文本\n${result.rawText}\n\n`;
        }

        if (result.text && !result.content) {
          output += `## 页面文本\n${result.text}\n\n`;
        }

        // 思维导图节点数据
        if (result.nodes && result.nodes.length > 0) {
          output += `## 思维导图节点（共 ${result.nodes.length} 个）\n`;
          result.nodes.forEach((node) => {
            const indent = '  '.repeat(node.depth || 0);
            output += `${indent}- ${node.text}\n`;
          });
          output += '\n';
        }

        // 思维导图截图
        if (result.mindmapScreenshot) {
          const imgPath = result.mindmapScreenshot;
          try {
            const imgBuf = fs.readFileSync(imgPath);
            if (imgBuf.length <= 500000) {
              const base64 = imgBuf.toString('base64');
              const isJpeg = imgPath.endsWith('.jpg') || imgPath.endsWith('.jpeg');

              // 构建包含文本和图片的 content 数组
              const contentItems = [{ type: 'text', text: output }];
              contentItems.push({
                type: 'image',
                data: base64,
                mimeType: isJpeg ? 'image/jpeg' : 'image/png',
              });

              return { content: contentItems };
            } else {
              output += `## 截图\n截图已保存到: ${imgPath} (${(imgBuf.length / 1024).toFixed(0)}KB)\n\n`;
            }
          } catch (imgErr) {
            output += `## 截图\n截图读取失败: ${imgErr.message}\n\n`;
          }
        }

        if (result.slideCount) {
          output += `## 文档信息\n总页数: ${result.slideCount}\n`;
          if (result.isReadOnly) output += `权限: 只读\n`;
          output += '\n';
        }

        if (result.slides && result.slides.length > 0) {
          const slidesWithText = result.slides.filter(s => s.text);
          if (slidesWithText.length > 0) {
            output += `## 幻灯片内容（${slidesWithText.length} 页有文本）\n`;
            result.slides.forEach((slide) => {
              if (slide.text) {
                output += `### 第 ${slide.index} 页\n${slide.text}\n\n`;
              }
            });
          } else {
            output += `## 幻灯片内容\n共 ${result.slides.length} 页，均为 canvas 渲染内容（纯图片/图表），无法直接提取文本。\n\n`;
          }
        }

        // 输出 rawText（slide 策略提取的非结构化文本，仅在前面通用逻辑未输出时）
        if (result.rawText && result.rawText.length > 10
            && (result.content || result.clipboardText || result.rawApiData)) {
          output += `## 提取的原始文本\n${result.rawText.substring(0, 10000)}\n\n`;
        }

        if (result.slideScreenshots && result.slideScreenshots.length > 0) {
          // 截图直接以 MCP image content 嵌入返回
          // 严格限制内联数量和单张大小，避免 AI 模型 "input length too long" 错误
          const MAX_INLINE_IMAGES = 3;          // 最多内联 3 张（base64 膨胀约 1.37x）
          const MAX_SINGLE_IMAGE_BYTES = 200000; // 单张图片原始大小上限 200KB
          const inlineCount = Math.min(result.slideScreenshots.length, MAX_INLINE_IMAGES);
          const totalPages = result.totalSlidePages || result.slideScreenshots.length;

          output += `## 截图（共 ${result.slideScreenshots.length} 张，总 ${totalPages} 页）\n`;
          if (result.slideScreenshots.length < totalPages) {
            output += `> 注意：PPT 共 ${totalPages} 页，已截取前 ${result.slideScreenshots.length} 页。\n`;
          }
          if (inlineCount < result.slideScreenshots.length) {
            output += `> 下方内联展示前 ${inlineCount} 张，其余截图路径：\n`;
            for (let i = inlineCount; i < result.slideScreenshots.length; i++) {
              output += `> 第 ${i + 1} 页: ${result.slideScreenshots[i]}\n`;
            }
          }
          output += '\n';

          // 构建包含文本和图片的 content 数组
          const contentItems = [{ type: 'text', text: output }];

          for (let i = 0; i < inlineCount; i++) {
            const imgPath = result.slideScreenshots[i];
            try {
              const imgBuf = fs.readFileSync(imgPath);
              // 跳过过大的图片（避免 base64 后超限），改为文件路径引用
              if (imgBuf.length > MAX_SINGLE_IMAGE_BYTES) {
                contentItems.push({
                  type: 'text',
                  text: `[第 ${i + 1} 页截图过大 (${(imgBuf.length / 1024).toFixed(0)}KB)，已保存到: ${imgPath}]\n`,
                });
                continue;
              }
              const base64 = imgBuf.toString('base64');
              const isJpeg = imgPath.endsWith('.jpg') || imgPath.endsWith('.jpeg');
              contentItems.push({
                type: 'image',
                data: base64,
                mimeType: isJpeg ? 'image/jpeg' : 'image/png',
              });
            } catch (imgErr) {
              contentItems.push({
                type: 'text',
                text: `[第 ${i + 1} 页截图读取失败: ${imgErr.message}]\n`,
              });
            }
          }

          return { content: contentItems };
        }

        return {
          content: [
            {
              type: 'text',
              text: output,
            },
          ],
        };
      }

      case 'wecom_doc_screenshot': {
        const { url: docUrl, output_path } = args;
        if (!docUrl) {
          return {
            content: [
              {
                type: 'text',
                text: '❌ 请提供文档 URL',
              },
            ],
          };
        }

        // os.tmpdir()（Multica fork 修改）：上游硬编码 '/tmp/...'，在 Windows 上
        // 会解析成 C:\tmp\ —— 该目录通常不存在，截图会直接失败。
        const screenshotPath =
          output_path || path.join(os.tmpdir(), 'wecom-doc-screenshot.png');
        const result = await screenshotDoc(docUrl, screenshotPath);

        if (!result.success) {
          return {
            content: [
              {
                type: 'text',
                text: `❌ 截图失败: ${result.error}`,
              },
            ],
          };
        }

        return {
          content: [
            {
              type: 'text',
              text: `📸 截图成功！\n路径: ${result.path}\n大小: ${(result.size / 1024).toFixed(1)} KB`,
            },
          ],
        };
      }

      case 'wecom_doc_write': {
        const { url: writeUrl, content: writeContent, type_delay, line_delay } = args;
        if (!writeUrl) {
          return {
            content: [{ type: 'text', text: '❌ 请提供文档 URL' }],
          };
        }
        if (!writeContent) {
          return {
            content: [{ type: 'text', text: '❌ 请提供要写入的内容' }],
          };
        }

        console.error(`[wecom-doc-mcp] 正在写入文档: ${writeUrl}`);
        const writeResult = await writeDocContent(writeUrl, writeContent, {
          typeDelay: type_delay ?? 10,
          lineDelay: line_delay ?? 300,
        });

        if (!writeResult.success) {
          return {
            content: [
              {
                type: 'text',
                text: `❌ 写入失败: ${writeResult.error}\nURL: ${writeResult.url}`,
              },
            ],
          };
        }

        return {
          content: [
            {
              type: 'text',
              text: `✅ 写入成功！\n${writeResult.message}\nURL: ${writeResult.url}`,
            },
          ],
        };
      }

      case 'wecom_doc_write_sheet': {
        const { url: sheetUrl, cell, content: sheetContent, mode, tab: sheetTab, type_delay: sheetTypeDelay } = args;
        if (!sheetUrl) {
          return {
            content: [{ type: 'text', text: '❌ 请提供表格 URL' }],
          };
        }
        if (!cell) {
          return {
            content: [{ type: 'text', text: '❌ 请提供目标单元格地址（如 A1, N1, B3）' }],
          };
        }
        if (!sheetContent) {
          return {
            content: [{ type: 'text', text: '❌ 请提供要写入的内容' }],
          };
        }

        console.error(`[wecom-doc-mcp] 正在写入表格单元格: ${sheetUrl} -> ${cell}`);
        const sheetWriteResult = await writeSheetCell(sheetUrl, cell, sheetContent, {
          mode: mode || 'append',
          tab: sheetTab,
          typeDelay: sheetTypeDelay ?? 15,
        });

        if (!sheetWriteResult.success) {
          return {
            content: [
              {
                type: 'text',
                text: `❌ 写入表格失败: ${sheetWriteResult.error}\nURL: ${sheetWriteResult.url}`,
              },
            ],
          };
        }

        return {
          content: [
            {
              type: 'text',
              text: `✅ 表格写入成功！\n${sheetWriteResult.message}\nURL: ${sheetWriteResult.url}`,
            },
          ],
        };
      }

      case 'wecom_doc_write_sheet_batch': {
        const { url: batchUrl, cells: batchCells, mode: batchMode, tab: batchTab } = args;
        if (!batchUrl) {
          return { content: [{ type: 'text', text: '❌ 请提供表格 URL' }] };
        }
        if (!batchCells || !Array.isArray(batchCells) || batchCells.length === 0) {
          return { content: [{ type: 'text', text: '❌ 请提供要写入的单元格数组 cells' }] };
        }

        console.error(`[wecom-doc-mcp] 批量写入 ${batchCells.length} 个单元格: ${batchUrl}`);
        const batchResult = await writeSheetCells(batchUrl, batchCells, {
          mode: batchMode || 'overwrite',
          tab: batchTab,
        });

        if (!batchResult.success) {
          return {
            content: [{ type: 'text', text: `❌ 批量写入失败: ${batchResult.error}\nURL: ${batchResult.url}` }],
          };
        }

        let detail = batchResult.results.map(r =>
          r.success ? `  ✅ ${r.cell} (${r.elapsed})` : `  ❌ ${r.cell}: ${r.error}`
        ).join('\n');

        return {
          content: [{
            type: 'text',
            text: `✅ ${batchResult.message}\n${detail}\nURL: ${batchResult.url}`,
          }],
        };
      }

      case 'wecom_doc_create': {
        const { type: docType = 'sheet', title: docTitle = '' } = args;

        console.error(`[wecom-doc-mcp] 正在创建文档: type=${docType}, title=${docTitle || '(默认)'}`);
        const createResult = await createDoc(docType, docTitle);

        if (!createResult.success) {
          return {
            content: [
              {
                type: 'text',
                text: `❌ 创建文档失败: ${createResult.error}`,
              },
            ],
          };
        }

        return {
          content: [
            {
              type: 'text',
              text: `✅ 文档创建成功！\n${createResult.message}`,
            },
          ],
        };
      }

      default:
        return {
          content: [
            {
              type: 'text',
              text: `❌ 未知工具: ${name}`,
            },
          ],
        };
    }
  } catch (err) {
    return {
      content: [
        {
          type: 'text',
          text: `❌ 执行出错: ${err.message}\n\n${err.stack}`,
        },
      ],
    };
  }
});

/**
 * 启动服务
 */
async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error('[wecom-doc-mcp] 🚀 MCP 服务已启动');

  // 优雅关闭
  process.on('SIGINT', async () => {
    console.error('[wecom-doc-mcp] 正在关闭...');
    await closeBrowser();
    process.exit(0);
  });

  process.on('SIGTERM', async () => {
    console.error('[wecom-doc-mcp] 正在关闭...');
    await closeBrowser();
    process.exit(0);
  });
}

main().catch((err) => {
  console.error('[wecom-doc-mcp] 启动失败:', err);
  process.exit(1);
});
