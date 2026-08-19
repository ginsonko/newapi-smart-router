#!/usr/bin/env python3
"""Bind a built release manifest to the exact public repository commit."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path


RELEASE_NAME = "newapi-smart-router-v0.3.0-alpha.1"


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-root", required=True, type=Path)
    parser.add_argument("--repository-commit", required=True)
    args = parser.parse_args()

    root = args.release_root.resolve(strict=True)
    marker = root / ".smart-router-release-root"
    commit = args.repository_commit.lower()
    if not marker.is_file() or marker.read_text(encoding="utf-8").strip() != RELEASE_NAME:
        raise RuntimeError("release root marker is missing or unexpected")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("repository commit must be a full 40-character SHA-1")

    artifacts = root / "artifacts"
    manifest_path = artifacts / "RELEASE-MANIFEST.json"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    repository = manifest.setdefault("repository", {})
    if repository.get("url") != "https://github.com/ginsonko/newapi-smart-router":
        raise RuntimeError("unexpected public repository origin")
    repository["commit"] = commit
    manifest["status"] = "public_alpha_validation_pending"
    completed = manifest.setdefault("completed_gates", [])
    if "public_repository_commit" not in completed:
        completed.append("public_repository_commit")
    manifest_path.write_text(
        json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + "\n",
        encoding="utf-8", newline="\n",
    )

    sums = []
    for path in sorted(item for item in artifacts.iterdir() if item.is_file() and item.name != "SHA256SUMS"):
        sums.append(f"{sha256_file(path)}  {path.name}")
    (artifacts / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8", newline="\n")
    print(json.dumps({"status": "BOUND", "repository_commit": commit}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
