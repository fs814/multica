/**
 * 浏览器管理模块
 * 负责 Playwright 浏览器实例的创建、Cookie 持久化和页面管理
 */
import { chromium } from 'playwright';
import fs from 'fs';
import path from 'path';
import os from 'os';

// Cookie 存储路径
const COOKIE_DIR = path.join(os.homedir(), '.wecom-doc-mcp');
const STATE_FILE = path.join(COOKIE_DIR, 'state.json');

/**
 * 浏览器来源（Multica fork 新增）。
 *
 * 上游只用 Playwright 自带的 Chromium。问题是那个二进制需要
 * `playwright install chromium` 下载 ~170MB 并解压，而在装了
 * 腾讯电脑管家一类端点安全软件的机器上，解压会被静默拦截 —— 进程在
 * chrome-win64\D3DCompiler_47.dll 之后就被杀掉，install 还返回 0，
 * 于是 launch 报 "Executable doesn't exist at ...\chromium-1208\chrome.exe"。
 *
 * `channel` 让 Playwright 直接驱动系统已装的 Chrome / Edge，完全绕开
 * 那次下载和解压。设 WECOM_MCP_BROWSER_CHANNEL=chrome|msedge 即可启用；
 * 不设则保持上游行为（用 Playwright 自带的 Chromium）。
 */
function launchOptions(extra = {}) {
  const channel = String(process.env.WECOM_MCP_BROWSER_CHANNEL || '').trim();
  return channel ? { ...extra, channel } : extra;
}

let browser = null;
let context = null;
let lastStateModTime = -1; // 上次读取 state.json 的修改时间，-1 表示尚未初始化

/**
 * 精确判断 URL 是否为登录页面
 * 通过解析 URL 的 hostname 和 pathname 来判断，避免简单 includes 造成误判
 */
function isLoginPage(urlStr) {
  try {
    const urlObj = new URL(urlStr);
    const hostname = urlObj.hostname;
    const pathname = urlObj.pathname;
    // 登录域名
    if (hostname.includes('passport.') || hostname.includes('login.')) {
      return true;
    }
    // 登录路径（以 /login 开头）
    if (pathname === '/login' || pathname.startsWith('/login/') || pathname.startsWith('/passport/')) {
      return true;
    }
    return false;
  } catch {
    // URL 解析失败时降级为包含检测
    return urlStr.includes('/login') || urlStr.includes('/passport');
  }
}

/**
 * 检查 state.json 是否被外部更新（如 login.js 进程）
 * 仅在已经初始化过 lastStateModTime 后才做对比
 */
function isStateFileUpdated() {
  if (lastStateModTime < 0) {
    return false; // 尚未初始化，不触发刷新
  }
  try {
    if (fs.existsSync(STATE_FILE)) {
      const stat = fs.statSync(STATE_FILE);
      const mtime = stat.mtimeMs;
      if (mtime > lastStateModTime) {
        return true;
      }
    }
  } catch {
    // ignore
  }
  return false;
}

/**
 * 确保 Cookie 目录存在
 *
 * mode 0700（Multica fork 新增）：这个目录下的 state.json 存放企微的
 * TOK / uid / wedrive_sid 等会话 Cookie，等同于账号凭据。上游用默认权限
 * 创建，在 Linux/macOS 上会是 0755，同机其他用户可以读到。
 */
function ensureCookieDir() {
  if (!fs.existsSync(COOKIE_DIR)) {
    fs.mkdirSync(COOKIE_DIR, { recursive: true, mode: 0o700 });
  }
}

/**
 * 保存浏览器状态（Cookie + localStorage）
 */
async function saveState() {
  ensureCookieDir();
  if (context) {
    const state = await context.storageState();
    // mode 0600（Multica fork 新增）：文件内容是可直接复用的企微会话
    // Cookie。上游省略 mode，落盘即 0644，共享主机上任何本地用户都能读走
    // 并冒用账号。Windows 下由用户目录 ACL 兜底，但 Linux/mac 必须显式限权。
    fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2), { mode: 0o600 });
    // 同步更新 mtime，防止 isStateFileUpdated() 误判为外部更新
    try {
      lastStateModTime = fs.statSync(STATE_FILE).mtimeMs;
    } catch {
      // ignore
    }
    return true;
  }
  return false;
}

