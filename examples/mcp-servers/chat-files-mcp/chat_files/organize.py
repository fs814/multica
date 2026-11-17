"""Categories, folder export, archive verification and CSV reports.

Nothing here deletes or moves chat files: export copies, and verify only
lists which originals are safely archived so the chat history can then be
cleared from inside WeChat / WeCom.
"""

from __future__ import annotations

import csv
import json
import os
import re
import shutil
from pathlib import Path

from .scan import hash_file
from .store import Manifest, home, is_archived, now, write_private

UNSORTED = "Unsorted"
_BAD = re.compile(r'[<>:"|?*\x00-\x1f\\]')


def clean_category(category: str) -> str:
    """'Papers / ../x' -> 'Papers/x'. Categories become folders and Zotero collections."""
    parts = [_BAD.sub("_", p).strip().rstrip(".") for p in category.replace("\\", "/").split("/")]
    parts = [p for p in parts if p and p not in (".", "..")]
    if not parts:
        raise ValueError(f"empty category {category!r}")
    return "/".join(parts)


def set_categories(m: Manifest, assignments: dict[str, str | None]) -> dict:
    """{id: category}; None or '' clears."""
    done, missing = {}, []
    for fid, cat in assignments.items():
        if fid not in m.files:
            missing.append(fid)
            continue
        m.files[fid]["category"] = clean_category(cat) if cat else None
        done[fid] = m.files[fid]["category"]
    m.save()
    return {"updated": done, "unknown_ids": missing}


def load_rules(path: str | None = None) -> dict[str, list[str]]:
    p = Path(path) if path else home() / "categories.json"
    if not p.exists():
        raise FileNotFoundError(f"no rules at {p}; copy categories.example.json there or pass rules")
    return json.loads(p.read_text(encoding="utf-8"))


def auto_categorize(m: Manifest, rules: dict[str, list[str]], overwrite: bool = False, dry_run: bool = True) -> dict:
    """First rule whose keyword (case-insensitive) occurs in the file name wins."""
    compiled = [(clean_category(c), [k.lower() for k in kws]) for c, kws in rules.items()]
    plan: dict[str, str] = {}
    for e in m.select():
        if e.get("category") and not overwrite:
            continue
        name = e["name"].lower()
        for cat, kws in compiled:
            if any(k in name for k in kws):
                plan[e["id"]] = cat
                break
    if not dry_run:
        for fid, cat in plan.items():
            m.files[fid]["category"] = cat
        m.save()
    by_cat: dict[str, list[str]] = {}
    for fid, cat in plan.items():
        by_cat.setdefault(cat, []).append(m.files[fid]["name"])
    return {"dry_run": dry_run, "matched": len(plan), "by_category": by_cat}


def source_path(e: dict) -> str:
    for p in e["paths"]:
        if os.path.exists(p):
            return p
    raise FileNotFoundError(f"{e['id']} {e['name']}: no copy left on disk")


def _free_name(folder: Path, name: str, sha: str) -> tuple[Path, bool]:
    """(target, already_there). Reuses an identical file, else picks 'name (2).ext'."""
    stem, dot, ext = name.rpartition(".")
    if not dot:
        stem, ext = name, ""
    n = 1
    while True:
        target = folder / (name if n == 1 else f"{stem} ({n}){dot}{ext}")
        if not target.exists():
            return target, False
        if target.is_file() and hash_file(str(target))[0] == sha:
            return target, True
        n += 1


def export(m: Manifest, dest: str, entries: list[dict], dry_run: bool = True) -> dict:
    """Copy each entry to DEST/<category or Unsorted>/<name>."""
    root = Path(os.path.expanduser(dest)).resolve()
    copied, skipped, failed = [], [], []
    for e in entries:
        folder = root.joinpath(*(e.get("category") or UNSORTED).split("/"))
        try:
            src = source_path(e)
            target, same = _free_name(folder, e["name"], e["sha256"])
        except OSError as exc:
            failed.append({"id": e["id"], "name": e["name"], "error": str(exc)})
            continue
        row = {"id": e["id"], "name": e["name"], "to": str(target)}
        if same:
            skipped.append(row)
        elif not dry_run:
            try:
                folder.mkdir(parents=True, exist_ok=True)
                part = target.with_name(target.name + ".part")
                shutil.copy2(src, part)
                os.replace(part, target)
            except OSError as exc:
                failed.append({**row, "error": str(exc)})
                continue
            copied.append(row)
        else:
            copied.append(row)
        if not dry_run:
            rec = e.setdefault("archive", {}).setdefault("export", [])
            if str(target) not in rec:
                rec.append(str(target))
    if not dry_run:
        m.save()
    return {"dry_run": dry_run, "dest": str(root), "copy": copied, "already_there": skipped, "failed": failed}


def verify(m: Manifest, zotero=None) -> dict:
    """Check every present document has a verified archive copy.

    Export copies are re-hashed. Zotero records are checked against the
    library (attachment exists with the same md5) when a client is given,
    otherwise trusted as recorded. Writes safe-to-delete.txt and
    not-archived.csv next to the manifest.
    """
    ok, missing = [], []
    for e in m.select():
        proofs = []
        for p in (e.get("archive") or {}).get("export", []):
            if os.path.isfile(p) and hash_file(p)[0] == e["sha256"]:
                proofs.append(f"export:{p}")
        for z in (e.get("archive") or {}).get("zotero", []):
            if zotero is None:
                proofs.append(f"zotero:{z['library']}/{z['key']} (recorded)")
            elif zotero.library_id() == z["library"] and zotero.attachment_md5(z["key"]) == e["md5"]:
                proofs.append(f"zotero:{z['library']}/{z['key']}")
        (ok if proofs else missing).append((e, proofs))

    out = home()
    safe = sorted(p for e, _ in ok for p in e["paths"])
    write_private(out / "safe-to-delete.txt", "".join(p + "\n" for p in safe))
    rows = [[e["id"], e["name"], e["month"], ";".join(e["apps"]), e.get("category") or "", e["paths"][0] if e["paths"] else ""] for e, _ in missing]
    _write_csv(out / "not-archived.csv", ["id", "name", "month", "apps", "category", "path"], rows)
    return {
        "checked_at": now(),
        "archived": len(ok),
        "not_archived": len(missing),
        "not_archived_files": [{"id": e["id"], "name": e["name"], "category": e.get("category")} for e, _ in missing[:100]],
        "safe_paths": len(safe),
        "safe_bytes": sum(e["size"] * len(e["paths"]) for e, _ in ok),
        "safe_to_delete_list": str(out / "safe-to-delete.txt"),
        "not_archived_csv": str(out / "not-archived.csv"),
    }


def _write_csv(path: Path, header: list[str], rows: list[list]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    # utf-8-sig so Excel shows the Chinese file names correctly
    with open(path, "w", newline="", encoding="utf-8-sig") as f:
        w = csv.writer(f)
        w.writerow(header)
        w.writerows(rows)


def report(m: Manifest, path: str, entries: list[dict]) -> dict:
    header = ["id", "name", "ext", "size", "month", "apps", "accounts", "copies", "category", "archived", "present", "path"]
    rows = [
        [e["id"], e["name"], e["ext"], e["size"], e["month"], ";".join(e["apps"]), ";".join(e["accounts"]),
         len(e["paths"]), e.get("category") or "", ";".join(is_archived(e)), e["present"], e["paths"][0] if e["paths"] else ""]
        for e in entries
    ]
    target = Path(os.path.expanduser(path))
    _write_csv(target, header, rows)
    return {"rows": len(rows), "csv": str(target)}
