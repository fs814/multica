"""Command line front end: python -m chat_files <command> ...

Same operations as the MCP server, for use without an agent. Commands that
change things (auto, export, zotero-import) only print the plan unless --apply.
"""

from __future__ import annotations

import argparse
import json
import sys

from . import organize, preview, scan, sources
from .store import Manifest, brief
from .zotero import Zotero, ZoteroError, import_entries


def _filters(p: argparse.ArgumentParser) -> None:
    g = p.add_argument_group("selection")
    g.add_argument("--id", action="append", dest="ids", help="file id (repeatable)")
    g.add_argument("--app", choices=["wechat", "wecom", "other"])
    g.add_argument("--ext", help="pdf, docx, pptx ...")
    g.add_argument("--category", help="this category and its sub-categories")
    g.add_argument("--uncategorized", action="store_true")
    g.add_argument("--query", "-q", help="substring of the file name")
    g.add_argument("--since", help="YYYY-MM")
    g.add_argument("--until", help="YYYY-MM")
    a = g.add_mutually_exclusive_group()
    a.add_argument("--archived", dest="archived", action="store_true", default=None)
    a.add_argument("--not-archived", dest="archived", action="store_false")
    g.add_argument("--include-missing", action="store_true", help="include files no longer on disk")


def _select(m: Manifest, a) -> list[dict]:
    return m.select(
        ids=a.ids, app=a.app, ext=a.ext, category=a.category, uncategorized=a.uncategorized,
        query=a.query, since=a.since, until=a.until, archived=a.archived,
        present=None if a.include_missing else True,
    )


def _print(obj) -> None:
    print(json.dumps(obj, ensure_ascii=False, indent=2))


def _size(n: int) -> str:
    for unit in ("B", "K", "M", "G"):
        if n < 1024 or unit == "G":
            return f"{n:.0f}{unit}" if unit == "B" else f"{n:.1f}{unit}"
        n /= 1024


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="chat-files", description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)

    sub.add_parser("sources", help="show the WeChat/WeCom folders found on this machine")
    p = sub.add_parser("scan", help="index documents (hashes them; re-runs are incremental)")
    p.add_argument("--app", action="append", choices=["wechat", "wecom", "other"])
    p.add_argument("--ext", help="comma list; default: documents (pdf, office, caj ...); * for everything")
    p = sub.add_parser("list", help="list indexed documents")
    _filters(p)
    p.add_argument("--limit", type=int, default=0)
    p.add_argument("--json", action="store_true")
    p = sub.add_parser("preview", help="first text of a document")
    p.add_argument("id")
    p.add_argument("--chars", type=int, default=1500)
    p = sub.add_parser("set-category", help="set (or with '' clear) the category of files")
    p.add_argument("category", help="e.g. 'Papers/LLM'; '' clears")
    p.add_argument("ids", nargs="+")
    p = sub.add_parser("auto", help="categorize by keyword rules (categories.json)")
    p.add_argument("--rules", help="rules file; default ~/.chat-files-mcp/categories.json")
    p.add_argument("--overwrite", action="store_true", help="also re-categorize files that have a category")
    p.add_argument("--apply", action="store_true")
    p = sub.add_parser("export", help="copy documents to DEST/<category>/")
    p.add_argument("dest")
    _filters(p)
    p.add_argument("--apply", action="store_true")
    p = sub.add_parser("zotero-status")
    p.add_argument("--backend", choices=["local", "web"])
    p = sub.add_parser("zotero-auth", help="get a local API key (click Allow in Zotero)")
    p = sub.add_parser("zotero-collections")
    p.add_argument("--backend", choices=["local", "web"])
    p = sub.add_parser("zotero-import", help="upload documents into collections named by category")
    _filters(p)
    p.add_argument("--backend", choices=["local", "web"])
    p.add_argument("--collection", default="Chat files", help="collection for uncategorized files")
    p.add_argument("--force", action="store_true", help="import again even if already imported")
    p.add_argument("--apply", action="store_true")
    p = sub.add_parser("verify", help="list originals that are safely archived (safe-to-delete.txt)")
    p.add_argument("--check-zotero", action="store_true", help="confirm each Zotero copy via the API")
    p.add_argument("--backend", choices=["local", "web"])
    p = sub.add_parser("report", help="write a CSV of the selection")
    p.add_argument("csv")
    _filters(p)

    a = ap.parse_args(argv)
    m = Manifest()
    try:
        if a.cmd == "sources":
            _print([r.__dict__ for r in sources.resolve()])
        elif a.cmd == "scan":
            exts = [e.strip() for e in a.ext.split(",")] if a.ext else None
            _print(scan.scan(m, apps=a.app, exts=exts))
        elif a.cmd == "list":
            rows = _select(m, a)
            rows = rows[: a.limit] if a.limit else rows
            if a.json:
                _print([brief(e) for e in rows])
            else:
                for e in rows:
                    mark = "A" if brief(e)["archived"] else " "
                    print(f"{e['id']}  {mark} {e['month']}  {_size(e['size']):>6}  {(e.get('category') or '-'):<20}  {e['name']}")
                print(f"{len(rows)} documents", file=sys.stderr)
        elif a.cmd == "preview":
            e = m.get(a.id)
            text, how = preview.extract(organize.source_path(e), e["ext"], a.chars)
            print(f"# {e['name']}  ({how})\n{text}")
        elif a.cmd == "set-category":
            _print(organize.set_categories(m, {i: a.category for i in a.ids}))
        elif a.cmd == "auto":
            _print(organize.auto_categorize(m, organize.load_rules(a.rules), a.overwrite, dry_run=not a.apply))
        elif a.cmd == "export":
            _print(organize.export(m, a.dest, _select(m, a), dry_run=not a.apply))
        elif a.cmd == "zotero-status":
            _print(Zotero(a.backend).status())
        elif a.cmd == "zotero-auth":
            print("Waiting for you to click Allow / Always Allow in Zotero ...", file=sys.stderr)
            _print(Zotero("local").authorize())
        elif a.cmd == "zotero-collections":
            _print(Zotero(a.backend).collections())
        elif a.cmd == "zotero-import":
            _print(import_entries(m, Zotero(a.backend), _select(m, a), a.collection, dry_run=not a.apply, force=a.force))
        elif a.cmd == "verify":
            _print(organize.verify(m, Zotero(a.backend) if a.check_zotero else None))
        elif a.cmd == "report":
            _print(organize.report(m, a.csv, _select(m, a)))
    except (ZoteroError, KeyError, ValueError, FileNotFoundError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0
