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
                    "submit.request_json", return_value={"id": IDENTIFIER}
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
            self.assertEqual(output.read_text(), f"submission-id={IDENTIFIER}\n")

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


class ConsumerTests(unittest.TestCase):
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
