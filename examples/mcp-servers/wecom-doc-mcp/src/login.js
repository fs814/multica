#!/usr/bin/env node
/**
 * 企业微信文档登录脚本
 * 
 * 使用方式：node src/login.js
 * 
 * 会打开一个可视化浏览器窗口，用户扫码登录后自动保存 Cookie。
 */
import { openLoginBrowser, saveState, closeBrowser, isLoginPage } from './browser.js';

async function login() {
  console.log('🚀 正在启动浏览器...');
  console.log('');

  const { context } = await openLoginBrowser();
  const page = await context.newPage();

  console.log('📄 正在打开企业微信文档登录页...');
  await page.goto('https://doc.weixin.qq.com/', {
    waitUntil: 'domcontentloaded',
    timeout: 30000,
  });

  console.log('');
  console.log('='.repeat(50));
  console.log('👋 请在浏览器中完成登录（扫码或账号密码）');
  console.log('   登录成功后会自动保存状态');
  console.log('='.repeat(50));
  console.log('');

  // 轮询检测登录状态
  let loggedIn = false;
  const maxWait = 180; // 最多等待180秒（给用户更充裕的时间）
  let waited = 0;

  while (!loggedIn && waited < maxWait) {
    await page.waitForTimeout(2000);
    waited += 2;

    const url = page.url();
    // 登录成功后会跳转到文档主页（不再是登录页）
    if (
      !isLoginPage(url) &&
      (url.includes('doc.weixin.qq.com') || url.includes('docs.qq.com'))
    ) {
      // 严格检查：必须同时存在 TOK 和 uid 两个关键 cookie
      try {
        const cookies = await context.cookies();
        const hasTOK = cookies.some(c => c.name === 'TOK');
        const hasUid = cookies.some(c => c.name === 'uid');
        const hasUidKey = cookies.some(c => c.name === 'uid_key');
        const hasWedriveSid = cookies.some(c => c.name === 'wedrive_sid');
        
        console.log(`\n  [调试] cookies数量: ${cookies.length}, TOK=${hasTOK}, uid=${hasUid}, uid_key=${hasUidKey}, wedrive_sid=${hasWedriveSid}`);
        
        // 要求至少有 TOK + uid 或 uid_key，且 cookie 总数足够
        if (hasTOK && (hasUid || hasUidKey) && cookies.length >= 10) {
          loggedIn = true;
        }
      } catch {
        // continue waiting
      }
    }

    if (!loggedIn) {
      try { process.stdout.write(`\r⏳ 等待登录中... ${waited}s/${maxWait}s`); } catch { /* stdout 可能不可用 */ }
    }
  }

  if (loggedIn) {
    console.log('\n');
    console.log('✅ 登录成功！正在等待所有 Cookie 稳定...');
    
    // 多等 5 秒确保所有 Cookie（包括异步设置的）都完全就位
    await page.waitForTimeout(5000);
    
    // 再访问一次文档首页，触发可能的额外 cookie 设置
    try {
      await page.goto('https://doc.weixin.qq.com/', {
        waitUntil: 'domcontentloaded',
        timeout: 10000,
      });
      await page.waitForTimeout(3000);
    } catch {
      // ignore navigation errors
    }
    
    // 最终保存
    const saved = await saveState();
    if (saved) {
      // 验证保存的内容
      const finalCookies = await context.cookies();
      console.log(`💾 登录状态已保存 (${finalCookies.length} 个 Cookie)`);
      console.log('  关键 Cookie:');
      finalCookies.filter(c => ['TOK', 'uid', 'uid_key', 'wedrive_sid', 'wedrive_skey'].includes(c.name))
        .forEach(c => console.log(`    ${c.name}: expires=${c.expires}, domain=${c.domain}`));
      console.log('');
      console.log('🎉 现在可以在 CodeBuddy 中使用企微文档 MCP 服务了！');
    } else {
      console.log('❌ 保存状态失败');
    }

    // 登录成功后等 3 秒再关闭浏览器
    console.log('\n🔒 3秒后关闭浏览器...');
    await page.waitForTimeout(3000);
  } else {
    console.log('\n');
    console.log('⏰ 等待超时，请重新运行此脚本登录');
  }

  await closeBrowser();

  process.exit(0);
}

login().catch((err) => {
  console.error('❌ 登录出错:', err.message);
  closeBrowser().then(() => process.exit(1));
});
