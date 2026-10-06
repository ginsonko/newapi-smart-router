#!/usr/bin/env python3
"""Upload a validated build to an existing matching GitHub draft, never publish it."""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import re
import sys
import time
from pathlib import Path
from urllib.parse import quote, urlencode, urlsplit

from build_release import MARKER_NAME, RELEASE_NAME, RELEASE_VERSION, sha256_file


REPOSITORY = "ginsonko/newapi-smart-router"
METADATA = {"RELEASE-MANIFEST.json", "SHA256SUMS"}
DIGEST = re.compile(r"[0-9a-f]{64}")
COMMIT = re.compile(r"[0-9a-f]{40}")


class UploadError(RuntimeError):
    pass


class APIError(UploadError):
    def __init__(self, status):
        self.status = status
        super().__init__(f"GitHub HTTP {status}; reconcile remote state before another run")


def unique_object(pairs):
    result = {}
    for name, value in pairs:
        if name in result:
            raise UploadError("Duplicate JSON key")
        result[name] = value
    return result


def expected_metadata(value):
    parsed = json.loads(value, object_pairs_hook=unique_object)
    if not isinstance(parsed, dict) or any(name not in METADATA or not isinstance(digest, str) or not DIGEST.fullmatch(digest) for name, digest in parsed.items()):
        raise UploadError("Expected metadata must map only manifest/checksums names to lowercase SHA-256")
    return parsed


