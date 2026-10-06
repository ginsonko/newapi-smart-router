#!/usr/bin/env python3
"""Build the generic v0.4 reference from the public base and pinned overlay."""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path, PurePosixPath

import build_release as release


def source_files(root: Path) -> list[tuple[Path, Path, str]]:
    selected = []
    for directory, folders, files in os.walk(root):
        folders[:] = sorted(name for name in folders if name not in {"node_modules", ".git", "dist", ".cache", "__pycache__", ".tanstack", "smart-router-release"})
        for name in sorted(files):
            path = Path(directory) / name
            relative = path.relative_to(root)
            if name.endswith(".tsbuildinfo") or name.startswith("SMART-ROUTER-"):
                continue
            if release.is_denied_relative(relative):
                raise RuntimeError(f"Denied source member: {relative.as_posix()}")
            selected.append((path, relative, "generic_r98_public_source"))
    return sorted(selected, key=lambda item: item[1].as_posix())


def safe_member(value: str) -> Path:
    pure = PurePosixPath(value)
    if pure.is_absolute() or ".." in pure.parts or ":" in value or "\\" in value:
        raise RuntimeError("Unsafe source member")
    return Path(*pure.parts)


def assemble(template: Path, archive_path: Path, target: Path) -> dict:
    manifest = json.loads((template / "full/reference-v0.4.json").read_text(encoding="utf-8"))
    if manifest.get("schema_version") != "full-reference-overlay-v2" or manifest["release"] != release.RELEASE_VERSION or release.sha256_file(archive_path) != manifest["base"]["sha256"]:
        raise RuntimeError("Public base/release identity mismatch")
    removed = set(manifest["remove"])
    with zipfile.ZipFile(archive_path) as archive:
        prefix = manifest["base"]["archive_root"] + "/"
        for entry in archive.infolist():
            if entry.is_dir():
                continue
            if not entry.filename.startswith(prefix):
                raise RuntimeError("Unexpected public base archive root")
            relative = entry.filename[len(prefix):]
            member = safe_member(relative)
            if relative in removed:
                continue
            if release.is_denied_relative(member):
                raise RuntimeError("Denied public base member")
            destination = target / member
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(archive.read(entry))
    for entry in manifest["overlay"]:
        source = template / "full/overlay" / safe_member(entry["blob"])
        if release.sha256_file(source) != entry["sha256"]:
            raise RuntimeError(f"Overlay digest mismatch: {entry['path']}")
        destination = target / safe_member(entry["path"])
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
    actual = {relative.as_posix(): release.sha256_file(path) for path, relative, _ in source_files(target)}
    expected = {entry["path"]: entry["sha256"] for entry in manifest["files"]}
    if actual != expected:
        raise RuntimeError("Assembled Full source differs from pinned manifest")
    return manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-full-archive", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--go", required=True)
    parser.add_argument("--bun", default="bun")
    parser.add_argument("--prepare-only", action="store_true", help="assemble public sources without claiming build validation")
    args = parser.parse_args()
    template = Path(__file__).resolve().parents[1]
    output = args.output.resolve()
    if output.exists():
        raise RuntimeError("Use a fresh output directory; existing evidence is never replaced")
    if output == template or template in output.parents:
        raise RuntimeError("Keep generated artifacts outside the repository")
    full = output / "full-source"
    repository = output / "repository"
    receipts = output / "receipts"
    artifacts = output / "artifacts"
    for directory in (full, repository, receipts, artifacts):
        directory.mkdir(parents=True)
    (output / release.MARKER_NAME).write_text(release.RELEASE_NAME + "\n", encoding="utf-8", newline="\n")
    manifest = assemble(template, args.base_full_archive.resolve(strict=True), full)
    release.copy_tree(template, repository, {".git", "__pycache__", ".pytest_cache", "node_modules"})
    if args.prepare_only:
        print(json.dumps({"status": "SOURCE_ASSEMBLED_NOT_VALIDATED", "output": str(output)}))
        return 0
    env = {**os.environ, "GOTOOLCHAIN": "local", "PYTHONDONTWRITEBYTECODE": "1", "CI": "true"}
    commands = []

    def checked(command: list[str], cwd: Path, extra: dict | None = None, timeout: int = 1800) -> None:
        print("RUN " + " ".join(command[:4]) + " @ " + cwd.relative_to(output).as_posix(), flush=True)
        commands.append(release.run(command, cwd, {**env, **(extra or {})}, timeout))

    bun = shutil.which(args.bun) or args.bun
    checked([bun, "install", "--frozen-lockfile"], full / "web")
    checked([bun, "test", "src/features/keys/lib"], full / "web/default")
    checked([bun, "run", "typecheck"], full / "web/default")
    lint_sources = ["src/features/keys/lib/api-key-form.ts", "src/features/keys/lib/api-key-memory.test.ts", "src/features/keys/lib/index.ts", "src/features/keys/components/api-keys-mutate-drawer.tsx"]
    if any(not (full / "web/default" / source).is_file() for source in lint_sources):
        raise RuntimeError("Required memory UI lint input is missing")
    checked([bun, "x", "--no-install", "oxlint", "-c", ".oxlintrc.json", *lint_sources], full / "web/default")
    for theme in ("default", "classic"):
        checked([bun, "run", "build"], full / "web" / theme)
    frontend_receipt = release.verify_frontend_builds(full)
    for path in (full / "web/default/node_modules", full / "web/classic/node_modules", full / "web/node_modules"):
        if path.is_symlink():
            path.unlink()
        elif path.exists():
            if output not in path.resolve().parents:
                raise RuntimeError("Dependency cleanup escaped generated output")
            shutil.rmtree(path)
    checked([args.go, "mod", "verify"], repository / "core")
    checked([args.go, "test", "./...", "-count=1"], repository / "core")
    checked([args.go, "run", "./cmd/conformance", "-vectors", "testdata/planner-v1.json"], repository / "core")
    checked([args.go, "mod", "verify"], repository / "bridge/newapi")
    checked([args.go, "test", "./...", "-count=1"], repository / "bridge/newapi")
    checked([args.go, "mod", "verify"], full)
    checked([args.go, "test", "./...", "-count=1", "-p=2"], full)
    binary = artifacts / f"{release.RELEASE_NAME}-linux-amd64"
    checked([args.go, "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-X github.com/QuantumNous/new-api/common.Version={release.RELEASE_VERSION}", "-o", str(binary), "."], full, {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"})
    checked([sys.executable, "-B", "-m", "unittest", "discover", "-s", "integration/doctor", "-p", "test_*.py"], repository)
    checked([sys.executable, "-B", "-m", "unittest", "discover", "-s", "scripts", "-p", "test_*.py"], repository)
    checked([sys.executable, "-B", "scripts/validate_repository.py", "--root", "."], repository)
    snapshot = release.snapshot_entries(source_files(full))
    snapshot_id = release.snapshot_id(snapshot)
    source_receipt = {
        "schema_version": "source-snapshot-v1", "release": release.RELEASE_VERSION,
        "source_snapshot_id": snapshot_id, "upstream_module": "github.com/QuantumNous/new-api",
        "base_full": manifest["base"], "files": snapshot,
        "frontend_build": frontend_receipt,
        "public_status": "public_alpha_snapshot_not_stable",
    }
    release.update_parts_manifest(repository / "parts/manifest/PARTS-MANIFEST.yaml", snapshot_id)
    release.attach_full_release_docs(full, source_receipt, repository)
    mappings = [(str(output), "<RELEASE_ROOT>"), (str(template), "<PUBLIC_REPOSITORY>"), (str(Path(args.go).resolve()), "<GO>"), (str(Path(sys.executable).resolve()), "<PYTHON>"), (str(Path.home()), "<USER_HOME>")]
    release.write_json(receipts / "source-snapshot.json", source_receipt)
    release.write_json(receipts / "build-and-test.json", {"release": release.RELEASE_VERSION, "commands": release.redact_local_paths(commands, mappings)})
    for path in (full / "web/node_modules", full / "web/default/node_modules", full / "web/classic/node_modules"):
        if path.is_symlink():
            path.unlink()
        elif path.exists():
            if output not in path.resolve().parents:
                raise RuntimeError("Dependency cleanup escaped generated output")
            shutil.rmtree(path)
    release.remove_generated_caches(repository)
    release.remove_generated_caches(full)
    common = ["README.md", "LICENSE", "NOTICE", "UPSTREAM-NOTICE", "THIRD-PARTY-LICENSES.md", "COMMERCIAL-LICENSE.md", "RELEASE-STATUS.md", "SECURITY.md", "CHANGELOG.md"]
    specs = [
        ("full-source", full, None, "full", True),
        ("bridge-candidate", repository, common + ["core", "bridge", "parts/spec", "parts/manifest", "docs"], "bridge", False),
        ("integration-kit", repository, common + ["core", "bridge", "integration", "parts", "docs"], "integration", False),
        ("agent-parts-kit", repository, common + ["parts", "core/README.md", "core/smartrouter", "core/testdata"], "agent-parts", False),
        ("documentation", repository, common + ["docs", "parts/docs", "parts/spec", "parts/manifest", "compatibility"], "documentation", False),
    ]
    records = []
    for suffix, root, includes, form, runnable in specs:
        record = release.zip_tree(root, artifacts / f"{release.RELEASE_NAME}-{suffix}.zip", f"{release.RELEASE_NAME}-{suffix}", includes)
        record.update({"form": form, "runnable": runnable, "status": "public_alpha_candidate" if runnable else "development_kit_not_runnable"})
        records.append(record)
    records.append({"name":binary.name, "sha256":release.sha256_file(binary), "size":binary.stat().st_size, "form":"full_linux_binary", "runnable":True, "status":"public_alpha_unsigned"})
    release.write_json(artifacts / "SPDX-SBOM.json", release.make_spdx(full, snapshot_id))
    release.write_json(artifacts / "RELEASE-MANIFEST.json", {
        "schema_version":"release-manifest-v1", "release":release.RELEASE_VERSION, "status":"local_rc_validation_pending",
        "source_snapshot_id":snapshot_id, "repository":{"url":"https://github.com/ginsonko/newapi-smart-router", "commit":"pending_public_commit"},
        "upstream":{"module":"github.com/QuantumNous/new-api", "base_tag":"v1.0.0-rc.20", "base_commit":"a7f3067bf34a2fa125f843acdfcf45d0b0bfd682", "latest_observed_release":"v1.0.0-rc.21", "observation_is_historical":True},
        "artifacts":records, "completed_gates":["local_build_and_tests"],
        "remaining_gates":["remote_ci", "official_revision_certification", "protocol_outcome_certification", "three_database_roundtrip", "external_non_production_site_roundtrip", "image_sbom_and_signature"],
    })
    (artifacts / "SHA256SUMS").write_text("".join(f"{release.sha256_file(path)}  {path.name}\n" for path in sorted(artifacts.iterdir()) if path.is_file() and path.name != "SHA256SUMS"), encoding="utf-8")
    release.write_json(receipts / "package-inventory.json", {"release":release.RELEASE_VERSION, "archives":[release.archive_inventory(path) for path in sorted(artifacts.glob("*.zip"))]})
    result = subprocess.run([sys.executable, "-B", str(repository / "scripts/validate_release.py"), "--release-root", str(output)], env=env, check=False)
    return result.returncode


if __name__ == "__main__":
    raise SystemExit(main())
