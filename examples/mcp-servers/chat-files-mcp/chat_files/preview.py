"""A short text preview of a document, to help decide its category.

Files received in chats are untrusted, so nothing here runs a full parser:
Office XML is read from the zip with a byte cap per member and scraped with a
regex (no entity expansion, no zip bombs); PDF and legacy formats go through
external converters (pdftotext, macOS textutil) with a timeout when present.
"""

from __future__ import annotations

import html
import re
import shutil
import subprocess
import sys
import zipfile

MEMBER_CAP = 4 << 20  # bytes read from any one zip member

_OFFICE = {
    "docx": (r"word/document\.xml$", r"<w:t[^>]*>([^<]*)</w:t>|</w:p>"),
    "pptx": (r"ppt/slides/slide(\d+)\.xml$", r"<a:t>([^<]*)</a:t>|</a:p>"),
    "ppsx": (r"ppt/slides/slide(\d+)\.xml$", r"<a:t>([^<]*)</a:t>|</a:p>"),
    "xlsx": (r"xl/sharedStrings\.xml$", r"<t[^>]*>([^<]*)</t>"),
    "odt": (r"content\.xml$", r"<text:[ph][^>]*>([^<]*)"),
    "odp": (r"content\.xml$", r"<text:[ph][^>]*>([^<]*)"),
    "ods": (r"content\.xml$", r"<text:p[^>]*>([^<]*)"),
}


def _office(path: str, ext: str, limit: int) -> str:
    member_re, text_re = _OFFICE[ext]
    out: list[str] = []
    with zipfile.ZipFile(path) as zf:
        members = [n for n in zf.namelist() if re.search(member_re, n)]
        # slides in numeric order
        members.sort(key=lambda n: int(m.group(1)) if (m := re.search(r"(\d+)\.xml$", n)) else 0)
        for name in members:
            with zf.open(name) as f:
                xml = f.read(MEMBER_CAP).decode("utf-8", "ignore")
            for m in re.finditer(text_re, xml):
                out.append(html.unescape(m.group(1)) if m.group(1) is not None else "\n")
            out.append("\n")
            if sum(map(len, out)) > limit:
                break
    return "".join(out)


def _run(cmd: list[str]) -> str:
    try:
        r = subprocess.run(cmd, capture_output=True, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        return ""
    return r.stdout.decode("utf-8", "ignore") if r.returncode == 0 else ""


def extract(path: str, ext: str, limit: int = 1500) -> tuple[str, str]:
    """(text, method). Text is whitespace-collapsed and cut to limit chars."""
    text, method = "", "none"
    try:
        if ext in _OFFICE:
            text, method = _office(path, ext, limit), "office-xml"
        elif ext == "pdf" and shutil.which("pdftotext"):
            text, method = _run(["pdftotext", "-l", "3", "-enc", "UTF-8", path, "-"]), "pdftotext"
        elif ext == "pdf":
            try:
                from pypdf import PdfReader  # optional

                reader = PdfReader(path)
                text = "\n".join((p.extract_text() or "") for p in reader.pages[:3])
                method = "pypdf"
            except ImportError:
                method = "none (install poppler's pdftotext or pypdf)"
        elif ext in ("doc", "rtf", "wps") and sys.platform == "darwin":
            text, method = _run(["textutil", "-convert", "txt", "-stdout", path]), "textutil"
        elif ext in ("txt", "csv", "md"):
            with open(path, "rb") as f:
                text, method = f.read(limit * 4).decode("utf-8", "ignore"), "plain"
    except (OSError, zipfile.BadZipFile, ValueError) as exc:
        return "", f"failed: {exc}"
    text = re.sub(r"[ \t\r\f\v]+", " ", text)
    text = re.sub(r"\n\s*\n+", "\n", text).strip()
    return text[:limit], method
