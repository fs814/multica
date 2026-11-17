"""python -m unittest discover -s tests   (stdlib only; no WeChat, WeCom or Zotero needed)"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
import tempfile
import threading
import unittest
import urllib.parse
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from chat_files import organize, preview, scan, sources  # noqa: E402
from chat_files.store import Manifest  # noqa: E402
from chat_files.zotero import Zotero, import_entries  # noqa: E402


def write(path: Path, data: bytes) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)
    return path


def pptx(path: Path, *slides: str) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(path, "w") as z:
        for i, text in enumerate(slides, 1):
            z.writestr(f"ppt/slides/slide{i}.xml", f"<p:sld><a:p><a:r><a:t>{text}</a:t></a:r></a:p></p:sld>")
    return path


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        os.environ["CHAT_FILES_HOME"] = str(self.tmp / "home")
        self.wx = self.tmp / "WeChat Files/wxid_abc/FileStorage/File"
        self.ww = self.tmp / "WXWork/1688/Cache/File"
        write(self.wx / "2025-03/paper.pdf", b"%PDF-1.4 paper")
        write(self.wx / "2025-03/paper(1).pdf", b"%PDF-1.4 paper")       # WeChat duplicate
        write(self.ww / "2026-07/9f3a/paper.pdf", b"%PDF-1.4 paper")     # same file via WeCom
        pptx(self.ww / "2026-07/77aa/大模型分享.pptx", "Transformer 架构", "RLHF")
        write(self.wx / "2025-04/photo.jpg", b"jpg")
        write(self.wx / "2025-04/~$lock.docx", b"x")
        self.roots = [
            sources.Root("wechat", "wxid_abc", str(self.wx), "test"),
            sources.Root("wecom", "1688", str(self.ww), "test"),
        ]

    def tearDown(self):
        os.environ.pop("CHAT_FILES_HOME", None)
        shutil.rmtree(self.tmp, ignore_errors=True)


class ScanTest(Base):
    def test_scan_merges_duplicates_and_skips_non_documents(self):
        m = Manifest()
        s = scan.scan(m, roots=self.roots)
        self.assertEqual(s["documents"], 2)
        self.assertEqual(s["copies"], 4)
        paper = next(e for e in m.files.values() if e["ext"] == "pdf")
        self.assertEqual(paper["name"], "paper.pdf")
        self.assertEqual(paper["apps"], ["wechat", "wecom"])
        self.assertEqual(paper["month"], "2025-03")
        self.assertEqual(paper["md5"], hashlib.md5(b"%PDF-1.4 paper").hexdigest())

    def test_rescan_marks_removed_files(self):
        m = Manifest()
        scan.scan(m, roots=self.roots)
        for p in (self.ww / "2026-07/77aa/大模型分享.pptx",):
            p.unlink()
        s = scan.scan(Manifest(), roots=self.roots)
        self.assertEqual(s["no_longer_on_disk"], 1)
        self.assertEqual(len(Manifest().select()), 1)
        self.assertEqual(len(Manifest().select(present=None)), 2)

    def test_partial_scan_keeps_other_roots(self):
        m = Manifest()
        scan.scan(m, roots=self.roots)
        scan.scan(Manifest(), roots=self.roots[:1])
        paper = next(e for e in Manifest().files.values() if e["ext"] == "pdf")
        self.assertEqual(len(paper["paths"]), 3)

    def test_account_and_extra_roots(self):
        pattern = str(self.tmp / "WeChat Files/*/FileStorage/File")
        roots = sources.resolve([sources.Source("wechat", pattern)])
        self.assertEqual([(r.app, r.account) for r in roots], [("wechat", "wxid_abc")])
        os.environ["CHAT_FILES_ROOTS"] = f"wecom={self.ww}{os.pathsep}{self.wx}"
        try:
            extra = sources._extra()
        finally:
            del os.environ["CHAT_FILES_ROOTS"]
        self.assertEqual([s.app for s in extra], ["wecom", "other"])


class OrganizeTest(Base):
    def setUp(self):
        super().setUp()
        self.m = Manifest()
        scan.scan(self.m, roots=self.roots)
        self.pdf = next(e["id"] for e in self.m.files.values() if e["ext"] == "pdf")
        self.ppt = next(e["id"] for e in self.m.files.values() if e["ext"] == "pptx")

    def test_preview_pptx(self):
        e = self.m.get(self.ppt)
        text, how = preview.extract(organize.source_path(e), "pptx")
        self.assertEqual(how, "office-xml")
        self.assertIn("Transformer 架构", text)
        self.assertIn("RLHF", text)

    def test_clean_category(self):
        self.assertEqual(organize.clean_category(" Papers / ../LLM: x "), "Papers/LLM_ x")
        with self.assertRaises(ValueError):
            organize.clean_category("../..")

    def test_auto_categorize_dry_run_then_apply(self):
        rules = {"Slides": ["分享"], "Papers": ["paper"]}
        r = organize.auto_categorize(self.m, rules)
        self.assertEqual(r["matched"], 2)
        self.assertIsNone(self.m.get(self.pdf)["category"])
        organize.auto_categorize(self.m, rules, dry_run=False)
        self.assertEqual(Manifest().get(self.ppt)["category"], "Slides")

    def test_export_and_verify(self):
        organize.set_categories(self.m, {self.pdf: "Papers/LLM"})
        dest = self.tmp / "out"
        plan = organize.export(self.m, str(dest), self.m.select())
        self.assertFalse(dest.exists())
        self.assertEqual(len(plan["copy"]), 2)
        organize.export(self.m, str(dest), self.m.select(), dry_run=False)
        self.assertTrue((dest / "Papers/LLM/paper.pdf").is_file())
        self.assertTrue((dest / "Unsorted/大模型分享.pptx").is_file())
        again = organize.export(self.m, str(dest), self.m.select(), dry_run=False)
        self.assertEqual(len(again["already_there"]), 2)
        v = organize.verify(Manifest())
        self.assertEqual((v["archived"], v["not_archived"], v["safe_paths"]), (2, 0, 4))
        (dest / "Papers/LLM/paper.pdf").write_bytes(b"changed")
        v = organize.verify(Manifest())
        self.assertEqual(v["not_archived"], 1)
        safe = Path(v["safe_to_delete_list"]).read_text(encoding="utf-8")
        self.assertNotIn("paper", safe)

    def test_report_csv(self):
        out = self.tmp / "r.csv"
        r = organize.report(self.m, str(out), self.m.select())
        self.assertEqual(r["rows"], 2)
        self.assertIn("大模型分享.pptx", out.read_text(encoding="utf-8-sig"))


class FakeZotero(BaseHTTPRequestHandler):
    """Just enough of the Zotero local API v3 for the import flow."""

    state: dict = {}

    def log_message(self, *a):
        pass

    def _send(self, code, body=b"", ctype="application/json", headers=None):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Zotero-Server-ID", "srv1")
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body if isinstance(body, bytes) else json.dumps(body).encode())

    def _body(self):
        return self.rfile.read(int(self.headers.get("Content-Length") or 0))

    def do_GET(self):
        s, path = self.state, urllib.parse.urlparse(self.path).path
        if path == "/api/":
            return self._send(200, b"ok", "text/plain")
        if path == "/api/users/0/collections":
            cols = [{"key": k, "data": v} for k, v in s["collections"].items()]
            return self._send(200, cols, headers={"Total-Results": str(len(cols))})
        if path.startswith("/api/users/0/items/"):
            item = s["items"].get(path.rsplit("/", 1)[1])
            return self._send(200, {"data": item}) if item else self._send(404, b"", "text/plain")
        self._send(404, b"", "text/plain")

    def do_POST(self):
        s, path, body = self.state, urllib.parse.urlparse(self.path).path, self._body()
        if path == "/api/local/authorize":
            if self.headers.get("Zotero-Server-ID") != "srv1":
                return self._send(428, b"Zotero-Server-ID not provided", "text/plain")
            return self._send(200, {"key": "K", "remember": True})
        if path.startswith("/api/local/uploads/"):
            up = s["uploads"][path.rsplit("/", 1)[1]]
            ctype = self.headers.get("Content-Type", "")
            if ctype.startswith("multipart/form-data"):
                # same parsing as Zotero's _decodeMultipartData
                boundary = "--" + ctype.split("boundary=", 1)[1]
                field = [f for f in body.split(boundary.encode())[1:-1] if b'name="file"' in f][0]
                body = field[field.index(b"\r\n\r\n") + 4:]
                body = body[:-2] if body.endswith(b"\r\n") else body
                s["multipart_uploads"] = s.get("multipart_uploads", 0) + 1
            up["data"] = body
            return self._send(201, b"", "text/plain")
        if self.headers.get("Zotero-API-Key") != "K":
            return self._send(401, b"bad key", "text/plain")
        if self.headers.get("Zotero-Server-ID") != "srv1":
            return self._send(428, b"no server id", "text/plain")
        if path == "/api/users/0/collections":
            key = f"C{len(s['collections']) + 1:07d}"
            obj = json.loads(body)[0]
            s["collections"][key] = {"name": obj["name"], "parentCollection": obj["parentCollection"]}
            return self._send(200, {"success": {"0": key}, "failed": {}})
        if path == "/api/users/0/items":
            key = f"I{len(s['items']) + 1:07d}"
            s["items"][key] = json.loads(body)[0]
            return self._send(200, {"success": {"0": key}, "failed": {}})
        if path.endswith("/file"):
            key = path.split("/")[-2]
            form = dict(urllib.parse.parse_qsl(body.decode()))
            if "upload" in form:
                up = s["uploads"].pop(form["upload"])
                assert hashlib.md5(up["data"]).hexdigest() == up["md5"]
                s["items"][key]["md5"] = up["md5"]
                return self._send(204, b"", "text/plain")
            uk = f"u{len(s['uploads'])}{key}"
            s["uploads"][uk] = {"md5": form["md5"]}
            host = self.headers["Host"]
            return self._send(200, {"url": f"http://{host}/api/local/uploads/{uk}", "uploadKey": uk,
                                    "contentType": "application/octet-stream", "prefix": "", "suffix": ""})
        self._send(404, b"", "text/plain")


class ZoteroTest(Base):
    def setUp(self):
        super().setUp()
        FakeZotero.state = {"collections": {"C0000000": {"name": "Papers", "parentCollection": False}},
                            "items": {}, "uploads": {}}
        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), FakeZotero)
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        os.environ["ZOTERO_LOCAL_URL"] = f"http://127.0.0.1:{self.srv.server_port}"
        self.m = Manifest()
        scan.scan(self.m, roots=self.roots)
        self.pdf = next(e["id"] for e in self.m.files.values() if e["ext"] == "pdf")
        organize.set_categories(self.m, {self.pdf: "Papers/LLM"})

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()
        os.environ.pop("ZOTERO_LOCAL_URL", None)
        super().tearDown()

    def test_import_needs_key_then_works_and_is_idempotent(self):
        z = Zotero("local")
        r = import_entries(self.m, z, self.m.select(), dry_run=False)
        self.assertEqual(len(r["failed"]), 2)
        self.assertIn("zotero-auth", r["failed"][0]["error"])

        z.authorize()
        self.assertEqual(json.loads((self.tmp / "home/zotero.json").read_text())["ZOTERO_LOCAL_API_KEY"], "K")
        z = Zotero("local")  # key now comes from zotero.json
        plan = import_entries(self.m, z, self.m.select())
        self.assertEqual(sorted(p["collection"] for p in plan["plan"]), ["Chat files", "Papers/LLM"])
        r = import_entries(self.m, z, self.m.select(), dry_run=False)
        self.assertEqual(len(r["imported"]), 2, r)
        cols = {c["path"] for c in z.collections()}
        self.assertTrue({"Papers", "Papers/LLM", "Chat files"} <= cols)
        llm = next(c["key"] for c in z.collections() if c["path"] == "Papers/LLM")
        item = next(i for i in FakeZotero.state["items"].values() if i["filename"] == "paper.pdf")
        self.assertEqual(item["collections"], [llm])
        self.assertEqual(sorted(t["tag"] for t in item["tags"]), ["chat-files", "wechat", "wecom"])

        again = import_entries(Manifest(), z, Manifest().select(), dry_run=False)
        self.assertEqual((len(again["imported"]), len(again["skipped_already_imported"])), (0, 2))
        v = organize.verify(Manifest(), z)
        self.assertEqual((v["archived"], v["not_archived"]), (2, 0))


class ZoteroBigFileTest(ZoteroTest):
    test_import_needs_key_then_works_and_is_idempotent = None  # inherited setUp only

    def test_large_files_go_multipart_and_failed_items_are_removed(self):
        from chat_files import zotero as zmod
        write(self.wx / "2025-05/big.pdf", bytes([0, 255, 13, 10, 13, 10, 128, 45, 45, 10]))
        scan.scan(Manifest(), roots=self.roots)
        z = Zotero("local")
        z.authorize()
        old = zmod.MULTIPART_FROM_BYTES
        zmod.MULTIPART_FROM_BYTES = 5
        try:
            big = [e for e in Manifest().select() if e["name"] == "big.pdf"]
            r = import_entries(Manifest(), z, big, dry_run=False)
        finally:
            zmod.MULTIPART_FROM_BYTES = old
        self.assertEqual(len(r["imported"]), 1, r)
        self.assertEqual(FakeZotero.state.get("multipart_uploads"), 1)
        item = next(i for i in FakeZotero.state["items"].values() if i["filename"] == "big.pdf")
        self.assertEqual(item["md5"], hashlib.md5(bytes([0, 255, 13, 10, 13, 10, 128, 45, 45, 10])).hexdigest())


if __name__ == "__main__":
    unittest.main()
