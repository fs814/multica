/**
 * 企业微信文档内容获取模块
 * 负责打开企微文档页面并提取内容
 */
import { launchBrowser, saveState, isLoginPage } from './browser.js';
import XLSX from 'xlsx';
import fs from 'fs';
import path from 'path';
import os from 'os';

/**
 * 允许导航的域名白名单（Multica fork 新增，上游没有这一层）。
 *
 * 为什么必须有：每个写/读工具都会把调用方给的 `url` 直接交给 page.goto，
 * 而那个浏览器上下文带着已登录的企微会话。上游唯一的检查是
 * detectDocType() 的子串判断，它对无法识别的地址返回 'unknown'，而写入
 * 路径又显式放行 'unknown' —— 等于没有检查。
 *
 * 后果（不加白名单时）：`file:///C:/Users/<you>/.ssh/id_rsa` 或
 * `http://169.254.169.254/`（云元数据）都能被打开，页面文本还会经
 * extractGenericContent 原样回传给模型。这在 AI 驱动、URL 可能来自
 * 提示注入的场景下是本地文件读取 + SSRF 原语。
 *
 * 这里用 URL 解析后比对 hostname，而不是子串匹配 —— 子串匹配会被
 * `https://evil.com/?x=doc.weixin.qq.com` 之类绕过。
 */
const ALLOWED_DOC_HOSTS = new Set([
  'doc.weixin.qq.com',
  'docs.qq.com',
]);

function assertAllowedDocUrl(rawUrl) {
  let parsed;
  try {
    parsed = new URL(String(rawUrl));
  } catch {
    throw new Error(
      `不是合法的 URL: ${rawUrl}。请传入完整的企微文档链接，例如 https://doc.weixin.qq.com/sheet/e3_xxx`
    );
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') {
    throw new Error(
      `拒绝访问 ${parsed.protocol} 协议。只允许 http/https 的企微文档链接，禁止 file:// 等本地协议。`
    );
  }
  if (!ALLOWED_DOC_HOSTS.has(parsed.hostname)) {
    throw new Error(
      `拒绝访问域名 ${parsed.hostname}。只允许: ${[...ALLOWED_DOC_HOSTS].join(', ')}。` +
        `（此限制由 Multica 仓库的 fork 添加，用于防止带着企微登录态访问任意地址。）`
    );
  }
  return parsed.toString();
}

/**
 * 获取企微文档/表格内容
 * @param {string} url - 企微文档URL
 * @param {string} tab - 可选的表格tab参数
 * @returns {Promise<object>} 文档内容
 */
async function fetchDocContent(url, tab = null) {
  assertAllowedDocUrl(url);
  // launchBrowser 内部会自动检测 state.json 是否更新，无需手动 closeBrowser
  const { context } = await launchBrowser(true);
  const page = await context.newPage();

  // 构造完整URL（提到 try 外，确保 catch 中可访问）
  let targetUrl = url;
  if (tab && !url.includes('tab=')) {
    targetUrl += (url.includes('?') ? '&' : '?') + `tab=${encodeURIComponent(tab)}`;
  }

  try {
    console.error(`[wecom-doc-mcp] 正在访问: ${targetUrl}`);

    await page.goto(targetUrl, {
      waitUntil: 'domcontentloaded',
      timeout: 30000,
    });

    // 等待页面充分加载（slide 类型减少等待时间）
    const docType = detectDocType(targetUrl);
    await page.waitForTimeout(docType === 'slide' ? 3000 : 5000);

    // 检测是否需要登录
    const currentUrl = page.url();
    if (isLoginPage(currentUrl)) {
      return {
        success: false,
        error: '未登录或登录已过期，请先运行登录脚本：cd wecom-doc-mcp && npm run login',
        url: targetUrl,
      };
    }

    // 判断文档类型并提取内容

    let result;
    switch (docType) {
      case 'smartsheet':
      case 'sheet':
        result = await extractSheetContent(page);
        break;
      case 'doc':
        result = await extractDocContent(page);
        break;
      case 'slide':
        result = await extractSlideContent(page);
        break;
      case 'mindmap':
        result = await extractMindmapContent(page);
        break;
      case 'flowchart':
      default:
        result = await extractGenericContent(page);
    }

    // 更新保存状态
    await saveState();

    return {
      success: true,
      url: targetUrl,
      docType,
      requestedTab: tab,  // 用户请求的 tab，供输出格式化时过滤
      ...result,
    };
  } catch (err) {
    return {
      success: false,
      error: err.message,
      url: targetUrl,
    };
  } finally {
    try { await page.close(); } catch { /* page 可能已关闭或从未创建 */ }
  }
}

/**
 * 检测文档类型
 */
function detectDocType(url) {
  if (url.includes('/smartsheet/')) return 'smartsheet';
  if (url.includes('/sheet/')) return 'sheet';
  if (url.includes('/doc/')) return 'doc';
  if (url.includes('/slide/')) return 'slide';
  if (url.includes('/flowchart/')) return 'flowchart';
  if (url.includes('/mindmap/')) return 'mindmap';
  return 'unknown';
}

/**
 * 提取表格内容
 * 核心策略（按优先级）：
 * E. 通过 UI 菜单「文件操作 → 导出 → 本地Excel表格」自动下载并用 xlsx 库解析（最可靠）
 * B/C. 拦截 opendoc API 提取 sheet 列表和元信息（作为补充）
 * A/D. 从 JS 运行时内存中提取数据（兜底）
 * F. Protobuf 裸解析（最后手段）
 */
