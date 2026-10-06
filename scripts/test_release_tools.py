import json
import tempfile
import unittest
import zipfile
from pathlib import Path

import build_release
import build_v04
import secret_scan


class ReleaseToolTest(unittest.TestCase):
    def test_manifest_paths_reject_escape(self):
        for value in ("../outside", "/absolute", "C:/local", "folder\\file"):
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                build_v04.safe_member(value)

    def test_private_literal_is_not_suppressed_by_example_context(self):
        rules = secret_scan.private_rules(["private-fixture-value"])
        findings = secret_scan.scan_text("fixture", "example private-fixture-value", rules)
        self.assertEqual(1, len(findings))
        self.assertNotIn("private-fixture-value", json.dumps(findings))

    def test_pinned_assembly_rejects_modified_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            template = root / "repository"
            (template / "full").mkdir(parents=True)
            archive = root / "base.zip"
            with zipfile.ZipFile(archive, "w") as bundle:
                bundle.writestr("base/main.go", "package main\n")
            manifest = {
                "schema_version": "full-reference-overlay-v2",
                "release": build_release.RELEASE_VERSION,
                "base": {"sha256": build_release.sha256_file(archive), "archive_root": "base"},
                "remove": [], "overlay": [],
                "files": [{"path": "main.go", "sha256": "0" * 64}],
            }
            (template / "full/reference-v0.4.json").write_text(json.dumps(manifest))
            with self.assertRaisesRegex(RuntimeError, "differs from pinned"):
                build_v04.assemble(template, archive, root / "output")


if __name__ == "__main__":
    unittest.main()