class GitHub:
    def __init__(self, token, repository):
        if repository != REPOSITORY or not token:
            raise UploadError("Unexpected repository or missing ephemeral token")
        self.token = token
        self.repository = repository
        self.api = f"https://api.github.com/repos/{repository}"

    def open(self, method, url, body=None, size=None, download=False):
        parsed = urlsplit(url)
        authenticated = parsed.hostname in {"api.github.com", "uploads.github.com"}
        allowed_download = parsed.hostname == "github.com" or (parsed.hostname or "").endswith(".githubusercontent.com")
        if parsed.scheme != "https" or parsed.username or parsed.password or parsed.port not in {None, 443} or parsed.fragment or not (authenticated or (download and allowed_download)):
            raise UploadError("Unexpected GitHub endpoint")
        headers = {"User-Agent": "smart-router-draft-uploader", "Accept": "application/octet-stream" if download else "application/vnd.github+json"}
        if authenticated:
            headers["Authorization"] = "Bearer " + self.token
            headers["X-GitHub-Api-Version"] = "2022-11-28"
        if body is not None:
            headers["Content-Type"] = "application/octet-stream"
            headers["Content-Length"] = str(size)
        connection = http.client.HTTPSConnection(parsed.hostname, timeout=120)
        try:
            connection.request(method, parsed.path + ("?" + parsed.query if parsed.query else ""), body=body, headers=headers)
            return connection, connection.getresponse()
        except Exception:
            connection.close()
            raise UploadError("GitHub transport outcome unknown; reconcile without replay") from None

    def json_request(self, method, url, body=None, size=None):
        connection, response = self.open(method, url, body, size)
        try:
            if not 200 <= response.status < 300:
                raise APIError(response.status)
            data = response.read(8 * 1024 * 1024 + 1)
            if len(data) > 8 * 1024 * 1024:
                raise UploadError("Oversized API response")
            return json.loads(data) if data else None
        finally:
            connection.close()

    def release(self, release_id):
        return self.json_request("GET", f"{self.api}/releases/{release_id}")

    def assets(self, release_id):
        records = []
        for page in range(1, 101):
            batch = self.json_request("GET", f"{self.api}/releases/{release_id}/assets?per_page=100&page={page}")
            if not isinstance(batch, list):
                raise UploadError("Invalid asset list")
            records.extend(batch)
            if len(batch) < 100:
                return records
        raise UploadError("Asset pagination limit exceeded")

    def tag_commit(self, tag):
        try:
            current = self.json_request("GET", f"{self.api}/git/ref/tags/{quote(tag, safe='')}")["object"]
        except APIError as error:
            if error.status == 404:
                return None
            raise
        for _ in range(6):
            if current.get("type") == "commit":
                return current["sha"]
            if current.get("type") != "tag" or not COMMIT.fullmatch(current.get("sha", "")):
                break
            current = self.json_request("GET", f"{self.api}/git/tags/{current['sha']}")["object"]
        raise UploadError("Unresolvable release tag")

    def asset_digest(self, asset):
        declared = asset.get("digest")
        if isinstance(declared, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", declared):
            return declared[7:]
        url = f"{self.api}/releases/assets/{int(asset['id'])}"
        for _ in range(6):
            connection, response = self.open("GET", url, download=True)
            try:
                if response.status in {301, 302, 303, 307, 308}:
                    url = response.getheader("Location", "")
                    continue
                if response.status != 200:
                    raise APIError(response.status)
                digest = hashlib.sha256()
                count = 0
                while chunk := response.read(1024 * 1024):
                    count += len(chunk)
                    if count > asset["size"]:
                        raise UploadError("Downloaded asset exceeds declared size")
                    digest.update(chunk)
                if count != asset["size"]:
                    raise UploadError("Incomplete asset readback")
                return digest.hexdigest()
            finally:
                connection.close()
        raise UploadError("Too many asset redirects")

    def delete(self, asset_id):
        return self.json_request("DELETE", f"{self.api}/releases/assets/{int(asset_id)}")

    def upload(self, release_id, path):
        url = f"https://uploads.github.com/repos/{self.repository}/releases/{release_id}/assets?" + urlencode({"name": path.name})
        with path.open("rb") as body:
            return self.json_request("POST", url, body, path.stat().st_size)


def validated_files(root, sha):
    if (root / MARKER_NAME).read_text(encoding="utf-8").strip() != RELEASE_NAME:
        raise UploadError("Release root marker mismatch")
    artifacts = root / "artifacts"
    manifest = json.loads((artifacts / "RELEASE-MANIFEST.json").read_text(encoding="utf-8"))
    validation = json.loads((root / "receipts/validation-report.json").read_text(encoding="utf-8"))
    if manifest.get("release") != RELEASE_VERSION or manifest.get("repository") != {"url": f"https://github.com/{REPOSITORY}", "commit": sha}:
        raise UploadError("Manifest source identity mismatch")
    local = manifest.get("local_validation", {})
    checks = validation.get("checks", [])
    if manifest.get("status") != "PUBLIC_ALPHA_READY" or local.get("status") != "pass" or validation.get("status") != "PUBLIC_ALPHA_READY" or validation.get("release") != RELEASE_VERSION or validation.get("checks_failed") != 0 or validation.get("checks_passed", 0) < 77 or len(checks) != validation.get("checks_passed") or local.get("checks") != len(checks) or any(check.get("status") != "pass" for check in checks):
        raise UploadError("Full release validation must pass after finalization")
    payload_names = {f"{RELEASE_NAME}-{suffix}" for suffix in ("full-source.zip", "bridge-candidate.zip", "integration-kit.zip", "agent-parts-kit.zip", "documentation.zip", "linux-amd64")}
    names = payload_names | METADATA | {"SPDX-SBOM.json"}
    paths = {path.name: path for path in artifacts.iterdir()}
    if set(paths) != names or any(not path.is_file() or path.is_symlink() for path in paths.values()):
        raise UploadError("Unexpected local artifact inventory")
    files = {name: {"path": path, "sha256": sha256_file(path), "size": path.stat().st_size} for name, path in paths.items()}
    sums = {}
    for line in paths["SHA256SUMS"].read_text(encoding="utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\]+)", line)
        if not match or match[2] in sums:
            raise UploadError("Invalid checksum manifest")
        sums[match[2]] = match[1]
    if sums != {name: entry["sha256"] for name, entry in files.items() if name != "SHA256SUMS"}:
        raise UploadError("Artifact checksums do not match")
    records = manifest.get("artifacts", [])
    if len(records) != len(payload_names) or {entry.get("name") for entry in records} != payload_names or any(entry.get("sha256") != files[entry["name"]]["sha256"] or entry.get("size") != files[entry["name"]]["size"] for entry in records):
        raise UploadError("Artifact records do not match")
    return files


class DraftUploader:
    def __init__(self, github, release_id, tag, sha, expected, report, sleep=time.sleep):
        if not isinstance(release_id, int) or release_id <= 0 or tag != RELEASE_VERSION or not COMMIT.fullmatch(sha):
            raise UploadError("Invalid release ID, version or workflow SHA")
        self.github = github
        self.release_id = release_id
        self.tag = tag
        self.sha = sha
        self.expected = expected
        self.report = report
        self.sleep = sleep

    def identity(self):
        release = self.github.release(self.release_id)
        if release.get("id") != self.release_id or release.get("draft") is not True or release.get("tag_name") != self.tag or release.get("target_commitish") != self.sha:
            raise UploadError("Release must remain the exact matching draft, tag and full target SHA")
        tag_sha = self.github.tag_commit(self.tag)
        if tag_sha is not None and tag_sha != self.sha:
            raise UploadError("Existing tag points to another commit")

    def inventory(self, files):
        assets = self.github.assets(self.release_id)
        result = {}
        for asset in assets:
            name = asset.get("name")
            if name not in files or name in result or not isinstance(asset.get("id"), int):
                raise UploadError("Unexpected or duplicate remote asset")
            result[name] = asset
        return result

    def uploaded_digest(self, asset):
        if asset.get("state") != "uploaded" or not isinstance(asset.get("size"), int) or asset["size"] < 0:
            raise UploadError("Remote asset is incomplete; do not delete or replay it")
        return self.github.asset_digest(asset)

    def classify(self, name, local, remote):
        if remote is None:
            return "upload"
        digest = self.uploaded_digest(remote)
        if digest == local["sha256"] and remote["size"] == local["size"]:
            return "skip"
        if name in METADATA and self.expected.get(name) == digest:
            return "replace_metadata"
        raise UploadError(f"Conflicting asset cannot be overwritten: {name}")

    def reconcile(self, name, files):
        for delay in (0, 2, 5, 10):
            if delay:
                self.sleep(delay)
            self.identity()
            remote = self.inventory(files).get(name)
            if remote is not None and remote.get("state") == "uploaded":
                digest = self.uploaded_digest(remote)
                if digest == files[name]["sha256"] and remote["size"] == files[name]["size"]:
                    return remote
                raise UploadError(f"Upload readback mismatch: {name}")
        raise UploadError(f"Upload outcome unresolved; no POST replay performed: {name}")

    def run(self, files):
        self.identity()
        initial = self.inventory(files)
        plan = {name: self.classify(name, entry, initial.get(name)) for name, entry in files.items()}
        self.report["plan"] = plan
        self.report["actions"] = []
        ordered = sorted(files, key=lambda name: (2 if name == "SHA256SUMS" else 1 if name == "RELEASE-MANIFEST.json" else 0, name))
        for name in ordered:
            self.identity()
            current = self.inventory(files).get(name)
            action = self.classify(name, files[name], current)
            if action != plan[name] or (current is not None and current["id"] != initial[name]["id"]):
                raise UploadError("Remote assets changed since preflight")
            event = {"name": name, "action": action, "sha256": files[name]["sha256"], "size": files[name]["size"]}
            self.report["actions"].append(event)
            if action == "skip":
                event["status"] = "verified_existing"
                continue
            if sha256_file(files[name]["path"]) != files[name]["sha256"]:
                raise UploadError("Local artifact changed after validation")
            if action == "replace_metadata":
                event["replaced_sha256"] = self.uploaded_digest(current)
                event["status"] = "delete_requested"
                try:
                    self.github.delete(current["id"])
                except Exception:
                    event["delete_response"] = "unknown"
                self.identity()
                if name in self.inventory(files):
                    raise UploadError("Metadata deletion not confirmed; no upload attempted")
            self.identity()
            if name in self.inventory(files):
                raise UploadError("Asset appeared before POST; no upload attempted")
            event["status"] = "post_requested"
            try:
                self.github.upload(self.release_id, files[name]["path"])
                event["post_response"] = "received"
            except Exception:
                event["post_response"] = "unknown_or_rejected"
            verified = self.reconcile(name, files)
            event.update({"status": "verified_uploaded", "asset_id": verified["id"]})
        self.identity()
        final = self.inventory(files)
        if set(final) != set(files) or any(self.uploaded_digest(final[name]) != entry["sha256"] or final[name]["size"] != entry["size"] for name, entry in files.items()):
            raise UploadError("Final draft inventory mismatch")
        self.report["status"] = "DRAFT_ASSETS_VERIFIED_NOT_PUBLISHED"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release-root", type=Path, required=True)
    parser.add_argument("--release-id", type=int, required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    report = {"status": "FAILED_NOT_PUBLISHED", "release_id": args.release_id, "tag": args.tag, "workflow_sha": args.sha}
    try:
        if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("GITHUB_EVENT_NAME") != "workflow_dispatch" or os.environ.get("GITHUB_SHA") != args.sha or os.environ.get("GITHUB_REPOSITORY") != REPOSITORY:
            raise UploadError("Only this repository's exact workflow_dispatch SHA is allowed")
        expected = expected_metadata(os.environ.get("EXPECTED_METADATA_JSON", "{}"))
        files = validated_files(args.release_root.resolve(strict=True), args.sha)
        github = GitHub(os.environ.get("GITHUB_TOKEN", ""), REPOSITORY)
        DraftUploader(github, args.release_id, args.tag, args.sha, expected, report).run(files)
        return 0
    except Exception as error:
        report["error"] = str(error) if isinstance(error, UploadError) else type(error).__name__
        return 1
    finally:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8", newline="\n")
        print(json.dumps(report, indent=2))


if __name__ == "__main__":
    raise SystemExit(main())