async function extractSheetContent(page) {
  let content = {
    title: '',
    sheets: [],
    rawText: '',
  };

  // 先获取标题
  try {
    content.title = await page.evaluate(() => {
      const titleEl =
        document.querySelector('.header-title-text') ||
        document.querySelector('.doc-header-title') ||
        document.querySelector('[class*="title"]') ||
        document.querySelector('title');
      return titleEl ? titleEl.textContent.trim() : document.title;
    });
  } catch {
    content.title = '';
  }

  // === 策略 A：从页面 JS 运行时中提取企微表格引擎的内部数据模型 ===
  // 企微表格在 canvas 渲染完成后，数据一定存在于 JS 内存中
  // 尝试从企微表格引擎的各种可能入口获取已解码的单元格数据
  try {
    const engineData = await page.evaluate(() => {
      const result = { found: false, cells: null, sheetInfo: null, source: '' };

      // 方法1: 尝试通过企微表格引擎的全局 API 获取数据
      // 企微表格通常将数据存储在某些全局变量或 React 组件的 state 中
      const globalCandidates = [
        'pad', 'padModel', 'sheetModel', 'workbook', 'wb',
        '__pad__', '__sheet__', '__workbook__', '__data__',
        'clientVars', 'initialData', 'appData',
      ];

      for (const key of globalCandidates) {
        try {
          if (window[key] && typeof window[key] === 'object') {
            const str = JSON.stringify(window[key]).substring(0, 500);
            result.source += `${key}: ${str.substring(0, 100)}; `;
          }
        } catch { /* ignore */ }
      }

      // 方法2: 深度扫描 window 上的所有属性，寻找包含 getCellValue / getSheetData 等方法的对象
      try {
        for (const key of Object.getOwnPropertyNames(window)) {
          try {
            const val = window[key];
            if (val && typeof val === 'object' && typeof val.getCellValue === 'function') {
              result.found = true;
              result.source = `window.${key}.getCellValue`;
              // 尝试获取数据
              const rows = [];
              for (let r = 0; r < 200; r++) {
                const row = [];
                let hasData = false;
                for (let c = 0; c < 20; c++) {
                  try {
                    const v = val.getCellValue(r, c);
                    const text = v != null ? String(v) : '';
                    row.push(text);
                    if (text) hasData = true;
                  } catch { row.push(''); }
                }
                if (hasData) rows.push(row);
              }
              if (rows.length > 0) {
                result.cells = rows;
              }
              break;
            }
          } catch { /* ignore */ }
        }
      } catch { /* ignore */ }

      return result;
    });

    if (engineData.found && engineData.cells) {
      content.sheets.push({ name: '__current_tab__', rows: engineData.cells });
      console.error(`[extractSheetContent] 从引擎API提取到 ${engineData.cells.length} 行数据`);
    } else {
      console.error(`[extractSheetContent] 引擎API扫描结果: ${engineData.source}`);
    }
  } catch (err) {
    console.error(`[extractSheetContent] 引擎数据提取失败: ${err.message}`);
  }

  // === 策略 B：拦截 opendoc API，提取 sheet header + 解压 workbook 数据 ===
  let apiData = [];
  let opendocData = null; // 专门存储 opendoc API 的原始数据
  try {
    const capturedResponses = [];

    page.on('response', async (response) => {
      try {
        const url = response.url();
        const status = response.status();
        const contentType = response.headers()['content-type'] || '';

        // 精准拦截 opendoc API（包含表格的核心数据）
        if (status === 200 && url.includes('opendoc')) {
          const body = await response.body().catch(() => null);
          if (body && body.length > 500) {
            capturedResponses.push({
              url: url.substring(0, 300),
              contentType,
              size: body.length,
              body,
              isOpendoc: true,
            });
            console.error(`[extractSheetContent] 捕获 opendoc API: ${url.substring(0, 120)} (${body.length} bytes)`);
          }
        }
        // 也拦截其他可能包含表格数据的 API
        else if (status === 200 && 
            (url.includes('GetSheet') || url.includes('getSheetData') || 
             url.includes('cell') || url.includes('GetBook') || 
             url.includes('spreadsheet'))) {
          const body = await response.body().catch(() => null);
          if (body && body.length > 200) {
            capturedResponses.push({
              url: url.substring(0, 300),
              contentType,
              size: body.length,
              body,
              isOpendoc: false,
            });
            console.error(`[extractSheetContent] 捕获其他API: ${url.substring(0, 120)} (${body.length} bytes)`);
          }
        }
      } catch { /* 某些响应可能无法读取 body */ }
    });

    // 刷新页面以重新触发数据加载
    console.error('[extractSheetContent] 刷新页面以捕获API数据...');
    await page.reload({ waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(8000);

    console.error(`[extractSheetContent] 共捕获 ${capturedResponses.length} 个API响应`);

    // 解析捕获的响应数据
    for (const resp of capturedResponses) {
      try {
        const bodyStr = resp.body.toString('utf-8');
        let parsed = null;
        try { parsed = JSON.parse(bodyStr); } catch { continue; }
        if (!parsed) continue;

        if (resp.isOpendoc) {
          opendocData = parsed;
          console.error(`[extractSheetContent] 成功解析 opendoc 数据`);
        }

        apiData.push({ url: resp.url, size: resp.size, data: parsed });
      } catch { /* ignore */ }
    }
  } catch (err) {
    console.error(`[extractSheetContent] 网络拦截失败: ${err.message}`);
  }

  // === 策略 C：从 opendoc 数据中提取 sheet 列表和尝试解压 workbook ===
  if (opendocData) {
    try {
      // 提取 sheet header 信息（tab名称和ID映射）
      const clientVars = opendocData.clientVars;
      const collabVars = clientVars?.collab_client_vars;
      
      if (collabVars?.header?.[0]?.d) {
        const sheetHeaders = collabVars.header[0].d;
        content.sheetList = sheetHeaders.map(sh => ({
          id: sh.id,
          name: sh.name,
          type: sh.type,
          hidden: !!sh.hidden,
        }));
        console.error(`[extractSheetContent] 提取到 ${content.sheetList.length} 个工作表信息`);
      }

      // 提取标题
      content.title = clientVars?.padTitle || clientVars?.title || clientVars?.initialTitle || '';

      // 提取 workbook 压缩数据
      const attrText = collabVars?.initialAttributedText;
      if (attrText?.text?.[0]?.workbook) {
        const workbookB64 = attrText.text[0].workbook;
        const maxRow = attrText.text[0].max_row || 0;
        const maxCol = attrText.text[0].max_col || 0;
        console.error(`[extractSheetContent] 发现 workbook 压缩数据 (${workbookB64.length} chars), 表格尺寸: ${maxRow}行 × ${maxCol}列`);

        // 尝试在浏览器中使用企微自带的解压库来解压
        const decompressed = await page.evaluate(async (b64Data) => {
          try {
            // 方法1: 使用 atob + pako (如果页面已加载 pako)
            if (typeof window.pako !== 'undefined') {
              const binaryStr = atob(b64Data);
              const bytes = new Uint8Array(binaryStr.length);
              for (let i = 0; i < binaryStr.length; i++) bytes[i] = binaryStr.charCodeAt(i);
              const decompressed = window.pako.inflate(bytes, { to: 'string' });
              return { success: true, data: decompressed.substring(0, 200000), method: 'pako' };
            }

            // 方法2: 使用 DecompressionStream API (现代浏览器原生支持)
            try {
              const binaryStr = atob(b64Data);
              const bytes = new Uint8Array(binaryStr.length);
              for (let i = 0; i < binaryStr.length; i++) bytes[i] = binaryStr.charCodeAt(i);
              
              const ds = new DecompressionStream('deflate');
              const writer = ds.writable.getWriter();
              writer.write(bytes);
              writer.close();
              
              const reader = ds.readable.getReader();
              const chunks = [];
              let totalLength = 0;
              while (true) {
                const { done, value } = await reader.read();
                if (done) break;
                chunks.push(value);
                totalLength += value.length;
                if (totalLength > 500000) break; // 限制大小
              }
              
              const result = new Uint8Array(totalLength);
              let offset = 0;
              for (const chunk of chunks) {
                result.set(chunk, offset);
                offset += chunk.length;
              }
              
              const text = new TextDecoder().decode(result);
              return { success: true, data: text.substring(0, 200000), method: 'DecompressionStream-deflate' };
            } catch (e1) {
              // 尝试 gzip
              try {
                const binaryStr = atob(b64Data);
                const bytes = new Uint8Array(binaryStr.length);
                for (let i = 0; i < binaryStr.length; i++) bytes[i] = binaryStr.charCodeAt(i);
                
                const ds = new DecompressionStream('gzip');
                const writer = ds.writable.getWriter();
                writer.write(bytes);
                writer.close();
                
                const reader = ds.readable.getReader();
                const chunks = [];
                let totalLength = 0;
                while (true) {
                  const { done, value } = await reader.read();
                  if (done) break;
                  chunks.push(value);
                  totalLength += value.length;
                  if (totalLength > 500000) break;
                }
                
                const result = new Uint8Array(totalLength);
                let offset = 0;
                for (const chunk of chunks) {
                  result.set(chunk, offset);
                  offset += chunk.length;
                }
                
                const text = new TextDecoder().decode(result);
                return { success: true, data: text.substring(0, 200000), method: 'DecompressionStream-gzip' };
              } catch (e2) {
                return { success: false, error: `deflate: ${e1.message}, gzip: ${e2.message}` };
              }
            }
          } catch (err) {
            return { success: false, error: err.message };
          }
        }, workbookB64);

        if (decompressed?.success) {
          console.error(`[extractSheetContent] workbook 解压成功 (${decompressed.method}), ${decompressed.data.length} 字符`);
          content.workbookData = decompressed.data;
          
          // 尝试解析解压后的数据
          try {
            const wbParsed = JSON.parse(decompressed.data);
            const sheetData = extractSheetFromApiData([{ data: wbParsed, url: 'workbook' }]);
            if (sheetData.length > 0) {
              content.sheets = sheetData;
              console.error(`[extractSheetContent] 从 workbook 中提取到 ${sheetData.length} 个 sheet`);
            }
          } catch {
            // 解压后的数据可能不是 JSON，保留原始数据
            console.error(`[extractSheetContent] workbook 数据不是标准 JSON，保留原始格式`);
          }
        } else {
          console.error(`[extractSheetContent] workbook 解压失败: ${decompressed?.error}`);
          // 保存压缩数据的摘要信息
          content.workbookInfo = {
            compressed: true,
            length: workbookB64.length,
            maxRow,
            maxCol,
            preview: workbookB64.substring(0, 100),
          };
        }
      }
    } catch (err) {
      console.error(`[extractSheetContent] opendoc 数据处理失败: ${err.message}`);
    }
  }

  // 获取 sheet tab 列表（DOM 方式，作为 opendoc API sheetList 的补充兜底）
  // 优先使用 opendoc API 获取的 sheetList（策略C），DOM 方式仅在 sheetList 为空时作为兜底
  if (!content.sheetList || content.sheetList.length === 0) {
    try {
      const tabInfo = await page.evaluate(() => {
        const tabs = [];
        
        // 方法1：精确匹配底部 tab 栏中的 sheet 标签
        // 企微表格底部 tab 栏的常见选择器
        const tabSelectors = [
          '.sheet-tab-bar .sheet-tab-item',    // 标准 sheet tab
          '.sheet-tab-container .tab-name',     // tab 名称元素
          '[class*="sheet-tab"] [class*="name"]', // 组合选择器
          '[class*="sheet-tab-item"]',           // tab item
          '[class*="sheet-bar"] [class*="tab"]', // sheet bar 下的 tab
        ];
        
        for (const selector of tabSelectors) {
          const elements = document.querySelectorAll(selector);
          if (elements.length > 0) {
            elements.forEach(el => {
              const name = el.textContent.trim();
              // 过滤掉空值和不合理的长名称（真实 sheet 名通常不超过 50 字符）
              if (name && name.length <= 50 && !tabs.includes(name)) {
                tabs.push(name);
              }
            });
            if (tabs.length > 0) return tabs;
          }
        }
        
        // 方法2：查找底部区域（y > 页面 80% 高度）中看起来像 tab 的元素
        const pageHeight = window.innerHeight;
        const threshold = pageHeight * 0.75;
        const allEl = document.querySelectorAll('*');
        const candidateTabs = [];
        
        for (const el of allEl) {
          const rect = el.getBoundingClientRect();
          const text = el.textContent?.trim();
          const cls = (el.className || '').toString().toLowerCase();
          
          // 底部区域、适当大小、包含 tab/sheet 相关 class
          if (text && text.length > 0 && text.length <= 50 
              && rect.y > threshold && rect.height > 10 && rect.height < 50
              && rect.width > 20 && rect.width < 300
              && (cls.includes('tab') || cls.includes('sheet'))
              && el.children.length <= 3) {
            // 确保不是嵌套重复
            const isDuplicate = candidateTabs.some(t => t.text === text);
            if (!isDuplicate) {
              candidateTabs.push({ text, y: rect.y, x: rect.x });
            }
          }
        }
        
        // 按 x 坐标排序（从左到右就是 tab 的显示顺序）
        candidateTabs.sort((a, b) => a.x - b.x);
        candidateTabs.forEach(t => {
          if (!tabs.includes(t.text)) tabs.push(t.text);
        });
        
        return tabs;
      });
      
      if (tabInfo.length > 0) {
        content.tabNames = tabInfo;
        console.error(`[extractSheetContent] DOM方式提取到 ${tabInfo.length} 个 tab 名称: ${tabInfo.join(', ')}`);
      }
    } catch {
      // ignore
    }
  }

  // === 策略 E（核心）：通过 UI 菜单操作「文件操作 → 导出 → 本地Excel表格」自动下载并解析 ===
  // 已验证有效的路径：点击右上角文件操作按钮 → hover导出子菜单 → 点击「本地Excel表格 (.xlsx)」
  // 触发条件：没有数据，或者只有策略 A/D 的单 sheet 占位符数据（不完整）
  const hasOnlyPlaceholderData = content.sheets.length > 0 
    && content.sheets.every(s => s.name === '__current_tab__');
  if (content.sheets.length === 0 || hasOnlyPlaceholderData) {
    try {
      console.error('[extractSheetContent] 策略E: 通过UI菜单导出Excel...');
      
      const downloadDir = path.join(os.tmpdir(), 'wecom-doc-export-' + Date.now());
      fs.mkdirSync(downloadDir, { recursive: true });
      
      // Step 1: 点击右上角「文件操作」按钮 (aria-label="file" 或 aria-label="按钮:文件操作")
      const fileBtn = await page.$('[aria-label="file"], [aria-label="按钮:文件操作"], .menu-button-file');
      if (fileBtn) {
        await fileBtn.click();
      } else {
        // 如果选择器找不到，通过坐标查找（顶部最右侧区域的按钮）
        const fileBtnPos = await page.evaluate(() => {
          const allEl = document.querySelectorAll('*');
          for (const el of allEl) {
            const ariaLabel = el.getAttribute('aria-label') || '';
            if (ariaLabel.includes('文件操作') || ariaLabel === 'file') {
              const rect = el.getBoundingClientRect();
              return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) };
            }
          }
          return null;
        });
        if (fileBtnPos) {
          await page.mouse.click(fileBtnPos.x, fileBtnPos.y);
        } else {
          console.error('[extractSheetContent] 未找到文件操作按钮');
        }
      }
      await page.waitForTimeout(1500);
      
      // Step 2: hover「导出」子菜单项以展开二级菜单
      const exportMenuPos = await page.evaluate(() => {
        const allEl = document.querySelectorAll('*');
        for (const el of allEl) {
          const text = el.textContent?.trim();
          const cls = (el.className || '').toString();
          const rect = el.getBoundingClientRect();
          // 查找菜单中的「导出」项（submenu 类型）
          if (text === '导出' && cls.includes('menu-item') && cls.includes('submenu') && rect.width > 30 && rect.y > 40) {
            return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) };
          }
        }
        // 兜底：查找纯文字「导出」
        for (const el of allEl) {
          const text = el.textContent?.trim();
          const rect = el.getBoundingClientRect();
          if (text === '导出' && rect.y > 100 && rect.y < 600 && rect.width > 30) {
            return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2) };
          }
        }
        return null;
      });
      
      if (exportMenuPos) {
        console.error(`[extractSheetContent] hover 导出菜单 at (${exportMenuPos.x}, ${exportMenuPos.y})`);
        await page.mouse.move(exportMenuPos.x, exportMenuPos.y);
        await page.waitForTimeout(2000);
        
        // Step 3: 在展开的二级菜单中查找「本地Excel表格 (.xlsx)」并点击
        const xlsxMenuPos = await page.evaluate(() => {
          const allEl = document.querySelectorAll('*');
          for (const el of allEl) {
            const text = el.textContent?.trim();
            const rect = el.getBoundingClientRect();
            if (text && /本地Excel表格.*\.xlsx/.test(text) && !text.includes('.zip') && rect.width > 30 && rect.height > 10) {
              return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2), text };
            }
          }
          // 兜底：查找包含 Excel 但不包含 zip 的菜单项
          for (const el of allEl) {
            const text = el.textContent?.trim();
            const rect = el.getBoundingClientRect();
            if (text && /Excel/i.test(text) && !/zip/i.test(text) && rect.width > 30 && rect.y > 100) {
              return { x: Math.round(rect.x + rect.width / 2), y: Math.round(rect.y + rect.height / 2), text };
            }
          }
          return null;
        });
        
        if (xlsxMenuPos) {
          console.error(`[extractSheetContent] 点击 "${xlsxMenuPos.text}" at (${xlsxMenuPos.x}, ${xlsxMenuPos.y})`);
          
          // 注册下载事件（在点击前注册）
          const downloadPromise = page.waitForEvent('download', { timeout: 45000 }).catch(() => null);
          
          await page.mouse.click(xlsxMenuPos.x, xlsxMenuPos.y);
          await page.waitForTimeout(3000);
          
          // 等待下载完成
          const download = await downloadPromise;
          if (download) {
            const downloadPath = path.join(downloadDir, download.suggestedFilename() || 'export.xlsx');
            await download.saveAs(downloadPath);
            console.error(`[extractSheetContent] 文件已下载: ${downloadPath} (${fs.statSync(downloadPath).size} bytes)`);
            
            // 用 xlsx 库解析 Excel 文件
            try {
              const workbook = XLSX.readFile(downloadPath);
              const excelSheets = [];
              for (const sheetName of workbook.SheetNames) {
                const sheet = workbook.Sheets[sheetName];
                const rows = XLSX.utils.sheet_to_json(sheet, { header: 1, defval: '' });
                if (rows.length > 0) {
                  excelSheets.push({
                    name: sheetName,
                    rows: rows.map(r => r.map(c => String(c))),
                  });
                }
              }
              if (excelSheets.length > 0) {
                // Excel 导出的数据比策略 A/D 的更完整（包含所有 sheet），替换占位符数据
                content.sheets = excelSheets;
              }
              console.error(`[extractSheetContent] Excel解析成功！共 ${content.sheets.length} 个sheet，总行数: ${content.sheets.reduce((s, sh) => s + sh.rows.length, 0)}`);
            } catch (parseErr) {
              console.error(`[extractSheetContent] Excel解析失败: ${parseErr.message}`);
            }
            try { fs.unlinkSync(downloadPath); } catch {}
          } else {
            console.error('[extractSheetContent] 未检测到下载事件');
          }
        } else {
          console.error('[extractSheetContent] 未找到 Excel 导出子菜单项');
        }
      } else {
        console.error('[extractSheetContent] 未找到导出菜单项');
      }
      
      // 关闭可能残留的菜单
      await page.keyboard.press('Escape');
      await page.waitForTimeout(500);
      
    } catch (err) {
      console.error(`[extractSheetContent] 导出Excel失败: ${err.message}`);
    } finally {
      // 确保清理临时下载目录（无论成功或异常）
      try { fs.rmSync(downloadDir, { recursive: true, force: true }); } catch {}
    }
  }

  // === 策略 D：等待表格渲染完成后，通过企微表格JS引擎逐个读取单元格 ===
  // 企微表格渲染完成后，数据一定已经被解码加载到JS内存中
  // 我们可以遍历 JS 堆中的所有对象，找到存储单元格数据的内部结构
  if (content.sheets.length === 0) {
    try {
      console.error('[extractSheetContent] 尝试从企微表格JS引擎中读取单元格数据...');
      
      // 等待表格完全渲染
      await page.waitForTimeout(3000);
      
      const cellData = await page.evaluate(() => {
        const result = { found: false, rows: [], debug: '' };
        
        // 深度搜索策略：遍历所有可能包含 getCellText/getCellValue 方法的对象
        function deepSearch(obj, depth, path, visited) {
          if (depth > 6 || !obj || typeof obj !== 'object') return null;
          if (visited.has(obj)) return null;
          visited.add(obj);
          
          // 检查是否有读取单元格的方法
          if (typeof obj.getCellText === 'function' || 
              typeof obj.getCellValue === 'function' ||
              typeof obj.getCellData === 'function' ||
              typeof obj.getCell === 'function') {
            return { obj, path, method: obj.getCellText ? 'getCellText' : obj.getCellValue ? 'getCellValue' : obj.getCellData ? 'getCellData' : 'getCell' };
          }
          
          // 检查是否有 _data / _cells / _rows 等内部属性
          if (obj._data && Array.isArray(obj._data)) {
            return { obj, path, method: '_data' };
          }
          if (obj._cells && typeof obj._cells === 'object') {
            return { obj, path, method: '_cells' };
          }
          
          try {
            const keys = Object.keys(obj);
            for (const key of keys.slice(0, 50)) { // 限制搜索宽度
              try {
                const val = obj[key];
                if (val && typeof val === 'object') {
                  const found = deepSearch(val, depth + 1, `${path}.${key}`, visited);
                  if (found) return found;
                }
              } catch { /* skip */ }
            }
          } catch { /* skip */ }
          
          return null;
        }
        
        const visited = new WeakSet();
        
        // 从 window 上的关键属性开始搜索
        const startPoints = ['pad', '__pad__', 'padModel', 'app', '__app__', 
          'sheetApp', 'workbook', 'wb', 'store', '__store__'];
        
        for (const sp of startPoints) {
          try {
            if (window[sp]) {
              const found = deepSearch(window[sp], 0, `window.${sp}`, visited);
              if (found) {
                result.debug += `Found at ${found.path} via ${found.method}; `;
                // 尝试读取数据
                if (found.method === 'getCellText' || found.method === 'getCellValue') {
                  const fn = found.obj[found.method].bind(found.obj);
                  for (let r = 0; r < 200; r++) {
                    const row = [];
                    let hasData = false;
                    for (let c = 0; c < 20; c++) {
                      try {
                        const v = fn(r, c);
                        const text = v != null ? String(v) : '';
                        row.push(text);
                        if (text) hasData = true;
                      } catch { row.push(''); }
                    }
                    if (hasData) result.rows.push(row);
                    else if (result.rows.length > 0 && !hasData) {
                      // 连续5行空行则停止
                      let emptyCount = 0;
                      for (let check = r; check < r + 5 && check < 200; check++) {
                        let anyData = false;
                        for (let c = 0; c < 20; c++) {
                          try { if (fn(check, c)) { anyData = true; break; } } catch {}
                        }
                        if (!anyData) emptyCount++;
                      }
                      if (emptyCount >= 5) break;
                    }
                  }
                }
                if (result.rows.length > 0) {
                  result.found = true;
                  break;
                }
              }
            }
          } catch { /* skip */ }
        }
        
        // 如果上面没找到，扫描所有 window 属性
        if (!result.found) {
          try {
            for (const key of Object.getOwnPropertyNames(window)) {
              try {
                const val = window[key];
                if (val && typeof val === 'object' && val !== window && 
                    !['location','navigator','document','performance','console','screen','history'].includes(key)) {
                  const found = deepSearch(val, 0, `window.${key}`, visited);
                  if (found) {
                    result.debug += `Found at ${found.path} via ${found.method}; `;
                    break;
                  }
                }
              } catch { /* skip */ }
            }
          } catch { /* skip */ }
        }
        
        return result;
      });
      
      if (cellData.found && cellData.rows.length > 0) {
        content.sheets.push({ name: '__current_tab__', rows: cellData.rows });
        console.error(`[extractSheetContent] 从JS引擎深度搜索提取到 ${cellData.rows.length} 行数据`);
      } else {
        console.error(`[extractSheetContent] JS引擎深度搜索结果: ${cellData.debug || '未找到数据接口'}`);
      }
    } catch (err) {
      console.error(`[extractSheetContent] JS引擎读取失败: ${err.message}`);
    }
  }

  // 获取 rawText 作为兜底
  if (content.sheets.length === 0 && !content.workbookData) {
    try {
      content.rawText = await page.evaluate(() => {
        return document.body.innerText.substring(0, 50000);
      });
    } catch {
      content.rawText = '';
    }
  }

  // === 最终校正：用权威的 sheetList 修正 sheets 中的名称 ===
  // sheetList 来自 opendoc API（策略C），是最准确的 sheet 元信息
  // sheets 中通过策略 A/D 提取的数据，其 name 可能是占位符 '__current_tab__'
  if (content.sheets.length > 0) {
    const authoritative = content.sheetList || []; // opendoc API 提取的权威列表
    const fallbackNames = content.tabNames || [];   // DOM 提取的兜底列表

    // 从当前页面 URL 中提取 tab 参数，用于匹配当前展示的 sheet
    let currentTabId = null;
    try {
      const pageUrl = page.url();
      const urlObj = new URL(pageUrl);
      currentTabId = urlObj.searchParams.get('tab');
    } catch { /* ignore */ }

    for (let i = 0; i < content.sheets.length; i++) {
      const sheet = content.sheets[i];
      
      if (sheet.name === '__current_tab__') {
        if (authoritative.length > 0) {
          if (content.sheets.length === 1) {
            // 单 sheet 数据场景（策略 A/D 只能提取当前展示的 sheet）
            // 优先用 URL 中的 tab 参数匹配 sheetList 中的 id
            let matched = null;
            if (currentTabId) {
              matched = authoritative.find(s => s.id === currentTabId);
            }
            if (!matched) {
              // 如果 URL 中没有 tab 参数或者没匹配到，用第一个可见 sheet
              const visibleSheets = authoritative.filter(s => !s.hidden);
              matched = visibleSheets.length > 0 ? visibleSheets[0] : authoritative[0];
            }
            if (matched) {
              sheet.name = matched.name;
            }
          } else if (i < authoritative.length) {
            sheet.name = authoritative[i].name;
          }
        } else if (i < fallbackNames.length) {
          sheet.name = fallbackNames[i];
        }
        
        // 如果还是占位符，用合理的默认名
        if (sheet.name === '__current_tab__') {
          sheet.name = `Sheet${i + 1}`;
        }
      }
    }
  }

  // 构建最终的权威工作表列表
  // 确保返回数据中有一个统一、准确的工作表列表
  if (!content.sheetList || content.sheetList.length === 0) {
    if (content.sheets.length > 0) {
      // 从 Excel 导出解析的 sheets 中构建 sheetList（策略E的结果最可靠）
      content.sheetList = content.sheets.map((sh, idx) => ({
        id: `sheet_${idx}`,
        name: sh.name,
        type: 'sheet',
        hidden: false,
      }));
    } else if (content.tabNames && content.tabNames.length > 0) {
      content.sheetList = content.tabNames.map((name, idx) => ({
        id: `tab_${idx}`,
        name,
        type: 'sheet',
        hidden: false,
      }));
    }
  }

  return content;
}

/**
 * 从捕获的 API 数据中提取结构化表格内容
 * 企微表格 API 返回的数据格式可能有多种，这里尝试兼容处理
 */
