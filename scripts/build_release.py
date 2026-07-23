#!/usr/bin/env python3
"""Build a reproducible public NewAPI Smart Router Alpha release candidate."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import zipfile
from pathlib import Path, PurePosixPath


RELEASE_VERSION = "v0.1.0-alpha.1"
RELEASE_NAME = f"newapi-smart-router-{RELEASE_VERSION}"
MARKER_NAME = ".smart-router-release-root"
FIXED_ZIP_TIME = (2026, 7, 23, 0, 0, 0)
ALLOWED_UNTRACKED_ROOTS = {
    "common", "constant", "controller", "docs", "middleware", "model",
    "pkg", "relay", "router", "service", "setting", "types", "web",
}
ALLOWED_UNTRACKED_SUFFIXES = {
    ".go", ".ts", ".tsx", ".js", ".mjs", ".json", ".md", ".sql",
    ".yaml", ".yml", ".toml", ".css", ".scss", ".html", ".svg",
    ".png", ".jpg", ".jpeg", ".webp", ".ico", ".woff", ".woff2", ".ttf",
}
DENIED_COMPONENTS = {
    ".git", ".tmp", ".cache", "node_modules", "coverage", "logs", "log",
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


def is_denied_relative(relative: Path) -> bool:
    lowered = {part.lower() for part in relative.parts}
    if lowered & DENIED_COMPONENTS:
        return True
    name = relative.name.lower()
    if name == ".env" or name.startswith(".env."):
        return True
    return relative.suffix.lower() in DENIED_SUFFIXES


def select_reference_files(reference: Path) -> list[tuple[Path, Path, str]]:
    selected: dict[str, tuple[Path, Path, str]] = {}
    for value in git_lines(reference, "ls-files"):
        relative = Path(value)
        source = reference / relative
        if source.is_file() and not is_denied_relative(relative):
            selected[relative.as_posix()] = (source, relative, "tracked")
    for value in git_lines(reference, "ls-files", "--others", "--exclude-standard"):
        relative = Path(value)
        if not relative.parts or relative.parts[0] not in ALLOWED_UNTRACKED_ROOTS:
            if relative.as_posix() != "main_migration_test.go":
                continue
        if is_denied_relative(relative) or relative.suffix.lower() not in ALLOWED_UNTRACKED_SUFFIXES:
            continue
        source = reference / relative
        if source.is_file():
            selected[relative.as_posix()] = (source, relative, "untracked_source")
    for dist_relative in (Path("web/default/dist"), Path("web/classic/dist")):
        dist = reference / dist_relative
        if not dist.is_dir():
            raise RuntimeError(f"required built frontend is missing: {dist}")
        for source in dist.rglob("*"):
            if source.is_file():
                relative = source.relative_to(reference)
                selected[relative.as_posix()] = (source, relative, "generated_frontend")
    return [selected[key] for key in sorted(selected)]


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
        shutil.rmtree(path)


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
    text = text.replace('manifest_version: "1.0.0-design"', 'manifest_version: "1.0.0-alpha.1"')
    text = text.replace('status: "approved_for_documentation_and_extraction_design"', 'status: "public_alpha_candidate"')
    text = text.replace('baseline: "pending_frozen_reference"', f'baseline: "{source_snapshot_id}"')
    text = text.replace(
        'note: "Do not extract public code until the semantic-success repair is frozen and the public repository is rebuilt from clean upstream history."',
        'note: "Public Alpha snapshot only. Protocol outcome certification, three-database round trips, external-site validation, and signed image gates remain open."',
    )
    text = text.replace("spec_ready_reference_extraction_pending", "spec_ready_public_reference_snapshot")
    text = text.replace("implemented_in_reference_fork_extraction_pending", "public_reference_core_extracted")
    text = text.replace("release_target_pending_frozen_semantic_baseline", "release_gate_pending_protocol_certification")
    path.write_text(text, encoding="utf-8", newline="\n")


def update_capability_matrix(path: Path) -> None:
    text = path.read_text(encoding="utf-8")
    text = text.replace('  - "public repository is not rebuilt from clean upstream history"\n', "")
    text = text.replace(
        '  - "release signing SBOM checksum and secret scanning are incomplete"',
        '  - "release signature and image-layer SBOM are incomplete"',
    )
    path.write_text(text, encoding="utf-8", newline="\n")


def assemble_repository(template: Path, reference: Path, target: Path, source_snapshot_id: str) -> None:
    copy_tree(template, target, {"__pycache__", ".pytest_cache"})
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


def assemble_full_source(
    reference: Path, target: Path, items: list[tuple[Path, Path, str]], source_receipt: dict, repository: Path
) -> None:
    for source, relative, _kind in items:
        destination = target / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, destination)
    release_docs = target / "smart-router-release"
    release_docs.mkdir(parents=True, exist_ok=True)
    for relative in (
        "README.md", "RELEASE-STATUS.md", "SECURITY.md", "CHANGELOG.md",
        "NOTICE", "COMMERCIAL-LICENSE.md",
    ):
        shutil.copy2(repository / relative, release_docs / relative)
    for directory in ("parts", "bridge", "integration"):
        copy_tree(repository / directory, release_docs / directory, {"__pycache__", ".pytest_cache"})
    (target / "SMART-ROUTER-RELEASE.md").write_text(
        "# NewAPI Smart Router Full Alpha\n\n"
        "This source snapshot derives from QuantumNous New API and includes the local Smart Router reference integration. "
        "It is an unsigned Alpha for isolated evaluation. Read `smart-router-release/RELEASE-STATUS.md` before use.\n",
        encoding="utf-8", newline="\n",
    )
    write_json(target / "SMART-ROUTER-SOURCE-SNAPSHOT.json", source_receipt)


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
        "creationInfo": {"created": "2026-07-23T00:00:00Z", "creators": ["Tool: local-build-release-v1"]},
        "packages": packages,
        "relationships": relationships,
        "annotations": [{
            "annotationDate": "2026-07-23T00:00:00Z",
            "annotationType": "OTHER",
            "annotator": "Tool: local-build-release-v1",
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


def main() -> int:
    script_root = Path(__file__).resolve().parent.parent
    default_work = script_root.parent
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--template", type=Path, default=script_root)
    parser.add_argument("--reference", type=Path, default=default_work / "new-api-smart-router")
    parser.add_argument("--output", type=Path, default=default_work / "releases" / RELEASE_NAME)
    parser.add_argument("--go", type=Path, required=True)
    parser.add_argument("--run-full-tests", action="store_true")
    parser.add_argument("--build-linux", action="store_true")
    args = parser.parse_args()

    template = args.template.resolve(strict=True)
    reference = args.reference.resolve(strict=True)
    output = args.output.resolve()
    releases_root = output.parent.resolve()
    if template == reference or reference in output.parents or output in reference.parents:
        raise RuntimeError("template, reference, and output must be independent")
    if not args.go.resolve(strict=True).is_file():
        raise RuntimeError("Go executable was not found")
    ensure_safe_generated_root(output, releases_root)
    staging = output.with_name(output.name + ".staging")
    ensure_safe_generated_root(staging, releases_root)
    staging.mkdir(parents=True)
    (staging / MARKER_NAME).write_text(RELEASE_NAME + "\n", encoding="utf-8")

    try:
        selected = select_reference_files(reference)
        before_entries = snapshot_entries(selected)
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
        assemble_full_source(reference, full_source, selected, source_receipt, repository)
        after_entries = snapshot_entries(selected)
        if before_entries != after_entries:
            raise RuntimeError("reference source changed during snapshot; retry after parallel work settles")
        write_json(receipts / "source-snapshot.json", source_receipt)

        build_receipts = command_receipts(
            repository, full_source, args.go, args.run_full_tests, args.build_linux, binaries,
        )
        python = sys.executable
        build_receipts.append(run([python, "-m", "unittest", "discover", "-s", "integration/doctor", "-p", "test_*.py"], repository))
        build_receipts.append(run([python, "scripts/validate_repository.py", "--root", "."], repository))
        build_receipts = redact_local_paths(build_receipts, [
            (str(staging), "<RELEASE_STAGING>"),
            (str(output), "<RELEASE_ROOT>"),
            (str(reference), "<REFERENCE_ROOT>"),
            (str(template), "<TEMPLATE_ROOT>"),
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


if __name__ == "__main__":
    raise SystemExit(main())
