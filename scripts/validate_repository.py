#!/usr/bin/env python3
"""Validate the public source repository independently from release assets."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import urllib.parse
from pathlib import Path

import jsonschema
import yaml


RELEASE_VERSION = "v0.3.0-alpha.1"
BRIDGE_PROTOCOL = "bridge-spi-v1alpha3"
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


class RepositoryValidator:
    def __init__(self, root: Path):
        self.root = root.resolve(strict=True)
        self.checks: list[dict] = []

    def check(self, name: str, condition: bool, detail: object = "") -> None:
        self.checks.append({"name": name, "status": "pass" if condition else "fail", "detail": detail})
        if not condition:
            raise AssertionError(f"{name}: {detail}")

    def validate_structure(self) -> None:
        required = [
            "README.md", "LICENSE", "NOTICE", "UPSTREAM-NOTICE",
            "THIRD-PARTY-LICENSES.md", "COMMERCIAL-LICENSE.md",
            "RELEASE-STATUS.md", "SECURITY.md", "CONTRIBUTING.md",
            "core/go.mod", "bridge/newapi/go.mod", "integration/doctor/doctor.py",
            "parts/manifest/PARTS-MANIFEST.yaml", "parts/manifest/capability-matrix.yaml",
            "parts/spec/route-price.schema.json", "scripts/secret_scan.py",
            "scripts/sync_reference_core.py", "scripts/validate_repository.py",
            ".github/workflows/ci.yml", ".github/ISSUE_TEMPLATE/bug-report.yml",
            ".github/ISSUE_TEMPLATE/commercial-license.yml",
        ]
        missing = [path for path in required if not (self.root / path).is_file()]
        self.check("required_public_structure", not missing, missing)
        generated = [
            path.relative_to(self.root).as_posix() for path in self.root.rglob("*")
            if path.name in {"__pycache__", ".pytest_cache"} or (path.is_file() and path.suffix.lower() == ".pyc")
        ]
        self.check("no_generated_python_caches", not generated, generated[:20])

    def validate_structured_files(self) -> None:
        json_files = sorted(self.root.rglob("*.json"))
        parsed = {path: json.loads(path.read_text(encoding="utf-8")) for path in json_files}
        self.check("json_parse", bool(json_files), {"files": len(json_files)})
        yaml_files = sorted(self.root.rglob("*.yaml")) + sorted(self.root.rglob("*.yml"))
        for path in yaml_files:
            yaml.safe_load(path.read_text(encoding="utf-8"))
        self.check("yaml_parse", bool(yaml_files), {"files": len(yaml_files)})
        schemas = sorted((self.root / "parts" / "spec").glob("*.schema.json"))
        for path in schemas:
            jsonschema.Draft202012Validator.check_schema(parsed[path])
        self.check("json_schema_valid", len(schemas) >= 9, {"schemas": len(schemas)})
        parts_manifest = yaml.safe_load((self.root / "parts" / "manifest" / "PARTS-MANIFEST.yaml").read_text(encoding="utf-8"))
        capability_matrix = yaml.safe_load((self.root / "parts" / "manifest" / "capability-matrix.yaml").read_text(encoding="utf-8"))
        self.check("v0_3_parts_versions", (
            parts_manifest.get("manifest_version") == "3.0.0-alpha.1"
            and capability_matrix.get("matrix_version") == "3.0.0-alpha.1"
        ), {"parts": parts_manifest.get("manifest_version"), "matrix": capability_matrix.get("matrix_version")})
        route_price_schema = parsed[self.root / "parts" / "spec" / "route-price.schema.json"]
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
        actual_cost_schema = parsed[self.root / "parts" / "spec" / "actual-input-cost.schema.json"]
        jsonschema.Draft202012Validator(actual_cost_schema).validate({
            "effective_ppm": 125000, "cache_read_rate_ppm": 900000,
            "source": "optimistic_unobserved", "has_real_evidence": False,
            "optimistic": True, "comparable": True,
        })
        media_schema = parsed[self.root / "parts" / "spec" / "media-contract.schema.json"]
        jsonschema.Draft202012Validator(media_schema).validate({
            "kind": "video", "request": {"duration_seconds": 10, "duration_known": True},
            "capability": {"reference_image": True, "revision": "fixture"},
        })
        self.check("v0_3_cost_and_media_schema_vectors", True, "actual input cost and media contract fixtures")

        outcome_schema = parsed[self.root / "parts" / "spec" / "outcome.schema.json"]
        invalid_side_effecting = {
            "schema_version": "outcome-v1", "contract_id": "fixture",
            "route_id": "route_sha256_" + "a" * 64, "kind": "retryable_failure",
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

    def validate_markdown(self) -> None:
        markdown = sorted(self.root.rglob("*.md"))
        missing = []
        unbalanced = []
        pattern = re.compile(r"(?<!!)\[[^\]]*\]\(([^)]+)\)")
        for path in markdown:
            text = path.read_text(encoding="utf-8")
            if text.count("```") % 2:
                unbalanced.append(path.relative_to(self.root).as_posix())
            for raw in pattern.findall(text):
                value = raw.strip().strip("<>").split()[0]
                if not value or value.startswith(("http://", "https://", "mailto:", "#")):
                    continue
                local = urllib.parse.unquote(value.split("#", 1)[0])
                if local and not (path.parent / local).resolve().exists():
                    missing.append({"source": path.relative_to(self.root).as_posix(), "target": value})
        self.check("markdown_fences", not unbalanced, unbalanced)
        self.check("markdown_local_links", not missing, missing[:20])

    def validate_public_contract(self) -> None:
        expected_origin = "https://github.com/ginsonko/newapi-smart-router"
        expected_core = "github.com/ginsonko/newapi-smart-router/core"
        expected_bridge = "github.com/ginsonko/newapi-smart-router/bridge/newapi"
        readme = (self.root / "README.md").read_text(encoding="utf-8")
        status = (self.root / "RELEASE-STATUS.md").read_text(encoding="utf-8")
        license_text = (self.root / "LICENSE").read_text(encoding="utf-8")
        commercial = (self.root / "COMMERCIAL-LICENSE.md").read_text(encoding="utf-8")
        core_mod = (self.root / "core" / "go.mod").read_text(encoding="utf-8")
        bridge_mod = (self.root / "bridge" / "newapi" / "go.mod").read_text(encoding="utf-8")
        matrix = (self.root / "parts" / "manifest" / "capability-matrix.yaml").read_text(encoding="utf-8")
        bridge_source = (self.root / "bridge" / "newapi" / "bridge.go").read_text(encoding="utf-8")
        bridge_manifest = json.loads((
            self.root / "bridge" / "newapi" / "compatibility" / "v1.0.0-rc.20-candidate.json"
        ).read_text(encoding="utf-8"))
        public_text_files = [
            path for path in self.root.rglob("*")
            if path.is_file() and path.suffix.lower() in {".go", ".mod", ".md", ".py", ".json", ".yaml", ".yml"}
        ]
        local_module_marker = "smart-router" + ".local"
        local_version_marker = RELEASE_VERSION + "-" + "local"
        local_markers = []
        for path in public_text_files:
            text = path.read_text(encoding="utf-8", errors="replace")
            if local_module_marker in text or local_version_marker in text:
                local_markers.append(path.relative_to(self.root).as_posix())
        self.check("public_origin", expected_origin in readme, "README origin")
        self.check("public_release_version", RELEASE_VERSION in readme, "README version")
        self.check("v0_3_delta_documented", "docs/v0.3-contract-delta.md" in readme, "README v0.3 link")
        self.check("public_core_module", core_mod.startswith(f"module {expected_core}\n"), core_mod.splitlines()[0])
        self.check("public_bridge_module", bridge_mod.startswith(f"module {expected_bridge}\n") and expected_core in bridge_mod, bridge_mod.splitlines()[:8])
        self.check("no_local_release_markers", not local_markers, local_markers)
        self.check("upstream_rc21_drift_disclosed", "v1.0.0-rc.21" in status and "rc.20" in status, "RELEASE-STATUS.md")
        self.check("agpl_preserved", "GNU AFFERO GENERAL PUBLIC LICENSE" in license_text and "Version 3" in license_text, "LICENSE")
        self.check("commercial_scope_bounded", all(value in commercial for value in (
            "AGPL-3.0-only", "does not grant", "QuantumNous New API", "not a license grant",
        )), "COMMERCIAL-LICENSE.md")
        self.check("public_history_gate_closed", "public repository is not rebuilt from clean upstream history" not in matrix, "capability matrix")
        self.check("bridge_protocol_v1alpha3", (
            f'ProtocolVersion = "{BRIDGE_PROTOCOL}"' in bridge_source
            and bridge_manifest.get("bridge_protocol") == BRIDGE_PROTOCOL
            and "HOOK-PRICE-001" in bridge_manifest.get("required_hooks", [])
            and "multimodal_models_discovery" in bridge_manifest.get("observed_r52_capabilities", [])
        ), bridge_manifest.get("bridge_protocol"))

        line_count, prefix_digest = normalized_readme_prefix(self.root / "README.md")
        self.check("readme_v0_2_strict_prefix_preserved", (
            line_count > README_BASELINE_LINES and prefix_digest == README_BASELINE_SHA256
        ), {"baseline_lines": README_BASELINE_LINES, "lines": line_count, "prefix_sha256": prefix_digest})

    def validate_core_mirrors(self) -> None:
        core_root = self.root / "core" / "smartrouter"
        parts_root = self.root / "parts" / "reference" / "go" / "smartrouter"
        core_files = {path.name: path for path in core_root.glob("*.go")}
        parts_files = {path.name: path for path in parts_root.glob("*.go")}
        self.check("smart_router_core_file_set", (
            len(core_files) == EXPECTED_CORE_FILE_COUNT
            and set(core_files) == set(parts_files)
            and {"route_price.go", "actual_input_cost.go", "media_price.go", "media_route.go"}.issubset(core_files)
        ), {"core": sorted(core_files), "parts": sorted(parts_files)})
        mismatches = [
            name for name in sorted(core_files)
            if sha256_file(core_files[name]) != sha256_file(parts_files[name])
        ]
        self.check("smart_router_core_mirror_digest", not mismatches, mismatches)

        core_vectors = self.root / "core" / "testdata" / "planner-v1.json"
        parts_vectors = self.root / "parts" / "conformance" / "golden-vectors" / "planner-v1.json"
        self.check("planner_vector_mirror_digest", (
            sha256_file(core_vectors) == sha256_file(parts_vectors)
        ), {"core": sha256_file(core_vectors), "parts": sha256_file(parts_vectors)})

    def run(self) -> dict:
        self.validate_structure()
        self.validate_structured_files()
        self.validate_markdown()
        self.validate_public_contract()
        self.validate_core_mirrors()
        return {
            "schema_version": "repository-validation-v1",
            "status": "PUBLIC_REPOSITORY_READY",
            "checks_passed": len(self.checks),
            "checks": self.checks,
        }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path("."))
    args = parser.parse_args()
    validator = RepositoryValidator(args.root)
    try:
        report = validator.run()
    except Exception as exc:
        print(json.dumps({
            "schema_version": "repository-validation-v1",
            "status": "FAIL",
            "error": str(exc),
            "checks": validator.checks,
        }, ensure_ascii=False, indent=2))
        return 1
    print(json.dumps({
        "status": report["status"],
        "checks_passed": report["checks_passed"],
    }, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