function extractSheetFromApiData(apiDataList) {
  const sheets = [];

  for (const { data, url } of apiDataList) {
    try {
      // 递归查找包含 celldata/cells/rows 的数据结构
      const cellDataArrays = findDeep(data, ['celldata', 'cellData', 'cells', 'cell_data']);
      const rowDataArrays = findDeep(data, ['rows', 'rowData', 'row_data', 'tableData', 'table_data']);
      const sheetNames = findDeep(data, ['name', 'sheetName', 'sheet_name', 'title']);

      // 处理 celldata 格式 (类似 Luckysheet 格式)
      // celldata: [{r: 0, c: 0, v: {v: "内容"}}, ...]
      for (const cellArr of cellDataArrays) {
        if (Array.isArray(cellArr) && cellArr.length > 0 && cellArr[0] && ('r' in cellArr[0] || 'row' in cellArr[0])) {
          const rowMap = {};
          let maxRow = 0;
          let maxCol = 0;

          cellArr.forEach(cell => {
            const r = cell.r ?? cell.row ?? 0;
            const c = cell.c ?? cell.col ?? cell.column ?? 0;
            let value = '';

            if (cell.v !== undefined && cell.v !== null) {
              if (typeof cell.v === 'object') {
                // Luckysheet 格式: v.v 是实际值, v.m 是显示值
                value = cell.v.m ?? cell.v.v ?? cell.v.value ?? JSON.stringify(cell.v);
              } else {
                value = String(cell.v);
              }
            } else if (cell.value !== undefined) {
              value = String(cell.value);
            }

            if (!rowMap[r]) rowMap[r] = {};
            rowMap[r][c] = value;
            maxRow = Math.max(maxRow, r);
            maxCol = Math.max(maxCol, c);
          });

          // 转换为二维数组
          const rows = [];
          for (let r = 0; r <= maxRow; r++) {
            const row = [];
            for (let c = 0; c <= maxCol; c++) {
              row.push(rowMap[r]?.[c] ?? '');
            }
            // 跳过全空行
            if (row.some(v => v !== '')) {
              rows.push(row);
            }
          }

          if (rows.length > 0) {
            sheets.push({
              name: (typeof sheetNames[0] === 'string' ? sheetNames[0] : '') || `Sheet${sheets.length + 1}`,
              rows,
            });
          }
        }
      }

      // 处理 rows 格式（二维数组）
      for (const rowArr of rowDataArrays) {
        if (Array.isArray(rowArr) && rowArr.length > 0) {
          if (Array.isArray(rowArr[0])) {
            // 已经是二维数组
            const rows = rowArr
              .map(row => row.map(cell => {
                if (cell === null || cell === undefined) return '';
                if (typeof cell === 'object') return cell.v ?? cell.m ?? cell.value ?? JSON.stringify(cell);
                return String(cell);
              }))
              .filter(row => row.some(v => v !== ''));

            if (rows.length > 0) {
              sheets.push({ name: `Sheet${sheets.length + 1}`, rows });
            }
          } else if (typeof rowArr[0] === 'object' && rowArr[0] !== null) {
            // 对象数组格式 [{col1: val1, col2: val2}, ...]
            const keys = Object.keys(rowArr[0]);
            const headerRow = keys;
            const dataRows = rowArr.map(obj => keys.map(k => {
              const v = obj[k];
              if (v === null || v === undefined) return '';
              if (typeof v === 'object') return JSON.stringify(v);
              return String(v);
            }));

            if (dataRows.length > 0) {
              sheets.push({ name: `Sheet${sheets.length + 1}`, rows: [headerRow, ...dataRows] });
            }
          }
        }
      }
    } catch (err) {
      console.error(`[extractSheetFromApiData] 处理数据失败: ${err.message}`);
    }
  }

  return sheets;
}

/**
 * 递归深搜查找指定 key 的值
 */
function findDeep(obj, targetKeys, maxDepth = 8, currentDepth = 0) {
  const results = [];
  if (currentDepth >= maxDepth || !obj || typeof obj !== 'object') return results;

  if (Array.isArray(obj)) {
    for (const item of obj) {
      results.push(...findDeep(item, targetKeys, maxDepth, currentDepth + 1));
    }
  } else {
    for (const [key, value] of Object.entries(obj)) {
      if (targetKeys.includes(key) && value !== undefined && value !== null) {
        results.push(value);
      }
      if (typeof value === 'object' && value !== null) {
        results.push(...findDeep(value, targetKeys, maxDepth, currentDepth + 1));
      }
    }
  }

  return results;
}

/**
 * 提取文档内容
 * 采用多层次策略：
 * 0. 检测 iframe 并进入（企微文档可能将编辑区放在 iframe 中）
 * 1. 等待编辑器区域加载
 * 2. 尝试多种 DOM 选择器提取
 * 3. 全选+复制作为兜底
 * 4. body.innerText 去噪兜底
 */
async function extractDocContent(page) {
  // 策略0：检测 iframe，企微文档可能将编辑区放在 iframe 中
  // 先在主 frame 中探测 DOM 结构
  const domInfo = await page.evaluate(() => {
    const iframes = document.querySelectorAll('iframe');
    const iframeInfo = Array.from(iframes).map((iframe, i) => ({
      index: i,
      src: iframe.src || '',
      id: iframe.id || '',
      className: iframe.className || '',
      width: iframe.offsetWidth,
      height: iframe.offsetHeight,
    }));
    
    // 同时收集所有 contenteditable 元素信息
    const editables = document.querySelectorAll('[contenteditable="true"]');
    const editableInfo = Array.from(editables).map(el => ({
      tag: el.tagName,
      className: el.className || '',
      textLength: el.innerText ? el.innerText.trim().length : 0,
    }));

    return {
      iframeCount: iframes.length,
      iframes: iframeInfo,
      editableCount: editables.length,
      editables: editableInfo,
      bodyTextLength: document.body.innerText ? document.body.innerText.trim().length : 0,
    };
  });

  console.error(`[extractDocContent] DOM探测: ${JSON.stringify(domInfo)}`);

  // 如果存在 iframe，尝试在 iframe 中提取内容
  let targetFrame = page;
  if (domInfo.iframeCount > 0) {
    const frames = page.frames();
    console.error(`[extractDocContent] 页面共有 ${frames.length} 个 frame`);
    
    // 遍历所有 frame，找到包含文档内容的那个（通常是内容最多的非空 frame）
    let bestFrame = null;
    let bestTextLength = 0;
    
    for (const frame of frames) {
      if (frame === page.mainFrame()) continue;
      try {
        const textLen = await frame.evaluate(() => {
          const body = document.body;
          if (!body) return 0;
          return body.innerText ? body.innerText.trim().length : 0;
        });
        console.error(`[extractDocContent] frame ${frame.url().substring(0, 80)}: ${textLen} 字符`);
        if (textLen > bestTextLength) {
          bestTextLength = textLen;
          bestFrame = frame;
        }
      } catch {
        // frame 可能不可访问（跨域等），忽略
      }
    }
    
    if (bestFrame && bestTextLength > 50) {
      targetFrame = bestFrame;
      console.error(`[extractDocContent] 使用 iframe 作为目标 frame (${bestTextLength} 字符)`);
    }
  }

  // 策略1：尝试等待编辑器容器出现（企微文档常见选择器）
  const editorSelectors = [
    '[class*="ql-editor"]',
    '[contenteditable="true"]',
    '[class*="editor-container"]',
    '[class*="doc-content"]',
    '[class*="editor-content"]',
    '[class*="document-content"]',
    '[class*="rich-text"]',
    '[class*="reader-container"]',
    '[class*="article-content"]',
    '.main-content',
  ];

  // 在 targetFrame 中尝试等待任一编辑器选择器出现
  let editorFound = false;
  for (const selector of editorSelectors) {
    try {
      await targetFrame.waitForSelector(selector, { timeout: 1500 });
      editorFound = true;
      console.error(`[extractDocContent] 找到编辑器: ${selector}`);
      break;
    } catch {
      // 继续尝试下一个选择器
    }
  }

  if (!editorFound) {
    console.error('[extractDocContent] 未找到特定编辑器选择器，等待页面渲染...');
    await page.waitForTimeout(5000);
  }

  // 策略2：通过 DOM 选择器提取内容（在 targetFrame 中）
  const content = await targetFrame.evaluate((selectors) => {
    const result = {
      title: '',
      content: '',
    };

    // 获取标题 - 尝试多种标题选择器
    const titleSelectors = [
      '.header-title-text',
      '.doc-header-title',
      '[class*="doc-title"]',
      '[class*="header"] [class*="title"]',
      '[class*="title-input"]',
      'h1',
    ];
    for (const sel of titleSelectors) {
      const el = document.querySelector(sel);
      if (el && el.textContent.trim()) {
        result.title = el.textContent.trim();
        break;
      }
    }
    if (!result.title) {
      result.title = document.title;
    }

    // 获取文档内容 - 按优先级尝试多种选择器
    for (const sel of selectors) {
      const el = document.querySelector(sel);
      if (el && el.innerText && el.innerText.trim().length > 10) {
        result.content = el.innerText.trim();
        break;
      }
    }

    // 如果以上选择器都没拿到内容，尝试遍历所有 contenteditable 元素
    if (!result.content) {
      const editables = document.querySelectorAll('[contenteditable="true"]');
      const texts = [];
      editables.forEach(el => {
        const t = el.innerText ? el.innerText.trim() : '';
        if (t.length > 5) {
          texts.push(t);
        }
      });
      if (texts.length > 0) {
        result.content = texts.join('\n\n');
      }
    }

    // 限制长度
    if (result.content) {
      result.content = result.content.substring(0, 50000);
    }

    return result;
  }, editorSelectors);

  // 如果 targetFrame 是 iframe，标题可能需要从主页面获取
  if (targetFrame !== page && !content.title) {
    content.title = await page.evaluate(() => {
      const titleSelectors = [
        '.header-title-text', '.doc-header-title',
        '[class*="doc-title"]', '[class*="title-input"]', 'h1',
      ];
      for (const sel of titleSelectors) {
        const el = document.querySelector(sel);
        if (el && el.textContent.trim()) return el.textContent.trim();
      }
      return document.title;
    });
  }

  // 策略3：企微文档使用 canvas 渲染，DOM 中无文本内容
  // 必须通过模拟键盘全选+复制，然后从剪贴板读取
  if (!content.content) {
    console.error('[extractDocContent] DOM提取失败（canvas渲染），尝试全选+复制到剪贴板...');
    try {
      // 点击文档编辑区域（避开工具栏，点击页面中间偏下的位置）
      await page.click('body', { position: { x: 500, y: 500 } });
      await page.waitForTimeout(800);

      const isMac = process.platform === 'darwin';
      const modifier = isMac ? 'Meta' : 'Control';

      // 全选: Ctrl/Cmd + A
      await page.keyboard.press(`${modifier}+A`);
      await page.waitForTimeout(800);

      // 复制: Ctrl/Cmd + C
      await page.keyboard.press(`${modifier}+C`);
      await page.waitForTimeout(800);

      // 方法1: 通过 clipboard API 读取（需要 context 权限）
      let clipText = '';
      try {
        clipText = await page.evaluate(async () => {
          try {
            return await navigator.clipboard.readText();
          } catch {
            return '';
          }
        });
      } catch {
        // clipboard API 可能不可用
      }

      // 方法2: 如果 clipboard API 不可用，创建一个隐藏的 textarea 来粘贴
      if (!clipText) {
        clipText = await page.evaluate(async (mod) => {
          return new Promise((resolve) => {
            const textarea = document.createElement('textarea');
            textarea.style.position = 'fixed';
            textarea.style.left = '-9999px';
            textarea.style.top = '-9999px';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.focus();

            // 监听 paste 事件
            textarea.addEventListener('paste', (e) => {
              const text = e.clipboardData ? e.clipboardData.getData('text/plain') : '';
              document.body.removeChild(textarea);
              resolve(text);
            });

            // 5 秒超时
            setTimeout(() => {
              try { document.body.removeChild(textarea); } catch {}
              resolve('');
            }, 5000);
          });
        }, modifier);

        // 在 textarea 中触发粘贴
        await page.keyboard.press(`${modifier}+V`);
        await page.waitForTimeout(1000);

        // 再次尝试直接读取 textarea 的值
        if (!clipText) {
          clipText = await page.evaluate(() => {
            const ta = document.querySelector('textarea[style*="-9999px"]');
            return ta ? ta.value : '';
          });
        }
      }

      // 方法3: 使用 window.getSelection 作为最后手段
      if (!clipText) {
        clipText = await page.evaluate(() => {
          const selection = window.getSelection();
          return selection ? selection.toString() : '';
        });
      }

      if (clipText && clipText.trim().length > 10) {
        content.content = clipText.trim().substring(0, 50000);
        console.error(`[extractDocContent] 剪贴板提取成功，获取 ${content.content.length} 字符`);
      }
    } catch (err) {
      console.error(`[extractDocContent] 全选+复制失败: ${err.message}`);
    }
  }

  // 策略4：最终兜底，取 targetFrame 的 body.innerText 去除工具栏噪音
  if (!content.content) {
    console.error('[extractDocContent] 尝试 body.innerText 兜底...');
    const bodyText = await targetFrame.evaluate(() => {
      const bodyClone = document.body.cloneNode(true);
      const noiseSelectors = [
        'header', 'nav', '[class*="toolbar"]', '[class*="header"]',
        '[class*="sidebar"]', '[class*="menu"]', '[class*="footer"]',
        '[class*="nav"]', '[class*="toast"]', '[class*="modal"]',
      ];
      noiseSelectors.forEach(sel => {
        bodyClone.querySelectorAll(sel).forEach(el => el.remove());
      });
      return bodyClone.innerText ? bodyClone.innerText.trim().substring(0, 50000) : '';
    });
    if (bodyText && bodyText.length > 10) {
      content.content = bodyText;
    }
  }

  return content;
}

/**
 * 从嵌套 JSON 对象中递归提取所有文本字符串
 * 用于解析企微 slide API 返回的数据结构
 */
function extractTextsFromObject(obj, maxDepth = 10, depth = 0) {
  const texts = [];
  if (depth > maxDepth || !obj) return texts;

  if (typeof obj === 'string') {
    const trimmed = obj.trim();
    // 过滤掉太短的、纯数字的、看起来像 key/id 的字符串
    if (trimmed.length > 2 && !/^\d+$/.test(trimmed) && !/^[a-f0-9-]{20,}$/i.test(trimmed)) {
      texts.push(trimmed);
    }
    return texts;
  }

  if (Array.isArray(obj)) {
    for (const item of obj) {
      texts.push(...extractTextsFromObject(item, maxDepth, depth + 1));
    }
    return texts;
  }

  if (typeof obj === 'object') {
    for (const [key, val] of Object.entries(obj)) {
      // 优先提取已知的文本字段
      if ((key === 'content' || key === 'text' || key === 'value' || key === 'label' || key === 'title' || key === 'name')
          && typeof val === 'string' && val.trim().length > 2) {
        texts.push(val.trim());
      } else if (val && typeof val === 'object') {
        texts.push(...extractTextsFromObject(val, maxDepth, depth + 1));
      }
    }
  }

  return texts;
}

/**
 * 在嵌套对象中查找第一个匹配 key 的值
 */
function findDeepValue(obj, targetKey, maxDepth = 8, depth = 0) {
  if (depth > maxDepth || !obj || typeof obj !== 'object') return null;

  if (Array.isArray(obj)) {
    for (const item of obj) {
      const found = findDeepValue(item, targetKey, maxDepth, depth + 1);
      if (found) return found;
    }
    return null;
  }

  for (const [key, val] of Object.entries(obj)) {
    if (key === targetKey && val !== undefined && val !== null) {
      return val;
    }
    if (val && typeof val === 'object') {
      const found = findDeepValue(val, targetKey, maxDepth, depth + 1);
      if (found) return found;
    }
  }
  return null;
}

/**
 * 从 PPTX XML 中提取文本的辅助函数
 * 提取所有 <a:t> 文本节点，按段落 <a:p> 分组
 */
