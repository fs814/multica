"""Where WeChat (微信) and WeCom (企业微信) keep received files on each OS.

Every source is a glob whose first ``*`` is the account (wxid / profile id).
Files are found by walking each matched folder recursively, so the month
(``YYYY-MM``) and per-message sub-folders below it don't need to be known.

Extra roots come from ``CHAT_FILES_ROOTS``: entries separated by ``os.pathsep``,
each ``app=path`` or a bare path (app ``other``). The path may contain globs.
"""

from __future__ import annotations

import glob
import os
import sys
from dataclasses import dataclass
from pathlib import Path

HOME = Path.home()


@dataclass(frozen=True)
class Source:
    app: str          # wechat | wecom | other
    pattern: str      # absolute glob
    note: str = ""


def _mac() -> list[Source]:
    c = HOME / "Library/Containers"
    return [
        Source("wecom", str(c / "com.tencent.WeWorkMac/Data/Documents/Profiles/*/Caches/Files"), "WeCom for Mac"),
        Source("wechat", str(c / "com.tencent.xinWeChat/Data/Documents/xwechat_files/*/msg/file"), "WeChat 4.x for Mac"),
        Source(
            "wechat",
            str(c / "com.tencent.xinWeChat/Data/Library/Application Support/com.tencent.xinWeChat/*/*/Message/MessageTemp/*/File"),
            "WeChat 3.x for Mac",
        ),
    ]


def _wechat3_windows_roots() -> list[Path]:
    """WeChat 3.x stores a custom 'file location' in 3ebffe94.ini (or MyDocument: for the default)."""
    roots = [HOME / "Documents"]
    appdata = os.environ.get("APPDATA")
    if appdata:
        ini = Path(appdata) / "Tencent/WeChat/All Users/config/3ebffe94.ini"
        try:
            value = ini.read_text(encoding="utf-8", errors="ignore").strip()
        except OSError:
            value = ""
        if value and value != "MyDocument:":
            roots.insert(0, Path(value))
    return roots


def _windows() -> list[Source]:
    out = []
    for root in _wechat3_windows_roots():
        out.append(Source("wechat", str(root / "WeChat Files/*/FileStorage/File"), "WeChat 3.x for Windows"))
    docs = HOME / "Documents"
    out += [
        Source("wechat", str(docs / "xwechat_files/*/msg/file"), "WeChat 4.x for Windows"),
        Source("wecom", str(docs / "WXWork/*/Cache/File"), "WeCom for Windows"),
    ]
    return out


def _linux() -> list[Source]:
    return [
        Source("wechat", str(HOME / "Documents/xwechat_files/*/msg/file"), "WeChat 4.x for Linux"),
        Source("wechat", str(HOME / "xwechat_files/*/msg/file"), "WeChat 4.x for Linux"),
    ]


def _extra() -> list[Source]:
    out = []
    for raw in filter(None, os.environ.get("CHAT_FILES_ROOTS", "").split(os.pathsep)):
        app, sep, path = raw.partition("=")
        if not sep or len(app) <= 1:  # a bare path (and C:\ on Windows) has no app=
            app, path = "other", raw
        out.append(Source(app.strip().lower(), str(Path(os.path.expanduser(path.strip()))), "CHAT_FILES_ROOTS"))
    return out


def default_sources() -> list[Source]:
    if sys.platform == "darwin":
        base = _mac()
    elif sys.platform == "win32":
        base = _windows()
    else:
        base = _linux()
    return base + _extra()


@dataclass(frozen=True)
class Root:
    app: str
    account: str
    path: str
    note: str


def _account(pattern: str, path: str) -> str:
    """The path component that the pattern's first '*' matched."""
    pp, mp = Path(pattern).parts, Path(path).parts
    for a, b in zip(pp, mp):
        if "*" in a:
            return b
    return ""


def resolve(sources: list[Source] | None = None, apps: list[str] | None = None) -> list[Root]:
    roots: list[Root] = []
    seen: set[str] = set()
    for s in sources or default_sources():
        if apps and s.app not in apps:
            continue
        for path in sorted(glob.glob(s.pattern)):
            if os.path.isdir(path) and path not in seen:
                seen.add(path)
                roots.append(Root(s.app, _account(s.pattern, path), path, s.note))
    return roots
