"""Browser management for Playwright-based automation."""

from __future__ import annotations

import os
from contextlib import contextmanager
from typing import Generator

from loguru import logger
from playwright.sync_api import sync_playwright, Browser, BrowserContext, Page

from zhihu.cookies import load_cookies, save_cookies


ZHIHU_BASE_URL = "https://www.zhihu.com"


@contextmanager
def create_browser(headless: bool = True) -> Generator[tuple[Browser, BrowserContext, Page], None, None]:
    """Create a browser instance with cookies loaded.

    Yields:
        (browser, context, page) tuple
    """
    proxy = os.environ.get("ZHIHU_PROXY")

    # ZHIHU_MCP_BROWSER_CHANNEL (Multica fork addition).
    #
    # Upstream only uses Playwright's own Chromium, which requires
    # `playwright install chromium` to download and unzip ~170MB. On machines
    # running endpoint-security software such as 腾讯电脑管家 that extraction is
    # silently killed partway through (consistently right after
    # chrome-win64\D3DCompiler_47.dll) while the installer still exits 0, so
    # the only symptom is a later
    #   "Executable doesn't exist at ...\chromium-<rev>\chrome.exe"
    #
    # Setting ZHIHU_MCP_BROWSER_CHANNEL=chrome|msedge drives the Chrome/Edge
    # already installed on the machine and skips that download entirely.
    # Unset keeps upstream behaviour.
    channel = (os.environ.get("ZHIHU_MCP_BROWSER_CHANNEL") or "").strip()

    with sync_playwright() as p:
        launch_args = [
            "--disable-blink-features=AutomationControlled",
            "--no-sandbox",
        ]
        launch_kwargs: dict = {
            "headless": headless,
            "args": launch_args,
        }
        if channel:
            launch_kwargs["channel"] = channel
        browser = p.chromium.launch(**launch_kwargs)

        context_options: dict = {
            "viewport": {"width": 1440, "height": 900},
            "user_agent": (
                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/131.0.0.0 Safari/537.36"
            ),
        }

        if proxy:
            context_options["proxy"] = {"server": proxy}
            logger.info("Using proxy for browser")

        context = browser.new_context(**context_options)

        # Load cookies if available
        cookies = load_cookies()
        if cookies:
            context.add_cookies(cookies)
            logger.info(f"Cookies loaded into browser context: {len(cookies)} cookies")
            # Log cookie domains for debugging
            domains = set(c.get("domain", "") for c in cookies)
            logger.info(f"Cookie domains: {domains}")
        else:
            logger.warning("No cookies found — please run login.py first")

        # Anti-detection: override webdriver property
        context.add_init_script(
            """
            Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
            Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3]});
            Object.defineProperty(navigator, 'languages', {get: () => ['zh-CN', 'zh', 'en']});
            window.chrome = { runtime: {} };
            """
        )

        page = context.new_page()
        page.set_default_timeout(30000)

        try:
            yield browser, context, page
        finally:
            page.close()
            context.close()
            browser.close()
            logger.debug("Browser closed")


def save_browser_cookies(context: BrowserContext) -> None:
    """Save cookies from browser context to disk."""
    cookies = context.cookies()
    save_cookies(cookies)
