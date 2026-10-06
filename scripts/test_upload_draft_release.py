import copy
import hashlib
import io
import json
import os
import tempfile
import unittest
import subprocess
import sys
from pathlib import Path
from unittest.mock import patch

import yaml

import upload_draft_release as uploader


class FakeGitHub:
    def __init__(self):
        self.release_record = {"id": 42, "draft": True, "tag_name": uploader.RELEASE_VERSION, "target_commitish": "a" * 40}
        self.tag_sha = None
        self.records = {}
        self.calls = []
        self.next_id = 1
        self.upload_mode = "success"
        self.delete_mode = "success"
        self.on_release = None

    def add(self, name, content, state="uploaded"):
        record = {"id": self.next_id, "name": name, "size": len(content), "state": state, "digest": "sha256:" + hashlib.sha256(content).hexdigest()}
        self.records[name] = record
        self.next_id += 1
        return record

    def release(self, release_id):
        if self.on_release:
            self.on_release()
        return copy.deepcopy(self.release_record)

    def tag_commit(self, tag):
        return self.tag_sha

    def assets(self, release_id):
        return copy.deepcopy(list(self.records.values()))

    def asset_digest(self, asset):
        return asset["digest"][7:]

    def delete(self, asset_id):
        self.calls.append(("DELETE", asset_id))
        if self.delete_mode != "unknown_retained":
            self.records = {name: entry for name, entry in self.records.items() if entry["id"] != asset_id}
        if self.delete_mode != "success":
            raise uploader.UploadError("synthetic uncertain delete")

    def upload(self, release_id, path):
        self.calls.append(("POST", path.name))
        if self.upload_mode not in {"unknown_missing", "unknown_partial"}:
            self.add(path.name, path.read_bytes() if self.upload_mode != "wrong_content" else b"wrong")
        elif self.upload_mode == "unknown_partial":
            self.add(path.name, b"", state="starter")
        if self.upload_mode.startswith("unknown"):
            raise uploader.UploadError("synthetic uncertain POST")


class DraftUploadTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.github = FakeGitHub()
        self.report = {}

    def files(self, names):
        result = {}
        for name in names:
            path = self.root / name
            path.write_bytes(("new " + name).encode())
            result[name] = {"path": path, "sha256": uploader.sha256_file(path), "size": path.stat().st_size}
        return result

    def run_upload(self, files, expected=None):
        instance = uploader.DraftUploader(self.github, 42, uploader.RELEASE_VERSION, "a" * 40, expected or {}, self.report, sleep=lambda delay: None)
        instance.run(files)

    def test_success_then_idempotent_skip_with_metadata_last(self):
        files = self.files(["SHA256SUMS", "RELEASE-MANIFEST.json", "binary"])
        self.run_upload(files)
        self.assertEqual([("POST", "binary"), ("POST", "RELEASE-MANIFEST.json"), ("POST", "SHA256SUMS")], self.github.calls)
        self.assertEqual("DRAFT_ASSETS_VERIFIED_NOT_PUBLISHED", self.report["status"])
        self.github.calls.clear()
        self.run_upload(files)
        self.assertEqual([], self.github.calls)
        self.assertTrue(all(entry["status"] == "verified_existing" for entry in self.report["actions"]))

    def test_reject_wrong_release_identity_before_any_mutation(self):
        files = self.files(["binary"])
        for field, value in (("id", 43), ("draft", False), ("tag_name", "other"), ("target_commitish", "main"), ("target_commitish", "b" * 40)):
            with self.subTest(field=field, value=value):
                old = self.github.release_record[field]
                self.github.release_record[field] = value
                with self.assertRaises(uploader.UploadError):
                    self.run_upload(files)
                self.github.release_record[field] = old
        self.github.tag_sha = "b" * 40
        with self.assertRaisesRegex(uploader.UploadError, "tag points"):
            self.run_upload(files)
        self.assertEqual([], self.github.calls)

    def test_binary_conflict_blocks_entire_batch(self):
        files = self.files(["absent", "binary", "SHA256SUMS"])
        self.github.add("binary", b"old binary")
        with self.assertRaisesRegex(uploader.UploadError, "cannot be overwritten"):
            self.run_upload(files, {"binary": hashlib.sha256(b"old binary").hexdigest()})
        self.assertEqual([], self.github.calls)

    def test_metadata_requires_exact_explicit_expected_digest(self):
        files = self.files(["RELEASE-MANIFEST.json"])
        remote = self.github.add("RELEASE-MANIFEST.json", b"old metadata")
        for expected in ({}, {"RELEASE-MANIFEST.json": "0" * 64}):
            with self.subTest(expected=expected), self.assertRaisesRegex(uploader.UploadError, "cannot be overwritten"):
                self.run_upload(files, expected)
        self.assertEqual([], self.github.calls)
        self.run_upload(files, {"RELEASE-MANIFEST.json": remote["digest"][7:]})
        self.assertEqual([("DELETE", remote["id"]), ("POST", "RELEASE-MANIFEST.json")], self.github.calls)

    def test_uncertain_upload_with_accepted_asset_is_reconciled_once(self):
        self.github.upload_mode = "unknown_accepted"
        self.run_upload(self.files(["binary"]))
        self.assertEqual([("POST", "binary")], self.github.calls)
        self.assertEqual("verified_uploaded", self.report["actions"][0]["status"])

    def test_uncertain_missing_or_partial_upload_never_replays(self):
        files = self.files(["binary"])
        for mode in ("unknown_missing", "unknown_partial"):
            with self.subTest(mode=mode):
                self.github = FakeGitHub()
                self.github.upload_mode = mode
                with self.assertRaisesRegex(uploader.UploadError, "unresolved"):
                    self.run_upload(files)
                self.assertEqual([("POST", "binary")], self.github.calls)

    def test_wrong_upload_bytes_stop_without_retry(self):
        self.github.upload_mode = "wrong_content"
        with self.assertRaisesRegex(uploader.UploadError, "readback mismatch"):
            self.run_upload(self.files(["binary"]))
        self.assertEqual([("POST", "binary")], self.github.calls)

    def test_uncertain_metadata_delete_requires_absence_before_post(self):
        files = self.files(["SHA256SUMS"])
        remote = self.github.add("SHA256SUMS", b"old")
        expected = {"SHA256SUMS": remote["digest"][7:]}
        self.github.delete_mode = "unknown_retained"
        with self.assertRaisesRegex(uploader.UploadError, "deletion not confirmed"):
            self.run_upload(files, expected)
        self.assertEqual([("DELETE", remote["id"])], self.github.calls)
        self.github.calls.clear()
        self.github.delete_mode = "unknown_deleted"
        self.run_upload(files, expected)
        self.assertEqual([("DELETE", remote["id"]), ("POST", "SHA256SUMS")], self.github.calls)

    def test_draft_changed_after_preflight_prevents_upload(self):
        count = 0

        def publish_between_checks():
            nonlocal count
            count += 1
            if count == 2:
                self.github.release_record["draft"] = False

        self.github.on_release = publish_between_checks
        with self.assertRaisesRegex(uploader.UploadError, "exact matching draft"):
            self.run_upload(self.files(["binary"]))
        self.assertEqual([], self.github.calls)

    def test_remote_metadata_changed_after_preflight_is_not_deleted(self):
        files = self.files(["SHA256SUMS"])
        remote = self.github.add("SHA256SUMS", b"old")
        count = 0

        def replace_between_checks():
            nonlocal count
            count += 1
            if count == 2:
                self.github.add("SHA256SUMS", b"another maintainer change")

        self.github.on_release = replace_between_checks
        with self.assertRaises(uploader.UploadError):
            self.run_upload(files, {"SHA256SUMS": remote["digest"][7:]})
        self.assertEqual([], self.github.calls)

    def test_duplicate_extra_and_incomplete_assets_rejected(self):
        files = self.files(["binary"])
        self.github.add("unexpected", b"old")
        with self.assertRaisesRegex(uploader.UploadError, "Unexpected"):
            self.run_upload(files)
        self.github.records.clear()
        self.github.add("binary", b"", "starter")
        with self.assertRaisesRegex(uploader.UploadError, "incomplete"):
            self.run_upload(files)
        self.github.records.clear()
        record = self.github.add("binary", b"old")
        with patch.object(self.github, "assets", return_value=[record, record]), self.assertRaisesRegex(uploader.UploadError, "duplicate"):
            self.run_upload(files)
        self.assertEqual([], self.github.calls)

    def test_expected_metadata_parser_rejects_unknown_and_duplicate_keys(self):
        for value in ('[]', '{"binary":"' + "a" * 64 + '"}', '{"SHA256SUMS":"invalid"}', '{"SHA256SUMS":"' + "a" * 64 + '","SHA256SUMS":"' + "b" * 64 + '"}'):
            with self.subTest(value=value), self.assertRaises(uploader.UploadError):
                uploader.expected_metadata(value)
        self.assertEqual({}, uploader.expected_metadata('{}'))

    def fixture_release(self):
        root = self.root / "release"
        artifacts = root / "artifacts"
        artifacts.mkdir(parents=True)
        (root / "receipts").mkdir()
        (root / uploader.MARKER_NAME).write_text(uploader.RELEASE_NAME)
        records = []
        for suffix in ("full-source.zip", "bridge-candidate.zip", "integration-kit.zip", "agent-parts-kit.zip", "documentation.zip", "linux-amd64"):
            path = artifacts / (uploader.RELEASE_NAME + "-" + suffix)
            path.write_bytes(b"synthetic content")
            records.append({"name": path.name, "sha256": uploader.sha256_file(path), "size": path.stat().st_size})
        (artifacts / "SPDX-SBOM.json").write_text('{}')
        manifest = {"release": uploader.RELEASE_VERSION, "repository": {"url": f"https://github.com/{uploader.REPOSITORY}", "commit": "a" * 40}, "status": "PUBLIC_ALPHA_READY", "local_validation": {"status": "pass", "checks": 77}, "artifacts": records}
        (artifacts / "RELEASE-MANIFEST.json").write_text(json.dumps(manifest))
        (artifacts / "SHA256SUMS").write_text(''.join(uploader.sha256_file(path) + '  ' + path.name + '\n' for path in sorted(artifacts.iterdir())))
        validation = {"release": uploader.RELEASE_VERSION, "status": "PUBLIC_ALPHA_READY", "checks_failed": 0, "checks_passed": 77, "checks": [{"status": "pass"} for _ in range(77)]}
        (root / "receipts/validation-report.json").write_text(json.dumps(validation))
        return root

    def test_local_validated_inventory_and_tampering(self):
        root = self.fixture_release()
        self.assertEqual(9, len(uploader.validated_files(root, "a" * 40)))
        with self.assertRaisesRegex(uploader.UploadError, "identity"):
            uploader.validated_files(root, "b" * 40)
        (root / "artifacts/SPDX-SBOM.json").write_text('{"tampered":true}')
        with self.assertRaisesRegex(uploader.UploadError, "checksums"):
            uploader.validated_files(root, "a" * 40)

    def test_failed_validation_receipt_cannot_upload(self):
        root = self.fixture_release()
        receipt = root / "receipts/validation-report.json"
        data = json.loads(receipt.read_text())
        data["checks"][0]["status"] = "fail"
        receipt.write_text(json.dumps(data))
        with self.assertRaisesRegex(uploader.UploadError, "validation must pass"):
            uploader.validated_files(root, "a" * 40)

    def test_cli_requires_exact_actions_event_repository_and_sha(self):
        root = self.fixture_release()
        report = self.root / "report.json"
        arguments = ["upload_draft_release.py", "--release-root", str(root), "--release-id", "42", "--tag", uploader.RELEASE_VERSION, "--sha", "a" * 40, "--report", str(report)]
        environment = {"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_SHA": "a" * 40, "GITHUB_REPOSITORY": uploader.REPOSITORY}
        for key, value in (("GITHUB_ACTIONS", "false"), ("GITHUB_EVENT_NAME", "pull_request"), ("GITHUB_SHA", "b" * 40), ("GITHUB_REPOSITORY", "example/fork")):
            with self.subTest(key=key), patch.dict(os.environ, {**environment, key: value}), patch.object(uploader.sys, "argv", arguments), patch.object(uploader, "GitHub") as api, patch('sys.stdout', new_callable=io.StringIO):
                self.assertEqual(1, uploader.main())
                api.assert_not_called()


