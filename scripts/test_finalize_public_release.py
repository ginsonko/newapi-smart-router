import contextlib
import hashlib
import io
import json
import subprocess
import sys
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

import build_release
import build_v04
import finalize_public_release
import validate_release
import validate_repository


class FinalizePublicReleaseTest(unittest.TestCase):
    def make_fixture(self, root):
        artifacts = root / "artifacts"
        artifacts.mkdir(parents=True)
        (root / build_release.MARKER_NAME).write_text(build_release.RELEASE_NAME + "\n", encoding="utf-8")
        manifest = {
            "release": build_release.RELEASE_VERSION,
            "status": "LOCAL_RC_READY",
            "repository": {"url": "https://github.com/ginsonko/newapi-smart-router", "commit": "pending_public_commit"},
            "completed_gates": ["local_build_and_tests"],
            "remaining_gates": ["official_revision_certification", "remote_ci"],
            "artifacts": [],
        }
        (artifacts / "RELEASE-MANIFEST.json").write_text(json.dumps(manifest), encoding="utf-8")
        (artifacts / "fixture.zip").write_bytes(b"synthetic artifact content")
        (artifacts / "SHA256SUMS").write_text("old checksum fixture\n", encoding="utf-8")
        return artifacts

    def finalize(self, root, commit="a" * 40):
        return subprocess.run(
            [sys.executable, "-B", str(Path(finalize_public_release.__file__).resolve()), "--release-root", str(root), "--repository-commit", commit],
            capture_output=True, text=True, encoding="utf-8", check=False,
        )

    def snapshot(self, root):
        return {path.relative_to(root).as_posix(): path.read_bytes() for path in root.rglob("*") if path.is_file()}

    def assert_bound(self, artifacts):
        manifest = json.loads((artifacts / "RELEASE-MANIFEST.json").read_text(encoding="utf-8"))
        self.assertEqual("a" * 40, manifest["repository"]["commit"])
        self.assertEqual("public_alpha_validation_pending", manifest["status"])
        self.assertEqual(["official_revision_certification", "remote_ci"], manifest["remaining_gates"])
        self.assertEqual(1, manifest["completed_gates"].count("public_repository_commit"))
        sums = {name: digest for digest, name in (line.split("  ", 1) for line in (artifacts / "SHA256SUMS").read_text().splitlines())}
        self.assertEqual({path.name for path in artifacts.iterdir() if path.name != "SHA256SUMS"}, set(sums))
        for name, digest in sums.items():
            self.assertEqual(hashlib.sha256((artifacts / name).read_bytes()).hexdigest(), digest)

    def test_shared_identity_matches_builder_and_validators(self):
        self.assertEqual(build_release.RELEASE_NAME, finalize_public_release.RELEASE_NAME)
        self.assertEqual(build_release.RELEASE_VERSION, finalize_public_release.RELEASE_VERSION)
        self.assertEqual(build_release.RELEASE_VERSION, validate_release.RELEASE_VERSION)
        self.assertEqual(build_release.RELEASE_VERSION, validate_repository.RELEASE_VERSION)

    def test_binding_updates_checksums_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            artifacts = self.make_fixture(root)
            result = self.finalize(root, "A" * 40)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assert_bound(artifacts)
            before = self.snapshot(root)
            result = self.finalize(root)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertEqual(before, self.snapshot(root))

    def test_rejected_identity_never_mutates_artifacts(self):
        for condition in ("missing-marker", "old-marker", "wrong-release", "missing-release", "wrong-origin", "invalid-commit"):
            with self.subTest(condition=condition), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                artifacts = self.make_fixture(root)
                marker = root / build_release.MARKER_NAME
                manifest_path = artifacts / "RELEASE-MANIFEST.json"
                manifest = json.loads(manifest_path.read_text())
                if condition == "missing-marker":
                    marker.unlink()
                elif condition == "old-marker":
                    marker.write_text("newapi-smart-router-v0.3.0-alpha.1\n")
                elif condition == "wrong-release":
                    manifest["release"] = "v0.3.0-alpha.1"
                elif condition == "missing-release":
                    del manifest["release"]
                elif condition == "wrong-origin":
                    manifest["repository"]["url"] = "https://example.invalid/other"
                manifest_path.write_text(json.dumps(manifest))
                before = self.snapshot(root)
                result = self.finalize(root, "invalid" if condition == "invalid-commit" else "a" * 40)
                self.assertNotEqual(0, result.returncode)
                self.assertEqual(before, self.snapshot(root))

    def test_current_assembly_emits_finalizer_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            template = root / "template"
            (template / "full").mkdir(parents=True)
            archive_path = root / "base.zip"
            content = b"package main\n"
            with zipfile.ZipFile(archive_path, "w") as archive:
                archive.writestr("base/main.go", content)
            manifest = {
                "schema_version": "full-reference-overlay-v2", "release": build_release.RELEASE_VERSION,
                "base": {"sha256": build_release.sha256_file(archive_path), "archive_root": "base"},
                "remove": [], "overlay": [],
                "files": [{"path": "main.go", "sha256": hashlib.sha256(content).hexdigest()}],
            }
            (template / "full/reference-v0.4.json").write_text(json.dumps(manifest))
            output = root / "release"
            arguments = ["build_v04.py", "--base-full-archive", str(archive_path), "--output", str(output), "--go", "unused-fixture-go", "--prepare-only"]
            with patch.object(build_v04, "__file__", str(template / "scripts/build_v04.py")), patch.object(sys, "argv", arguments), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(0, build_v04.main())
            self.assertEqual(content, (output / "full-source/main.go").read_bytes())
            marker = output / build_release.MARKER_NAME
            self.assertEqual(build_release.RELEASE_NAME, marker.read_text().strip())
            fixture = root / "binding-fixture"
            artifacts = self.make_fixture(fixture)
            for source in artifacts.iterdir():
                (output / "artifacts" / source.name).write_bytes(source.read_bytes())
            result = self.finalize(output)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assert_bound(output / "artifacts")

    def test_default_builder_cli_uses_current_assembly(self):
        result = subprocess.run([sys.executable, "-B", str(Path(build_release.__file__).resolve()), "--help"], capture_output=True, text=True, check=False)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("--prepare-only", result.stdout)
        self.assertNotIn("--reference-overlay", result.stdout)


if __name__ == "__main__":
    unittest.main()
