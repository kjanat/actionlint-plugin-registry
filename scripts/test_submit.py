import io
import json
import os
import tempfile
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from unittest.mock import MagicMock, patch

import consume
import registry_api
import submit

IDENTIFIER = "12345678-1234-4234-8234-123456789abc"
TOKEN = "header.payload.signature"


class SubmissionTests(unittest.TestCase):
    def setUp(self):
        self.environ = {
            "PLUGIN_REPOSITORY": "publisher/plugin",
            "PLUGIN_TAG": "v1.0.0",
            "PLUGIN_ASSET": "plugin.wasm",
            "PLUGIN_SHA256": "ab" * 32,
            "PLUGIN_KIND": "component",
        }

    def test_reject_invalid_metadata_before_submission(self):
        for key, value in (
            ("PLUGIN_REPOSITORY", "owner/repo/extra"),
            ("PLUGIN_TAG", "v1\n::warning::payload"),
            ("PLUGIN_TAG", "v1.0.0-$(touch-something)"),
            ("PLUGIN_ASSET", "../plugin.wasm"),
            ("PLUGIN_SHA256", "not-a-digest"),
            ("PLUGIN_KIND", "executable"),
            ("PLUGIN_PROVIDER", "unknown"),
        ):
            with self.subTest(key=key), self.assertRaises(ValueError):
                submit.submission(self.environ | {key: value})

    def test_gitlab_nested_project(self):
        result = submit.submission(
            self.environ
            | {
                "PLUGIN_PROVIDER": "gitlab",
                "PLUGIN_REPOSITORY": "group/subgroup/project",
            }
        )
        self.assertEqual(result["repository"], "group/subgroup/project")

    def test_submit_oidc_without_pat_or_issue_creation(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            with (
                patch.dict(
                    os.environ,
                    self.environ | {"GITHUB_OUTPUT": str(output)},
                    clear=True,
                ),
                patch("submit.oidc_token", return_value=TOKEN) as token,
                patch(
                    "submit.request_json",
                    return_value={"id": IDENTIFIER, "status": "pending"},
                ) as request,
                patch("builtins.print"),
            ):
                submit.main()
            token.assert_called_once()
            self.assertEqual(token.call_args.args[0], registry_api.ORIGIN)
            self.assertEqual(
                request.call_args.args[0], registry_api.ORIGIN + "/v1/submissions"
            )
            self.assertEqual(request.call_args.args[2]["provider"], "github")
            self.assertEqual(
                output.read_text(),
                f"submission-id={IDENTIFIER}\nsubmission-status=pending\n",
            )

    def test_submission_reports_actual_state_and_terminal_failures(self):
        for status in ("pending", "validating", "published", "rejected", "failed"):
            with (
                self.subTest(status=status),
                patch.dict(os.environ, self.environ, clear=True),
                patch("submit.oidc_token", return_value=TOKEN),
                patch(
                    "submit.request_json",
                    return_value={"id": IDENTIFIER, "status": status},
                ),
                patch("submit.output") as output,
                patch("builtins.print") as log,
            ):
                if status in ("rejected", "failed"):
                    with self.assertRaisesRegex(registry_api.RequestError, status):
                        submit.main()
                else:
                    submit.main()
                output.assert_any_call("submission-status", status)
                log.assert_any_call(f"Registry submission {IDENTIFIER}: {status}.")
                self.assertNotIn("queued", str(log.call_args_list))

    def test_unknown_submission_status_fails_without_outputs(self):
        with (
            patch.dict(os.environ, self.environ, clear=True),
            patch("submit.oidc_token", return_value=TOKEN),
            patch("submit.request_json", return_value={"id": IDENTIFIER}),
            patch("submit.output") as output,
            self.assertRaises(ValueError),
        ):
            submit.main()
        output.assert_not_called()

    def test_output_command_injection_rejected(self):
        with (
            patch.dict(os.environ, self.environ, clear=True),
            patch("submit.oidc_token", return_value=TOKEN),
            patch(
                "submit.request_json",
                return_value={"id": IDENTIFIER + "\nother-output=evil"},
            ),
            self.assertRaises(ValueError),
        ):
            submit.main()


class HTTPTests(unittest.TestCase):
    def test_redirects_rejected_without_forwarding_authorization(self):
        for code in (301, 302, 303, 307, 308):
            with self.subTest(code=code), self.assertRaises(ValueError):
                registry_api.NoRedirect().redirect_request(
                    urllib.request.Request(
                        registry_api.ORIGIN,
                        headers={"Authorization": "Bearer " + TOKEN},
                    ),
                    None,
                    code,
                    "redirect",
                    {},
                    "https://evil.example",
                )

    def test_bounded_strict_json(self):
        for body in (b'{"id":1,"id":2}', b"[]", b"x" * (registry_api.MAX_RESPONSE + 1)):
            response = MagicMock()
            response.status = 200
            response.read.return_value = body
            response.__enter__.return_value = response
            with (
                patch("registry_api.urllib.request.build_opener") as opener,
                self.assertRaises((ValueError, TypeError)),
            ):
                opener.return_value.open.return_value = response
                registry_api.request_json(registry_api.ORIGIN, TOKEN, {})

    def test_http_error_never_echoes_token_or_response(self):
        error = urllib.error.HTTPError(
            registry_api.ORIGIN, 403, TOKEN, {}, io.BytesIO(TOKEN.encode())
        )
        with patch("registry_api.urllib.request.build_opener") as opener:
            opener.return_value.open.side_effect = error
            with self.assertRaises(ValueError) as caught:
                registry_api.request_json(registry_api.ORIGIN, TOKEN, {})
        self.assertNotIn(TOKEN, str(caught.exception))
        self.assertEqual(str(caught.exception), "HTTP request failed with status 403")

    def test_failure_output_keeps_safe_operation_status_only(self):
        for operation in ("GitHub OIDC token request", "Registry claim"):
            error = urllib.error.HTTPError(
                "https://example.test/" + TOKEN,
                403,
                TOKEN,
                {},
                io.BytesIO(TOKEN.encode()),
            )
            with patch("registry_api.urllib.request.build_opener") as opener:
                opener.return_value.open.side_effect = error
                with self.assertRaises(registry_api.RequestError) as caught:
                    registry_api.request_json(
                        registry_api.ORIGIN, TOKEN, {}, operation=operation
                    )
            self.assertEqual(
                registry_api.failure_message(caught.exception, "fallback"),
                f"{operation}: HTTP request failed with status 403",
            )
        for error in (OSError(TOKEN), ValueError(TOKEN), KeyError(TOKEN)):
            self.assertEqual(
                registry_api.failure_message(error, "fallback"), "fallback"
            )

    def test_insecure_endpoint_rejected_before_network(self):
        for url in (
            "http://example.test",
            "https://user:pass@example.test",
            "https://example.test/#fragment",
        ):
            with (
                patch("registry_api.urllib.request.build_opener") as opener,
                self.assertRaises(ValueError),
            ):
                registry_api.request_json(url, TOKEN)
            opener.assert_not_called()

    def test_oidc_audience_replaced_and_tokens_masked(self):
        environ = {
            "GITHUB_ACTIONS": "true",
            "ACTIONS_ID_TOKEN_REQUEST_URL": "https://run.actions.githubusercontent.com/token?audience=old&x=1",
            "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
        }
        with (
            patch(
                "registry_api.request_json", return_value={"value": TOKEN}
            ) as request,
            patch("builtins.print") as log,
        ):
            result = registry_api.oidc_token(registry_api.ORIGIN, environ)
        self.assertEqual(result, TOKEN)
        query = urllib.parse.parse_qs(
            urllib.parse.urlsplit(request.call_args.args[0]).query
        )
        self.assertEqual(query["audience"], [registry_api.ORIGIN])
        self.assertEqual(log.call_args_list[0].args, ("::add-mask::request-token",))
        self.assertEqual(log.call_args_list[1].args, ("::add-mask::" + TOKEN,))

    def test_missing_or_foreign_oidc_endpoint_fails_closed(self):
        for endpoint in (
            "",
            "http://run.actions.githubusercontent.com/token",
            "https://evil.test/token",
        ):
            with (
                patch("registry_api.request_json") as request,
                self.assertRaises(ValueError),
            ):
                registry_api.oidc_token(
                    registry_api.ORIGIN, {"ACTIONS_ID_TOKEN_REQUEST_URL": endpoint}
                )
            request.assert_not_called()

    def test_gitlab_does_not_fetch_github_token(self):
        with patch("registry_api.request_json") as request:
            self.assertEqual(
                registry_api.oidc_token(
                    registry_api.ORIGIN, {"REGISTRY_ID_TOKEN": TOKEN}, "gitlab"
                ),
                TOKEN,
            )
        request.assert_not_called()

    def test_oidc_endpoint_diagnostics_distinguish_missing_and_invalid_without_echo(
        self,
    ):
        for endpoint, expected in (
            ("", "missing"),
            ("https://evil.test/" + TOKEN, "invalid or unsupported"),
            (
                "https://run.actions.githubusercontent.com:bad/" + TOKEN,
                "invalid or unsupported",
            ),
        ):
            with (
                self.subTest(endpoint=endpoint),
                self.assertRaises(registry_api.RequestError) as caught,
            ):
                registry_api.oidc_token(
                    registry_api.ORIGIN, {"ACTIONS_ID_TOKEN_REQUEST_URL": endpoint}
                )
            self.assertIn(expected, str(caught.exception))
            self.assertNotIn(TOKEN, str(caught.exception))


class ConsumerTests(unittest.TestCase):
    def test_only_explicit_matching_deterministic_failure_completes_rejection(self):
        for disposition in ("rejected", "retryable", "unknown", None):
            with (
                self.subTest(disposition=disposition),
                tempfile.TemporaryDirectory() as directory,
            ):
                target = Path(directory)
                lease = target / "lease.json"
                report = target / "report.json"
                lease.write_text(json.dumps({"id": IDENTIFIER, "lease_id": IDENTIFIER}))
                report.write_text(
                    json.dumps(
                        {
                            "schema_version": 1,
                            "mode": "admit",
                            "submission_id": IDENTIFIER,
                            "accepted": [],
                            "rejected": [{"disposition": disposition, "error": TOKEN}],
                        }
                    )
                )
                with (
                    patch("consume.complete") as complete,
                    patch("builtins.print") as log,
                ):
                    if disposition in ("rejected", "retryable"):
                        consume.reject(lease, report)
                    else:
                        with self.assertRaises(ValueError):
                            consume.reject(lease, report)
                    if disposition == "rejected":
                        complete.assert_called_once_with(lease, "rejected")
                    else:
                        complete.assert_not_called()
                    self.assertNotIn(TOKEN, str(log.call_args_list))

    def test_failure_report_cannot_complete_another_submission_or_partial_success(self):
        for change in (
            {"submission_id": "00000000-0000-4000-8000-000000000001"},
            {"accepted": [{}]},
            {"rejected": []},
            {"schema_version": 2},
        ):
            with (
                self.subTest(change=change),
                tempfile.TemporaryDirectory() as directory,
            ):
                target = Path(directory)
                lease = target / "lease.json"
                report = target / "report.json"
                lease.write_text(json.dumps({"id": IDENTIFIER, "lease_id": IDENTIFIER}))
                report.write_text(
                    json.dumps(
                        {
                            "schema_version": 1,
                            "mode": "admit",
                            "submission_id": IDENTIFIER,
                            "accepted": [],
                            "rejected": [{"disposition": "rejected"}],
                        }
                        | change
                    )
                )
                with (
                    patch("consume.complete") as complete,
                    self.assertRaises(ValueError),
                ):
                    consume.reject(lease, report)
                complete.assert_not_called()

    def test_empty_queue_writes_no_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "handoff"
            with (
                patch("consume.oidc_token", return_value=TOKEN),
                patch("consume.request_json", return_value={"claim": None}),
                patch("consume.output") as output,
            ):
                consume.claim(target)
            output.assert_called_once_with("claimed", "false")
            self.assertFalse(target.exists())

    def test_invalid_receipt_writes_no_files(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "handoff"
            with (
                patch("consume.oidc_token", return_value=TOKEN),
                patch(
                    "consume.request_json",
                    return_value={
                        "claim": {
                            "receipt": {"id": "../../evil"},
                            "lease_id": IDENTIFIER,
                        }
                    },
                ),
                self.assertRaises(ValueError),
            ):
                consume.claim(target)
            self.assertFalse(target.exists())

    def test_handoff_contains_receipt_and_lease_never_token(self):
        receipt = {
            "schema_version": 1,
            "id": IDENTIFIER,
            "submission": {},
            "identity": {},
        }
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            with (
                patch("consume.oidc_token", return_value=TOKEN),
                patch(
                    "consume.request_json",
                    return_value={
                        "claim": {"receipt": receipt, "lease_id": IDENTIFIER}
                    },
                ),
                patch("consume.output"),
            ):
                consume.claim(target)
            self.assertEqual(json.loads((target / "receipt.json").read_text()), receipt)
            for path in target.iterdir():
                self.assertNotIn(TOKEN, path.read_text())
            with (
                patch("consume.oidc_token", return_value=TOKEN) as token,
                patch(
                    "consume.request_json",
                    return_value={"id": IDENTIFIER, "status": "published"},
                ) as request,
            ):
                consume.complete(target / "lease.json", "published")
            self.assertEqual(
                token.call_args.args[0], registry_api.ORIGIN + "/validator"
            )
            self.assertEqual(
                request.call_args.args[2],
                {"lease_id": IDENTIFIER, "status": "published"},
            )


if __name__ == "__main__":
    unittest.main()