/**
 * 读取原始 state.json（不做任何 cookie 修改）
 * 用于 checkLoginStatus 等需要判断原始过期状态的场景
 */
function loadRawState() {
  if (fs.existsSync(STATE_FILE)) {
    try {
      const data = fs.readFileSync(STATE_FILE, 'utf-8');
      return JSON.parse(data);
    } catch (err) {
      console.error(`[loadRawState] 解析 state.json 失败: ${err.message}`);
      return null;
    }
  }
  return null;
}

/**
 * 加载已保存的浏览器状态（修复 cookie 兼容性问题后用于创建浏览器上下文）
 * 修复以下问题：
 * 1. session cookie (expires=-1) 在新上下文中被忽略
 * 2. 已过期的非 session cookie 需要过滤掉
 * 3. sameSite 属性需要有效值
 */
function loadState() {
  if (fs.existsSync(STATE_FILE)) {
    try {
      const data = fs.readFileSync(STATE_FILE, 'utf-8');
      const state = JSON.parse(data);
      
      if (state && state.cookies) {
        const now = Math.floor(Date.now() / 1000);
        const futureExpires = now + 30 * 24 * 3600; // 30天后
        
        state.cookies = state.cookies
          // 过滤掉已过期的非 session cookie
          .filter(cookie => {
            if (cookie.expires > 0 && cookie.expires < now) {
              console.error(`[loadState] 过滤已过期cookie: ${cookie.name}`);
              return false;
            }
            return true;
          })
          .map(cookie => {
            const fixed = { ...cookie };
            
            // 修复 session cookie: 将 expires=-1 改为未来30天
            if (fixed.expires === -1) {
              fixed.expires = futureExpires;
            }
            
            // 确保 sameSite 属性有效（Playwright 只接受 Strict/Lax/None）
            if (fixed.sameSite && !['Strict', 'Lax', 'None'].includes(fixed.sameSite)) {
              fixed.sameSite = 'Lax';
            }
            
            return fixed;
          });
        
        console.error(`[loadState] 加载了 ${state.cookies.length} 个 cookie`);
      }
      
      return state;
    } catch (err) {
      console.error(`[loadState] 解析 state.json 失败: ${err.message}`);
      return null;
    }
  }
  console.error('[loadState] state.json 不存在');
  return null;
}

/**
 * 启动浏览器（带 Cookie 恢复）
 * 如果 state.json 被外部更新（如 login.js），会自动关闭旧实例并重新启动
 */
async function launchBrowser(headless = true) {
  // 检查是否需要刷新：state.json 被外部进程更新了
  if (browser && browser.isConnected() && isStateFileUpdated()) {
    console.error('[launchBrowser] 检测到 state.json 已更新，重新加载 cookies...');
    await closeBrowser();
  }
  
  if (browser && browser.isConnected()) {
    return { browser, context };
  }

  const state = loadState();
  
  // 记录当前 state.json 的修改时间
  try {
    if (fs.existsSync(STATE_FILE)) {
      lastStateModTime = fs.statSync(STATE_FILE).mtimeMs;
    }
  } catch {
    // ignore
  }

  browser = await chromium.launch(launchOptions({
    headless,
    args: [
      '--no-sandbox',
      '--disable-setuid-sandbox',
      '--disable-blink-features=AutomationControlled',
    ],
  }));

  const contextOptions = {
    userAgent:
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36',
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
    // 赋予剪贴板读写权限，用于从 canvas 渲染的企微文档中提取内容
    permissions: ['clipboard-read', 'clipboard-write'],
  };

  if (state) {
    contextOptions.storageState = state;
  }

  context = await browser.newContext(contextOptions);

  // 注入反检测脚本
  await context.addInitScript(() => {
    Object.defineProperty(navigator, 'webdriver', { get: () => false });
  });

  return { browser, context };
}