class TransportAndWorkflowTest(unittest.TestCase):
    def workflow_inline(self, step_name):
        path = Path(__file__).resolve().parents[1] / '.github/workflows/build-draft.yml'
        workflow = yaml.load(path.read_text(encoding='utf-8'), Loader=yaml.BaseLoader)
        step = next(entry for entry in workflow['jobs']['build-draft']['steps'] if entry.get('name') == step_name)
        return step['run'].split("python - <<'PY'", 1)[1].split('\n', 1)[1].split('\nPY', 1)[0]

    def test_base_download_fields_are_pinned_and_consistent(self):
        script = self.workflow_inline('Download pinned public Full base')
        template = Path(__file__).resolve().parents[1]
        original = json.loads((template / 'full/reference-v0.4.json').read_text())['base']
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'full').mkdir()
            for field, value in ((None, None), ('release', '../other'), ('asset', 'another.zip'), ('archive_root', 'another-root'), ('sha256', 'invalid')):
                with self.subTest(field=field):
                    base = dict(original)
                    if field:
                        base[field] = value
                    (root / 'full/reference-v0.4.json').write_text(json.dumps({'base': base}))
                    result = subprocess.run([sys.executable, '-B', '-c', script], cwd=root, capture_output=True, text=True)
                    self.assertEqual(field is None, result.returncode == 0)
                    if field is None:
                        self.assertEqual(original['sha256'], result.stdout.splitlines()[1])

    def test_hosted_build_gate_requires_success_and_preserves_remote_ci(self):
        script = self.workflow_inline('Bind actual workflow commit and revalidate')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / 'smart-router-release'
            (root / 'receipts').mkdir(parents=True)
            (root / 'artifacts').mkdir()
            manifest = {'completed_gates': ['local_build_and_tests'], 'remaining_gates': ['remote_ci']}
            path = root / 'artifacts/RELEASE-MANIFEST.json'
            path.write_text(json.dumps(manifest))
            validation = {'status': 'LOCAL_RC_READY', 'checks_passed': 77, 'checks_failed': 0}
            (root / 'receipts/validation-report.json').write_text(json.dumps(validation))
            environment = {**os.environ, 'RUNNER_TEMP': directory, 'GITHUB_REPOSITORY': uploader.REPOSITORY, 'GITHUB_RUN_ID': '123'}
            for exit_code in (1, 0):
                (root / 'receipts/build-and-test.json').write_text(json.dumps({'commands': [{'exit_code': exit_code}]}))
                result = subprocess.run([sys.executable, '-B', '-c', script], env=environment, capture_output=True, text=True)
                self.assertEqual(exit_code == 0, result.returncode == 0)
                saved = json.loads(path.read_text())
                if exit_code:
                    self.assertEqual(manifest, saved)
                else:
                    self.assertIn('github_hosted_build_and_tests', saved['completed_gates'])
                    self.assertEqual(['remote_ci'], saved['remaining_gates'])
                    self.assertNotIn('remote_ci', saved['completed_gates'])
                    self.assertEqual('https://github.com/' + uploader.REPOSITORY + '/actions/runs/123', saved['build_run_url'])

    def test_token_not_forwarded_to_download_redirect(self):
        client = uploader.GitHub("synthetic-token", uploader.REPOSITORY)
        with patch.object(uploader.http.client, "HTTPSConnection") as connection:
            client.open("GET", "https://release-assets.githubusercontent.com/asset", download=True)
            headers = connection.return_value.request.call_args.kwargs["headers"]
            self.assertNotIn("Authorization", headers)
            client.open("GET", client.api + "/releases/42")
            self.assertIn("Authorization", connection.return_value.request.call_args.kwargs["headers"])
        for url in ("http://api.github.com/asset", "https://example.invalid/asset", "https://api.github.com:444/asset", "https://user:pass@api.github.com/asset"):
            with self.subTest(url=url), self.assertRaises(uploader.UploadError):
                client.open("GET", url, download=True)

    def test_mutating_redirect_is_not_followed_or_retried(self):
        client = uploader.GitHub("synthetic-token", uploader.REPOSITORY)
        with patch.object(uploader.http.client, "HTTPSConnection") as connection:
            connection.return_value.getresponse.return_value.status = 307
            with self.assertRaises(uploader.APIError):
                client.json_request("POST", "https://uploads.github.com/repos/example/fixture/releases/42/assets", io.BytesIO(b"test"), 4)
            self.assertEqual(1, connection.return_value.request.call_count)

    def test_digest_fallback_reads_redirected_bytes_without_token(self):
        client = uploader.GitHub("synthetic-token", uploader.REPOSITORY)
        with patch.object(uploader.http.client, "HTTPSConnection") as connection:
            response = connection.return_value.getresponse.return_value
            response.status = 200
            response.read.side_effect = [b"contents", b""]
            digest = client.asset_digest({"id": 1, "size": 8})
            self.assertEqual(hashlib.sha256(b"contents").hexdigest(), digest)

    def test_workflow_is_manual_pinned_full_build_and_draft_only(self):
        path = Path(__file__).resolve().parents[1] / '.github/workflows/build-draft.yml'
        workflow = yaml.load(path.read_text(encoding='utf-8'), Loader=yaml.BaseLoader)
        self.assertEqual({"workflow_dispatch"}, set(workflow["on"]))
        self.assertEqual("1", workflow["env"]["PYTHONDONTWRITEBYTECODE"])
        job = workflow["jobs"]["build-draft"]
        steps = job["steps"]
        settings = {entry['uses']: entry.get('with', {}) for entry in steps if 'uses' in entry}
        self.assertEqual("1.25.1", settings['actions/setup-go@v5']['go-version'])
        self.assertEqual("1.2.21", settings['oven-sh/setup-bun@v2']['bun-version'])
        self.assertEqual("22.14.0", settings['actions/setup-node@v4']['node-version'])
        self.assertEqual("false", settings['actions/checkout@v4']['persist-credentials'])
        commands = '\n'.join(step.get('run', '') for step in steps)
        self.assertIn('scripts/build_v04.py', commands)
        self.assertIn('scripts/finalize_public_release.py', commands)
        self.assertIn('scripts/validate_release.py', commands)
        self.assertIn('scripts/upload_draft_release.py', commands)
        self.assertNotIn('--prepare-only', commands)
        self.assertNotIn('--clobber', commands)
        self.assertNotIn('gh release create', commands)
        self.assertNotIn('secrets.', path.read_text())
        self.assertEqual('always()', steps[-1]['if'])


if __name__ == "__main__":
    unittest.main()
