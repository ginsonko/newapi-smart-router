#!/usr/bin/env python3
"""Read-only New API Smart Router integration doctor.

The doctor inspects public source structure only. It never applies patches,
reads secret-bearing files, or upgrades a host to Certified status.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Iterable


PROTOCOL_VERSION = "doctor-v1alpha3"
MAX_SOURCE_BYTES = 4 * 1024 * 1024
SAFE_SUFFIXES = {".go", ".ts", ".tsx", ".json", ".md", ".mod", ".sum"}
DENIED_NAMES = {
    ".env",
    ".git",
    ".svn",
    ".hg",
    "node_modules",
    "vendor",
    "logs",
    "log",
    "backups",
    "backup",
    "data",
    "dist",
    "build",
    ".tmp",
    ".cache",
}
DENIED_SUFFIXES = {".key", ".pem", ".p12", ".pfx", ".db", ".sqlite", ".sqlite3", ".log"}


@dataclass(frozen=True)
class HookSpec:
    hook_id: str
    anchors: tuple[str, ...]
    implementation_patterns: tuple[str, ...]


HOOKS = (
    HookSpec("HOOK-PRICE-001", ("service/smart_router_catalog_price.go",), (r"Price", r"Revision")),
    HookSpec("HOOK-POLICY-MEMORY-001", ("model/token.go", "model/token_routing_memory.go", "controller/token.go"), (r"routing_policy_memory", r"ResolveTokenRoutingPolicyMemory")),
    HookSpec("HOOK-CATALOG-SCOPE-001", ("middleware/auth.go", "controller/token.go", "service/smart_router_catalog.go"), (r"GetUserGroup", r"GroupInUserUsableGroups")),
    HookSpec("HOOK-IMAGE-JOB-001", ("controller/image_route_job.go", "model/image_route_job.go"), (r"[Rr]efund", r"[Uu]nknown")),
    HookSpec("HOOK-AUTH-001", ("middleware/auth.go", "model/token.go"), (r"routing_policy", r"TokenRoutingPolicy")),
    HookSpec("HOOK-CONTRACT-001", ("relay/common/relay_info.go",), (r"SmartRoute", r"ContractID")),
    HookSpec("HOOK-ROUTE-001", ("middleware/distributor.go", "controller/relay.go"), (r"SmartRoute", r"ExactRoute")),
    HookSpec("HOOK-COMMIT-001", ("controller/relay.go",), (r"Committed", r"smart_stream_guard")),
    HookSpec("HOOK-OUTCOME-001", ("controller/relay.go",), (r"Outcome", r"semantic")),
    HookSpec("HOOK-BILL-001", ("service/pre_consume_quota.go", "service/quota.go"), (r"SmartRoute", r"RouteReceipt")),
    HookSpec("HOOK-LOG-001", ("service/log_info_generate.go",), (r"SmartRouter", r"smart_router_receipt")),
    HookSpec("HOOK-MEDIA-001", ("controller/task_video.go", "relay/relay_task.go"), (r"SmartRoute", r"ReplaySafeMedia")),
)


@dataclass(frozen=True)
class FeatureSpec:
    feature_id: str
    paths: tuple[str, ...]
    markers: tuple[str, ...]


R52_FEATURES = (
    FeatureSpec(
        "actual_input_cost_and_cache_evidence",
        ("service/smart_router_actual_input_cost.go", "pkg/smartrouter/actual_input_cost.go"),
        (r"ActualInputCost", r"cache"),
    ),
    FeatureSpec(
        "media_price_and_route_contract",
        ("pkg/smartrouter/media_price.go", "pkg/smartrouter/media_route.go"),
        (r"Media", r"RoutePrice"),
    ),
    FeatureSpec(
        "multimodal_model_discovery",
        ("controller/model.go", "router/api-router.go"),
        (r"model", r"models"),
    ),
    FeatureSpec(
        "group_visual_metadata",
        ("setting/group_visuals.go", "model/group_visuals.go"),
        (r"color", r"group"),
    ),
    FeatureSpec(
        "expanded_error_taxonomy",
        ("controller/smart_route_user_error.go", "service/error.go"),
        (r"error", r"retry"),
    ),
)


def is_denied(path: Path, root: Path) -> bool:
    try:
        relative = path.relative_to(root)
    except ValueError:
        return True
    lowered = {part.lower() for part in relative.parts}
    if lowered & DENIED_NAMES:
        return True
    return path.suffix.lower() in DENIED_SUFFIXES


def safe_read(path: Path, root: Path) -> str:
    if is_denied(path, root):
        raise ValueError(f"refusing denied path: {path}")
    if path.suffix.lower() not in SAFE_SUFFIXES and path.name != "go.mod":
        raise ValueError(f"refusing unsupported source type: {path}")
    stat = path.stat()
    if stat.st_size > MAX_SOURCE_BYTES:
        raise ValueError(f"refusing oversized source file: {path}")
    return path.read_text(encoding="utf-8", errors="replace")


def git_value(root: Path, *args: str) -> str:
    try:
        completed = subprocess.run(
            ["git", *args], cwd=root, text=True, encoding="utf-8",
            errors="replace", stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            timeout=5, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return ""
    return completed.stdout.strip() if completed.returncode == 0 else ""


def source_digest(root: Path, paths: Iterable[Path]) -> str:
    digest = hashlib.sha256()
    for path in sorted(paths, key=lambda item: item.as_posix().lower()):
        relative = path.relative_to(root).as_posix()
        digest.update(relative.encode("utf-8"))
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return "sha256:" + digest.hexdigest()


def inspect_features(root: Path) -> list[dict]:
    """Report additive R52 markers without turning them into parity claims."""
    results = []
    for spec in R52_FEATURES:
        present_paths = []
        combined = ""
        for relative in spec.paths:
            path = root / relative
            if path.is_file() and not is_denied(path, root):
                present_paths.append(relative)
                combined += "\n" + safe_read(path, root)
        markers = sorted(
            marker for marker in spec.markers
            if re.search(marker, combined, flags=re.IGNORECASE)
        )
        results.append({
            "feature_id": spec.feature_id,
            "status": "observed_candidate" if present_paths and markers else "not_observed",
            "paths": present_paths,
            "markers": markers,
            "certified": False,
        })
    return results


def inspect(source: Path) -> tuple[dict, int]:
    root = source.resolve(strict=True)
    if not root.is_dir():
        raise ValueError("source must be a directory")
    go_mod_path = root / "go.mod"
    if not go_mod_path.is_file():
        raise ValueError("go.mod was not found; this does not look like a New API source checkout")
    go_mod = safe_read(go_mod_path, root)
    module_match = re.search(r"(?m)^module\s+([^\s]+)\s*$", go_mod)
    module = module_match.group(1) if module_match else ""

    hook_results = []
    fingerprint_paths = {go_mod_path}
    missing_anchor_count = 0
    for spec in HOOKS:
        anchors = []
        combined = ""
        for relative in spec.anchors:
            path = root / relative
            present = path.is_file() and not is_denied(path, root)
            anchors.append({"path": relative, "present": present})
            if present:
                text = safe_read(path, root)
                combined += "\n" + text
                fingerprint_paths.add(path)
        anchor_ready = all(item["present"] for item in anchors)
        if not anchor_ready:
            missing_anchor_count += 1
        evidence = sorted(
            pattern for pattern in spec.implementation_patterns
            if re.search(pattern, combined, flags=re.IGNORECASE)
        )
        hook_results.append({
            "hook_id": spec.hook_id,
            "anchors": anchors,
            "anchor_ready": anchor_ready,
            "implementation_markers": evidence,
            "implementation_status": "observed_candidate" if evidence else "not_observed",
            "certified": False,
        })

    commit = git_value(root, "rev-parse", "HEAD")
    describe = git_value(root, "describe", "--tags", "--always")
    status = git_value(root, "status", "--porcelain=v1", "--untracked-files=no")
    report = {
        "doctor_protocol": PROTOCOL_VERSION,
        "mode": "read_only",
        "source": str(root),
        "host": {
            "module": module,
            "version": describe,
            "commit": commit,
            "dirty_tracked_worktree": bool(status),
            "source_anchor_digest": source_digest(root, fingerprint_paths),
        },
        "hooks": hook_results,
        "r52_features": inspect_features(root),
        "summary": {
            "recognized_new_api_module": module == "github.com/QuantumNous/new-api",
            "critical_hook_count": len(HOOKS),
            "anchor_ready_count": len(HOOKS) - missing_anchor_count,
            "missing_anchor_count": missing_anchor_count,
            "certified": False,
            "full_parity": False,
            "next_action": "review_and_integrate" if missing_anchor_count == 0 else "repair_host_anchors_before_integration",
        },
        "limitations": [
            "Filename and symbol evidence cannot certify semantic behavior.",
            "The doctor does not inspect secrets, databases, production logs, or VCS objects.",
            "Conformance, billing atomicity, commit boundaries, media idempotency, and round-trip tests remain required.",
        ],
    }
    if module != "github.com/QuantumNous/new-api":
        return report, 3
    if missing_anchor_count:
        return report, 4
    return report, 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Read-only New API Smart Router integration doctor")
    parser.add_argument("--source", required=True, type=Path, help="New API source checkout")
    parser.add_argument("--json", dest="json_path", type=Path, help="write machine-readable report")
    args = parser.parse_args()
    try:
        report, exit_code = inspect(args.source)
    except (OSError, ValueError) as exc:
        report = {"doctor_protocol": PROTOCOL_VERSION, "mode": "read_only", "error": str(exc)}
        exit_code = 2
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.json_path:
        target = args.json_path.resolve()
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(rendered, encoding="utf-8", newline="\n")
    sys.stdout.write(rendered)
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
