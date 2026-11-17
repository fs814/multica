"""Zotero client for the local desktop API and the zotero.org web API.

Both speak Zotero API v3, so one client covers both:

* local (default): Zotero 7.1+/10 desktop on http://127.0.0.1:23119/api, user 0.
  Needs Settings → Advanced → "Allow other applications on this computer to
  communicate with Zotero". Writes need a local key from /api/local/authorize,
  which pops an Allow / Always Allow dialog in Zotero (``authorize()``), and
  the Zotero-Server-ID header.
* web: https://api.zotero.org with ZOTERO_API_KEY plus ZOTERO_USER_ID (or
  ZOTERO_GROUP_ID). Files then count against your zotero.org storage quota.

Settings (environment, else ~/.chat-files-mcp/zotero.json):
  ZOTERO_BACKEND        local | web                   (default local)
  ZOTERO_LOCAL_URL      http://127.0.0.1:23119
  ZOTERO_LOCAL_API_KEY  key from authorize()          (saved by authorize)
  ZOTERO_API_KEY, ZOTERO_USER_ID, ZOTERO_GROUP_ID     (web)
"""

from __future__ import annotations

import json
import mimetypes
import os
import time
import urllib.error
import urllib.parse
import urllib.request

from .store import Manifest, home, now, write_private

APP_NAME = "chat-files-mcp"
EXTRA_TYPES = {
    "caj": "application/caj", "kdh": "application/caj", "nh": "application/caj",
    "wps": "application/vnd.ms-works", "et": "application/vnd.ms-excel", "dps": "application/vnd.ms-powerpoint",
    "key": "application/x-iwork-keynote-sffkey", "pages": "application/x-iwork-pages-sffpages",
    "numbers": "application/x-iwork-numbers-sffnumbers",
}


# Zotero's local upload receiver turns a raw body into a JS array (one element
# per byte), which fails past ~256 MiB; bigger files go as multipart/form-data,
# which it reads as a string (engine limit near 1 GiB). zotero.org has no such limits.
MULTIPART_FROM_BYTES = 128 << 20
LOCAL_MAX_BYTES = 900 << 20


class ZoteroError(RuntimeError):
    pass


def content_type(name: str) -> str:
    ext = name.rsplit(".", 1)[-1].lower()
    return EXTRA_TYPES.get(ext) or mimetypes.guess_type(name)[0] or "application/octet-stream"


