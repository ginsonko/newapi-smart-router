#!/usr/bin/env python3
"""Adversarial local validation for a NewAPI Smart Router release tree."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import stat
import subprocess
import sys
import urllib.parse
import zipfile
from pathlib import Path, PurePosixPath


RELEASE_VERSION = "v0.3.0-alpha.1"
FIXED_ZIP_TIME = (2026, 8, 19, 0, 0, 0)
EXPECTED_CORE_FILE_COUNT = 20
README_BASELINE_LINES = 2372
README_BASELINE_SHA256 = "6eaae6be969cc543f08215d7a87774c3c20c113b067bd07dda8827b4b61d118b"


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def normalized_readme_prefix(path: Path) -> tuple[int, str]:
    normalized = path.read_bytes().replace(b"\r\n", b"\n").replace(b"\r", b"\n")
    lines = normalized.splitlines(keepends=True)
    prefix = b"".join(lines[:README_BASELINE_LINES])
    return len(lines), hashlib.sha256(prefix).hexdigest()


class Validator:
    def __init__(self, root: Path, private_terms: list[str] | None = None):
        self.root = root.resolve(strict=True)
        self.repository = self.root / "repository"
        self.full = self.root / "full-source"
        self.artifacts = self.root / "artifacts"
        self.receipts = self.root / "receipts"
        self.checks: list[dict] = []
        self.private_terms = list(private_terms or [])

    def check(self, name: str, condition: bool, detail: object = "") -> None:
        self.checks.append({"name": name, "status": "pass" if condition else "fail", "detail": detail})
        if not condition:
            raise AssertionError(f"{name}: {detail}")

    def validate_structure(self) -> None:
        required = [
            self.repository / "README.md", self.repository / "LICENSE", self.repository / "NOTICE",
            self.repository / "UPSTREAM-NOTICE", self.repository / "THIRD-PARTY-LICENSES.md",
            self.repository / "COMMERCIAL-LICENSE.md", self.repository / ".github" / "workflows" / "ci.yml",
            self.repository / "RELEASE-STATUS.md", self.repository / "core" / "go.mod",
            self.repository / "bridge" / "newapi" / "go.mod", self.repository / "integration" / "doctor" / "doctor.py",
            self.repository / "parts" / "manifest" / "PARTS-MANIFEST.yaml",
            self.repository / "parts" / "spec" / "route-price.schema.json",
            self.full / "go.mod", self.full / "SMART-ROUTER-RELEASE.md",
            self.full / "web" / "default" / "dist" / "index.html",
            self.artifacts / "RELEASE-MANIFEST.json", self.artifacts / "SPDX-SBOM.json", self.artifacts / "SHA256SUMS",
        ]
        missing = [str(path.relative_to(self.root)) for path in required if not path.is_file()]
        self.check("required_release_structure", not missing, missing)
        generated_caches = [
            path.relative_to(self.root).as_posix() for path in self.root.rglob("*")
            if path.name in {"__pycache__", ".pytest_cache"} or (path.is_file() and path.suffix.lower() == ".pyc")
        ]
        self.check("no_generated_python_caches", not generated_caches, generated_caches[:20])
        vcs_metadata = [
            path.relative_to(self.root).as_posix() for path in self.root.rglob(".git")
        ]
        self.check("no_vcs_metadata_in_release", not vcs_metadata, vcs_metadata[:20])
        status = (self.repository / "RELEASE-STATUS.md").read_text(encoding="utf-8")
        self.check("four_release_forms_labeled", all(value in status for value in (
            "Full compatibility distribution", "Certified Bridge Add-on", "Custom Fork Integration Kit", "Agent Parts Kit",
        )), "release form table")
        self.check("parts_not_runnable_label", "Development Kit / Not runnable" in status, "Agent Parts label")

    def validate_public_metadata(self) -> None:
        expected_origin = "https://github.com/ginsonko/newapi-smart-router"
        expected_core = "github.com/ginsonko/newapi-smart-router/core"
        expected_bridge = "github.com/ginsonko/newapi-smart-router/bridge/newapi"
        core_mod = (self.repository / "core" / "go.mod").read_text(encoding="utf-8")
        bridge_mod = (self.repository / "bridge" / "newapi" / "go.mod").read_text(encoding="utf-8")
        readme = (self.repository / "README.md").read_text(encoding="utf-8")
        release_status = (self.repository / "RELEASE-STATUS.md").read_text(encoding="utf-8")
        license_text = (self.repository / "LICENSE").read_text(encoding="utf-8")
        commercial = (self.repository / "COMMERCIAL-LICENSE.md").read_text(encoding="utf-8")
        manifest = json.loads((self.artifacts / "RELEASE-MANIFEST.json").read_text(encoding="utf-8"))
        self.check("public_core_module", core_mod.startswith(f"module {expected_core}\n"), core_mod.splitlines()[0])
        self.check("public_bridge_module", bridge_mod.startswith(f"module {expected_bridge}\n") and expected_core in bridge_mod, bridge_mod.splitlines()[:8])
        local_module_marker = "smart-router" + ".local"
        local_version_marker = RELEASE_VERSION + "-" + "local"
        self.check("no_local_module_placeholder", not any(
            local_module_marker in path.read_text(encoding="utf-8", errors="replace")
            for path in self.repository.rglob("*") if path.is_file() and path.suffix.lower() in {".go", ".mod", ".md"}
        ), "public Go and Markdown files")
        self.check("public_origin_documented", expected_origin in readme and manifest.get("repository", {}).get("url") == expected_origin, manifest.get("repository"))
        self.check("public_version_documented", RELEASE_VERSION in readme and local_version_marker not in readme, "README release status")
        self.check("v0_3_delta_documented", "docs/v0.3-contract-delta.md" in readme, "README v0.3 link")
        self.check("upstream_drift_disclosed", "v1.0.0-rc.21" in release_status and "rc.20" in release_status, "release compatibility boundary")
        self.check("agpl_license_preserved", "GNU AFFERO GENERAL PUBLIC LICENSE" in license_text and "Version 3" in license_text, "LICENSE")
        self.check("commercial_boundary_documented", all(value in commercial for value in (
            "AGPL-3.0-only", "does not grant", "QuantumNous New API", "not a license grant",
        )), "COMMERCIAL-LICENSE.md")
        self.check("release_manifest_version", manifest.get("release") == RELEASE_VERSION, manifest.get("release"))
        line_count, prefix_digest = normalized_readme_prefix(self.repository / "README.md")
        self.check("readme_v0_2_strict_prefix_preserved", (
            line_count > README_BASELINE_LINES and prefix_digest == README_BASELINE_SHA256
        ), {"baseline_lines": README_BASELINE_LINES, "lines": line_count, "prefix_sha256": prefix_digest})

    def validate_json_yaml_schema(self) -> None:
        json_files = sorted(self.repository.rglob("*.json")) + sorted(self.artifacts.glob("*.json")) + sorted(self.receipts.glob("*.json"))
        parsed = {}
        for path in json_files:
            parsed[path] = json.loads(path.read_text(encoding="utf-8"))
        self.check("json_parse", bool(json_files), {"files": len(json_files)})
        try:
            import yaml
            import jsonschema
        except ImportError as exc:
            raise AssertionError(f"validation dependency missing: {exc}") from exc
        yaml_files = sorted(self.repository.rglob("*.yaml")) + sorted(self.repository.rglob("*.yml"))
        for path in yaml_files:
            yaml.safe_load(path.read_text(encoding="utf-8"))
        self.check("yaml_parse", True, {"files": len(yaml_files)})
        schemas = sorted((self.repository / "parts" / "spec").glob("*.schema.json"))
        for path in schemas:
            jsonschema.Draft202012Validator.check_schema(parsed[path])
        self.check("json_schema_valid", len(schemas) >= 9, {"schemas": len(schemas)})
        parts_manifest = yaml.safe_load((self.repository / "parts" / "manifest" / "PARTS-MANIFEST.yaml").read_text(encoding="utf-8"))
        capability_matrix = yaml.safe_load((self.repository / "parts" / "manifest" / "capability-matrix.yaml").read_text(encoding="utf-8"))
        self.check("v0_3_parts_versions", (
            parts_manifest.get("manifest_version") == "3.0.0-alpha.1"
            and capability_matrix.get("matrix_version") == "3.0.0-alpha.1"
        ), {"parts": parts_manifest.get("manifest_version"), "matrix": capability_matrix.get("matrix_version")})
        route_price_schema = parsed[self.repository / "parts" / "spec" / "route-price.schema.json"]
        route_price_validator = jsonschema.Draft202012Validator(route_price_schema)
        route_price_validator.validate({
            "billing_mode": "ratio", "billing_unit": "per_token", "static_comparable": True,
            "comparison_class": "token:input", "score_ppm": 2500000, "synthetic": False,
            "scope": "group_model", "revision": "fixture-revision",
        })
        rejected = False
        try:
            route_price_validator.validate({
                "billing_mode": "ratio", "billing_unit": "per_token", "static_comparable": True,
                "score_ppm": 2500000,
            })
        except jsonschema.ValidationError:
            rejected = True
        self.check("route_price_comparison_class_required", rejected, "comparable prices require an exact class")
        actual_cost_schema = parsed[self.repository / "parts" / "spec" / "actual-input-cost.schema.json"]
        jsonschema.Draft202012Validator(actual_cost_schema).validate({
            "effective_ppm": 125000, "cache_read_rate_ppm": 900000,
            "source": "optimistic_unobserved", "has_real_evidence": False,
            "optimistic": True, "comparable": True,
        })
        media_schema = parsed[self.repository / "parts" / "spec" / "media-contract.schema.json"]
        jsonschema.Draft202012Validator(media_schema).validate({
            "kind": "video", "request": {"duration_seconds": 10, "duration_known": True},
            "capability": {"reference_image": True, "revision": "fixture"},
        })
        self.check("v0_3_cost_and_media_schema_vectors", True, "actual input cost and media contract fixtures")
        outcome_schema = parsed[self.repository / "parts" / "spec" / "outcome.schema.json"]
        invalid_outcome = {
            "schema_version": "outcome-v1", "contract_id": "fixture", "route_id": "route_sha256_" + "a" * 64,
            "kind": "retryable_failure", "attribution": "route", "commit_state": "committed",
            "acceptance_state": "not_applicable", "replay_class": "safe_text", "retry_allowed": True,
            "reason_code": "fixture_failure", "observed_at_ms": 1,
        }
        rejected = False
        try:
            jsonschema.Draft202012Validator(outcome_schema).validate(invalid_outcome)
        except jsonschema.ValidationError:
            rejected = True
        self.check("committed_retry_negative_vector", rejected, "committed outcome must reject retry_allowed=true")
        invalid_side_effecting = {
            "schema_version": "outcome-v1", "contract_id": "fixture",
            "route_id": "route_sha256_" + "b" * 64, "kind": "retryable_failure",
            "attribution": "route", "commit_state": "buffered", "acceptance_state": "not_accepted",
            "dispatch_state": "started", "replay_class": "side_effecting", "retry_allowed": True,
            "reason_code": "fixture_failure", "observed_at_ms": 1,
        }
        rejected = False
        try:
            jsonschema.Draft202012Validator(outcome_schema).validate(invalid_side_effecting)
        except jsonschema.ValidationError:
            rejected = True
        self.check("side_effecting_second_dispatch_rejected", rejected, "started dispatch must forbid retry")
        parity_schema = parsed[self.repository / "parts" / "spec" / "parity-manifest.schema.json"]
        invalid_parity = {
            "schema_version": "parity-manifest-v1", "generated_at_ms": 1, "artifact": "fixture",
            "form": "agent_parts", "host_commit": "1234567", "smart_router_commit": "7654321",
            "claim": "full_parity", "capabilities": [], "critical_invariants": [],
            "database_results": {"sqlite": "pass", "mysql": "pass", "postgresql": "pass"},
            "roundtrip_results": {"install": "pass", "upgrade": "pass", "rollback": "pass", "uninstall": "pass", "purge": "pass"},
            "known_gaps": [], "evidence_digest": "sha256:" + "a" * 64,
        }
        rejected = False
        try:
            jsonschema.Draft202012Validator(parity_schema).validate(invalid_parity)
        except jsonschema.ValidationError:
            rejected = True
        self.check("agent_parts_full_parity_negative_vector", rejected, "Agent Parts must not claim Full Parity")

    def validate_markdown(self) -> None:
        markdown = sorted(self.repository.rglob("*.md"))
        missing = []
        unbalanced = []
        pattern = re.compile(r"(?<!!)\[[^\]]*\]\(([^)]+)\)")
        for path in markdown:
            text = path.read_text(encoding="utf-8")
            if text.count("```") % 2:
                unbalanced.append(path.relative_to(self.repository).as_posix())
            for raw in pattern.findall(text):
                value = raw.strip().strip("<>").split()[0]
                if not value or value.startswith(("http://", "https://", "mailto:", "#")):
                    continue
                local = urllib.parse.unquote(value.split("#", 1)[0])
                if local and not (path.parent / local).resolve().exists():
                    missing.append({"source": path.relative_to(self.repository).as_posix(), "target": value})
        self.check("markdown_fences", not unbalanced, unbalanced)
        self.check("markdown_local_links", not missing, {"files": len(markdown), "missing": missing[:20]})

    def validate_source_parity(self) -> None:
        receipt = json.loads((self.receipts / "source-snapshot.json").read_text(encoding="utf-8"))
        mismatches = []
        for entry in receipt["files"]:
            if entry["path"].startswith("@public-base/"):
                continue
            path = self.full / entry["path"]
            if not path.is_file() or sha256_file(path) != entry["sha256"]:
                mismatches.append(entry["path"])
        self.check("full_source_snapshot_parity", not mismatches, {"files": len(receipt["files"]), "mismatches": mismatches[:20]})
        full_root = self.full / "pkg" / "smartrouter"
        core_root = self.repository / "core" / "smartrouter"
        parts_root = self.repository / "parts" / "reference" / "go" / "smartrouter"
        full_files = {path.name: path for path in full_root.glob("*.go")}
        core_files = {path.name: path for path in core_root.glob("*.go")}
        parts_files = {path.name: path for path in parts_root.glob("*.go")}
        self.check("core_extraction_file_set", (
            len(full_files) == EXPECTED_CORE_FILE_COUNT
            and set(full_files) == set(core_files) == set(parts_files)
            and {"route_price.go", "actual_input_cost.go", "media_price.go", "media_route.go"}.issubset(full_files)
        ), {"full": sorted(full_files), "core": sorted(core_files), "parts": sorted(parts_files)})

        frontend = receipt.get("frontend_build", {})
        themes = frontend.get("themes", {})
        self.check("isolated_frontend_builds", (
            frontend.get("mode") == "isolated_full_tree_production_build"
            and set(themes) == {"default", "classic"}
            and all(themes[name].get("bundle_files", 0) > 0 for name in themes)
            and not (self.full / "web" / "node_modules").exists()
        ), frontend)
        core_mismatches = []
        for name in sorted(full_files):
            expected = sha256_file(full_files[name])
            for label, candidate in (("core", core_files[name]), ("parts", parts_files[name])):
                if sha256_file(candidate) != expected:
                    core_mismatches.append(f"{label}/{name}")
        self.check("core_extraction_parity", not core_mismatches, {"mismatches": core_mismatches})

        core_vectors = self.repository / "core" / "testdata" / "planner-v1.json"
        parts_vectors = self.repository / "parts" / "conformance" / "golden-vectors" / "planner-v1.json"
        self.check("planner_vector_mirror_digest", (
            sha256_file(core_vectors) == sha256_file(parts_vectors)
        ), {"core": sha256_file(core_vectors), "parts": sha256_file(parts_vectors)})

        import yaml
        parts_manifest = yaml.safe_load((
            self.repository / "parts" / "manifest" / "PARTS-MANIFEST.yaml"
        ).read_text(encoding="utf-8"))
        self.check("parts_manifest_source_snapshot_bound", (
            parts_manifest.get("reference", {}).get("baseline") == receipt.get("source_snapshot_id")
        ), parts_manifest.get("reference", {}).get("baseline"))

    def validate_archives(self) -> None:
        manifest = json.loads((self.artifacts / "RELEASE-MANIFEST.json").read_text(encoding="utf-8"))
        records = {item["name"]: item for item in manifest["artifacts"]}
        zips = sorted(self.artifacts.glob("*.zip"))
        self.check("five_release_archives", len(zips) == 5, [path.name for path in zips])
        for path in zips:
            names = []
            unsafe_paths = []
            wrong_times = []
            symbolic_links = []
            roundtrip_mismatches = []
            with zipfile.ZipFile(path) as archive:
                bad_crc = archive.testzip()
                for info in archive.infolist():
                    pure = PurePosixPath(info.filename)
                    if pure.is_absolute() or ".." in pure.parts or re.match(r"^[A-Za-z]:", info.filename):
                        unsafe_paths.append(info.filename)
                    if info.date_time != FIXED_ZIP_TIME:
                        wrong_times.append({"path": info.filename, "time": info.date_time})
                    mode = (info.external_attr >> 16) & 0xFFFF
                    if stat.S_ISLNK(mode):
                        symbolic_links.append(info.filename)
                    parts = pure.parts
                    if len(parts) < 2:
                        roundtrip_mismatches.append({"path": info.filename, "reason": "missing archive root"})
                    else:
                        source_root = self.full if path.name.endswith("-full-source.zip") else self.repository
                        source_path = source_root.joinpath(*parts[1:])
                        if not source_path.is_file():
                            roundtrip_mismatches.append({"path": info.filename, "reason": "source missing"})
                        else:
                            archived_digest = hashlib.sha256(archive.read(info)).hexdigest()
                            if archived_digest != sha256_file(source_path):
                                roundtrip_mismatches.append({"path": info.filename, "reason": "content digest mismatch"})
                    names.append(info.filename)
            self.check(f"archive_crc:{path.name}", bad_crc is None, bad_crc or "all entries")
            self.check(f"archive_safe_paths:{path.name}", not unsafe_paths, unsafe_paths[:20])
            self.check(f"archive_fixed_times:{path.name}", not wrong_times, wrong_times[:20])
            self.check(f"archive_no_symlinks:{path.name}", not symbolic_links, symbolic_links[:20])
            self.check(f"archive_roundtrip_content:{path.name}", not roundtrip_mismatches, roundtrip_mismatches[:20])
            self.check(f"archive_sorted:{path.name}", names == sorted(names), {"entries": len(names)})
            self.check(f"archive_manifest_hash:{path.name}", path.name in records and records[path.name]["sha256"] == sha256_file(path), records.get(path.name))
        sums = {}
        for line in (self.artifacts / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
            digest, name = line.split("  ", 1)
            sums[name] = digest
        checksum_errors = [path.name for path in self.artifacts.iterdir() if path.is_file() and path.name != "SHA256SUMS" and sums.get(path.name) != sha256_file(path)]
        self.check("sha256sums", not checksum_errors, checksum_errors)

    def validate_build_receipts(self) -> None:
        receipt = json.loads((self.receipts / "build-and-test.json").read_text(encoding="utf-8"))
        failed = [item for item in receipt["commands"] if item["exit_code"] != 0]
        commands = [" ".join(item["command"]) for item in receipt["commands"]]
        self.check("build_and_test_receipts", not failed and len(commands) >= 8, {"commands": commands, "failed": failed})
        binary = next(iter(self.artifacts.glob("*-linux-amd64")), None)
        self.check("linux_binary_present", binary is not None and binary.read_bytes()[:4] == b"\x7fELF", binary.name if binary else "missing")

    def validate_sbom(self) -> None:
        sbom = json.loads((self.artifacts / "SPDX-SBOM.json").read_text(encoding="utf-8"))
        self.check("spdx_header", sbom.get("spdxVersion") == "SPDX-2.3" and sbom.get("dataLicense") == "CC0-1.0", sbom.get("spdxVersion"))
        self.check("spdx_dependencies", len(sbom.get("packages", [])) > 50 and len(sbom.get("relationships", [])) > 40, {"packages": len(sbom.get("packages", [])), "relationships": len(sbom.get("relationships", []))})

    def validate_secret_scan(self) -> None:
        report_path = self.receipts / "secret-scan.json"
        command = [
            sys.executable, str(self.repository / "scripts" / "secret_scan.py"),
            "--root", str(self.repository), "--root", str(self.full),
            "--root", str(self.receipts),
            "--archive-dir", str(self.artifacts), "--json", str(report_path),
        ]
        for term in self.private_terms:
            command.extend(["--deny-term", term])
        completed = subprocess.run(command, text=True, encoding="utf-8", errors="replace", stdout=subprocess.PIPE, stderr=subprocess.STDOUT, check=False)
        report = json.loads(report_path.read_text(encoding="utf-8")) if report_path.is_file() else {}
        self.check("local_secret_scan", completed.returncode == 0 and report.get("high_findings") == 0, {"exit": completed.returncode, "output": completed.stdout[-4000:], "findings": report.get("findings", [])[:20]})

    def finalize(self) -> dict:
        manifest_path = self.artifacts / "RELEASE-MANIFEST.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        repository_commit = manifest.get("repository", {}).get("commit", "")
        public_ready = bool(re.fullmatch(r"[0-9a-f]{40}", repository_commit))
        release_status = "PUBLIC_ALPHA_READY" if public_ready else "LOCAL_RC_READY"
        manifest["status"] = release_status
        manifest["local_validation"] = {
            "status": "pass",
            "checks": len(self.checks),
            "report": "../receipts/validation-report.json",
        }
        manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8", newline="\n")
        sums = []
        for path in sorted(item for item in self.artifacts.iterdir() if item.is_file() and item.name != "SHA256SUMS"):
            sums.append(f"{sha256_file(path)}  {path.name}")
        (self.artifacts / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8", newline="\n")
        report = {
            "schema_version": "release-validation-v1",
            "release": manifest["release"],
            "status": release_status,
            "checks_passed": len(self.checks),
            "checks_failed": 0,
            "checks": self.checks,
            "remaining_external_gates": manifest["remaining_gates"],
        }
        path = self.receipts / "validation-report.json"
        path.write_text(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8", newline="\n")
        return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-root", required=True, type=Path)
    parser.add_argument("--deny-term", action="append", default=[], help="private literal to reject without storing it")
    args = parser.parse_args()
    validator = Validator(args.release_root, args.deny_term)
    try:
        validator.validate_structure()
        validator.validate_public_metadata()
        validator.validate_json_yaml_schema()
        validator.validate_markdown()
        validator.validate_source_parity()
        validator.validate_archives()
        validator.validate_build_receipts()
        validator.validate_sbom()
        validator.validate_secret_scan()
        report = validator.finalize()
    except Exception as exc:
        validator.receipts.mkdir(parents=True, exist_ok=True)
        report = {
            "schema_version": "release-validation-v1",
            "status": "FAIL",
            "error": str(exc),
            "checks": validator.checks,
        }
        (validator.receipts / "validation-report.json").write_text(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8", newline="\n")
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 1
    print(json.dumps({"status": report["status"], "checks_passed": report["checks_passed"]}, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