function extractTextFromSlideXml(xml) {
  const paragraphs = xml.match(/<a:p\b[\s\S]*?<\/a:p>/g) || [];
  if (paragraphs.length > 0) {
    const paraTexts = paragraphs.map(p => {
      const tMatches = p.match(/<a:t[^>]*>([\s\S]*?)<\/a:t>/g) || [];
      return tMatches.map(m => m.replace(/<a:t[^>]*>/, '').replace(/<\/a:t>/, '')
        .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
        .replace(/&quot;/g, '"').replace(/&apos;/g, "'")
        .replace(/&#x([0-9a-fA-F]+);/g, (_, h) => String.fromCharCode(parseInt(h, 16)))
        .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(parseInt(n)))
      ).join('');
    });
    return paraTexts.filter(t => t.trim()).join('\n');
  }
  // fallback: 直接提取所有 <a:t>
  const matches = xml.match(/<a:t[^>]*>([\s\S]*?)<\/a:t>/g);
  if (matches) {
    return matches.map(m => m.replace(/<a:t[^>]*>/, '').replace(/<\/a:t>/, '')
      .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
      .replace(/&quot;/g, '"').replace(/&apos;/g, "'")
      .replace(/&#x([0-9a-fA-F]+);/g, (_, h) => String.fromCharCode(parseInt(h, 16)))
      .replace(/&#(\d+);/g, (_, n) => String.fromCharCode(parseInt(n)))
    ).join('');
  }
  return '';
}

/**
 * 在页面中查找主编辑/展示区域的边界框
 * 用于截取 slide 主内容区域而非整个页面
 */
async function findSlideMainArea(page) {
  return page.evaluate(() => {
    // 按优先级查找主内容区域选择器
    const selectors = [
      '.pre-slide-canvas-wrap',
      '.pre-canvas-wrap',
      '[class*="slide-canvas"]',
      '[class*="slide-main"]',
      '[class*="canvas-container"]',
      '[class*="editor-canvas"]',
      '.pre-main',
      'canvas',
    ];
    for (const sel of selectors) {
      const el = document.querySelector(sel);
      if (el) {
        const r = el.getBoundingClientRect();
        // 主内容区域应该足够大
        if (r.width > 200 && r.height > 150) {
          return {
            x: Math.max(0, Math.round(r.x)),
            y: Math.max(0, Math.round(r.y)),
            width: Math.round(r.width),
            height: Math.round(r.height),
          };
        }
      }
    }
    return null;
  }).catch(() => null);
}

/**
 * 提取幻灯片内容
 * 
 * 企微 PPT 使用 canvas 渲染，无法直接从 DOM 提取文本。
 * 重构后的策略：
 *   A. 拦截 API 网络请求 + JS 内存扫描（合并为一步，刷新页面时同时执行）
 *   B. 导出 PPTX → jszip 解析（有编辑权限时尝试）
 *   C. 逐页截图（最稳定的保底方案，截取主编辑区域）
 */
async function extractSlideContent(page) {
  const content = {
    title: '',
    slides: [],
    rawText: '',
    slideScreenshots: [],
    slideCount: 0,
    isReadOnly: false,
  };

  // ========================================================================
  // 策略 A：拦截 API 网络请求 + 刷新后从 JS 内存提取
  // ========================================================================
  // 企微 PPT 加载时会通过 opendoc/slide API 拉取所有 slide 数据
  // 即使只读模式也会请求这些 API，因此可以在任何权限下尝试
  let apiTextsPerSlide = new Map(); // slideIndex → texts[]
  let apiRawTexts = [];
  let apiTitle = '';

  try {
    const capturedResponses = [];

    const responseHandler = async (response) => {
      try {
        const url = response.url();
        const status = response.status();
        if (status === 200 && (
          url.includes('opendoc') ||
          url.includes('slide') ||
          url.includes('presentation') ||
          url.includes('GetSlide') ||
          url.includes('getSlideData') ||
          url.includes('pad/open') ||
          url.includes('collab') ||
          url.includes('pre/open') ||
          url.includes('docdata')
        )) {
          const body = await response.body().catch(() => null);
          if (body && body.length > 100) {
            capturedResponses.push({
              url: url.substring(0, 300),
              size: body.length,
              body,
            });
            console.error(`[slide-A] 捕获API: ${url.substring(0, 100)} (${body.length}B)`);
          }
        }
      } catch { /* 某些响应可能无法读取 body */ }
    };

    page.on('response', responseHandler);

    // 刷新页面以重新触发数据加载
    console.error('[slide-A] 刷新页面以捕获API数据...');
    await page.reload({ waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(8000); // 企微 PPT 加载较慢，多等一会

    // 移除监听器，避免后续操作被干扰
    page.off('response', responseHandler);

    console.error(`[slide-A] 共捕获 ${capturedResponses.length} 个API响应`);

    // 解析捕获的响应数据
    for (const resp of capturedResponses) {
      try {
        const bodyStr = resp.body.toString('utf-8');
        let parsed = null;
        try { parsed = JSON.parse(bodyStr); } catch { continue; }
        if (!parsed || typeof parsed !== 'object') continue;

        const topKeys = Object.keys(parsed).slice(0, 15).join(',');
        console.error(`[slide-A] 解析: ${resp.url.substring(0, 80)}, keys=[${topKeys}]`);

        // --- 提取标题 ---
        const cv = parsed.clientVars || parsed;
        if (!apiTitle) {
          apiTitle = cv.padTitle || cv.title || '';
          if (!apiTitle && cv.docInfo?.padInfo?.padTitle) {
            apiTitle = cv.docInfo.padInfo.padTitle;
          }
        }

        // --- 方法1：从 initialAttributedText 提取 slide JSON ---
        const collabVars = cv.collab_client_vars || cv.collabVars || cv;
        const attrText = collabVars?.initialAttributedText;
        if (attrText) {
          // attrText.text 可能是包含 slide 内容的 JSON 字符串数组
          const textItems = attrText.text 
            ? (Array.isArray(attrText.text) ? attrText.text : [attrText.text])
            : [];
          for (const textItem of textItems) {
            if (typeof textItem === 'string') {
              // 尝试解析为 JSON（企微常用的 slide 数据格式）
              try {
                const inner = JSON.parse(textItem);
                if (inner && typeof inner === 'object') {
                  const extracted = extractTextsFromObject(inner);
                  if (extracted.length > 0) apiRawTexts.push(...extracted);
                }
              } catch {
                // 不是 JSON，作为纯文本处理
                if (textItem.trim().length > 2) apiRawTexts.push(textItem.trim());
              }
            } else if (textItem && typeof textItem === 'object') {
              const extracted = extractTextsFromObject(textItem);
              if (extracted.length > 0) apiRawTexts.push(...extracted);
            }
          }
        }

        // --- 方法2：从已知 key 提取 slide 数据 ---
        const slideKeys = [
          'slides', 'pages', 'slideData', 'slide_data', 'presentation',
          'slideRenderData', 'slidesData', 'pageData', 'bodyContents',
        ];
        for (const key of slideKeys) {
          const val = findDeepValue(parsed, key);
          if (val) {
            if (Array.isArray(val)) {
              // 如果是数组，按索引分组
              val.forEach((item, idx) => {
                const extracted = extractTextsFromObject(item);
                if (extracted.length > 0) {
                  const existing = apiTextsPerSlide.get(idx) || [];
                  existing.push(...extracted);
                  apiTextsPerSlide.set(idx, existing);
                }
              });
            } else {
              const extracted = extractTextsFromObject(val);
              if (extracted.length > 0) {
                console.error(`[slide-A] 从 "${key}" 提取到 ${extracted.length} 段文本`);
                apiRawTexts.push(...extracted);
              }
            }
          }
        }
      } catch (e) {
        console.error(`[slide-A] 解析响应失败: ${e.message}`);
      }
    }

    if (apiTextsPerSlide.size > 0 || apiRawTexts.length > 0) {
      console.error(`[slide-A] API结果: ${apiTextsPerSlide.size} 页分组文本, ${apiRawTexts.length} 段原始文本`);
    }
  } catch (err) {
    console.error(`[slide-A] 失败: ${err.message}`);
  }

  // --- 从 JS 内存提取元信息 + 文本 ---
  const pageData = await page.evaluate(() => {
    const result = {
      title: '',
      slideCount: 0,
      isReadOnly: false,
      memoryTexts: [],     // 从 JS 内存提取的逐页文本
      rawMemoryTexts: [],  // 深度扫描的原始文本
    };

    // 标题
    const titleEl =
      document.querySelector('.pre-title-name') ||
      document.querySelector('.header-title-text') ||
      document.querySelector('.doc-header-title') ||
      document.querySelector('[class*="doc-title"]') ||
      document.querySelector('h1');
    result.title = titleEl?.textContent?.trim() || document.title || '';

    // 只读检测
    const bodyText = document.body.innerText || '';
    result.isReadOnly = bodyText.includes('只能查看') || bodyText.includes('申请编辑权限');
    if (document.querySelector('[contenteditable="true"]') ||
        document.querySelector('.editing-mode, .edit-mode, [class*="editing-mode"]')) {
      result.isReadOnly = false;
    }

    // 页数
    try {
      if (window.preloadData?.slideCount) result.slideCount = window.preloadData.slideCount;
    } catch {}
    if (!result.slideCount) {
      const m = bodyText.match(/共\s*(\d+)\s*页/);
      if (m) result.slideCount = parseInt(m[1]);
    }
    if (!result.slideCount) {
      // 多种缩略图选择器
      const thumbs = document.querySelectorAll(
        '.pre-slide-list-item, [class*="slide-thumb"], [class*="slide-list-item"], [class*="thumbnail-item"], [class*="slide-item"]'
      );
      if (thumbs.length > 0) result.slideCount = thumbs.length;
    }

    // --- 从 renderData.slideRenderData 提取 ---
    try {
      if (window.renderData?.slideRenderData) {
        const srd = window.renderData.slideRenderData;
        const slides = Array.isArray(srd) ? srd : [srd];
        const extractTexts = (node, texts) => {
          if (!node) return;
          if (node.type === 'Text' && node.content) texts.push(node.content);
          if (typeof node.text === 'string' && node.text.trim()) texts.push(node.text.trim());
          if (node.children && Array.isArray(node.children)) {
            node.children.forEach(child => extractTexts(child, texts));
          }
          if (node.items) {
            (Array.isArray(node.items) ? node.items : [node.items]).forEach(item => extractTexts(item, texts));
          }
          if (node.elements && Array.isArray(node.elements)) {
            node.elements.forEach(el => extractTexts(el, texts));
          }
        };
        for (const slide of slides) {
          const texts = [];
          extractTexts(slide, texts);
          result.memoryTexts.push(texts.join(' '));
        }
      }
    } catch {}

    // --- 深度扫描 window 全局变量 ---
    try {
      const scanForTexts = (obj, depth, visited) => {
        if (depth > 6 || !obj || typeof obj !== 'object') return [];
        if (visited.has(obj)) return [];
        visited.add(obj);
        const texts = [];
        try {
          if (Array.isArray(obj)) {
            for (let i = 0; i < Math.min(obj.length, 50); i++) {
              texts.push(...scanForTexts(obj[i], depth + 1, visited));
            }
          } else {
            for (const k of Object.keys(obj).slice(0, 100)) {
              try {
                const val = obj[k];
                if ((k === 'content' || k === 'text' || k === 'value' || k === 'label' || k === 'title' || k === 'plainText')
                    && typeof val === 'string' && val.trim().length > 2) {
                  texts.push(val.trim());
                } else if (val && typeof val === 'object') {
                  texts.push(...scanForTexts(val, depth + 1, visited));
                }
              } catch { /* skip */ }
            }
          }
        } catch {}
        return texts;
      };

      const visited = new WeakSet();
      for (const key of ['preloadData', 'clientVars', 'basicClientVars', 'slideModel', 'pad', 'padModel', 'app', 'store']) {
        try {
          if (window[key] && typeof window[key] === 'object') {
            const found = scanForTexts(window[key], 0, visited);
            if (found.length > 0) {
              result.rawMemoryTexts.push(...found.slice(0, 500));
              break;
            }
          }
        } catch {}
      }
    } catch {}

    // 补充标题
    try {
      const cv = window.clientVars || window.basicClientVars;
      if (cv?.docInfo?.padInfo?.padTitle) {
        result.title = result.title || cv.docInfo.padInfo.padTitle;
      }
    } catch {}

    return result;
  }).catch(() => ({
    title: '', slideCount: 0, isReadOnly: false, memoryTexts: [], rawMemoryTexts: [],
  }));

  console.error(`[slide] 元信息: title="${pageData.title}", pages=${pageData.slideCount}, ` +
    `readOnly=${pageData.isReadOnly}, memoryTexts=${pageData.memoryTexts.length}, rawScan=${pageData.rawMemoryTexts.length}`);

  content.title = pageData.title || apiTitle;
  content.slideCount = pageData.slideCount;
  content.isReadOnly = pageData.isReadOnly;

  // --- 合并策略 A 结果 ---
  // 优先使用按页分组的数据
  if (apiTextsPerSlide.size > 0) {
    const maxIdx = Math.max(...apiTextsPerSlide.keys());
    for (let i = 0; i <= maxIdx; i++) {
      const texts = apiTextsPerSlide.get(i) || [];
      content.slides.push({ index: i + 1, text: texts.join('\n').trim() });
    }
    console.error(`[slide-A] 按页分组: ${content.slides.filter(s => s.text).length}/${content.slides.length} 页有文本`);
  }

  // 如果分组数据不足，尝试使用 JS 内存提取的逐页文本
  const hasMemoryTexts = pageData.memoryTexts.some(t => t.trim().length > 0);
  if (content.slides.filter(s => s.text).length === 0 && hasMemoryTexts) {
    content.slides = [];
    pageData.memoryTexts.forEach((text, i) => {
      content.slides.push({ index: i + 1, text: text.trim() });
    });
    console.error(`[slide-A] 内存提取: ${content.slides.filter(s => s.text).length} 页有文本`);
  }

  // 如果还是没有分页数据，合并所有原始文本
  if (content.slides.filter(s => s.text).length === 0) {
    const allRaw = [...apiRawTexts, ...pageData.rawMemoryTexts];
    // 去重
    const unique = [...new Set(allRaw.filter(t => t.length > 3))];
    if (unique.length > 0) {
      content.rawText = unique.join('\n\n').trim();
      console.error(`[slide-A] 原始文本合并: ${unique.length} 段, ${content.rawText.length} 字符`);
    }
  }

  // ========================================================================
  // 策略 B：导出 PPTX → jszip 解析（仅非只读 + 策略 A 没有有效文本时）
  // ========================================================================
  const hasValidText = content.slides.filter(s => s.text && s.text.length > 5).length > 0;
  if (!hasValidText && !content.isReadOnly) {
    console.error('[slide-B] 尝试导出 PPTX...');
    const downloadDir = path.join(os.tmpdir(), 'wecom-slide-export-' + Date.now());
    fs.mkdirSync(downloadDir, { recursive: true });
    try {
      await exportAndParsePptx(page, content, downloadDir);
    } catch (err) {
      console.error(`[slide-B] 失败: ${err.message}`);
      await page.keyboard.press('Escape').catch(() => {});
    } finally {
      try { fs.rmSync(downloadDir, { recursive: true, force: true }); } catch {}
    }
  }

  // ========================================================================
  // 策略 C：逐页截图（始终执行，作为保底视觉内容）
  // ========================================================================
  try {
    await screenshotSlides(page, content);
  } catch (err) {
    console.error(`[slide-C] 截图失败: ${err.message}`);
    // 兜底：至少截一张
    try {
      const fallbackDir = path.join(os.tmpdir(), `wecom-slide-fallback-${Date.now()}`);
      fs.mkdirSync(fallbackDir, { recursive: true });
      const p = path.join(fallbackDir, 'slide-current.jpg');
      const clip = await findSlideMainArea(page);
      await page.screenshot({ path: p, ...(clip ? { clip } : {}), type: 'jpeg', quality: 60 });
      content.slideScreenshots.push(p);
    } catch { /* ignore */ }
  }

  // 如果所有策略都没拿到文本，填充空 slide 占位
  if (content.slides.length === 0 && content.slideCount > 0) {
    for (let i = 0; i < content.slideCount; i++) {
      content.slides.push({ index: i + 1, text: '' });
    }
  }

  const textPages = content.slides.filter(s => s.text).length;
  console.error(`[slide] 完成: ${content.slides.length} 页, ${textPages} 页有文本, ` +
    `${content.slideScreenshots.length} 张截图, rawText=${content.rawText?.length || 0} 字符`);

  return content;
}

/**
 * 策略 B 子流程：通过 UI 导出 PPTX 并用 jszip 解析
 */
async function exportAndParsePptx(page, content, downloadDir) {
  // Step 1: 点击「文件」菜单按钮
  let fileBtnClicked = false;
  const fileBtnSelectors = [
    '[aria-label="file"]', '[aria-label="按钮:文件操作"]',
    '[aria-label="文件操作"]', '[aria-label="文件"]',
    '.menu-button-file', '.pre-file-menu', '.pre-menu-file',
    '[class*="file-menu"]', '[class*="menu-file"]',
  ];
  for (const sel of fileBtnSelectors) {
    try {
      const btn = await page.$(sel);
      if (btn) {
        const box = await btn.boundingBox();
        if (box && box.width > 5 && box.height > 5) {
          await btn.click();
          fileBtnClicked = true;
          console.error(`[slide-B] Step1: 选择器 "${sel}" 点击文件按钮`);
          break;
        }
      }
    } catch {}
  }

  // 启发式查找
  if (!fileBtnClicked) {
    const pos = await page.evaluate(() => {
      const candidates = [];
      for (const el of document.querySelectorAll('*')) {
        const text = el.textContent?.trim();
        const ariaLabel = el.getAttribute('aria-label') || '';
        const cls = (el.className || '').toString().toLowerCase();
        const r = el.getBoundingClientRect();
        if (r.y < 150 && r.width > 10 && r.height > 10 && r.width < 200 && r.height < 60) {
          let score = 0;
          if (text === '文件' || text === '文件操作') score += 10;
          if (ariaLabel.includes('文件') || ariaLabel === 'file') score += 8;
          if (cls.includes('file') || cls.includes('menu')) score += 3;
          if (r.x < 200) score += 2;
          if (score > 0) candidates.push({ x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2), score });
        }
      }
      candidates.sort((a, b) => b.score - a.score);
      return candidates[0] || null;
    });
    if (pos) {
      await page.mouse.click(pos.x, pos.y);
      fileBtnClicked = true;
      console.error(`[slide-B] Step1: 启发式点击 (${pos.x}, ${pos.y})`);
    }
  }

  if (!fileBtnClicked) {
    console.error('[slide-B] Step1: 未找到文件按钮，跳过');
    return;
  }

  await page.waitForTimeout(1500);

  // Step 2: hover「导出」菜单项
  const exportPos = await page.evaluate(() => {
    for (const el of document.querySelectorAll('*')) {
      const text = el.textContent?.trim();
      const directText = el.childNodes.length <= 3 ? Array.from(el.childNodes).map(n => n.textContent?.trim()).join('') : '';
      const r = el.getBoundingClientRect();
      if (r.width < 20 || r.height < 10 || r.height > 80 || r.y < 30) continue;
      if (directText === '导出' || text === '导出') {
        return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) };
      }
    }
    // 宽松匹配
    for (const el of document.querySelectorAll('*')) {
      const text = el.textContent?.trim();
      const r = el.getBoundingClientRect();
      if (r.width < 20 || r.height < 10 || r.y < 30) continue;
      if (text?.startsWith('导出') && text.length < 20) {
        return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) };
      }
    }
    return null;
  });

  if (!exportPos) {
    console.error('[slide-B] Step2: 未找到导出菜单项，关闭菜单');
    await page.keyboard.press('Escape').catch(() => {});
    return;
  }

  console.error(`[slide-B] Step2: hover 导出菜单 (${exportPos.x}, ${exportPos.y})`);
  await page.mouse.move(exportPos.x, exportPos.y);
  await page.waitForTimeout(2000);

  // Step 3: 查找并点击 PPTX 导出选项
  const pptxTarget = await page.evaluate(() => {
    const candidates = [];
    for (const el of document.querySelectorAll('*')) {
      const text = el.textContent?.trim();
      const r = el.getBoundingClientRect();
      if (!text || r.width < 20 || r.height < 8 || r.y < 30) continue;
      let score = 0;
      if (/\.pptx/i.test(text)) score += 10;
      if (/pptx/i.test(text)) score += 8;
      if (/powerpoint/i.test(text)) score += 8;
      if (/演示文稿/.test(text)) score += 7;
      if (/\.zip/i.test(text)) score -= 20;
      if (/打包/.test(text)) score -= 10;
      if (text.length > 60) score -= 5;
      if (el.children.length <= 3) score += 2;
      if (score >= 7) candidates.push({ x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2), score, text: text.substring(0, 50) });
    }
    candidates.sort((a, b) => b.score - a.score);
    return candidates[0] || null;
  });

  if (!pptxTarget) {
    console.error('[slide-B] Step3: 未找到 PPTX 导出选项');
    await page.keyboard.press('Escape').catch(() => {});
    return;
  }

  console.error(`[slide-B] Step3: 点击 "${pptxTarget.text}" (${pptxTarget.x}, ${pptxTarget.y})`);
  const dlPromise = page.waitForEvent('download', { timeout: 45000 }).catch(() => null);
  await page.mouse.click(pptxTarget.x, pptxTarget.y);
  await page.waitForTimeout(3000);

  let download = await dlPromise;

  // 可能需要处理确认对话框
  if (!download) {
    const confirmBtn = await page.evaluate(() => {
      for (const el of document.querySelectorAll('button, [role="button"], [class*="btn"]')) {
        const text = el.textContent?.trim();
        if (text && /^(确[认定]|下载|导出|OK|确认导出)$/i.test(text)) {
          const r = el.getBoundingClientRect();
          if (r.width > 20 && r.height > 15) {
            return { x: Math.round(r.x + r.width / 2), y: Math.round(r.y + r.height / 2) };
          }
        }
      }
      return null;
    }).catch(() => null);

    if (confirmBtn) {
      console.error(`[slide-B] 点击确认按钮 (${confirmBtn.x}, ${confirmBtn.y})`);
      const dlPromise2 = page.waitForEvent('download', { timeout: 30000 }).catch(() => null);
      await page.mouse.click(confirmBtn.x, confirmBtn.y);
      download = await dlPromise2;
    }
  }

  if (!download) {
    console.error('[slide-B] 未检测到下载事件');
    await page.keyboard.press('Escape').catch(() => {});
    return;
  }

  const filename = download.suggestedFilename() || 'export.pptx';
  const dlPath = path.join(downloadDir, filename);
  await download.saveAs(dlPath);
  const size = fs.statSync(dlPath).size;
  console.error(`[slide-B] 下载完成: ${dlPath} (${size}B)`);

  if (size <= 100) return;

  try {
    const JSZip = (await import('jszip')).default;
    const zip = await JSZip.loadAsync(fs.readFileSync(dlPath));
    const slideFiles = Object.keys(zip.files)
      .filter(n => /^ppt\/slides\/slide\d+\.xml$/.test(n))
      .sort((a, b) => parseInt(a.match(/slide(\d+)/)[1]) - parseInt(b.match(/slide(\d+)/)[1]));

    console.error(`[slide-B] PPTX 包含 ${slideFiles.length} 个 slide`);
    content.slides = [];
    for (let i = 0; i < slideFiles.length; i++) {
      const xml = await zip.files[slideFiles[i]].async('string');
      const text = extractTextFromSlideXml(xml);
      content.slides.push({ index: i + 1, text: text.trim() });
    }
    if (!content.slideCount) content.slideCount = slideFiles.length;
    console.error(`[slide-B] 成功: ${content.slides.length} 页, ${content.slides.filter(s => s.text).length} 页有文本`);
  } catch (e) {
    console.error(`[slide-B] PPTX 解析失败: ${e.message}`);
  }
}

