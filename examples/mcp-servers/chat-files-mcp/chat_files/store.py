"""The manifest: every document found, keyed by content hash.

Lives in ``$CHAT_FILES_HOME`` (default ``~/.chat-files-mcp``) as manifest.json.
One entry per distinct content, so the same PDF received in five chats is one
entry with five paths. Entries stay after their files disappear (``present``
false) so archive records survive the chat history being cleared.
"""

from __future__ import annotations

import json
import os
from datetime import datetime, timezone
from pathlib import Path

VERSION = 1


def home() -> Path:
    return Path(os.path.expanduser(os.environ.get("CHAT_FILES_HOME", "~/.chat-files-mcp")))


def now() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def write_private(path: Path, text: str) -> None:
    """Atomic write, owner-only permissions (the manifest lists private file names)."""
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    part = path.with_name(path.name + ".part")
    fd = os.open(part, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(text)
    os.replace(part, path)


class Manifest:
    def __init__(self, path: Path | None = None):
        self.path = path or home() / "manifest.json"
        self.data = {"version": VERSION, "files": {}, "hashcache": {}, "scanned_at": None}
        if self.path.exists():
            self.data.update(json.loads(self.path.read_text(encoding="utf-8")))

    @property
    def files(self) -> dict[str, dict]:
        return self.data["files"]

    @property
    def hashcache(self) -> dict[str, list]:
        return self.data["hashcache"]

    def save(self) -> None:
        write_private(self.path, json.dumps(self.data, ensure_ascii=False, indent=1))

    def get(self, file_id: str) -> dict:
        try:
            return self.files[file_id]
        except KeyError:
            raise KeyError(f"unknown file id {file_id!r}; run scan, then list") from None

    def select(
        self,
        ids: list[str] | None = None,
        app: str | None = None,
        ext: str | None = None,
        category: str | None = None,
        uncategorized: bool = False,
        query: str | None = None,
        since: str | None = None,
        until: str | None = None,
        archived: bool | None = None,
        present: bool | None = True,
    ) -> list[dict]:
        """Filter entries. since/until compare against the YYYY-MM month."""
        out = []
        wanted = set(ids) if ids else None
        q = query.lower() if query else None
        for e in self.files.values():
            if wanted is not None and e["id"] not in wanted:
                continue
            if present is not None and wanted is None and e["present"] != present:
                continue
            if app and app not in e["apps"]:
                continue
            if ext and e["ext"] != ext.lower().lstrip("."):
                continue
            if category is not None and (e.get("category") or "") != category and not (e.get("category") or "").startswith(category + "/"):
                continue
            if uncategorized and e.get("category"):
                continue
            if q and q not in e["name"].lower() and q not in (e.get("category") or "").lower():
                continue
            if since and e["month"] < since:
                continue
            if until and e["month"] > until:
                continue
            if archived is not None and bool(is_archived(e)) != archived:
                continue
            out.append(e)
        out.sort(key=lambda e: (e["month"], e["name"]), reverse=True)
        return out


def is_archived(entry: dict) -> list[str]:
    """Where this content is archived (empty when nowhere)."""
    a = entry.get("archive") or {}
    where = [f"export:{p}" for p in a.get("export", [])]
    where += [f"zotero:{z['library']}/{z['key']}" for z in a.get("zotero", [])]
    return where


def brief(e: dict) -> dict:
    """The compact view returned by list tools."""
    return {
        "id": e["id"],
        "name": e["name"],
        "ext": e["ext"],
        "size": e["size"],
        "month": e["month"],
        "apps": e["apps"],
        "copies": len(e["paths"]),
        "category": e.get("category"),
        "archived": is_archived(e),
        "present": e["present"],
    }