class Zotero:
    def __init__(self, backend: str | None = None):
        self.cfg_path = home() / "zotero.json"
        saved = json.loads(self.cfg_path.read_text(encoding="utf-8")) if self.cfg_path.exists() else {}
        env = lambda k: os.environ.get(k) or saved.get(k)  # noqa: E731
        self.backend = (backend or env("ZOTERO_BACKEND") or "local").lower()
        self.saved = saved
        self.server_id: str | None = None
        if self.backend == "local":
            self.base = (env("ZOTERO_LOCAL_URL") or "http://127.0.0.1:23119").rstrip("/") + "/api"
            self.key = env("ZOTERO_LOCAL_API_KEY")
            self.lib = "users/0"
        elif self.backend == "web":
            self.base = "https://api.zotero.org"
            self.key = env("ZOTERO_API_KEY")
            group, user = env("ZOTERO_GROUP_ID"), env("ZOTERO_USER_ID")
            self.lib = f"groups/{group}" if group else f"users/{user}" if user else ""
        else:
            raise ZoteroError(f"ZOTERO_BACKEND must be local or web, not {self.backend!r}")

    # -- plumbing ---------------------------------------------------------

    def library_id(self) -> str:
        return f"{self.backend}:{self.lib}"

    def _request(self, method: str, url: str, body: bytes | None = None, headers: dict | None = None, write: bool = False):
        h = {"Zotero-API-Version": "3", **(headers or {})}
        if self.key and url.startswith(self.base):
            h["Zotero-API-Key"] = self.key
        if write and self.backend == "local":
            h["Zotero-Server-ID"] = self._server_id()
        req = urllib.request.Request(url, data=body, method=method, headers=h)
        try:
            with urllib.request.urlopen(req, timeout=120) as r:
                return r.status, r.headers, r.read()
        except urllib.error.HTTPError as e:
            with e:
                return e.code, e.headers, e.read()
        except urllib.error.URLError as e:
            hint = " (is Zotero running with the local API enabled?)" if self.backend == "local" else ""
            raise ZoteroError(f"{method} {url}: {e.reason}{hint}") from None

    def _json(self, method: str, path: str, payload=None, ok=(200,), write: bool = False, headers: dict | None = None):
        body = json.dumps(payload).encode() if payload is not None else None
        h = {"Content-Type": "application/json", **(headers or {})} if body is not None else dict(headers or {})
        status, hdrs, data = self._request(method, f"{self.base}/{path}", body, h, write=write)
        if status not in ok:
            self._fail(method, path, status, data)
        return (json.loads(data) if data.strip() else None), hdrs

    def _fail(self, method: str, path: str, status: int, data: bytes):
        msg = data.decode("utf-8", "ignore")[:300]
        if "not enabled" in msg and self.backend == "local":
            msg += (" -- in Zotero: Settings → Advanced → tick 'Allow other applications on this"
                    " computer to communicate with Zotero'")
        elif status in (401, 403) and self.backend == "local":
            msg += " -- run zotero-auth (chat_files_zotero_authorize) and allow it in Zotero"
        raise ZoteroError(f"{method} {path}: HTTP {status} {msg}")

    def _server_id(self) -> str:
        if not self.server_id:
            status, hdrs, data = self._request("GET", f"{self.base}/")
            if status != 200:
                self._fail("GET", "/", status, data)
            self.server_id = hdrs.get("Zotero-Server-ID")
            if not self.server_id:
                raise ZoteroError("local Zotero sent no Zotero-Server-ID; Zotero 7.1 or newer is needed for writes")
        return self.server_id

    # -- api ----------------------------------------------------------------

    def status(self) -> dict:
        out = {"backend": self.backend, "base": self.base, "library": self.lib, "has_key": bool(self.key)}
        if self.backend == "web":
            if not self.key or not self.lib:
                out["error"] = "set ZOTERO_API_KEY and ZOTERO_USER_ID (or ZOTERO_GROUP_ID)"
                return out
            info, _ = self._json("GET", "keys/current")
            out["key_access"] = info.get("access")
        else:
            out["server_id"] = self._server_id()
        out["collections"] = len(self.collections())
        return out

    def authorize(self) -> dict:
        """Local only: Zotero shows Allow / Always Allow / Deny. Blocks until answered."""
        if self.backend != "local":
            raise ZoteroError("authorize is for the local backend; the web backend uses ZOTERO_API_KEY")
        status, _, data = self._request(
            "POST", f"{self.base}/local/authorize",
            json.dumps({"appName": APP_NAME}).encode(),
            {"Content-Type": "application/json", "Zotero-Server-ID": self._server_id()},
        )
        if status != 200:
            self._fail("POST", "local/authorize", status, data)
        got = json.loads(data)
        self.key = got["key"]
        if got.get("remember"):
            self.saved["ZOTERO_LOCAL_API_KEY"] = self.key
            write_private(self.cfg_path, json.dumps(self.saved, indent=1))
        return {"authorized": True, "remembered": bool(got.get("remember"))}

    def collections(self) -> list[dict]:
        """Every collection, with its full path ('Parent/Child')."""
        raw, start = [], 0
        while True:
            page, hdrs = self._json("GET", f"{self.lib}/collections?limit=100&start={start}")
            raw += page
            total = int(hdrs.get("Total-Results") or len(raw))
            if not page or len(raw) >= total:
                break
            start = len(raw)
        by_key = {c["key"]: c["data"] for c in raw}

        def path(key: str) -> str:
            parts, seen = [], set()
            while key and key in by_key and key not in seen:
                seen.add(key)
                parts.append(by_key[key]["name"])
                key = by_key[key].get("parentCollection") or ""
            return "/".join(reversed(parts))

        return sorted(({"key": k, "path": path(k)} for k in by_key), key=lambda c: c["path"])

    def ensure_collection(self, path: str, known: dict[str, str]) -> str:
        """Key of collection PATH ('A/B'), creating missing levels. KNOWN caches path -> key."""
        parent, sofar = None, ""
        for name in path.split("/"):
            sofar = f"{sofar}/{name}" if sofar else name
            if sofar not in known:
                created, _ = self._json(
                    "POST", f"{self.lib}/collections",
                    [{"name": name, "parentCollection": parent or False}], write=True,
                )
                if created.get("failed"):
                    raise ZoteroError(f"creating collection {sofar}: {created['failed']}")
                known[sofar] = created["success"]["0"]
            parent = known[sofar]
        return parent

    def create_attachment(self, name: str, collections: list[str], tags: list[str]) -> str:
        item = {
            "itemType": "attachment", "linkMode": "imported_file", "title": name,
            "filename": name, "contentType": content_type(name), "charset": "",
            "collections": collections, "tags": [{"tag": t} for t in tags],
        }
        created, _ = self._json("POST", f"{self.lib}/items", [item], write=True)
        if created.get("failed"):
            raise ZoteroError(f"creating item {name}: {created['failed']}")
        return created["success"]["0"]

    def upload(self, key: str, path: str, name: str, md5: str) -> str:
        """Authorize, send and register the file. Returns 'uploaded' or 'exists'."""
        st = os.stat(path)
        local = self.backend == "local"
        if local and st.st_size > LOCAL_MAX_BYTES:
            raise ZoteroError(f"too large for Zotero's local API ({st.st_size >> 20} MB, limit {LOCAL_MAX_BYTES >> 20} MB); drag it into Zotero instead")
        multipart = local and st.st_size >= MULTIPART_FROM_BYTES
        fields = {
            "md5": md5, "filename": name, "filesize": st.st_size,
            "mtime": int(st.st_mtime * 1000), "contentType": content_type(name),
        }
        if multipart:
            fields["params"] = 1
        form = urllib.parse.urlencode(fields).encode()
        url = f"{self.base}/{self.lib}/items/{key}/file"
        hdr = {"Content-Type": "application/x-www-form-urlencoded", "If-None-Match": "*"}
        status, _, data = self._request("POST", url, form, hdr, write=True)
        if status != 200:
            self._fail("POST", f"items/{key}/file", status, data)
        auth = json.loads(data)
        if auth.get("exists"):
            return "exists"
        with open(path, "rb") as f:
            content = f.read()
        if multipart:
            boundary = "----chatfiles" + os.urandom(16).hex()
            payload = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"upload\"\r\n"
                       "Content-Type: application/octet-stream\r\n\r\n").encode() + content + f"\r\n--{boundary}--\r\n".encode()
            ctype = f"multipart/form-data; boundary={boundary}"
        else:
            payload = auth["prefix"].encode("latin-1") + content + auth["suffix"].encode("latin-1")
            ctype = auth["contentType"]
        status, _, data = self._request("POST", auth["url"], payload, {"Content-Type": ctype})
        if status not in (200, 201, 204):
            self._fail("POST", "upload", status, data)
        reg = urllib.parse.urlencode({"upload": auth["uploadKey"]}).encode()
        status, _, data = self._request("POST", url, reg, hdr, write=True)
        if status != 204:
            self._fail("POST", f"items/{key}/file (register)", status, data)
        return "uploaded"

    def delete_item(self, key: str) -> None:
        """Erase an item (an attachment whose file never arrived)."""
        url = f"{self.base}/{self.lib}/items/{key}"
        status, hdrs, data = self._request("GET", url)
        if status == 404:
            return
        version = hdrs.get("Last-Modified-Version") or "0"
        status, _, data = self._request("DELETE", url, None, {"If-Unmodified-Since-Version": version}, write=True)
        if status != 204:
            self._fail("DELETE", f"items/{key}", status, data)

    def attachment_md5(self, key: str) -> str | None:
        status, _, data = self._request("GET", f"{self.base}/{self.lib}/items/{key}")
        if status == 404:
            return None
        if status != 200:
            self._fail("GET", f"items/{key}", status, data)
        return json.loads(data)["data"].get("md5")