/**
 * 策略 C 子流程：逐页截图
 * 尝试截取主编辑区域（排除工具栏/侧边栏），失败时回退全页截图
 */
async function screenshotSlides(page, content) {
  const screenshotDir = path.join(os.tmpdir(), `wecom-slide-${Date.now()}`);
  fs.mkdirSync(screenshotDir, { recursive: true });

  // 截图配置：使用 JPEG 格式 + 降低质量以减小体积，避免 AI 模型 input length 超限
  const SCREENSHOT_MAX_PAGES = 10; // 单次最多截 10 页，避免输出过大
  const SCREENSHOT_QUALITY = 40;   // JPEG 质量 0-100，40 在文字可辨认的前提下大幅压缩体积
  const SCREENSHOT_MAX_WIDTH = 1280; // 截图最大宽度（px），超过时会等比缩小

  // 查找侧边栏缩略图
  const thumbSelector = '.pre-slide-list-item, [class*="slide-thumb"], [class*="slide-list-item"], [class*="thumbnail-item"], [class*="slide-item"]';
  const thumbCount = await page.evaluate((sel) => {
    return document.querySelectorAll(sel).length;
  }, thumbSelector).catch(() => 0);

  console.error(`[slide-C] thumbCount=${thumbCount}, slideCount=${content.slideCount}`);

  // 尝试获取主内容区域的裁剪框
  const mainClip = await findSlideMainArea(page);
  const screenshotOpts = mainClip ? { clip: mainClip } : {};
  if (mainClip) {
    console.error(`[slide-C] 主内容区域: ${mainClip.x},${mainClip.y} ${mainClip.width}x${mainClip.height}`);
  }

  // 通用截图参数：JPEG 格式 + 降低质量
  const jpegOpts = { type: 'jpeg', quality: SCREENSHOT_QUALITY };

  // 如果主内容区域过宽，调整裁剪框到合理尺寸以控制截图体积
  if (mainClip && mainClip.width > SCREENSHOT_MAX_WIDTH) {
    const scale = SCREENSHOT_MAX_WIDTH / mainClip.width;
    mainClip.width = SCREENSHOT_MAX_WIDTH;
    mainClip.height = Math.round(mainClip.height * scale);
  }

  if (thumbCount > 1) {
    const maxPages = Math.min(thumbCount, SCREENSHOT_MAX_PAGES);
    if (thumbCount > SCREENSHOT_MAX_PAGES) {
      console.error(`[slide-C] 缩略图 ${thumbCount} 页，截图上限 ${SCREENSHOT_MAX_PAGES} 页`);
    }
    // 记录总页数供输出提示
    content.totalSlidePages = thumbCount;

    for (let i = 0; i < maxPages; i++) {
      try {
        const clicked = await page.evaluate((index, sel) => {
          const thumbs = document.querySelectorAll(sel);
          if (index < thumbs.length) { thumbs[index].click(); return true; }
          return false;
        }, i, thumbSelector);
        if (!clicked) break;
        await page.waitForTimeout(600);

        // 重新获取裁剪区域（翻页后可能有变化）
        const clip = (i === 0) ? mainClip : (await findSlideMainArea(page) || mainClip);
        const opts = clip ? { clip } : {};

        const p = path.join(screenshotDir, `slide-${i + 1}.jpg`);
        await page.screenshot({ path: p, ...opts, ...jpegOpts });
        content.slideScreenshots.push(p);
      } catch (e) {
        console.error(`[slide-C] 第 ${i + 1} 页截图失败: ${e.message}`);
      }
    }
    console.error(`[slide-C] 缩略图逐页截图完成: ${content.slideScreenshots.length} 张`);
  } else if (content.slideCount > 1) {
    // 没有缩略图，用键盘翻页
    // 先点击主区域确保焦点在幻灯片上
    if (mainClip) {
      await page.mouse.click(mainClip.x + mainClip.width / 2, mainClip.y + mainClip.height / 2).catch(() => {});
      await page.waitForTimeout(300);
    }

    const maxPages = Math.min(content.slideCount, SCREENSHOT_MAX_PAGES);
    content.totalSlidePages = content.slideCount;

    const firstPath = path.join(screenshotDir, 'slide-1.jpg');
    await page.screenshot({ path: firstPath, ...screenshotOpts, ...jpegOpts });
    content.slideScreenshots.push(firstPath);

    for (let i = 1; i < maxPages; i++) {
      try {
        await page.keyboard.press('ArrowRight');
        await page.waitForTimeout(600);
        const clip = await findSlideMainArea(page) || mainClip;
        const opts = clip ? { clip } : {};
        const p = path.join(screenshotDir, `slide-${i + 1}.jpg`);
        await page.screenshot({ path: p, ...opts, ...jpegOpts });
        content.slideScreenshots.push(p);
      } catch (e) {
        console.error(`[slide-C] 第 ${i + 1} 页翻页截图失败: ${e.message}`);
        break;
      }
    }
    console.error(`[slide-C] 键盘翻页截图完成: ${content.slideScreenshots.length} 张`);
  } else {
    // 单页截图
    const p = path.join(screenshotDir, 'slide-current.jpg');
    await page.screenshot({ path: p, ...screenshotOpts, ...jpegOpts });
    content.slideScreenshots.push(p);
    console.error('[slide-C] 单页截图');
  }
}

/**
 * 提取思维导图内容（mindmap 类型）
 *
 * 企微思维导图使用 Canvas 渲染，DOM 中无文本。
 * 策略：
 *   A. 拦截 opendoc API 网络请求 + JS 内存扫描（获取结构化节点数据）
 *   B. 全页截图（保底视觉内容）
 */
