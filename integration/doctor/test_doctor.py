from __future__ import annotations

import hashlib
import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("doctor.py")
SPEC = importlib.util.spec_from_file_location("smart_router_doctor", MODULE_PATH)
doctor = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
sys.modules[SPEC.name] = doctor
SPEC.loader.exec_module(doctor)


class DoctorTest(unittest.TestCase):
    def make_host(self, root: Path, module: str = "github.com/QuantumNous/new-api") -> None:
        (root / "go.mod").write_text(f"module {module}\n\ngo 1.25.1\n", encoding="utf-8")
        for spec in doctor.HOOKS:
            for relative in spec.anchors:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("package fixture\n", encoding="utf-8")

    def tree_hash(self, root: Path) -> str:
        digest = hashlib.sha256()
        for path in sorted(item for item in root.rglob("*") if item.is_file()):
            digest.update(path.relative_to(root).as_posix().encode())
            digest.update(path.read_bytes())
        return digest.hexdigest()

    def test_supported_anchor_scan_is_read_only(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_host(root)
            before = self.tree_hash(root)
            report, code = doctor.inspect(root)
            after = self.tree_hash(root)
            self.assertEqual(0, code)
            self.assertEqual(before, after)
            self.assertEqual(len(doctor.HOOKS), report["summary"]["anchor_ready_count"])
            self.assertFalse(report["summary"]["certified"])
            self.assertEqual(len(doctor.R52_FEATURES), len(report["r52_features"]))
            self.assertTrue(all(not feature["certified"] for feature in report["r52_features"]))

    def test_unknown_module_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_host(root, "example.invalid/custom-gateway")
            report, code = doctor.inspect(root)
            self.assertEqual(3, code)
            self.assertFalse(report["summary"]["recognized_new_api_module"])

    def test_missing_anchor_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_host(root)
            (root / doctor.HOOKS[0].anchors[0]).unlink()
            report, code = doctor.inspect(root)
            self.assertEqual(4, code)
            self.assertEqual(1, report["summary"]["missing_anchor_count"])

    def test_secret_path_is_refused(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / ".env"
            path.write_text("TOKEN=synthetic\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                doctor.safe_read(path, root)


if __name__ == "__main__":
    unittest.main()