/**
 * 打开可视化浏览器，供用户扫码登录
 */
async function openLoginBrowser() {
  // 关闭已有浏览器
  await closeBrowser();

  browser = await chromium.launch(launchOptions({
    headless: false, // 可视化模式
    args: [
      '--no-sandbox',
      '--disable-setuid-sandbox',
      '--disable-blink-features=AutomationControlled',
    ],
  }));

  context = await browser.newContext({
    userAgent:
      'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36',
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
  });

  await context.addInitScript(() => {
    Object.defineProperty(navigator, 'webdriver', { get: () => false });
  });

  return { browser, context };
}

/**
 * 关闭浏览器
 */
async function closeBrowser() {
  if (browser) {
    try {
      await browser.close();
    } catch {
      // ignore
    }
    browser = null;
    context = null;
  }
}

/**
 * 检查是否已登录（通过访问文档页判断）
 * 使用独立浏览器实例，避免与缓存实例冲突
 */
async function checkLoginStatus() {
  // 使用 loadRawState 读取原始 cookie，避免 loadState 的过滤/修改导致过期检查失效
  const rawState = loadRawState();
  if (!rawState || !rawState.cookies || rawState.cookies.length === 0) {
    return { loggedIn: false, reason: '无已保存的登录状态' };
  }

  // 检查关键 cookie 是否存在
  const hasTOK = rawState.cookies.some(c => c.name === 'TOK');
  const hasUid = rawState.cookies.some(c => c.name === 'uid');
  const hasUidKey = rawState.cookies.some(c => c.name === 'uid_key');
  
  if (!hasTOK && !hasUid && !hasUidKey) {
    return { loggedIn: false, reason: '缺少关键认证 Cookie (TOK/uid/uid_key)' };
  }

  // 基于原始 cookie 数据检查过期情况
  const now = Math.floor(Date.now() / 1000);
  const nonSessionCookies = rawState.cookies.filter(c => c.expires > 0);
  if (nonSessionCookies.length > 0) {
    const expiredCount = nonSessionCookies.filter(c => c.expires < now).length;
    if (expiredCount > nonSessionCookies.length / 2) {
      return { loggedIn: false, reason: `大部分 Cookie 已过期（${expiredCount}/${nonSessionCookies.length}），需重新登录` };
    }
  }

  // 使用经过兼容性修复的 state 创建浏览器上下文来在线验证
  const state = loadState();
  let checkBrowser = null;
  try {
    checkBrowser = await chromium.launch(launchOptions({
      headless: true,
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    }));
    
    const contextOptions = {
      userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36',
      viewport: { width: 1440, height: 900 },
      locale: 'zh-CN',
    };
    if (state) {
      contextOptions.storageState = state;
    }
    const ctx = await checkBrowser.newContext(contextOptions);
    const page = await ctx.newPage();

    await page.goto('https://doc.weixin.qq.com/', {
      waitUntil: 'domcontentloaded',
      timeout: 15000,
    });

    await page.waitForTimeout(3000);
    const currentUrl = page.url();

    await ctx.close();
    await checkBrowser.close();
    checkBrowser = null;

    if (isLoginPage(currentUrl)) {
      return { loggedIn: false, reason: 'Cookie 已过期，需重新登录' };
    }

    return { loggedIn: true, reason: '已登录' };
  } catch (err) {
    if (checkBrowser) {
      try { await checkBrowser.close(); } catch { /* ignore */ }
    }
    return { loggedIn: false, reason: `检查失败: ${err.message}` };
  }
}

export {
  launchBrowser,
  openLoginBrowser,
  closeBrowser,
  saveState,
  loadState,
  checkLoginStatus,
  isLoginPage,
  COOKIE_DIR,
  STATE_FILE,
};
