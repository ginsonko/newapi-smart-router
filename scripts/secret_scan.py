#!/usr/bin/env python3
"""Scan release trees and ZIP members for likely production secrets."""

from __future__ import annotations

import argparse
import json
import re
import os
import sys
import zipfile
from pathlib import Path


TEXT_SUFFIXES = {
    ".go", ".py", ".ps1", ".sh", ".cmd", ".bat", ".ts", ".tsx", ".js",
    ".mjs", ".json", ".yaml", ".yml", ".toml", ".md", ".txt", ".sql",
    ".html", ".css", ".scss", ".mod", ".sum", ".xml", ".env",
}
SKIP_COMPONENTS = {".git", "node_modules", ".cache", ".tmp", "vendor"}
MAX_TEXT_BYTES = 16 * 1024 * 1024
ALLOW_CONTEXT = re.compile(
    r"(?i)(<API_KEY>|placeholder|synthetic|redacted|example(?:\.com|\.invalid)?|dummy|fake|"
    r"secret-without-delimiter|task-logs-table|pattern|regex|scanner|test fixture|noassertion)"
)
RULES = (
    ("openai_style_key", "high", re.compile(r"\bsk-[A-Za-z0-9_-]{20,}\b")),
    ("github_token", "high", re.compile(r"\b(?:ghp|gho|ghu|ghs|github_pat)_[A-Za-z0-9_]{20,}\b")),
    ("aws_access_key", "high", re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b")),
    ("slack_token", "high", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{20,}\b")),
    ("private_key_material", "high", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----\s+[A-Za-z0-9+/=\r\n]{80,}", re.DOTALL)),
    ("bearer_credential", "high", re.compile(r"(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{24,}\b")),
    ("production_server_path", "high", re.compile(r"(?i)/opt/(?:new-api|sub2api|1panel)(?:/|\b)")),
    ("codex_private_path", "high", re.compile(r"(?i)\.codex[/\\](?:attachments|memories|auth|sessions)")),
)


def looks_text(name: str, data: bytes) -> bool:
    if len(data) > MAX_TEXT_BYTES or b"\0" in data[:8192]:
        return False
    suffix = Path(name).suffix.lower()
    return suffix in TEXT_SUFFIXES or not suffix


def private_rules(extra_terms: list[str]) -> tuple:
    rules = list(RULES)
    home = str(Path.home()).strip()
    if home:
        rules.append(("local_user_path", "high", re.compile(re.escape(home), re.IGNORECASE)))
    env_terms = os.environ.get("SMART_ROUTER_PRIVATE_DENY_TERMS", "").split(os.pathsep)
    for index, term in enumerate([*env_terms, *extra_terms], start=1):
        term = term.strip()
        if term:
            rules.append((f"private_term_{index}", "high", re.compile(re.escape(term), re.IGNORECASE)))
    return tuple(rules)


def scan_text(label: str, text: str, rules: tuple) -> list[dict]:
    findings = []
    for rule_id, severity, pattern in rules:
        for match in pattern.finditer(text):
            start = max(0, match.start() - 100)
            end = min(len(text), match.end() + 100)
            context = text[start:end].replace("\r", " ").replace("\n", " ")
            if ALLOW_CONTEXT.search(context):
                continue
            line = text.count("\n", 0, match.start()) + 1
            findings.append({
                "rule": rule_id,
                "severity": severity,
                "location": label,
                "line": line,
                "match_sha256_prefix": __import__("hashlib").sha256(match.group(0).encode("utf-8", errors="replace")).hexdigest()[:16],
            })
    return findings


def scan_tree(root: Path, label: str, rules: tuple) -> tuple[list[dict], int]:
    findings = []
    files_scanned = 0
    for path in root.rglob("*"):
        if not path.is_file() or any(part in SKIP_COMPONENTS for part in path.relative_to(root).parts):
            continue
        data = path.read_bytes()
        if not looks_text(path.name, data):
            continue
        files_scanned += 1
        text = data.decode("utf-8", errors="replace")
        findings.extend(scan_text(f"{label}/{path.relative_to(root).as_posix()}", text, rules))
    return findings, files_scanned


def scan_archive(path: Path, rules: tuple) -> tuple[list[dict], int]:
    findings = []
    files_scanned = 0
    with zipfile.ZipFile(path) as archive:
        for info in archive.infolist():
            if info.is_dir() or info.file_size > MAX_TEXT_BYTES:
                continue
            data = archive.read(info)
            if not looks_text(info.filename, data):
                continue
            files_scanned += 1
            findings.extend(scan_text(f"archive:{path.name}!/{info.filename}", data.decode("utf-8", errors="replace"), rules))
    return findings, files_scanned


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", action="append", default=[], type=Path, help="tree to scan; may repeat")
    parser.add_argument("--archive-dir", type=Path, help="scan every ZIP in this directory")
    parser.add_argument("--deny-term", action="append", default=[], help="private literal to reject; may repeat and is never written to the report")
    parser.add_argument("--json", dest="json_path", required=True, type=Path)
    args = parser.parse_args()
    findings = []
    scanned = 0
    rules = private_rules(args.deny_term)
    for index, root_arg in enumerate(args.root, start=1):
        root = root_arg.resolve(strict=True)
        current, count = scan_tree(root, f"root-{index}", rules)
        findings.extend(current)
        scanned += count
    archives = 0
    if args.archive_dir:
        for archive in sorted(args.archive_dir.resolve(strict=True).glob("*.zip")):
            current, count = scan_archive(archive, rules)
            findings.extend(current)
            scanned += count
            archives += 1
    high = [finding for finding in findings if finding["severity"] == "high"]
    report = {
        "schema_version": "secret-scan-v1",
        "status": "pass" if not high else "fail",
        "scanner": "local-pattern-scanner-v1",
        "files_scanned": scanned,
        "archives_scanned": archives,
        "private_rule_count": max(0, len(rules) - len(RULES)),
        "high_findings": len(high),
        "findings": findings,
        "limitations": [
            "Pattern scanning cannot prove the absence of every secret.",
            "Run full-history gitleaks and trufflehog scans before public upload.",
            "Run an image-layer scanner after the public container image is built.",
        ],
    }
    args.json_path.parent.mkdir(parents=True, exist_ok=True)
    args.json_path.write_text(json.dumps(report, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(json.dumps({"status": report["status"], "files_scanned": scanned, "archives_scanned": archives, "high_findings": len(high)}, indent=2))
    return 0 if not high else 1


if __name__ == "__main__":
    raise SystemExit(main())