def import_entries(m: Manifest, z: Zotero, entries: list[dict], default_collection: str = "Chat files",
                   dry_run: bool = True, force: bool = False) -> dict:
    """Upload each entry as a standalone attachment into the collection named by its category."""
    lib = z.library_id()
    plan, skipped = [], []
    for e in entries:
        done = [r for r in (e.get("archive") or {}).get("zotero", []) if r["library"] == lib]
        if done and not force:
            skipped.append({"id": e["id"], "name": e["name"], "key": done[0]["key"]})
            continue
        coll = e.get("category") or default_collection
        plan.append((e, coll))
    result = {
        "dry_run": dry_run, "library": lib, "skipped_already_imported": skipped,
        "plan": [{"id": e["id"], "name": e["name"], "collection": c} for e, c in plan],
    }
    if dry_run:
        return result

    known = {c["path"]: c["key"] for c in z.collections()}
    imported, failed = [], []
    for e, coll in plan:
        try:
            src = next((p for p in e["paths"] if os.path.exists(p)), None)
            if not src:
                raise ZoteroError("no copy left on disk")
            ckey = z.ensure_collection(coll, known)
            if z.backend == "local" and os.path.getsize(src) > LOCAL_MAX_BYTES:
                raise ZoteroError(f"too large for Zotero's local API (limit {LOCAL_MAX_BYTES >> 20} MB); drag it into Zotero instead")
            key = z.create_attachment(e["name"], [ckey], ["chat-files", *e["apps"]])
            try:
                how = z.upload(key, src, e["name"], e["md5"])
                if z.attachment_md5(key) != e["md5"]:
                    raise ZoteroError(f"item {key} created but its md5 doesn't match after upload")
            except (ZoteroError, OSError):
                try:
                    z.delete_item(key)  # don't leave an empty attachment behind
                except (ZoteroError, OSError):
                    pass
                raise
        except (ZoteroError, OSError) as exc:
            failed.append({"id": e["id"], "name": e["name"], "error": str(exc)})
            continue
        e.setdefault("archive", {}).setdefault("zotero", []).append(
            {"library": lib, "key": key, "collection": coll, "at": now()})
        m.save()  # after each file, so an interrupted run never re-uploads
        imported.append({"id": e["id"], "name": e["name"], "key": key, "collection": coll, "upload": how})
        time.sleep(0.05 if z.backend == "local" else 0.5)
    result.update(imported=imported, failed=failed)
    del result["plan"]
    return result
