#!/usr/bin/env python3
"""Validate the public source repository independently from release assets."""

from __future__ import annotations

import argparse
import json
import re
import urllib.parse
from pathlib import Path

import jsonschema
import yaml


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
            "scripts/secret_scan.py", "scripts/validate_repository.py",
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
        self.check("json_schema_valid", len(schemas) >= 8, {"schemas": len(schemas)})

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
        public_text_files = [
            path for path in self.root.rglob("*")
            if path.is_file() and path.suffix.lower() in {".go", ".mod", ".md", ".py", ".json", ".yaml", ".yml"}
        ]
        local_module_marker = "smart-router" + ".local"
        local_version_marker = "v0.1.0-alpha.1-" + "local"
        local_markers = []
        for path in public_text_files:
            text = path.read_text(encoding="utf-8", errors="replace")
            if local_module_marker in text or local_version_marker in text:
                local_markers.append(path.relative_to(self.root).as_posix())
        self.check("public_origin", expected_origin in readme, "README origin")
        self.check("public_release_version", "v0.1.0-alpha.1" in readme, "README version")
        self.check("public_core_module", core_mod.startswith(f"module {expected_core}\n"), core_mod.splitlines()[0])
        self.check("public_bridge_module", bridge_mod.startswith(f"module {expected_bridge}\n") and expected_core in bridge_mod, bridge_mod.splitlines()[:8])
        self.check("no_local_release_markers", not local_markers, local_markers)
        self.check("upstream_rc21_drift_disclosed", "v1.0.0-rc.21" in status and "rc.20" in status, "RELEASE-STATUS.md")
        self.check("agpl_preserved", "GNU AFFERO GENERAL PUBLIC LICENSE" in license_text and "Version 3" in license_text, "LICENSE")
        self.check("commercial_scope_bounded", all(value in commercial for value in (
            "AGPL-3.0-only", "does not grant", "QuantumNous New API", "not a license grant",
        )), "COMMERCIAL-LICENSE.md")
        self.check("public_history_gate_closed", "public repository is not rebuilt from clean upstream history" not in matrix, "capability matrix")

    def run(self) -> dict:
        self.validate_structure()
        self.validate_structured_files()
        self.validate_markdown()
        self.validate_public_contract()
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
