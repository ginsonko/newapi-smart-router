#!/usr/bin/env python3
"""Synchronize the public Smart Router core from a New API source tree."""

from __future__ import annotations

import argparse
import hashlib
import shutil
from pathlib import Path


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def sync_directory(source: Path, target: Path, expected: set[str]) -> None:
    target.mkdir(parents=True, exist_ok=True)
    extras = {path.name for path in target.glob("*.go")} - expected
    if extras:
        raise RuntimeError(f"refusing to prune unexpected public files: {sorted(extras)}")
    for name in sorted(expected):
        shutil.copyfile(source / name, target / name)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--repository", default=Path(__file__).resolve().parents[1], type=Path)
    args = parser.parse_args()

    source = args.source.resolve() / "pkg" / "smartrouter"
    repository = args.repository.resolve()
    files = sorted(source.glob("*.go"))
    if not files:
        raise RuntimeError(f"no Smart Router Go files found under {source}")
    names = {path.name for path in files}
    if "planner.go" not in names or "planner_test.go" not in names:
        raise RuntimeError("source does not look like the Smart Router core")

    core = repository / "core" / "smartrouter"
    parts = repository / "parts" / "reference" / "go" / "smartrouter"
    sync_directory(source, core, names)
    sync_directory(source, parts, names)

    mismatches: list[str] = []
    for name in sorted(names):
        source_hash = sha256(source / name)
        for target in (core / name, parts / name):
            if sha256(target) != source_hash:
                mismatches.append(str(target))
    if mismatches:
        raise RuntimeError(f"core synchronization mismatch: {mismatches}")

    print(f"SMART_ROUTER_CORE_SYNC=PASS files={len(names)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