async function extractMindmapContent(page) {
  const content = {
    title: '',
    nodes: [],       // 结构化节点列表 { id, text, parentId, children? }
    rawText: '',      // 合并后的原始文本
    mindmapScreenshot: null, // 截图路径
  };

  // ========================================================================
  // 策略 A：拦截 opendoc API 网络请求 + 刷新后从 JS 内存提取
  // ========================================================================
  let apiRawTexts = [];
  let apiTitle = '';
  let apiNodes = []; // 结构化节点

  try {
    const capturedResponses = [];

    const responseHandler = async (response) => {
      try {
        const url = response.url();
        const status = response.status();
        if (status === 200 && (
          url.includes('opendoc') ||
          url.includes('mindmap') ||
          url.includes('mind') ||
          url.includes('pad/open') ||
          url.includes('collab') ||
          url.includes('docdata')
        )) {
          const body = await response.body().catch(() => null);
          if (body && body.length > 100) {
            capturedResponses.push({
              url: url.substring(0, 300),
              size: body.length,
              body,
            });
            console.error(`[mindmap-A] 捕获API: ${url.substring(0, 100)} (${body.length}B)`);
          }
        }
      } catch { /* 某些响应可能无法读取 body */ }
    };

    page.on('response', responseHandler);

    // 刷新页面以重新触发数据加载
    console.error('[mindmap-A] 刷新页面以捕获API数据...');
    await page.reload({ waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(8000);

    // 移除监听器
    page.off('response', responseHandler);

    console.error(`[mindmap-A] 共捕获 ${capturedResponses.length} 个API响应`);

    // 解析捕获的响应数据
    for (const resp of capturedResponses) {
      try {
        const bodyStr = resp.body.toString('utf-8');
        let parsed = null;
        try { parsed = JSON.parse(bodyStr); } catch { continue; }
        if (!parsed || typeof parsed !== 'object') continue;

        const topKeys = Object.keys(parsed).slice(0, 15).join(',');
        console.error(`[mindmap-A] 解析: ${resp.url.substring(0, 80)}, keys=[${topKeys}]`);

        // --- 提取标题 ---
        const cv = parsed.clientVars || parsed;
        if (!apiTitle) {
          apiTitle = cv.padTitle || cv.title || '';
          if (!apiTitle && cv.docInfo?.padInfo?.padTitle) {
            apiTitle = cv.docInfo.padInfo.padTitle;
          }
        }

        // --- 从 initialAttributedText 提取思维导图数据 ---
        const collabVars = cv.collab_client_vars || cv.collabVars || cv;
        const attrText = collabVars?.initialAttributedText;
        if (attrText) {
          const textItems = attrText.text
            ? (Array.isArray(attrText.text) ? attrText.text : [attrText.text])
            : [];
          for (const textItem of textItems) {
            if (typeof textItem === 'string') {
              try {
                const inner = JSON.parse(textItem);
                if (inner && typeof inner === 'object') {
                  // 尝试提取思维导图节点结构
                  const nodes = extractMindmapNodes(inner);
                  if (nodes.length > 0) {
                    apiNodes.push(...nodes);
                    console.error(`[mindmap-A] 从 initialAttributedText 提取到 ${nodes.length} 个节点`);
                  } else {
                    // 没有发现节点结构，提取所有文本
                    const extracted = extractTextsFromObject(inner);
                    if (extracted.length > 0) apiRawTexts.push(...extracted);
                  }
                }
              } catch {
                if (textItem.trim().length > 2) apiRawTexts.push(textItem.trim());
              }
            } else if (textItem && typeof textItem === 'object') {
              const nodes = extractMindmapNodes(textItem);
              if (nodes.length > 0) {
                apiNodes.push(...nodes);
              } else {
                const extracted = extractTextsFromObject(textItem);
                if (extracted.length > 0) apiRawTexts.push(...extracted);
              }
            }
          }
        }

        // --- 从已知 key 提取思维导图数据 ---
        const mindmapKeys = [
          'nodes', 'root', 'data', 'mindData', 'mind_data',
          'nodeData', 'node_data', 'treeData', 'tree',
          'topics', 'children', 'mindmap',
        ];
        for (const key of mindmapKeys) {
          const val = findDeepValue(parsed, key);
          if (val) {
            const nodes = extractMindmapNodes(
              typeof val === 'object' && !Array.isArray(val) ? val : { [key]: val }
            );
            if (nodes.length > 0) {
              apiNodes.push(...nodes);
              console.error(`[mindmap-A] 从 "${key}" 提取到 ${nodes.length} 个节点`);
            } else {
              const extracted = extractTextsFromObject(val);
              if (extracted.length > 0) {
                apiRawTexts.push(...extracted);
                console.error(`[mindmap-A] 从 "${key}" 提取到 ${extracted.length} 段文本`);
              }
            }
          }
        }
      } catch (e) {
        console.error(`[mindmap-A] 解析响应失败: ${e.message}`);
      }
    }
  } catch (err) {
    console.error(`[mindmap-A] API拦截失败: ${err.message}`);
  }

  // --- 从 JS 内存提取 ---
  const pageData = await page.evaluate(() => {
    const result = {
      title: '',
      memoryNodes: [],
      rawMemoryTexts: [],
    };

    // 标题
    const titleEl =
      document.querySelector('.header-title-text') ||
      document.querySelector('.doc-header-title') ||
      document.querySelector('[class*="doc-title"]') ||
      document.querySelector('[class*="title"]') ||
      document.querySelector('h1');
    result.title = titleEl?.textContent?.trim() || document.title || '';

    // 补充标题
    try {
      const cv = window.clientVars || window.basicClientVars;
      if (cv?.docInfo?.padInfo?.padTitle) {
        result.title = result.title || cv.docInfo.padInfo.padTitle;
      }
    } catch {}

    // --- 深度扫描 window 全局变量，寻找思维导图节点 ---
    try {
      const scanForNodes = (obj, depth, visited) => {
        if (depth > 6 || !obj || typeof obj !== 'object') return [];
        if (visited.has(obj)) return [];
        visited.add(obj);
        const nodes = [];
        try {
          if (Array.isArray(obj)) {
            for (let i = 0; i < Math.min(obj.length, 100); i++) {
              nodes.push(...scanForNodes(obj[i], depth + 1, visited));
            }
          } else {
            // 检查是否是思维导图节点（有 text/content/topic + children/sub 的对象）
            const text = obj.text || obj.content || obj.topic || obj.label || obj.title || obj.value || obj.name;
            const children = obj.children || obj.sub || obj.subTopics || obj.kids || obj.nodes;
            if (typeof text === 'string' && text.trim().length > 0) {
              const node = { text: text.trim() };
              nodes.push(node);  // 先 push 父节点
              if (children && Array.isArray(children)) {
                for (const child of children) {
                  nodes.push(...scanForNodes(child, depth + 1, visited));
                }
              }
            } else {
              for (const k of Object.keys(obj).slice(0, 100)) {
                try {
                  const val = obj[k];
                  if (val && typeof val === 'object') {
                    nodes.push(...scanForNodes(val, depth + 1, visited));
                  } else if ((k === 'content' || k === 'text' || k === 'value' || k === 'label' || k === 'title' || k === 'topic' || k === 'name' || k === 'plainText')
                    && typeof val === 'string' && val.trim().length > 2) {
                    nodes.push({ text: val.trim() });
                  }
                } catch { /* skip */ }
              }
            }
          }
        } catch {}
        return nodes;
      };

      const visited = new WeakSet();
      for (const key of ['preloadData', 'clientVars', 'basicClientVars', 'mindModel', 'pad', 'padModel', 'app', 'store', 'renderData']) {
        try {
          if (window[key] && typeof window[key] === 'object') {
            const found = scanForNodes(window[key], 0, visited);
            if (found.length > 0) {
              result.memoryNodes.push(...found.slice(0, 500));
              break;
            }
          }
        } catch {}
      }

      // 如果没找到结构化节点，扫描所有文本
      if (result.memoryNodes.length === 0) {
        const scanForTexts = (obj, depth, visited2) => {
          if (depth > 6 || !obj || typeof obj !== 'object') return [];
          if (visited2.has(obj)) return [];
          visited2.add(obj);
          const texts = [];
          try {
            if (Array.isArray(obj)) {
              for (let i = 0; i < Math.min(obj.length, 50); i++) {
                texts.push(...scanForTexts(obj[i], depth + 1, visited2));
              }
            } else {
              for (const k of Object.keys(obj).slice(0, 100)) {
                try {
                  const val = obj[k];
                  if ((k === 'content' || k === 'text' || k === 'value' || k === 'label' || k === 'title' || k === 'plainText')
                      && typeof val === 'string' && val.trim().length > 2) {
                    texts.push(val.trim());
                  } else if (val && typeof val === 'object') {
                    texts.push(...scanForTexts(val, depth + 1, visited2));
                  }
                } catch { /* skip */ }
              }
            }
          } catch {}
          return texts;
        };

        const visited2 = new WeakSet();
        for (const key of ['preloadData', 'clientVars', 'basicClientVars', 'mindModel', 'pad', 'padModel', 'app', 'store']) {
          try {
            if (window[key] && typeof window[key] === 'object') {
              const found = scanForTexts(window[key], 0, visited2);
              if (found.length > 0) {
                result.rawMemoryTexts.push(...found.slice(0, 500));
                break;
              }
            }
          } catch {}
        }
      }
    } catch {}

    return result;
  }).catch(() => ({
    title: '', memoryNodes: [], rawMemoryTexts: [],
  }));

  console.error(`[mindmap] 元信息: title="${pageData.title}", apiNodes=${apiNodes.length}, memoryNodes=${pageData.memoryNodes.length}, apiTexts=${apiRawTexts.length}, memTexts=${pageData.rawMemoryTexts.length}`);

  content.title = pageData.title || apiTitle;

  // --- 合并节点数据 ---
  if (apiNodes.length > 0) {
    content.nodes = apiNodes;
  } else if (pageData.memoryNodes.length > 0) {
    content.nodes = pageData.memoryNodes;
  }

  // --- 合并原始文本 ---
  if (content.nodes.length === 0) {
    const allRaw = [...apiRawTexts, ...pageData.rawMemoryTexts];
    const unique = [...new Set(allRaw.filter(t => t.length > 2))];
    if (unique.length > 0) {
      content.rawText = unique.join('\n').trim();
      console.error(`[mindmap] 原始文本合并: ${unique.length} 段, ${content.rawText.length} 字符`);
    }
  } else {
    // 从节点中生成 rawText
    content.rawText = content.nodes.map(n => n.text).filter(Boolean).join('\n').trim();
  }

  // ========================================================================
  // 策略 B：全页截图（始终执行，作为保底视觉内容）
  // ========================================================================
  try {
    const screenshotDir = path.join(os.tmpdir(), `wecom-mindmap-${Date.now()}`);
    fs.mkdirSync(screenshotDir, { recursive: true });

    // 查找主内容区域
    const mainClip = await page.evaluate(() => {
      const selectors = [
        '[class*="mind-canvas"]',
        '[class*="mindmap"]',
        '[class*="canvas-container"]',
        '[class*="editor-canvas"]',
        'canvas',
      ];
      for (const sel of selectors) {
        const el = document.querySelector(sel);
        if (el) {
          const r = el.getBoundingClientRect();
          if (r.width > 200 && r.height > 150) {
            return {
              x: Math.max(0, Math.round(r.x)),
              y: Math.max(0, Math.round(r.y)),
              width: Math.round(r.width),
              height: Math.round(r.height),
            };
          }
        }
      }
      return null;
    }).catch(() => null);

    const screenshotOpts = mainClip ? { clip: mainClip } : {};
    if (mainClip) {
      console.error(`[mindmap-B] 主内容区域: ${mainClip.x},${mainClip.y} ${mainClip.width}x${mainClip.height}`);
    }

    const p = path.join(screenshotDir, 'mindmap.jpg');
    await page.screenshot({ path: p, ...screenshotOpts, type: 'jpeg', quality: 60 });
    content.mindmapScreenshot = p;
    console.error(`[mindmap-B] 截图完成: ${p}`);
  } catch (err) {
    console.error(`[mindmap-B] 截图失败: ${err.message}`);
  }

  const nodeCount = content.nodes.length;
  console.error(`[mindmap] 完成: ${nodeCount} 个节点, rawText=${content.rawText?.length || 0} 字符, screenshot=${!!content.mindmapScreenshot}`);

  return content;
}

/**
 * 从对象中提取思维导图节点结构
 * 递归搜索具有 text + children 模式的对象
 */
function extractMindmapNodes(obj, maxDepth = 10, depth = 0) {
  const nodes = [];
  if (depth > maxDepth || !obj || typeof obj !== 'object') return nodes;

  if (Array.isArray(obj)) {
    for (const item of obj) {
      nodes.push(...extractMindmapNodes(item, maxDepth, depth + 1));
    }
    return nodes;
  }

  // 检测节点文本
  const textKeys = ['text', 'content', 'topic', 'label', 'title', 'name', 'value', 'plainText'];
  let nodeText = '';
  for (const k of textKeys) {
    if (typeof obj[k] === 'string' && obj[k].trim().length > 0) {
      nodeText = obj[k].trim();
      break;
    }
  }

  // 检测子节点
  const childrenKeys = ['children', 'sub', 'subTopics', 'kids', 'nodes', 'topics'];
  let childNodes = null;
  for (const k of childrenKeys) {
    if (obj[k] && Array.isArray(obj[k]) && obj[k].length > 0) {
      childNodes = obj[k];
      break;
    }
  }

  if (nodeText) {
    const node = { text: nodeText, depth };
    nodes.push(node);
  }

  // 递归处理子节点
  if (childNodes) {
    for (const child of childNodes) {
      nodes.push(...extractMindmapNodes(child, maxDepth, depth + 1));
    }
  } else if (!nodeText) {
    // 既没有文本也没有子节点结构，深入搜索
    for (const val of Object.values(obj)) {
      if (val && typeof val === 'object') {
        nodes.push(...extractMindmapNodes(val, maxDepth, depth + 1));
      }
    }
  }

  return nodes;
}

/**
 * 通用内容提取（用于 flowchart/unknown 等类型）
 * 采用与 extractDocContent 类似的多层策略
 */
async function extractGenericContent(page) {
  await page.waitForTimeout(5000);

  let content = await page.evaluate(() => {
    return {
      title: document.title,
      url: window.location.href,
      text: '',
    };
  });

  // 尝试全选+复制获取内容
  try {
    await page.click('body', { position: { x: 400, y: 400 } });
    await page.waitForTimeout(500);
    const selectAllKey = process.platform === 'darwin' ? 'Meta+A' : 'Control+A';
    await page.keyboard.press(selectAllKey);
    await page.waitForTimeout(500);

    const selectedText = await page.evaluate(() => {
      const selection = window.getSelection();
      return selection ? selection.toString() : '';
    });

    if (selectedText && selectedText.trim().length > 10) {
      content.text = selectedText.trim().substring(0, 50000);
    }
  } catch {
    // ignore
  }

  // 兜底：body.innerText 去噪
  if (!content.text) {
    content.text = await page.evaluate(() => {
      const bodyClone = document.body.cloneNode(true);
      const noiseSelectors = [
        'header', 'nav', '[class*="toolbar"]', '[class*="header"]',
        '[class*="sidebar"]', '[class*="menu"]', '[class*="footer"]',
        '[class*="nav"]', '[class*="toast"]', '[class*="modal"]',
      ];
      noiseSelectors.forEach(sel => {
        bodyClone.querySelectorAll(sel).forEach(el => el.remove());
      });
      return bodyClone.innerText ? bodyClone.innerText.trim().substring(0, 50000) : '';
    });
  }

  return content;
}

/**
 * 截图文档页面
 */
async function screenshotDoc(url, outputPath) {
  assertAllowedDocUrl(url);
  // launchBrowser 内部会自动检测 state.json 是否更新，无需手动 closeBrowser
  const { context } = await launchBrowser(true);
  const page = await context.newPage();

  try {
    await page.goto(url, {
      waitUntil: 'domcontentloaded',
      timeout: 30000,
    });

    await page.waitForTimeout(5000);

    const screenshot = await page.screenshot({
      path: outputPath,
      fullPage: true,
    });

    await saveState();

    return {
      success: true,
      path: outputPath,
      size: screenshot.length,
    };
  } catch (err) {
    return {
      success: false,
      error: err.message,
    };
  } finally {
    try { await page.close(); } catch { /* page 可能已关闭或从未创建 */ }
  }
}

/**
 * 向企微文档末尾追加内容
 * 由于企微文档使用 canvas 渲染，只能通过模拟键盘操作输入
 * 策略：Ctrl/Cmd+End 跳到文档末尾 → 回车换行 → 逐行输入文本
 * 
 * @param {string} url - 企微文档URL
 * @param {string} text - 要写入的文本内容（支持换行符）
 * @param {object} options - 可选参数
 * @param {number} options.typeDelay - 每个字符的输入延时(ms)，默认 10
 * @param {number} options.lineDelay - 每行之间的等待时间(ms)，默认 300
 * @returns {Promise<object>} 写入结果
 */
async function writeDocContent(url, text, options = {}) {
  assertAllowedDocUrl(url);
  const { typeDelay = 10, lineDelay = 300 } = options;

  // 校验文档类型，writeDocContent 仅适用于 doc 类型
  const docType = detectDocType(url);
  if (docType !== 'doc' && docType !== 'unknown') {
    return {
      success: false,
      error: `此工具仅支持 doc 类型文档，当前 URL 检测为 "${docType}" 类型。表格请使用 wecom_doc_write_sheet`,
      url,
    };
  }

  const { context } = await launchBrowser(true);
  const page = await context.newPage();

  try {
    console.error(`[wecom-doc-mcp] 正在打开文档准备写入: ${url}`);

    await page.goto(url, {
      waitUntil: 'domcontentloaded',
      timeout: 30000,
    });

    // 等待页面充分加载（canvas 渲染需要时间）
    await page.waitForTimeout(6000);

    // 检测是否需要登录
    const currentUrl = page.url();
    if (isLoginPage(currentUrl)) {
      return {
        success: false,
        error: '未登录或登录已过期，请先运行登录脚本',
        url,
      };
    }

    const isMac = process.platform === 'darwin';
    const modifier = isMac ? 'Meta' : 'Control';

    // 1. 点击文档编辑区域激活编辑器
    await page.click('body', { position: { x: 500, y: 500 } });
    await page.waitForTimeout(800);

    // 2. 跳到文档末尾: Ctrl/Cmd + End
    await page.keyboard.press(`${modifier}+End`);
    await page.waitForTimeout(500);

    // 3. 按两次 Enter 确保有空行分隔
    await page.keyboard.press('Enter');
    await page.waitForTimeout(200);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(200);

    // 4. 逐行输入文本
    const lines = text.split('\n');
    let linesWritten = 0;

    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];

      if (line.length > 0) {
        // 输入当前行内容
        await page.keyboard.type(line, { delay: typeDelay });
      }

      // 最后一行不需要按 Enter
      if (i < lines.length - 1) {
        await page.keyboard.press('Enter');
      }

      linesWritten++;

      // 每行之间稍等片刻，让 canvas 渲染跟上
      if (lineDelay > 0 && i < lines.length - 1) {
        await page.waitForTimeout(lineDelay);
      }

      // 每 20 行输出一次进度
      if (linesWritten % 20 === 0) {
        console.error(`[writeDocContent] 已写入 ${linesWritten}/${lines.length} 行`);
      }
    }

    console.error(`[writeDocContent] 全部 ${linesWritten} 行写入完成`);

    // 5. 等待 canvas 渲染完成
    await page.waitForTimeout(1000);

    // 6. 保存文档: Ctrl/Cmd + S
    await page.keyboard.press(`${modifier}+S`);
    await page.waitForTimeout(2000);

    // 7. 更新 cookie 状态
    await saveState();

    return {
      success: true,
      url,
      linesWritten,
      charsWritten: text.length,
      message: `成功向文档末尾追加 ${linesWritten} 行 (${text.length} 字符)`,
    };
  } catch (err) {
    return {
      success: false,
      error: err.message,
      url,
    };
  } finally {
    try { await page.close(); } catch { /* ignore */ }
  }
}

/**
 * 解析单元格地址（如 "A1" → {col: 0, row: 0}，"B3" → {col: 1, row: 2}）
 */
function parseCellAddress(cell) {
  const match = cell.toUpperCase().match(/^([A-Z]{1,3})(\d{1,7})$/);
  if (!match) return null;
  const colStr = match[1];
  const rowNum = parseInt(match[2], 10) - 1; // 0-based
  let colNum = 0;
  for (let i = 0; i < colStr.length; i++) {
    colNum = colNum * 26 + (colStr.charCodeAt(i) - 64);
  }
  colNum -= 1; // 0-based
  return { row: rowNum, col: colNum };
}

/**
 * 向企微表格的指定单元格写入/追加内容
 * 
 * 多层策略：
 * 策略一（最可靠）：通过 page.evaluate 探测企微表格内部 JS API，找到 setCellValue 等方法直接写入内存
 * 策略二（回退）：通过 Ctrl+Home + 箭头键精确导航到目标单元格，再用剪贴板粘贴内容
 * 策略三（兜底）：通过名称框（Name Box）定位到目标单元格，再用键盘输入
 * 
 * 写入后会读取目标单元格进行验证。
 * 
 * @param {string} url - 企微表格URL（sheet类型）
 * @param {string} cell - 单元格地址，如 "A1", "N1", "B3"
 * @param {string} text - 要写入的内容
 * @param {object} options - 可选参数
 * @param {string} options.mode - 写入模式: "append"(追加到末尾,默认) / "overwrite"(覆盖)
 * @param {string} options.tab - 可选的工作表tab标识
 * @param {number} options.typeDelay - 每个字符的输入延时(ms)，默认 15
 * @returns {Promise<object>} 写入结果
 */
// 记录已知存在合并单元格的文档 URL（去掉查询参数后的基础 URL），
// 后续对同一文档的写入直接跳过策略二，节省约 6s
const mergedCellDocUrls = new Set();

function getBaseDocUrl(url) {
  try {
    const u = new URL(url);
    return `${u.origin}${u.pathname}`;
  } catch {
    return url;
  }
}

