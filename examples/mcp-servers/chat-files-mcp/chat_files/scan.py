"""Walk the chat-file roots and update the manifest."""

from __future__ import annotations

import hashlib
import os
import re
from collections import Counter
from datetime import datetime
from pathlib import Path

from .sources import Root, resolve
from .store import Manifest, now

# Documents worth keeping in a reference manager. "*" in exts means every file.
DOC_EXTS = (
    "pdf caj kdh nh epub djvu "
    "ppt pptx pps ppsx key odp dps "
    "doc docx rtf odt pages wps "
    "xls xlsx ods numbers et csv"
).split()

_MONTH = re.compile(r"^(19|20)\d\d-(0[1-9]|1[0-2])$")
_DUP = re.compile(r"\(\d+\)(\.[^.]+)?$")  # name(1).pdf, the copies WeChat makes


def ext_of(name: str) -> str:
    return name.rsplit(".", 1)[1].lower() if "." in name.strip(".") else ""


def hash_file(path: str) -> tuple[str, str]:
    sha, md5 = hashlib.sha256(), hashlib.md5()
    with open(path, "rb") as f:
        while chunk := f.read(1 << 20):
            sha.update(chunk)
            md5.update(chunk)
    return sha.hexdigest(), md5.hexdigest()


def _month(root: str, path: str, mtime: float) -> str:
    for part in Path(os.path.relpath(path, root)).parts[:-1]:
        if _MONTH.match(part):
            return part
    return datetime.fromtimestamp(mtime).strftime("%Y-%m")


def _under(path: str, roots: list[str]) -> bool:
    return any(path == r or path.startswith(r.rstrip(os.sep) + os.sep) for r in roots)


def scan(
    manifest: Manifest,
    apps: list[str] | None = None,
    exts: list[str] | None = None,
    roots: list[Root] | None = None,
) -> dict:
    """Rescan; returns a summary. Paths under roots not scanned this time are kept."""
    roots = roots if roots is not None else resolve(apps=apps)
    wanted = {e.lower().lstrip(".") for e in (exts or DOC_EXTS)}
    every = "*" in wanted
    cache = manifest.hashcache
    found: dict[str, dict] = {}  # sha256 -> fresh info
    errors: list[str] = []

    for root in roots:
        for dirpath, dirnames, filenames in os.walk(root.path):
            dirnames[:] = [d for d in dirnames if not d.startswith(".")]
            for name in filenames:
                if name.startswith((".", "~$")):
                    continue
                ext = ext_of(name)
                if not every and ext not in wanted:
                    continue
                path = os.path.join(dirpath, name)
                try:
                    st = os.stat(path)
                    hit = cache.get(path)
                    if hit and hit[0] == st.st_size and hit[1] == st.st_mtime_ns:
                        sha, md5 = hit[2], hit[3]
                    else:
                        sha, md5 = hash_file(path)
                        cache[path] = [st.st_size, st.st_mtime_ns, sha, md5]
                except OSError as exc:
                    errors.append(f"{path}: {exc.strerror or exc}")
                    continue
                info = found.setdefault(
                    sha,
                    {"md5": md5, "size": st.st_size, "paths": [], "apps": set(), "accounts": set(), "months": [], "mtime": st.st_mtime},
                )
                info["paths"].append(path)
                info["apps"].add(root.app)
                info["accounts"].add(f"{root.app}:{root.account}")
                info["months"].append(_month(root.path, path, st.st_mtime))
                info["mtime"] = min(info["mtime"], st.st_mtime)

    scanned = [r.path for r in roots]
    files = manifest.files
    added = 0
    for sha, info in found.items():
        fid = sha[:12]
        e = files.get(fid)
        if e is None:
            added += 1
            e = files[fid] = {
                "id": fid,
                "sha256": sha,
                "md5": info["md5"],
                "size": info["size"],
                "paths": [],
                "apps": [],
                "accounts": [],
                "first_seen": now(),
                "category": None,
                "archive": {},
            }
        e["paths"] = sorted(set(p for p in e["paths"] if not _under(p, scanned)) | set(info["paths"]))
        e["apps"] = sorted(set(e["apps"]) | info["apps"])
        e["accounts"] = sorted(set(e["accounts"]) | info["accounts"])
        e["month"] = min(info["months"])
        e["mtime"] = datetime.fromtimestamp(info["mtime"]).isoformat(timespec="seconds")
        names = [os.path.basename(p) for p in e["paths"]]
        e["name"] = min(names, key=lambda n: (bool(_DUP.search(n)), len(n), n))
        e["ext"] = ext_of(e["name"])

    gone = 0
    for e in files.values():
        if e["sha256"] in found:
            e["present"] = True
            continue
        kept = [p for p in e["paths"] if not _under(p, scanned)]
        if len(kept) != len(e["paths"]):
            e["paths"] = kept
        present = bool(kept)
        if e.get("present", True) and not present:
            gone += 1
        e["present"] = present

    for path in list(cache):
        if _under(path, scanned) and not os.path.exists(path):
            del cache[path]

    manifest.data["scanned_at"] = now()
    manifest.save()
    current = [e for e in files.values() if e["present"]]
    return {
        "roots": [{"app": r.app, "account": r.account, "path": r.path} for r in roots],
        "documents": len(current),
        "copies": sum(len(e["paths"]) for e in current),
        "bytes": sum(e["size"] for e in current),
        "new": added,
        "no_longer_on_disk": gone,
        "by_app": dict(Counter(a for e in current for a in e["apps"])),
        "by_ext": dict(Counter(e["ext"] for e in current).most_common()),
        "by_year": dict(sorted(Counter(e["month"][:4] for e in current).items())),
        "uncategorized": sum(1 for e in current if not e.get("category")),
        "errors": errors[:20],
        "manifest": str(manifest.path),
    }
