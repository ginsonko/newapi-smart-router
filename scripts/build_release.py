#!/usr/bin/env python3
"""Build a reproducible public NewAPI Smart Router Alpha release candidate."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import zipfile
from pathlib import Path, PurePosixPath

import yaml


RELEASE_VERSION = "v0.4.0-alpha.1"
RELEASE_NAME = f"newapi-smart-router-{RELEASE_VERSION}"
MARKER_NAME = ".smart-router-release-root"
FIXED_ZIP_TIME = (2026, 10, 6, 0, 0, 0)
DENIED_COMPONENTS = {
    ".git", ".tmp", ".cache", "node_modules", "coverage",
    "backup", "backups", "vendor",
}
DENIED_SUFFIXES = {
    ".env", ".key", ".pem", ".p12", ".pfx", ".db", ".sqlite", ".sqlite3",
    ".log", ".tar", ".gz", ".tgz", ".zip", ".7z", ".rar", ".exe", ".dll",
    ".so", ".dylib",
}


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def canonical_json(value: object) -> bytes:
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(canonical_json(value))


def redact_local_paths(value: object, mappings: list[tuple[str, str]]) -> object:
    if isinstance(value, dict):
        return {key: redact_local_paths(item, mappings) for key, item in value.items()}
    if isinstance(value, list):
        return [redact_local_paths(item, mappings) for item in value]
    if isinstance(value, str):
        rendered = value
        for raw, replacement in sorted(mappings, key=lambda item: len(item[0]), reverse=True):
            if raw:
                rendered = rendered.replace(raw, replacement).replace(raw.replace("\\", "/"), replacement)
        return rendered
    return value


def run(command: list[str], cwd: Path, env: dict[str, str] | None = None, timeout: int = 900) -> dict:
    started = time.monotonic()
    completed = subprocess.run(
        command, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        text=True, encoding="utf-8", errors="replace", timeout=timeout, check=False,
    )
    result = {
        "command": command,
        "cwd": str(cwd),
        "exit_code": completed.returncode,
        "elapsed_ms": round((time.monotonic() - started) * 1000),
        "output": completed.stdout[-20000:],
    }
    if completed.returncode != 0:
        raise RuntimeError(json.dumps(result, ensure_ascii=False, indent=2))
    return result


def git_lines(reference: Path, *args: str) -> list[str]:
    completed = subprocess.run(
        ["git", *args], cwd=reference, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, encoding="utf-8", errors="replace", check=False,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {completed.stderr}")
    return [line for line in completed.stdout.splitlines() if line]


def load_overlay_manifest(path: Path) -> dict:
    manifest = json.loads(path.read_text(encoding="utf-8"))
    if manifest.get("schema_version") != "full-reference-overlay-v1":
        raise RuntimeError("unexpected Full reference overlay schema")
    if manifest.get("release") != RELEASE_VERSION:
        raise RuntimeError("Full reference overlay release does not match builder")
    base = manifest.get("base")
    if not isinstance(base, dict) or not base.get("archive_root") or not base.get("sha256"):
        raise RuntimeError("Full reference overlay base is incomplete")
    if not isinstance(manifest.get("include"), list) or not manifest["include"]:
        raise RuntimeError("Full reference overlay include list is empty")
    restore = manifest.get("restore_from_reference_head")
    if not isinstance(restore, dict) or not re.fullmatch(r"[0-9a-f]{40}", str(restore.get("commit", ""))):
        raise RuntimeError("Full reference restore commit must be an exact lowercase SHA-1")
    if not isinstance(restore.get("files"), list) or not restore["files"]:
        raise RuntimeError("Full reference restore file list is empty")
    translations = manifest.get("translation_overlay")
    if not isinstance(translations, dict) or not isinstance(translations.get("files"), list):
        raise RuntimeError("Full translation overlay is incomplete")
    return manifest


def manifest_relative(value: object, label: str) -> Path:
    pure = PurePosixPath(str(value))
    if pure.is_absolute() or not pure.parts or ".." in pure.parts or "." in pure.parts:
        raise RuntimeError(f"unsafe {label} path: {value}")
    relative = Path(*pure.parts)
    if is_denied_relative(relative):
        raise RuntimeError(f"denied {label} path: {pure.as_posix()}")
    return relative


def git_blob(reference: Path, commit: str, relative: Path) -> bytes:
    spec = f"{commit}:{relative.as_posix()}"
    completed = subprocess.run(
        ["git", "show", spec], cwd=reference, stdout=subprocess.PIPE,
        stderr=subprocess.PIPE, check=False,
    )
    if completed.returncode != 0:
        message = completed.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"cannot read fixed reference blob {spec}: {message}")
    return completed.stdout


def select_reference_head_restores(reference: Path, manifest: dict) -> list[dict]:
    config = manifest["restore_from_reference_head"]
    commit = str(config["commit"])
    completed = subprocess.run(
        ["git", "cat-file", "-e", f"{commit}^{{commit}}"], cwd=reference,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
    )
    if completed.returncode != 0:
        raise RuntimeError(f"fixed reference commit is unavailable: {commit}")
    selected = []
    seen = set()
    for value in config["files"]:
        relative = manifest_relative(value, "reference restore")
        key = relative.as_posix()
        if key in seen:
            raise RuntimeError(f"duplicate reference restore path: {key}")
        seen.add(key)
        data = git_blob(reference, commit, relative)
        selected.append({
            "relative": relative,
            "data": data,
            "commit": commit,
            "size": len(data),
            "sha256": hashlib.sha256(data).hexdigest(),
        })
    return selected


def reference_restore_entries(items: list[dict]) -> list[dict]:
    return [
        {
            "path": f"@reference-head/{item['commit']}/{item['relative'].as_posix()}",
            "kind": "fixed_public_reference_blob",
            "size": item["size"],
            "sha256": item["sha256"],
        }
        for item in items
    ]


def apply_reference_head_restores(target: Path, items: list[dict]) -> None:
    for item in items:
        destination = target / item["relative"]
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(item["data"])


def select_translation_sources(reference: Path, manifest: dict) -> list[tuple[Path, Path, str]]:
    selected = []
    seen = set()
    for value in manifest["translation_overlay"]["files"]:
        relative = manifest_relative(value, "translation source")
        if relative.suffix.lower() != ".json" or relative.as_posix() in seen:
            raise RuntimeError(f"invalid or duplicate translation source: {relative.as_posix()}")
        source = reference / relative
        if not source.is_file():
            raise RuntimeError(f"translation source is missing: {relative.as_posix()}")
        seen.add(relative.as_posix())
        selected.append((source, relative, "r52_translation_source"))
    return selected


def merge_referenced_translations(
    target: Path,
    translation_sources: list[tuple[Path, Path, str]],
    selected: list[tuple[Path, Path, str]],
    manifest: dict,
) -> dict:
    source_text = "\n".join(
        source.read_text(encoding="utf-8")
        for source, relative, _kind in selected
        if relative.suffix.lower() in {".ts", ".tsx", ".js", ".jsx"}
        and relative.as_posix().startswith("web/default/src/")
    )
    explicit = {str(value) for value in manifest["translation_overlay"].get("explicit_keys", [])}
    denied = [str(value).lower() for value in manifest["translation_overlay"].get("deny_contains", [])]
    receipts = []
    union_keys = set()
    for source, relative, _kind in translation_sources:
        destination = target / relative
        if not destination.is_file():
            raise RuntimeError(f"base Full translation is missing: {relative.as_posix()}")
        source_document = json.loads(source.read_text(encoding="utf-8"))
        target_document = json.loads(destination.read_text(encoding="utf-8"))
        source_values = source_document.get("translation")
        target_values = target_document.get("translation")
        if not isinstance(source_values, dict) or not isinstance(target_values, dict):
            raise RuntimeError(f"translation object is missing: {relative.as_posix()}")
        referenced = set(explicit)
        for key in source_values:
            double_quoted = json.dumps(key, ensure_ascii=False)
            single_quoted = "'" + key.replace("\\", "\\\\").replace("'", "\\'") + "'"
            if double_quoted in source_text or single_quoted in source_text:
                referenced.add(key)
        selected_keys = sorted(key for key in referenced if key in source_values)
        for key in selected_keys:
            rendered = (key + "\n" + str(source_values[key])).lower()
            if any(term in rendered for term in denied):
                raise RuntimeError(f"denied private translation selected in {relative.as_posix()}: {key}")
            target_values[key] = source_values[key]
        destination.write_bytes(canonical_json(target_document))
        union_keys.update(selected_keys)
        receipts.append({
            "path": relative.as_posix(),
            "source_sha256": sha256_file(source),
            "assembled_sha256": sha256_file(destination),
            "selected_key_count": len(selected_keys),
            "selected_keys_sha256": hashlib.sha256("\n".join(selected_keys).encode("utf-8")).hexdigest(),
        })
    return {
        "mode": "referenced_keys_only",
        "selected_key_count": len(union_keys),
        "selected_keys_sha256": hashlib.sha256("\n".join(sorted(union_keys)).encode("utf-8")).hexdigest(),
        "files": receipts,
    }


def extract_verified_base(archive_path: Path, target: Path, manifest: dict) -> dict:
    expected = manifest["base"]["sha256"].lower()
    actual = sha256_file(archive_path)
    if actual != expected:
        raise RuntimeError(f"base Full archive SHA-256 mismatch: {actual}")
    archive_root = manifest["base"]["archive_root"]
    extracted = 0
    with zipfile.ZipFile(archive_path) as archive:
        if archive.testzip() is not None:
            raise RuntimeError("base Full archive failed CRC validation")
        for info in archive.infolist():
            pure = PurePosixPath(info.filename)
            if info.is_dir():
                continue
            if pure.is_absolute() or ".." in pure.parts or len(pure.parts) < 2 or pure.parts[0] != archive_root:
                raise RuntimeError(f"unsafe or unexpected base Full archive path: {info.filename}")
            relative = Path(*pure.parts[1:])
            if is_denied_relative(relative):
                raise RuntimeError(f"denied file found in public v0.2 Full base: {relative.as_posix()}")
            destination = target / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(archive.read(info))
            extracted += 1
    if extracted == 0:
        raise RuntimeError("base Full archive contained no files")
    return {
        "release": manifest["base"].get("release"),
        "asset": manifest["base"].get("asset"),
        "sha256": actual,
        "files": extracted,
    }


def select_overlay_files(reference: Path, manifest: dict) -> list[tuple[Path, Path, str]]:
    selected: dict[str, tuple[Path, Path, str]] = {}
    denied = [str(value).lower() for value in manifest.get("deny_path_contains", []) if str(value).strip()]
    for pattern in manifest["include"]:
        for source in reference.glob(str(pattern)):
            if not source.is_file():
                continue
            relative = source.relative_to(reference)
            normalized = "/" + relative.as_posix().lower()
            if is_denied_relative(relative) or any(value in normalized for value in denied):
                raise RuntimeError(f"overlay include matched a denied path: {relative.as_posix()}")
            selected[relative.as_posix()] = (source, relative, "r52_overlay")
    missing = [
        value for value in manifest.get("required", [])
        if value not in selected
    ]
    if missing:
        raise RuntimeError(f"required Full overlay files are missing: {missing}")
    if not selected:
        raise RuntimeError("Full overlay selected no files")
    return [selected[key] for key in sorted(selected)]


def apply_overlay(target: Path, items: list[tuple[Path, Path, str]]) -> None:
    for source, relative, _kind in items:
        destination = target / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)


PUBLIC_TEXT_SUFFIXES = {
    ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".json", ".yaml", ".yml",
    ".md", ".html", ".css", ".scss", ".txt", ".toml", ".mod", ".sum",
}


def sanitize_full_source(target: Path, private_terms: list[str]) -> list[dict]:
    """Apply caller-supplied private-term substitutions without recording terms."""
    changed = []
    terms = [term.strip() for term in private_terms if term.strip()]
    for path in sorted(item for item in target.rglob("*") if item.is_file()):
        if path.suffix.lower() not in PUBLIC_TEXT_SUFFIXES:
            continue
        relative = path.relative_to(target)
        original = path.read_text(encoding="utf-8")
        updated = original
        replacement_count = 0
        for term in terms:
            updated, count = re.subn(re.escape(term), "ExampleProvider", updated, flags=re.IGNORECASE)
            replacement_count += count
        if updated != original:
            path.write_text(updated, encoding="utf-8", newline="\n")
            changed.append({
                "path": relative.as_posix(),
                "source_sha256": hashlib.sha256(original.encode("utf-8")).hexdigest(),
                "assembled_sha256": sha256_file(path),
                "replacement_count": replacement_count,
            })
    return changed


def create_dependency_link(link: Path, target: Path) -> None:
    if not target.is_dir():
        raise RuntimeError(f"frontend dependency directory is missing: {target}")
    if link.exists() or link.is_symlink():
        raise RuntimeError(f"refusing to replace existing frontend dependency path: {link}")
    link.parent.mkdir(parents=True, exist_ok=True)
    if os.name == "nt":
        completed = subprocess.run(
            ["cmd", "/c", "mklink", "/J", str(link), str(target)],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace", check=False,
        )
        if completed.returncode != 0:
            raise RuntimeError(f"cannot create temporary frontend junction: {completed.stdout}")
    else:
        link.symlink_to(target, target_is_directory=True)


def remove_dependency_link(link: Path) -> None:
    if not link.exists() and not link.is_symlink():
        return
    if os.name == "nt":
        completed = subprocess.run(
            ["cmd", "/c", "rmdir", str(link)],
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace", check=False,
        )
        if completed.returncode != 0:
            raise RuntimeError(f"cannot remove temporary frontend junction: {completed.stdout}")
    else:
        link.unlink()


def build_frontends(full_source: Path, reference: Path, private_terms: list[str]) -> list[dict]:
    """Build both public themes in the assembled tree, never in the reference tree."""
    full_web = full_source / "web"
    reference_web = reference / "web"
    links = [(full_web / "node_modules", reference_web / "node_modules")]
    for theme in ("default", "classic"):
        links.append((full_web / theme / "node_modules", reference_web / theme / "node_modules"))
    for link, target in links:
        create_dependency_link(link, target)
    receipts = []
    try:
        for theme in ("default", "classic"):
            theme_root = full_web / theme
            dist = theme_root / "dist"
            if dist.exists():
                shutil.rmtree(dist)
            rsbuild = full_web / "node_modules" / ".bin" / ("rsbuild.cmd" if os.name == "nt" else "rsbuild")
            command = [str(rsbuild), "build"]
            if os.name == "nt":
                command = ["cmd", "/d", "/s", "/c", str(rsbuild), "build"]
            receipts.append(run(
                command,
                theme_root,
                {**os.environ, "CI": "true", "NODE_ENV": "production"},
                timeout=1800,
            ))
            if not (dist / "index.html").is_file():
                raise RuntimeError(f"frontend build did not produce {dist / 'index.html'}")
            files = [path for path in dist.rglob("*") if path.is_file()]
            marker_hits = []
            for path in files:
                data = path.read_bytes()
                text = data.decode("utf-8", errors="ignore")
                for marker in private_terms:
                    if marker.lower() in text.lower():
                        marker_hits.append({"path": path.relative_to(full_source).as_posix(), "marker": marker})
            if marker_hits:
                raise RuntimeError(f"private branding marker found in {theme} bundle: {marker_hits[:5]}")
    finally:
        for link, _target in reversed(links):
            remove_dependency_link(link)
    return receipts


def select_tree_files(root: Path) -> list[tuple[Path, Path, str]]:
    selected = []
    for source in root.rglob("*"):
        if not source.is_file():
            continue
        relative = source.relative_to(root)
        if is_denied_relative(relative):
            raise RuntimeError(f"denied file found in assembled Full source: {relative.as_posix()}")
        selected.append((source, relative, "assembled_full"))
    return sorted(selected, key=lambda item: item[1].as_posix())


def is_denied_relative(relative: Path) -> bool:
    lowered = {part.lower() for part in relative.parts}
    if lowered & DENIED_COMPONENTS:
        return True
    name = relative.name.lower()
    if name == ".env" or name.startswith(".env."):
        return True
    return relative.suffix.lower() in DENIED_SUFFIXES


def snapshot_entries(items: list[tuple[Path, Path, str]]) -> list[dict]:
    return [
        {"path": relative.as_posix(), "kind": kind, "size": source.stat().st_size, "sha256": sha256_file(source)}
        for source, relative, kind in items
    ]


def snapshot_id(entries: list[dict]) -> str:
    digest = hashlib.sha256()
    for entry in entries:
        digest.update(entry["path"].encode("utf-8"))
        digest.update(b"\0")
        digest.update(entry["sha256"].encode("ascii"))
        digest.update(b"\n")
    return "snapshot_sha256_" + digest.hexdigest()


def ensure_safe_generated_root(path: Path, releases_root: Path) -> None:
    resolved = path.resolve()
    allowed = releases_root.resolve()
    if resolved == allowed or allowed not in resolved.parents:
        raise RuntimeError(f"refusing output outside releases root: {resolved}")
    if path.exists():
        marker = path / MARKER_NAME
        if not marker.is_file() or marker.read_text(encoding="utf-8").strip() != RELEASE_NAME:
            raise RuntimeError(f"refusing to replace unmarked output: {path}")

        def clear_readonly_and_retry(function, value, _error) -> None:
            os.chmod(value, stat.S_IWRITE)
            function(value)

        shutil.rmtree(path, onerror=clear_readonly_and_retry)


def copy_tree(source: Path, target: Path, ignore_names: set[str] | None = None) -> None:
    ignored = ignore_names or set()
    for path in source.rglob("*"):
        relative = path.relative_to(source)
        if any(part in ignored for part in relative.parts):
            continue
        destination = target / relative
        if path.is_dir():
            destination.mkdir(parents=True, exist_ok=True)
        elif path.is_file():
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, destination)


def update_parts_manifest(path: Path, source_snapshot_id: str) -> None:
    text = path.read_text(encoding="utf-8")
    manifest = yaml.safe_load(text)
    if manifest.get("manifest_version") != "4.0.0-alpha.1":
        raise RuntimeError("unexpected Agent Parts manifest version")
    reference = manifest.get("reference")
    if not isinstance(reference, dict):
        raise RuntimeError("Agent Parts reference contract is missing")
    placeholder = "bound_by_v0_4_release_source_receipt"
    if reference.get("baseline") != placeholder:
        raise RuntimeError("Agent Parts source baseline placeholder is missing or already bound")
    needle = f'  baseline: "{placeholder}"'
    if text.count(needle) != 1:
        raise RuntimeError("Agent Parts baseline is not represented exactly once")
    updated = text.replace(needle, f'  baseline: "{source_snapshot_id}"')
    rebound = yaml.safe_load(updated)
    if rebound.get("reference", {}).get("baseline") != source_snapshot_id:
        raise RuntimeError("Agent Parts source baseline binding failed")
    path.write_text(updated, encoding="utf-8", newline="\n")


def update_capability_matrix(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    text = text.replace('  - "public repository is not rebuilt from clean upstream history"\n', "")
    text = text.replace(
        '  - "release signing SBOM checksum and secret scanning are incomplete"',
        '  - "release signature and image-layer SBOM are incomplete"',
    )
    path.write_text(text, encoding="utf-8", newline="\n")


def assemble_repository(template: Path, reference: Path, target: Path, source_snapshot_id: str) -> None:
    copy_tree(template, target, {".git", "__pycache__", ".pytest_cache"})
    shutil.copy2(reference / "LICENSE", target / "LICENSE")
    shutil.copy2(reference / "NOTICE", target / "UPSTREAM-NOTICE")
    third_party = reference / "THIRD-PARTY-LICENSES.md"
    if third_party.is_file():
        shutil.copy2(third_party, target / third_party.name)
    core_target = target / "core" / "smartrouter"
    core_target.mkdir(parents=True, exist_ok=True)
    for source in sorted((reference / "pkg" / "smartrouter").glob("*.go")):
        shutil.copy2(source, core_target / source.name)
    shutil.copy2(reference / "go.sum", target / "core" / "go.sum")
    parts_go = target / "parts" / "reference" / "go" / "smartrouter"
    parts_go.mkdir(parents=True, exist_ok=True)
    for source in sorted(core_target.glob("*.go")):
        shutil.copy2(source, parts_go / source.name)
    planner_vector = target / "core" / "testdata" / "planner-v1.json"
    vector_target = target / "parts" / "conformance" / "golden-vectors" / "planner-v1.json"
    vector_target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(planner_vector, vector_target)
    update_parts_manifest(target / "parts" / "manifest" / "PARTS-MANIFEST.yaml", source_snapshot_id)
    update_capability_matrix(target / "parts" / "manifest" / "capability-matrix.yaml")


def attach_full_release_docs(target: Path, source_receipt: dict, repository: Path) -> None:
    release_docs = target / "smart-router-release"
    release_docs.mkdir(parents=True, exist_ok=True)
    for relative in (
        "README.md", "RELEASE-STATUS.md", "SECURITY.md", "CHANGELOG.md",
        "NOTICE", "COMMERCIAL-LICENSE.md",
    ):
        shutil.copy2(repository / relative, release_docs / relative)
    for directory in ("parts", "bridge", "integration", "core", "docs", "compatibility"):
        copy_tree(repository / directory, release_docs / directory, {"__pycache__", ".pytest_cache"})
    (target / "SMART-ROUTER-RELEASE.md").write_text(
        "# NewAPI Smart Router Full Alpha\n\n"
        "This source snapshot derives from QuantumNous New API and includes the local Smart Router reference integration. "
        "It is an unsigned Alpha for isolated evaluation. Read `smart-router-release/RELEASE-STATUS.md` before use.\n",
        encoding="utf-8", newline="\n",
    )
    write_json(target / "SMART-ROUTER-SOURCE-SNAPSHOT.json", source_receipt)


def frontend_source_digest(full_source: Path, relative: Path) -> str:
    digest = hashlib.sha256()
    root = full_source / relative
    for source in sorted(path for path in root.rglob("*") if path.is_file() and not any(part in {"dist", "node_modules", ".tanstack"} for part in path.relative_to(root).parts) and not path.name.endswith(".tsbuildinfo")):
        path = source.relative_to(full_source).as_posix()
        digest.update(path.encode("utf-8"))
        digest.update(b"\0")
        digest.update(sha256_file(source).encode("ascii"))
        digest.update(b"\n")
    return digest.hexdigest()


def verify_frontend_builds(full_source: Path) -> dict:
    receipt = {"mode": "isolated_full_tree_production_build", "themes": {}}
    for relative in (Path("web/default"), Path("web/classic")):
        built = full_source / relative / "dist"
        if not (built / "index.html").is_file():
            raise RuntimeError(f"required isolated frontend build is missing: {built}")
        source_digest = frontend_source_digest(full_source, relative)
        receipt["themes"][relative.name] = {
            "source_sha256": source_digest,
            "bundle_files": len([path for path in built.rglob("*") if path.is_file()]),
            "bundle_sha256": snapshot_id(snapshot_entries([
                (path, path.relative_to(built), "frontend_bundle")
                for path in sorted(item for item in built.rglob("*") if item.is_file())
            ])),
        }
    return receipt


def command_receipts(repository: Path, full_source: Path, go_binary: Path, run_full_tests: bool, build_linux: bool, binaries: Path) -> list[dict]:
    go = str(go_binary.resolve())
    env = os.environ.copy()
    env["GOTOOLCHAIN"] = "local"
    env["GOSUMDB"] = "off"
    env["GOPROXY"] = "off"
    env["PYTHONDONTWRITEBYTECODE"] = "1"
    receipts = []
    # Do not run `go mod tidy` in the offline local builder: tidy resolves
    # transitive test-only modules that are not needed for a locked build and
    # may contact a checksum proxy. `go mod verify` checks the existing lock.
    receipts.append(run([go, "mod", "verify"], repository / "core", env))
    receipts.append(run([go, "test", "./...", "-count=1"], repository / "core", env))
    receipts.append(run([go, "run", "./cmd/conformance", "-vectors", "testdata/planner-v1.json"], repository / "core", env))
    receipts.append(run([go, "mod", "verify"], repository / "bridge" / "newapi", env))
    receipts.append(run([go, "test", "./...", "-count=1"], repository / "bridge" / "newapi", env))
    if run_full_tests:
        receipts.append(run([go, "test", "./...", "-count=1", "-p=2"], full_source, env, timeout=1800))
    if build_linux:
        binaries.mkdir(parents=True, exist_ok=True)
        linux_env = env.copy()
        linux_env.update({"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"})
        output = binaries / "new-api-smart-router-linux-amd64"
        receipts.append(run([go, "build", "-trimpath", "-buildvcs=false", "-o", str(output), "."], full_source, linux_env, timeout=1800))
    return receipts


def remove_generated_caches(root: Path) -> None:
    for path in sorted(root.rglob("*"), key=lambda item: len(item.parts), reverse=True):
        if path.is_file() and path.suffix.lower() == ".pyc":
            path.unlink()
        elif path.is_dir() and path.name in {"__pycache__", ".pytest_cache"}:
            shutil.rmtree(path)


def add_zip_file(archive: zipfile.ZipFile, source: Path, arcname: str) -> None:
    posix = PurePosixPath(arcname)
    if posix.is_absolute() or ".." in posix.parts:
        raise RuntimeError(f"unsafe archive path: {arcname}")
    info = zipfile.ZipInfo(posix.as_posix(), FIXED_ZIP_TIME)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = (0o100644 & 0xFFFF) << 16
    with source.open("rb") as handle:
        archive.writestr(info, handle.read(), compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)


def zip_tree(source: Path, destination: Path, root_name: str, includes: list[str] | None = None) -> dict:
    selected: list[Path] = []
    if includes is None:
        selected = [path for path in source.rglob("*") if path.is_file()]
    else:
        for value in includes:
            path = source / value
            if path.is_file():
                selected.append(path)
            elif path.is_dir():
                selected.extend(item for item in path.rglob("*") if item.is_file())
    unique = {path.relative_to(source).as_posix(): path for path in selected}
    destination.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(destination, "w", allowZip64=True) as archive:
        for relative, path in sorted(unique.items()):
            add_zip_file(archive, path, f"{root_name}/{relative}")
    return {"name": destination.name, "size": destination.stat().st_size, "sha256": sha256_file(destination), "entries": len(unique)}


def parse_go_dependencies(go_mod: Path) -> list[dict]:
    dependencies = []
    indirect = False
    in_block = False
    for raw in go_mod.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if line == "require (":
            in_block = True
            continue
        if in_block and line == ")":
            in_block = False
            continue
        if not line or line.startswith("//"):
            continue
        if line.startswith("require "):
            line = line[len("require "):]
        elif not in_block:
            continue
        fields = line.split()
        if len(fields) >= 2:
            indirect = "// indirect" in line
            dependencies.append({"name": fields[0], "version": fields[1], "indirect": indirect})
    return dependencies


def make_spdx(full_source: Path, source_snapshot_id: str) -> dict:
    packages = [{
        "SPDXID": "SPDXRef-Package-NewAPI-Smart-Router",
        "name": "NewAPI Smart Router Full Alpha",
        "versionInfo": RELEASE_VERSION,
        "downloadLocation": "NOASSERTION",
        "filesAnalyzed": False,
        "licenseConcluded": "AGPL-3.0-only",
        "licenseDeclared": "AGPL-3.0-only",
        "copyrightText": "Copyright QuantumNous, New API contributors, and NewAPI Smart Router contributors",
        "externalRefs": [{"referenceCategory": "OTHER", "referenceType": "source-snapshot", "referenceLocator": source_snapshot_id}],
    }]
    relationships = []
    for index, dependency in enumerate(parse_go_dependencies(full_source / "go.mod"), start=1):
        spdx_id = f"SPDXRef-Go-{index}"
        packages.append({
            "SPDXID": spdx_id, "name": dependency["name"], "versionInfo": dependency["version"],
            "downloadLocation": "NOASSERTION", "filesAnalyzed": False,
            "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION",
            "copyrightText": "NOASSERTION",
        })
        relationships.append({"spdxElementId": "SPDXRef-Package-NewAPI-Smart-Router", "relationshipType": "DEPENDS_ON", "relatedSpdxElement": spdx_id})
    package_path = full_source / "web" / "default" / "package.json"
    if package_path.is_file():
        package = json.loads(package_path.read_text(encoding="utf-8"))
        merged = {}
        merged.update(package.get("dependencies", {}))
        merged.update(package.get("devDependencies", {}))
        for name, version in sorted(merged.items()):
            index = len(packages)
            spdx_id = f"SPDXRef-Npm-{index}"
            packages.append({
                "SPDXID": spdx_id, "name": name, "versionInfo": str(version),
                "downloadLocation": "NOASSERTION", "filesAnalyzed": False,
                "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION",
                "copyrightText": "NOASSERTION",
            })
            relationships.append({"spdxElementId": "SPDXRef-Package-NewAPI-Smart-Router", "relationshipType": "DEPENDS_ON", "relatedSpdxElement": spdx_id})
    return {
        "spdxVersion": "SPDX-2.3",
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"NewAPI-Smart-Router-{RELEASE_VERSION}",
        "documentNamespace": f"https://github.com/ginsonko/newapi-smart-router/releases/tag/{RELEASE_VERSION}#{source_snapshot_id}",
        "creationInfo": {"created": "2026-08-19T00:00:00Z", "creators": ["Tool: local-build-release-v2"]},
        "packages": packages,
        "relationships": relationships,
        "annotations": [{
            "annotationDate": "2026-08-19T00:00:00Z",
            "annotationType": "OTHER",
            "annotator": "Tool: local-build-release-v2",
            "comment": "Public Alpha source SBOM. Image-layer, signature, and resolved-license certification remain Stable gates.",
        }],
    }


def archive_inventory(path: Path) -> dict:
    with zipfile.ZipFile(path) as archive:
        entries = []
        for info in archive.infolist():
            pure = PurePosixPath(info.filename)
            unsafe = pure.is_absolute() or ".." in pure.parts or bool(re.match(r"^[A-Za-z]:", info.filename))
            entries.append({"path": info.filename, "size": info.file_size, "crc32": f"{info.CRC:08x}", "unsafe": unsafe})
    return {"archive": path.name, "entries": entries}


def legacy_main() -> int:
    script_root = Path(__file__).resolve().parent.parent
    default_work = script_root.parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--template", type=Path, default=script_root)
    parser.add_argument("--reference", type=Path, default=default_work / "new-api-smart-router")
    parser.add_argument(
        "--base-full-archive",
        type=Path,
        default=default_work / "publication" / "baselines" / "v0.2.0-alpha.1" /
        "newapi-smart-router-v0.2.0-alpha.1-full-source.zip",
    )
    parser.add_argument(
        "--overlay-manifest",
        type=Path,
        default=script_root / "full" / "reference-overlay-v0.3.json",
    )
    parser.add_argument("--output", type=Path, default=default_work / "releases" / RELEASE_NAME)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument(
        "--redact-term", action="append", default=[],
        help="private literal to replace in assembled public Full text; values are never written to receipts",
    )
    parser.add_argument("--run-full-tests", action="store_true")
    parser.add_argument("--build-linux", action="store_true")
    args = parser.parse_args()

    template = args.template.resolve(strict=True)
    reference = args.reference.resolve(strict=True)
    base_full_archive = args.base_full_archive.resolve(strict=True)
    overlay_manifest_path = args.overlay_manifest.resolve(strict=True)
    output = args.output.resolve()
    releases_root = output.parent.resolve()
    if template == reference or reference in output.parents or output in reference.parents:
        raise RuntimeError("template, reference, and output must be independent")
    if output in base_full_archive.parents or output in overlay_manifest_path.parents:
        raise RuntimeError("release inputs must not be nested inside output")
    if not args.go.resolve(strict=True).is_file():
        raise RuntimeError("Go executable was not found")
    ensure_safe_generated_root(output, releases_root)
    staging = output.with_name(output.name + ".staging")
    ensure_safe_generated_root(staging, releases_root)
    staging.mkdir(parents=True)
    (staging / MARKER_NAME).write_text(RELEASE_NAME + "\n", encoding="utf-8")

    try:
        overlay_manifest = load_overlay_manifest(overlay_manifest_path)
        selected = select_overlay_files(reference, overlay_manifest)
        restored = select_reference_head_restores(reference, overlay_manifest)
        translation_sources = select_translation_sources(reference, overlay_manifest)
        overlay_entries = snapshot_entries(selected)
        restore_entries = reference_restore_entries(restored)
        translation_entries = snapshot_entries(translation_sources)
        selected_paths = {relative.as_posix() for _source, relative, _kind in selected}
        restore_paths = {item["relative"].as_posix() for item in restored}
        if selected_paths & restore_paths:
            raise RuntimeError(f"overlay and fixed-reference paths overlap: {sorted(selected_paths & restore_paths)}")
        base_entry = {
            "path": "@public-base/" + overlay_manifest["base"]["asset"],
            "kind": "public_release_base",
            "size": base_full_archive.stat().st_size,
            "sha256": sha256_file(base_full_archive),
        }
        before_entries = [base_entry, *overlay_entries, *restore_entries, *translation_entries]
        current_snapshot_id = snapshot_id(before_entries)
        head = git_lines(reference, "rev-parse", "HEAD")[0]
        describe = git_lines(reference, "describe", "--tags", "--always", "--dirty")[0]
        status_lines = git_lines(reference, "status", "--porcelain=v1")
        source_receipt = {
            "schema_version": "source-snapshot-v1",
            "release": RELEASE_VERSION,
            "upstream_module": "github.com/QuantumNous/new-api",
            "reference_head": head,
            "reference_describe": describe,
            "reference_dirty": bool(status_lines),
            "reference_status_sha256": hashlib.sha256("\n".join(status_lines).encode("utf-8")).hexdigest(),
            "base_full": {
                "release": overlay_manifest["base"].get("release"),
                "asset": overlay_manifest["base"].get("asset"),
                "sha256": base_entry["sha256"],
            },
            "overlay_manifest": overlay_manifest_path.relative_to(template).as_posix(),
            "source_snapshot_id": current_snapshot_id,
            "selected_file_count": len(before_entries),
            "files": before_entries,
            "public_status": "public_alpha_snapshot_not_stable",
        }
        repository = staging / "repository"
        full_source = staging / "full-source"
        artifacts = staging / "artifacts"
        receipts = staging / "receipts"
        binaries = staging / "binaries"
        repository.mkdir()
        full_source.mkdir()
        artifacts.mkdir()
        receipts.mkdir()
        assemble_repository(template, reference, repository, current_snapshot_id)
        base_receipt = extract_verified_base(base_full_archive, full_source, overlay_manifest)
        apply_overlay(full_source, selected)
        apply_reference_head_restores(full_source, restored)
        translation_receipt = merge_referenced_translations(
            full_source, translation_sources, selected, overlay_manifest,
        )
        source_receipt["base_full"].update(base_receipt)
        source_receipt["public_sanitization"] = sanitize_full_source(full_source, args.redact_term)
        assembled_paths = sorted(
            selected_paths |
            restore_paths |
            {relative.as_posix() for _source, relative, _kind in translation_sources}
        )
        assembled_overlay_items = [
            (full_source / Path(relative), Path(relative), "r52_public_assembly")
            for relative in assembled_paths
        ]
        source_receipt["overlay_sources"] = overlay_entries
        source_receipt["fixed_reference_sources"] = restore_entries
        source_receipt["translation_sources"] = translation_entries
        source_receipt["translation_overlay"] = translation_receipt
        source_receipt["files"] = [base_entry, *snapshot_entries(assembled_overlay_items)]
        source_receipt["assembled_overlay_file_count"] = len(assembled_overlay_items)
        frontend_receipts = build_frontends(full_source, reference, args.redact_term)
        source_receipt["frontend_build"] = verify_frontend_builds(full_source)
        source_receipt["assembled_full_file_count"] = len(select_tree_files(full_source))
        attach_full_release_docs(full_source, source_receipt, repository)
        after_entries = snapshot_entries(selected)
        after_restores = reference_restore_entries(select_reference_head_restores(reference, overlay_manifest))
        after_translations = snapshot_entries(select_translation_sources(reference, overlay_manifest))
        if (
            overlay_entries != after_entries or restore_entries != after_restores or
            translation_entries != after_translations or
            base_entry["sha256"] != sha256_file(base_full_archive)
        ):
            raise RuntimeError("reference source changed during snapshot; retry after parallel work settles")
        write_json(receipts / "source-snapshot.json", source_receipt)

        build_receipts = [*frontend_receipts, *command_receipts(
            repository, full_source, args.go, args.run_full_tests, args.build_linux, binaries,
        )]
        python = sys.executable
        python_env = os.environ.copy()
        python_env["PYTHONDONTWRITEBYTECODE"] = "1"
        build_receipts.append(run(
            [python, "-m", "unittest", "discover", "-s", "integration/doctor", "-p", "test_*.py"],
            repository, python_env,
        ))
        build_receipts.append(run(
            [python, "scripts/validate_repository.py", "--root", "."], repository, python_env,
        ))
        build_receipts = redact_local_paths(build_receipts, [
            (str(staging), "<RELEASE_STAGING>"),
            (str(output), "<RELEASE_ROOT>"),
            (str(reference), "<REFERENCE_ROOT>"),
            (str(template), "<TEMPLATE_ROOT>"),
            (str(base_full_archive), "<PUBLIC_BASE_ARCHIVE>"),
            (str(overlay_manifest_path), "<OVERLAY_MANIFEST>"),
            (str(Path.home()), "<USER_HOME>"),
        ])
        write_json(receipts / "build-and-test.json", {"release": RELEASE_VERSION, "commands": build_receipts})
        remove_generated_caches(repository)
        remove_generated_caches(full_source)

        common = [
            "README.md", "LICENSE", "NOTICE", "UPSTREAM-NOTICE",
            "THIRD-PARTY-LICENSES.md", "COMMERCIAL-LICENSE.md",
            "RELEASE-STATUS.md", "SECURITY.md", "CHANGELOG.md",
        ]
        package_specs = [
            ("bridge-candidate", common + ["core", "bridge", "parts/spec", "parts/manifest", "docs/release-forms.md", "docs/source-provenance.md"]),
            ("integration-kit", common + ["core", "bridge", "integration", "parts", "docs"]),
            ("agent-parts-kit", common + ["parts", "core/README.md", "core/smartrouter", "core/testdata"]),
            ("documentation", common + ["docs", "parts/docs", "parts/spec", "parts/manifest", "compatibility"]),
        ]
        artifact_records = []
        full_archive = artifacts / f"{RELEASE_NAME}-full-source.zip"
        record = zip_tree(full_source, full_archive, f"{RELEASE_NAME}-full-source")
        record.update({"form": "full", "status": "public_alpha", "runnable": True})
        artifact_records.append(record)
        for suffix, includes in package_specs:
            archive = artifacts / f"{RELEASE_NAME}-{suffix}.zip"
            record = zip_tree(repository, archive, f"{RELEASE_NAME}-{suffix}", includes)
            form = suffix.replace("-candidate", "").replace("-kit", "")
            record.update({"form": form, "status": "public_alpha_candidate", "runnable": suffix != "agent-parts-kit"})
            if suffix == "agent-parts-kit":
                record["status"] = "development_kit_not_runnable"
            artifact_records.append(record)
        if args.build_linux:
            binary = binaries / "new-api-smart-router-linux-amd64"
            binary_target = artifacts / f"{RELEASE_NAME}-linux-amd64"
            shutil.copy2(binary, binary_target)
            artifact_records.append({
                "name": binary_target.name, "size": binary_target.stat().st_size,
                "sha256": sha256_file(binary_target), "entries": 1,
                "form": "full_linux_binary", "status": "public_alpha_unsigned", "runnable": True,
            })
        sbom_path = artifacts / "SPDX-SBOM.json"
        write_json(sbom_path, make_spdx(full_source, current_snapshot_id))
        manifest = {
            "schema_version": "release-manifest-v1",
            "release": RELEASE_VERSION,
            "status": "local_rc_validation_pending",
            "source_snapshot_id": current_snapshot_id,
            "repository": {
                "url": "https://github.com/ginsonko/newapi-smart-router",
                "commit": "pending_public_commit"
            },
            "upstream": {
                "module": "github.com/QuantumNous/new-api",
                "base_tag": "v1.0.0-rc.20",
                "base_commit": "a7f3067bf34a2fa125f843acdfcf45d0b0bfd682",
                "reference_fork_head": head,
                "reference_fork_describe": describe,
                "latest_observed_release": "v1.0.0-rc.21",
                "latest_observed_main": "1721144221ec5c94dd87891a7ae1bee228e7bb63"
            },
            "artifacts": artifact_records,
            "completed_gates": [
                "network_upstream_drift_audit", "public_repository_origin"
            ],
            "remaining_gates": [
                "remote_ci", "protocol_outcome_certification", "three_database_roundtrip",
                "external_non_production_site_roundtrip", "image_sbom_and_signature",
            ],
        }
        write_json(artifacts / "RELEASE-MANIFEST.json", manifest)
        sums = []
        for path in sorted(item for item in artifacts.iterdir() if item.is_file() and item.name != "SHA256SUMS"):
            sums.append(f"{sha256_file(path)}  {path.name}")
        (artifacts / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8", newline="\n")
        inventories = [archive_inventory(path) for path in sorted(artifacts.glob("*.zip"))]
        write_json(receipts / "package-inventory.json", {"release": RELEASE_VERSION, "archives": inventories})
        (staging / MARKER_NAME).write_text(RELEASE_NAME + "\n", encoding="utf-8")
        staging.rename(output)
        print(json.dumps({
            "status": "ASSEMBLED",
            "release": RELEASE_VERSION,
            "output": str(output),
            "source_snapshot_id": current_snapshot_id,
            "artifacts": [record["name"] for record in artifact_records],
        }, ensure_ascii=False, indent=2))
        return 0
    except Exception:
        print(f"staging retained for diagnosis: {staging}", file=sys.stderr)
        raise


def main() -> int:
    from build_v04 import main as current_main
    return current_main()


if __name__ == "__main__":
    raise SystemExit(main())