async function writeSheetCell(url, cell, text, options = {}) {
  assertAllowedDocUrl(url);
  // 校验单元格地址格式（如 A1, N1, B3, AA100）
  if (!/^[A-Za-z]{1,3}\d{1,7}$/.test(cell)) {
    return {
      success: false,
      error: `无效的单元格地址: "${cell}"，格式应为字母+数字（如 A1, N1, B3）`,
      url,
    };
  }

  // 解析单元格地址为行列号
  const cellPos = parseCellAddress(cell);
  if (!cellPos) {
    return { success: false, error: `无法解析单元格地址: "${cell}"`, url };
  }

  // 校验文档类型，writeSheetCell 仅适用于 sheet/smartsheet 类型
  const docType = detectDocType(url);
  if (docType !== 'sheet' && docType !== 'smartsheet' && docType !== 'unknown') {
    return {
      success: false,
      error: `此工具仅支持 sheet 类型表格，当前 URL 检测为 "${docType}" 类型。文档请使用 wecom_doc_write`,
      url,
    };
  }

  const { mode = 'append', tab = null, typeDelay = 15 } = options;

  // 校验写入模式
  if (mode !== 'append' && mode !== 'overwrite') {
    return {
      success: false,
      error: `无效的写入模式: "${mode}"，仅支持 "append"（追加）或 "overwrite"（覆盖）`,
      url,
    };
  }

  const { context } = await launchBrowser(true);
  const page = await context.newPage();

  // 构造完整URL
  let targetUrl = url;
  if (tab && !url.includes('tab=')) {
    targetUrl += (url.includes('?') ? '&' : '?') + `tab=${encodeURIComponent(tab)}`;
  }

  try {
    console.error(`[writeSheetCell] 正在打开表格: ${targetUrl}`);

    await page.goto(targetUrl, {
      waitUntil: 'domcontentloaded',
      timeout: 30000,
    });

    // 等待表格充分加载
    await page.waitForTimeout(6000);

    // 检测是否需要登录
    const currentUrl = page.url();
    if (isLoginPage(currentUrl)) {
      return {
        success: false,
        error: '未登录或登录已过期，请先运行登录脚本',
        url: targetUrl,
      };
    }

    const isMac = process.platform === 'darwin';
    const modifier = isMac ? 'Meta' : 'Control';

    let writeSuccess = false;
    let writeMethod = '';

    // =====================================================
    // 策略一（最可靠）：通过企微内部 JS API 直接写入内存
    // =====================================================
    try {
      console.error('[writeSheetCell] 策略一: 探测企微表格内部 JS API...');

      const jsApiResult = await page.evaluate(({ row, col, text, mode }) => {
        const result = { success: false, method: '', debug: '', oldValue: '' };

        // 深度搜索函数：从一个对象出发，寻找含有 setCellValue/setCellText 方法的对象
        function deepSearch(obj, depth, path, visited) {
          if (depth > 6 || !obj || typeof obj !== 'object') return null;
          if (visited.has(obj)) return null;
          visited.add(obj);

          // 检查是否有写入单元格的方法
          const hasSet = typeof obj.setCellValue === 'function' || typeof obj.setCellText === 'function';
          const hasGet = typeof obj.getCellValue === 'function' || typeof obj.getCellText === 'function';

          if (hasSet || hasGet) {
            return {
              obj,
              path,
              setMethod: obj.setCellValue ? 'setCellValue' : obj.setCellText ? 'setCellText' : null,
              getMethod: obj.getCellValue ? 'getCellValue' : obj.getCellText ? 'getCellText' : null,
            };
          }

          try {
            const keys = Object.keys(obj);
            for (const key of keys.slice(0, 80)) {
              try {
                const val = obj[key];
                if (val && typeof val === 'object') {
                  const found = deepSearch(val, depth + 1, `${path}.${key}`, visited);
                  if (found) return found;
                }
              } catch { /* skip */ }
            }
          } catch { /* skip */ }

          return null;
        }

        const visited = new WeakSet();

        // 从 window 上的关键全局属性开始搜索
        const startPoints = [
          'pad', '__pad__', 'padModel', 'app', '__app__',
          'sheetApp', 'workbook', 'wb', 'store', '__store__',
          'sheetModel', '__sheet__', '__workbook__', '__data__',
          'clientVars', 'initialData', 'appData',
        ];

        let apiObj = null;

        // 先从已知候选开始
        for (const sp of startPoints) {
          try {
            if (window[sp]) {
              const found = deepSearch(window[sp], 0, `window.${sp}`, visited);
              if (found) {
                apiObj = found;
                result.debug += `Found at ${found.path} set:${found.setMethod} get:${found.getMethod}; `;
                break;
              }
            }
          } catch { /* skip */ }
        }

        // 如果候选没找到，扫描 window 上所有属性
        if (!apiObj) {
          try {
            for (const key of Object.getOwnPropertyNames(window)) {
              try {
                const val = window[key];
                if (val && typeof val === 'object') {
                  if (typeof val.setCellValue === 'function' || typeof val.setCellText === 'function') {
                    apiObj = {
                      obj: val,
                      path: `window.${key}`,
                      setMethod: val.setCellValue ? 'setCellValue' : 'setCellText',
                      getMethod: val.getCellValue ? 'getCellValue' : val.getCellText ? 'getCellText' : null,
                    };
                    result.debug += `Found directly at window.${key}; `;
                    break;
                  }
                }
              } catch { /* skip */ }
            }
          } catch { /* skip */ }
        }

        if (!apiObj) {
          result.debug += 'No JS API found';
          return result;
        }

        // 读取旧值（如果有 get 方法）
        if (apiObj.getMethod) {
          try {
            const oldVal = apiObj.obj[apiObj.getMethod](row, col);
            result.oldValue = oldVal != null ? String(oldVal) : '';
          } catch { /* ignore */ }
        }

        // 执行写入
        if (apiObj.setMethod) {
          try {
            const newValue = (mode === 'append' && result.oldValue) ? result.oldValue + text : text;
            apiObj.obj[apiObj.setMethod](row, col, newValue);
            result.success = true;
            result.method = `JS API: ${apiObj.path}.${apiObj.setMethod}`;

            // 验证写入：读取回来确认
            if (apiObj.getMethod) {
              try {
                const readBack = apiObj.obj[apiObj.getMethod](row, col);
                const readBackStr = readBack != null ? String(readBack) : '';
                if (readBackStr !== newValue) {
                  result.debug += `VERIFY WARN: expected "${newValue}" but got "${readBackStr}"; `;
                }
              } catch { /* ignore */ }
            }
          } catch (e) {
            result.debug += `setCellValue failed: ${e.message}; `;
          }
        } else {
          result.debug += 'Has get but no set method; ';
        }

        return result;
      }, { row: cellPos.row, col: cellPos.col, text, mode });

      if (jsApiResult.success) {
        writeSuccess = true;
        writeMethod = jsApiResult.method;
        console.error(`[writeSheetCell] 策略一成功: ${jsApiResult.method} | debug: ${jsApiResult.debug}`);
      } else {
        console.error(`[writeSheetCell] 策略一未成功: ${jsApiResult.debug}`);
      }
    } catch (err) {
      console.error(`[writeSheetCell] 策略一异常: ${err.message}`);
    }

    // =====================================================
    // 策略二（回退）：Ctrl+Home + 箭头键导航 + 剪贴板粘贴
    // =====================================================
    const baseUrl = getBaseDocUrl(targetUrl);
    if (!writeSuccess && cellPos.row + cellPos.col <= 200 && !mergedCellDocUrls.has(baseUrl)) {
      // 策略二仅在目标单元格离 A1 较近且文档未被标记为有合并单元格时使用
      try {
        console.error(`[writeSheetCell] 策略二: 箭头键导航到 ${cell.toUpperCase()} (row=${cellPos.row}, col=${cellPos.col})...`);

        // Step 1: 点击表格区域先激活
        await page.click('body', { position: { x: 500, y: 400 } });
        await page.waitForTimeout(800);

        // Step 2: Ctrl+Home 回到 A1
        await page.keyboard.press(`${modifier}+Home`);
        await page.waitForTimeout(800);

        // Step 3: 用箭头键导航到目标行列
        for (let r = 0; r < cellPos.row; r++) {
          await page.keyboard.press('ArrowDown');
          await page.waitForTimeout(30);
        }
        for (let c = 0; c < cellPos.col; c++) {
          await page.keyboard.press('ArrowRight');
          await page.waitForTimeout(30);
        }
        await page.waitForTimeout(500);

        // Step 3.5: 验证导航位置——读取名称框确认当前选中单元格是否为目标
        const actualAddr = await page.evaluate(() => {
          const barLabel = document.querySelector('input.bar-label');
          if (barLabel && barLabel.value) return barLabel.value.trim().toUpperCase();
          const inputs = document.querySelectorAll('input[type="text"], input:not([type])');
          for (const inp of inputs) {
            const val = (inp.value || '').trim().toUpperCase();
            if (/^[A-Z]{1,3}\d{1,6}$/.test(val)) {
              const rect = inp.getBoundingClientRect();
              if (rect.width > 30 && rect.width < 150 && rect.x < 300 && rect.y < 300) return val;
            }
          }
          return null;
        });
        if (actualAddr && actualAddr !== cell.toUpperCase()) {
          console.error(`[writeSheetCell] 策略二: 导航验证失败，期望=${cell.toUpperCase()} 实际=${actualAddr}（合并单元格跳格），放弃策略二`);
          mergedCellDocUrls.add(baseUrl);
          console.error(`[writeSheetCell] 已标记文档存在合并单元格，后续写入将跳过策略二`);
          throw new Error(`导航偏移: 期望${cell.toUpperCase()} 实际${actualAddr}`);
        }
        console.error(`[writeSheetCell] 策略二: 已导航到 ${cell.toUpperCase()}${actualAddr ? ' ✓已验证' : ''}`);

        // Step 4: 根据模式写入内容
        if (mode === 'overwrite') {
          await page.keyboard.press('Delete');
          await page.waitForTimeout(300);
        } else {
          await page.keyboard.press('F2');
          await page.waitForTimeout(300);
          await page.keyboard.press('End');
          await page.waitForTimeout(200);
        }

        // Step 5: 通过剪贴板粘贴内容
        try {
          await page.evaluate(async (t) => {
            await navigator.clipboard.writeText(t);
          }, text);
          await page.waitForTimeout(200);
          await page.keyboard.press(`${modifier}+V`);
          await page.waitForTimeout(500);
          console.error(`[writeSheetCell] 策略二: 剪贴板粘贴成功`);
        } catch {
          console.error(`[writeSheetCell] 策略二: 剪贴板不可用，使用键盘输入`);
          await page.keyboard.type(text, { delay: typeDelay });
        }

        // Step 6: Enter 确认
        await page.keyboard.press('Enter');
        await page.waitForTimeout(800);

        writeSuccess = true;
        writeMethod = `箭头键导航 (row=${cellPos.row}, col=${cellPos.col}) + ${text.length <= 50 ? '剪贴板粘贴' : '键盘输入'}`;
        console.error(`[writeSheetCell] 策略二完成`);
      } catch (err) {
        console.error(`[writeSheetCell] 策略二异常: ${err.message}`);
      }
    }

    // =====================================================
    // 策略三（兜底）：名称框定位 + 公式栏写入
    // 流程：名称框三连击选中 → 输入地址 → Enter跳转 → Tab到公式栏 → 写入内容 → Enter确认
    // =====================================================
    if (!writeSuccess) {
      try {
        console.error('[writeSheetCell] 策略三: 名称框定位 + 公式栏写入...');

        // Step 0: Escape 退出可能的编辑态
        await page.keyboard.press('Escape');
        await page.waitForTimeout(300);
        await page.keyboard.press('Escape');
        await page.waitForTimeout(500);

        // Step 1: 点击表格区域先激活
        await page.click('body', { position: { x: 500, y: 400 } });
        await page.waitForTimeout(1000);

        // Step 2: 定位名称框（多种selector兼容不同企微版本）
        let nbRect = await page.evaluate(() => {
          // 优先：input.bar-label（新版企微）
          const barLabel = document.querySelector('input.bar-label');
          if (barLabel) {
            const rect = barLabel.getBoundingClientRect();
            return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2, value: barLabel.value };
          }
          // 兼容旧版：多种 selector 尝试
          const selectors = [
            '[class*="name-box"] input', '[class*="namebox"] input',
            '[class*="cell-address"] input', '[class*="name-box"]',
            '[class*="namebox"]', '[class*="cell-address"]',
            '[class*="address-input"]', 'input[class*="name"]',
          ];
          for (const sel of selectors) {
            try {
              const el = document.querySelector(sel);
              if (el) {
                const rect = el.getBoundingClientRect();
                if (rect.width > 20 && rect.width < 200 && rect.x < 400 && rect.y < 400) {
                  return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2, value: el.value || '' };
                }
              }
            } catch { /* continue */ }
          }
          // 最后：扫描页面 input 找到值匹配单元格地址模式且位于左上角的
          const inputs = document.querySelectorAll('input[type="text"], input:not([type])');
          for (const inp of inputs) {
            const val = (inp.value || '').trim().toUpperCase();
            if (/^[A-Z]{1,3}\d{1,6}$/.test(val)) {
              const rect = inp.getBoundingClientRect();
              if (rect.width > 30 && rect.width < 150 && rect.x < 300 && rect.y < 300) {
                return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2, value: val };
              }
            }
          }
          return null;
        });

        if (!nbRect) {
          console.error('[writeSheetCell] 策略三: 名称框未找到，使用默认坐标');
        } else {
          console.error(`[writeSheetCell] 策略三: 名称框定位成功 (${nbRect.x.toFixed(0)},${nbRect.y.toFixed(0)})，值="${nbRect.value}"`);
        }

        const nameBoxX = nbRect ? nbRect.x : 48;
        const nameBoxY = nbRect ? nbRect.y : 162;
        console.error(`[writeSheetCell] 策略三: 名称框坐标 (${nameBoxX.toFixed(0)},${nameBoxY.toFixed(0)})`);

        // Step 3: 三连击名称框选中当前地址文本
        await page.mouse.click(nameBoxX, nameBoxY, { clickCount: 3 });
        await page.waitForTimeout(500);

        // Step 4: 逐字符输入目标单元格地址
        for (const ch of cell.toUpperCase()) {
          await page.keyboard.press(ch);
          await page.waitForTimeout(50);
        }
        await page.waitForTimeout(400);

        // Step 5: Enter 触发名称框跳转到目标单元格
        await page.keyboard.press('Enter');
        await page.waitForTimeout(1500);

        // Step 6: Tab 将焦点从名称框转到公式栏
        await page.keyboard.press('Tab');
        await page.waitForTimeout(500);

        // 安全检查：确认焦点已转到公式栏，避免 Ctrl+A 意外全选表格
        let formulaBarReady = await page.evaluate(() => {
          const focused = document.activeElement;
          if (!focused) return false;
          const cls = (focused.className || '').toLowerCase();
          const id = (focused.id || '').toLowerCase();
          const tag = (focused.tagName || '').toLowerCase();
          // 公式栏特征：class 含 formula/editor，或 id 含 editor，或是 input/textarea/contenteditable
          if (cls.includes('formula') || cls.includes('editor') || id.includes('editor')) return true;
          if ((tag === 'input' || tag === 'textarea') && focused.closest('[class*="formula"]')) return true;
          if (focused.isContentEditable) return true;
          return false;
        });

        if (!formulaBarReady) {
          // 焦点未在公式栏，尝试直接点击公式栏
          console.error('[writeSheetCell] 策略三: Tab后焦点不在公式栏，尝试点击公式栏元素');
          const formulaBarSelectors = [
            '.formula-input', '#alloy-simple-text-editor',
            '[class*="formula"] input', '[class*="formula"] textarea',
            '[class*="formula"][contenteditable]', '[class*="editor-area"]',
          ];
          let clicked = false;
          for (const sel of formulaBarSelectors) {
            try {
              const el = await page.$(sel);
              if (el) {
                await el.click();
                await page.waitForTimeout(300);
                clicked = true;
                break;
              }
            } catch { /* continue */ }
          }
          if (clicked) {
            // 再次确认焦点
            formulaBarReady = await page.evaluate(() => {
              const focused = document.activeElement;
              if (!focused) return false;
              const tag = (focused.tagName || '').toLowerCase();
              return tag === 'input' || tag === 'textarea' || focused.isContentEditable;
            });
          }
          if (!formulaBarReady) {
            // 公式栏定位彻底失败，回退到直接在单元格内编辑（兼容旧版行为）
            console.error('[writeSheetCell] 策略三: 公式栏定位失败，回退到单元格内直接编辑');
            // F2 进入编辑态
            await page.keyboard.press('F2');
            await page.waitForTimeout(500);
          }
        }

        // Step 7: 写入内容
        if (formulaBarReady) {
          // 公式栏模式
          if (mode === 'overwrite') {
            await page.keyboard.press(`${modifier}+A`);
            await page.waitForTimeout(200);
          } else {
            await page.keyboard.press('End');
            await page.waitForTimeout(200);
          }
        } else {
          // 回退模式：单元格内编辑（与旧版策略三行为一致）
          if (mode === 'overwrite') {
            // F2 进入后 Ctrl+A 全选再 Delete 清空
            await page.keyboard.press(`${modifier}+A`);
            await page.waitForTimeout(200);
            await page.keyboard.press('Delete');
            await page.waitForTimeout(300);
          } else {
            await page.keyboard.press('End');
            await page.waitForTimeout(200);
          }
        }

        // 通过剪贴板粘贴内容
        try {
          await page.evaluate(async (t) => {
            await navigator.clipboard.writeText(t);
          }, text);
          await page.waitForTimeout(200);
          await page.keyboard.press(`${modifier}+V`);
          await page.waitForTimeout(500);
        } catch {
          await page.keyboard.type(text, { delay: typeDelay });
        }

        // Step 8: Enter 确认写入
        await page.keyboard.press('Enter');
        await page.waitForTimeout(800);

        writeSuccess = true;
        writeMethod = formulaBarReady ? '名称框定位 + 公式栏写入' : '名称框定位 + 单元格内编辑(回退)';
        console.error(`[writeSheetCell] 策略三完成 (${writeMethod})`);
      } catch (err) {
        console.error(`[writeSheetCell] 策略三异常: ${err.message}`);
      }
    }

    if (!writeSuccess) {
      return {
        success: false,
        error: '所有写入策略均失败',
        url: targetUrl,
        cell: cell.toUpperCase(),
      };
    }

    // =====================================================
    // 保存 Ctrl+S
    // =====================================================
    await page.keyboard.press(`${modifier}+S`);
    await page.waitForTimeout(2000);
    console.error('[writeSheetCell] 已发送 Ctrl+S 保存');

    // =====================================================
    // 写入后验证：读取目标单元格确认写入成功
    // =====================================================
    let verified = false;
    let verifyValue = '';
    try {
      const verifyResult = await page.evaluate(({ row, col }) => {
        // 复用策略一的搜索逻辑查找 getCellValue/getCellText
        for (const key of Object.getOwnPropertyNames(window)) {
          try {
            const val = window[key];
            if (val && typeof val === 'object') {
              if (typeof val.getCellValue === 'function') {
                const v = val.getCellValue(row, col);
                return { found: true, value: v != null ? String(v) : '' };
              }
              if (typeof val.getCellText === 'function') {
                const v = val.getCellText(row, col);
                return { found: true, value: v != null ? String(v) : '' };
              }
            }
          } catch { /* skip */ }
        }
        return { found: false, value: '' };
      }, { row: cellPos.row, col: cellPos.col });

      if (verifyResult.found) {
        verifyValue = verifyResult.value;
        verified = (mode === 'overwrite' && verifyValue === text) || (mode === 'append' && verifyValue.endsWith(text));
        console.error(`[writeSheetCell] 验证结果: ${verified ? '✓ 成功' : '✗ 不匹配'} | 读回值="${verifyValue}" | 写入值="${text}"`);
      } else {
        console.error('[writeSheetCell] 验证: JS API 不可用，无法验证');
      }
    } catch (err) {
      console.error(`[writeSheetCell] 验证异常: ${err.message}`);
    }

    // 更新 cookie
    await saveState();

    return {
      success: true,
      url: targetUrl,
      cell: cell.toUpperCase(),
      mode,
      charsWritten: text.length,
      method: writeMethod,
      verified,
      verifyValue: verifyValue || undefined,
      message: `成功${mode === 'overwrite' ? '覆盖写入' : '追加内容到'}单元格 ${cell.toUpperCase()} (${text.length} 字符) [${writeMethod}]${verified ? ' ✓已验证' : ''}`,
    };
  } catch (err) {
    return {
      success: false,
      error: err.message,
      url: targetUrl,
    };
  } finally {
    try { await page.close(); } catch { /* ignore */ }
  }
}

