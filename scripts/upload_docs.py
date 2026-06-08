#!/usr/bin/env python3
"""Upload markdown docs under docs/ to the autopilot docs service.

Stdlib-only (no pip installs). Walks docs/, parses YAML frontmatter, keeps
files that declare a `slug`, errors on duplicate slugs, and POSTs the docs in
batches of 50 to:

    {DOCS_ENDPOINT}/api/v1/docs/repositories/{url-encoded repo}/documents

with `Authorization: Bearer $DOCS_UPLOAD_TOKEN` and body:

    {"documents": [{"docId": slug, "content": body}, ...]}

Config via env:
    DOCS_ENDPOINT        default https://autopilot.rxlab.app
    DOCS_REPOSITORY_ID   e.g. owner/repo   (default: sirily11/debate-bot)
    DOCS_UPLOAD_TOKEN    bearer token (required unless --dry-run)

Usage:
    python scripts/upload_docs.py [--dry-run]
"""
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

DEFAULT_ENDPOINT = "https://autopilot.rxlab.app"
DEFAULT_REPOSITORY_ID = "sirily11/debate-bot"
BATCH_SIZE = 50


def parse_frontmatter(text: str) -> tuple[dict, str]:
    """Return (frontmatter_dict, body). Minimal YAML: flat `key: value` pairs.

    Only the leading `---\n ... \n---` block is treated as frontmatter. The
    body is everything after the closing fence.
    """
    if not text.startswith("---"):
        return {}, text
    lines = text.splitlines()
    # lines[0] == "---"
    end = None
    for i in range(1, len(lines)):
        if lines[i].strip() == "---":
            end = i
            break
    if end is None:
        return {}, text
    meta: dict[str, str] = {}
    for line in lines[1:end]:
        line = line.rstrip()
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if ":" not in line:
            continue
        key, _, value = line.partition(":")
        value = value.strip()
        # strip matching surrounding quotes
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
            value = value[1:-1]
        meta[key.strip()] = value
    body = "\n".join(lines[end + 1 :]).lstrip("\n")
    return meta, body


def collect_docs(docs_dir: Path) -> list[dict]:
    """Walk docs_dir, return [{slug, content, path}] for files with a slug."""
    docs: list[dict] = []
    slugs: dict[str, Path] = {}
    for path in sorted(docs_dir.rglob("*.md")):
        text = path.read_text(encoding="utf-8")
        meta, body = parse_frontmatter(text)
        slug = meta.get("slug")
        if not slug:
            print(f"  skip (no slug): {path}", file=sys.stderr)
            continue
        if slug in slugs:
            raise SystemExit(
                f"duplicate slug {slug!r}:\n  {slugs[slug]}\n  {path}"
            )
        slugs[slug] = path
        docs.append({"slug": slug, "content": body, "path": str(path)})
    return docs


def batched(items: list, size: int):
    for i in range(0, len(items), size):
        yield items[i : i + size]


def post_batch(endpoint: str, repo: str, token: str, batch: list[dict]) -> None:
    url = (
        f"{endpoint.rstrip('/')}/api/v1/docs/repositories/"
        f"{urllib.parse.quote(repo, safe='')}/documents"
    )
    payload = {
        "documents": [{"docId": d["slug"], "content": d["content"]} for d in batch]
    }
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(req) as resp:
            body = resp.read().decode("utf-8", "replace")
            print(f"  -> {resp.status} {body[:200]}")
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "replace")
        raise SystemExit(f"upload failed: HTTP {e.code} {detail[:500]}")
    except urllib.error.URLError as e:
        raise SystemExit(f"upload failed: {e.reason}")


def main() -> int:
    dry_run = "--dry-run" in sys.argv[1:]
    endpoint = os.environ.get("DOCS_ENDPOINT", DEFAULT_ENDPOINT)
    repo = os.environ.get("DOCS_REPOSITORY_ID", DEFAULT_REPOSITORY_ID)
    token = os.environ.get("DOCS_UPLOAD_TOKEN", "")

    docs_dir = Path(__file__).resolve().parent.parent / "docs"
    if not docs_dir.is_dir():
        raise SystemExit(f"no docs/ directory at {docs_dir}")

    docs = collect_docs(docs_dir)
    print(f"found {len(docs)} doc(s) with a slug under {docs_dir}")
    for d in docs:
        print(f"  - {d['slug']}  ({d['path']})")

    batches = list(batched(docs, BATCH_SIZE))
    print(f"would POST {len(batches)} batch(es) of up to {BATCH_SIZE} to {endpoint}")

    if dry_run:
        print("dry-run: no network calls made.")
        return 0

    if not token:
        raise SystemExit("DOCS_UPLOAD_TOKEN is not set (required without --dry-run)")
    if not docs:
        print("nothing to upload.")
        return 0

    for i, batch in enumerate(batches, 1):
        print(f"uploading batch {i}/{len(batches)} ({len(batch)} doc(s)) for {repo}…")
        post_batch(endpoint, repo, token, batch)
    print("done.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