/**
 * 批量写入企微表格多个单元格（复用同一个页面，大幅减少总耗时）
 * @param {string} url - 企微表格 URL
 * @param {Array<{cell: string, content: string}>} cells - 要写入的单元格数组
 * @param {object} options - 选项（mode, tab, typeDelay）
 * @returns {Promise<object>} 批量写入结果
 */
async function writeSheetCells(url, cells, options = {}) {
  assertAllowedDocUrl(url);
  if (!Array.isArray(cells) || cells.length === 0) {
    return { success: false, error: '请提供要写入的单元格数组', url };
  }

  // 校验所有单元格地址
  for (const item of cells) {
    if (!item.cell || !/^[A-Za-z]{1,3}\d{1,7}$/.test(item.cell)) {
      return { success: false, error: `无效的单元格地址: "${item.cell}"`, url };
    }
    if (item.content == null || item.content === '') {
      return { success: false, error: `单元格 ${item.cell} 缺少写入内容`, url };
    }
  }

  const docType = detectDocType(url);
  if (docType !== 'sheet' && docType !== 'smartsheet' && docType !== 'unknown') {
    return { success: false, error: `此工具仅支持 sheet 类型表格，当前为 "${docType}"`, url };
  }

  const { mode = 'overwrite', tab = null, typeDelay = 15 } = options;
  const { context } = await launchBrowser(true);
  const page = await context.newPage();

  let targetUrl = url;
  if (tab && !url.includes('tab=')) {
    targetUrl += (url.includes('?') ? '&' : '?') + `tab=${encodeURIComponent(tab)}`;
  }

  try {
    console.error(`[writeSheetCells] 批量写入 ${cells.length} 个单元格: ${targetUrl}`);

    await page.goto(targetUrl, { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(5000);

    const currentUrl = page.url();
    if (isLoginPage(currentUrl)) {
      return { success: false, error: '未登录或登录已过期', url: targetUrl };
    }

    const modifier = process.platform === 'darwin' ? 'Meta' : 'Control';
    const baseUrl = getBaseDocUrl(targetUrl);

    // 定位名称框坐标（只做一次）
    const nbRect = await page.evaluate(() => {
      const barLabel = document.querySelector('input.bar-label');
      if (barLabel) {
        const rect = barLabel.getBoundingClientRect();
        return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
      }
      const selectors = [
        '[class*="name-box"] input', '[class*="namebox"] input',
        '[class*="cell-address"] input', '[class*="name-box"]',
        '[class*="namebox"]', '[class*="cell-address"]',
        '[class*="address-input"]', 'input[class*="name"]',
      ];
      for (const sel of selectors) {
        try {
          const el = document.querySelector(sel);
          if (el) {
            const rect = el.getBoundingClientRect();
            if (rect.width > 20 && rect.width < 200 && rect.x < 400 && rect.y < 400) {
              return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
            }
          }
        } catch { /* continue */ }
      }
      const inputs = document.querySelectorAll('input[type="text"], input:not([type])');
      for (const inp of inputs) {
        const val = (inp.value || '').trim().toUpperCase();
        if (/^[A-Z]{1,3}\d{1,6}$/.test(val)) {
          const rect = inp.getBoundingClientRect();
          if (rect.width > 30 && rect.width < 150 && rect.x < 300 && rect.y < 300) {
            return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
          }
        }
      }
      return null;
    });

    const nameBoxX = nbRect ? nbRect.x : 48;
    const nameBoxY = nbRect ? nbRect.y : 162;
    console.error(`[writeSheetCells] 名称框坐标: (${nameBoxX.toFixed(0)},${nameBoxY.toFixed(0)})`);

    const results = [];

    for (let i = 0; i < cells.length; i++) {
      const { cell, content } = cells[i];
      const cellUpper = cell.toUpperCase();
      const startTime = Date.now();

      try {
        console.error(`[writeSheetCells] [${i + 1}/${cells.length}] 写入 ${cellUpper} = "${content.substring(0, 30)}${content.length > 30 ? '...' : ''}"`);

        // Escape 退出可能的编辑态
        await page.keyboard.press('Escape');
        await page.waitForTimeout(200);

        // 三连击名称框
        await page.mouse.click(nameBoxX, nameBoxY, { clickCount: 3 });
        await page.waitForTimeout(300);

        // 输入目标单元格地址
        for (const ch of cellUpper) {
          await page.keyboard.press(ch);
          await page.waitForTimeout(30);
        }
        await page.waitForTimeout(200);

        // Enter 跳转
        await page.keyboard.press('Enter');
        await page.waitForTimeout(800);

        // Tab 到公式栏
        await page.keyboard.press('Tab');
        await page.waitForTimeout(300);

        // 写入内容
        if (mode === 'overwrite') {
          await page.keyboard.press(`${modifier}+A`);
          await page.waitForTimeout(100);
        } else {
          await page.keyboard.press('End');
          await page.waitForTimeout(100);
        }

        // 剪贴板粘贴
        try {
          await page.evaluate(async (t) => {
            await navigator.clipboard.writeText(t);
          }, content);
          await page.waitForTimeout(100);
          await page.keyboard.press(`${modifier}+V`);
          await page.waitForTimeout(300);
        } catch {
          await page.keyboard.type(content, { delay: typeDelay });
        }

        // Enter 确认
        await page.keyboard.press('Enter');
        await page.waitForTimeout(500);

        const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
        results.push({ cell: cellUpper, success: true, elapsed: `${elapsed}s` });
        console.error(`[writeSheetCells] [${i + 1}/${cells.length}] ${cellUpper} 完成 (${elapsed}s)`);
      } catch (err) {
        const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
        results.push({ cell: cellUpper, success: false, error: err.message, elapsed: `${elapsed}s` });
        console.error(`[writeSheetCells] [${i + 1}/${cells.length}] ${cellUpper} 失败: ${err.message}`);
      }
    }

    // 统一保存
    await page.keyboard.press(`${modifier}+S`);
    await page.waitForTimeout(2000);
    console.error('[writeSheetCells] 已保存');

    await saveState();

    const successCount = results.filter(r => r.success).length;
    return {
      success: successCount > 0,
      url: targetUrl,
      total: cells.length,
      successCount,
      failCount: cells.length - successCount,
      results,
      message: `批量写入完成: ${successCount}/${cells.length} 成功`,
    };
  } catch (err) {
    return { success: false, error: err.message, url: targetUrl };
  } finally {
    try { await page.close(); } catch { /* ignore */ }
  }
}

/**
 * 创建企微在线文档
 * 通过 Playwright 模拟浏览器操作在企微文档首页新建文档/表格
 * 策略：点击新建菜单后拦截 precreate API 响应获取新文档 URL
 * @param {string} type - 文档类型：'doc' | 'sheet' | 'slide'
 * @param {string} [title] - 可选的文档标题
 * @returns {Promise<object>} 包含新文档 URL 的结果
 */
async function createDoc(type = 'sheet', title = '') {
  const { context } = await launchBrowser(true);
  const page = await context.newPage();

  // 文档类型与菜单项的映射（menuTexts 包含多种可能的菜单文案以兼容不同版本）
  const typeConfig = {
    doc: { menuTexts: ['智能文档', '在线文档', '文档'], createType: 1, urlPattern: '/doc/' },
    sheet: { menuTexts: ['智能表格', '在线表格', '表格'], createType: 2, urlPattern: '/sheet/' },
    slide: { menuTexts: ['幻灯片', '演示文稿'], createType: 3, urlPattern: '/slide/' },
    mindmap: { menuTexts: ['思维导图'], createType: 4, urlPattern: '/mindmap/' },
    flowchart: { menuTexts: ['流程图'], createType: 5, urlPattern: '/flowchart/' },
    form: { menuTexts: ['收集表', '表单'], createType: 6, urlPattern: '/form/' },
  };
  const config = typeConfig[type] || typeConfig.sheet;

  // 提升到 try 外声明，避免 try 早期抛错时 finally 因 TDZ 触发 ReferenceError
  let pageHandler = null;
  let newPage = null;

  try {
    // 访问企微文档首页
    console.error(`[createDoc] 正在访问企微文档首页...`);
    await page.goto('https://doc.weixin.qq.com', { waitUntil: 'domcontentloaded', timeout: 30000 });
    await page.waitForTimeout(3000);

    // 检测是否需要登录
    const currentUrl = page.url();
    if (isLoginPage(currentUrl)) {
      return {
        success: false,
        error: '未登录或登录已过期，请先运行登录脚本：wecom_doc_login',
      };
    }

    // 设置 API 响应拦截，捕获 precreate 返回的新文档 URL
    let newDocUrl = null;
    page.on('response', async (resp) => {
      const url = resp.url();
      if (url.includes('precreate') || url.includes('pre_create')) {
        try {
          const body = await resp.json();
          if (body.body && body.body.items && body.body.items.length > 0) {
            // 找到匹配类型的预创建文档
            for (const item of body.body.items) {
              if (item.url && item.url.includes(config.urlPattern)) {
                newDocUrl = item.url;
                console.error(`[createDoc] 从 precreate API 获取到 URL: ${newDocUrl}`);
                break;
              }
            }
            // 如果没有精确匹配，取第一个
            if (!newDocUrl && body.body.items[0].url) {
              newDocUrl = body.body.items[0].url;
              console.error(`[createDoc] 从 precreate API 获取到 URL (首项): ${newDocUrl}`);
            }
          }
        } catch { /* ignore */ }
      }
    });

    // 同时监听新 tab 打开
    pageHandler = async (p) => {
      try {
        await p.waitForLoadState('domcontentloaded');
        await new Promise(r => setTimeout(r, 3000));
        if (p.url().includes('doc.weixin.qq.com') && !p.url().includes('/home/')) {
          newDocUrl = p.url();
          newPage = p;
          console.error(`[createDoc] 新 tab 打开: ${newDocUrl}`);
        }
      } catch { /* ignore */ }
    };
    context.on('page', pageHandler);

    // 点击"新建"按钮
    console.error('[createDoc] 点击新建按钮...');
    await page.click('button:has-text("新建")');
    await page.waitForTimeout(1500);

    // 在下拉菜单中点击对应文档类型
    console.error(`[createDoc] 选择: ${config.menuTexts[0]}`);
    await page.evaluate((menuTexts) => {
      // 在 dropdown 菜单中找到并点击
      const items = document.querySelectorAll('[class*="dropdownMenu_item"], [class*="menu-item"], [role="menuitem"]');
      for (const menuText of menuTexts) {
        for (const item of items) {
          if (item.textContent.trim() === menuText || item.textContent.includes(menuText)) {
            item.click();
            return true;
          }
        }
      }
      // 备选：直接文本匹配
      const allEls = document.querySelectorAll('span, div, a');
      for (const menuText of menuTexts) {
        for (const el of allEls) {
          if (el.textContent.trim() === menuText && el.offsetParent !== null) {
            el.click();
            return true;
          }
        }
      }
      return false;
    }, config.menuTexts);

    // 等待 API 响应或新 tab
    await page.waitForTimeout(8000);

    // 如果 precreate API 没拦截到，检查是否有新 tab 或页面跳转
    if (!newDocUrl) {
      // 检查所有页面
      const allPages = context.pages();
      for (const p of allPages) {
        const pUrl = p.url();
        if (pUrl.includes(config.urlPattern) && !pUrl.includes('/home/')) {
          newDocUrl = pUrl;
          newPage = p;
          break;
        }
      }
    }

    // 如果还是没有，可能是跳到了列表页需要二次新建
    if (!newDocUrl) {
      console.error('[createDoc] 首次点击未直接创建，尝试在列表页二次新建...');
      await page.waitForTimeout(2000);

      // 在列表页找新建按钮
      const hasNewBtn = await page.$('button.fileToolbar_button:has-text("新建")');
      if (hasNewBtn) {
        await hasNewBtn.click();
        await page.waitForTimeout(8000);

        // 再次检查
        const allPages2 = context.pages();
        for (const p of allPages2) {
          const pUrl = p.url();
          if (pUrl.includes(config.urlPattern) && !pUrl.includes('/home/')) {
            newDocUrl = pUrl;
            newPage = p;
            break;
          }
        }
      }
    }

    if (!newDocUrl) {
      return {
        success: false,
        error: '未能获取新文档 URL，企微可能需要在浏览器中手动操作',
      };
    }

    // 如果需要设置标题且有新文档页面可操作
    if (title && newPage) {
      console.error(`[createDoc] 尝试设置标题: ${title}`);
      try {
        await newPage.waitForTimeout(2000);
        // 尝试点击标题区域
        const titleEl = await newPage.$('[class*="title"] input, [class*="title"][contenteditable], [class*="doc-name"]');
        if (titleEl) {
          await titleEl.click();
          await newPage.waitForTimeout(300);
          const modifier = process.platform === 'darwin' ? 'Meta' : 'Control';
          await newPage.keyboard.press(`${modifier}+A`);
          await newPage.waitForTimeout(200);
          await newPage.keyboard.type(title, { delay: 30 });
          await newPage.keyboard.press('Enter');
          await newPage.waitForTimeout(500);
          console.error('[createDoc] 标题设置成功');
        }
      } catch (e) {
        console.error(`[createDoc] 标题设置失败: ${e.message}`);
      }
    }

    // 保存状态
    await saveState();

    // 关闭新文档 tab（后续读写操作会按 URL 重新打开）
    if (newPage) {
      try { await newPage.close(); } catch { /* ignore */ }
    }

    const typeLabelMap = { doc: '在线文档', sheet: '在线表格', slide: '幻灯片', mindmap: '思维导图', flowchart: '流程图', form: '收集表' };
    const typeLabel = typeLabelMap[type] || '文档';
    return {
      success: true,
      url: newDocUrl,
      type,
      title: title || '(默认标题)',
      message: `成功创建${typeLabel}，URL: ${newDocUrl}`,
    };
  } catch (err) {
    return {
      success: false,
      error: err.message,
    };
  } finally {
    if (pageHandler) {
      try { context.off('page', pageHandler); } catch { /* ignore */ }
    }
    try { await page.close(); } catch { /* ignore */ }
  }
}

export { fetchDocContent, screenshotDoc, detectDocType, writeDocContent, writeSheetCell, writeSheetCells, createDoc };
