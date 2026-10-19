"""Send authenticated HTTPS requests with size limits and without exposing credentials."""

import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request

ORIGIN = "https://registry.actionlint.kjanat.dev"
MAX_RESPONSE = 1024 * 1024
MAX_ERROR_RESPONSE = 8192
# These labels are local constants. Never display response text or claim values.
REGISTRY_ERROR_CODES = {
    "oidc_invalid": (401, "OIDC token invalid or expired"),
    "oidc_bearer_missing": (401, "OIDC bearer credential missing or invalid"),
    "github_issuer": (403, "GitHub issuer required"),
    "github_visibility": (403, "public GitHub repository required"),
    "github_subject": (403, "GitHub subject claim missing or invalid"),
    "github_repository_id": (403, "GitHub repository ID claim missing or invalid"),
    "github_owner_id": (403, "GitHub owner ID claim missing or invalid"),
    "github_repository": (403, "GitHub repository claim missing or invalid"),
    "github_ref": (403, "GitHub ref claim missing or invalid"),
    "github_commit": (403, "GitHub commit claim missing or invalid"),
    "github_workflow": (403, "GitHub workflow claim missing or invalid"),
    "github_run_id": (403, "GitHub run ID claim missing or invalid"),
    "github_run_attempt": (403, "GitHub run attempt claim missing or invalid"),
    "validator_repository": (403, "validator repository ID mismatch"),
    "validator_workflow": (403, "validator workflow reference mismatch"),
    "validator_ref": (403, "validator branch reference mismatch"),
    "validator_ref_type": (403, "validator reference type must be branch"),
    "validator_event": (403, "validator event must be schedule or workflow_dispatch"),
}


class RequestError(ValueError):
    """A credential-free request failure safe to display to workflow operators."""


def failure_message(error, fallback):
    return str(error) if isinstance(error, RequestError) else fallback


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RequestError("Authenticated HTTP redirects are forbidden.")


def json_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON member")
        result[key] = value
    return result


def request_json(url, token, payload=None, *, operation=None):
    try:
        return _request_json(url, token, payload)
    except RequestError as error:
        if operation is None:
            raise
        raise RequestError(f"{operation}: {error}") from None


def registry_error_reason(error):
    try:
        body = error.read(MAX_ERROR_RESPONSE + 1)
        if len(body) <= MAX_ERROR_RESPONSE:
            value = json.loads(body, object_pairs_hook=json_object)
            if isinstance(value, dict) and isinstance(value.get("code"), str):
                known = REGISTRY_ERROR_CODES.get(value["code"])
                if known is not None and known[0] == error.code:
                    return known[1]
    except (OSError, ValueError, TypeError, RecursionError):
        pass
    return "no recognized registry error code"


def _request_json(url, token, payload):
    parsed = urllib.parse.urlsplit(url)
    if (
        parsed.scheme != "https"
        or not parsed.hostname
        or parsed.username
        or parsed.password
        or parsed.fragment
    ):
        raise ValueError("HTTPS endpoint without user information required")
    if (
        not isinstance(token, str)
        or re.fullmatch(r"[A-Za-z0-9._~+/-]+=*", token) is None
        or len(token) > 16384
    ):
        raise ValueError("invalid credential format")
    data = None if payload is None else json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=data,
        headers={
            "Authorization": "Bearer " + token,
            "User-Agent": "actionlint-plugin-registry/1",
            "Accept": "application/json",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.build_opener(NoRedirect).open(
            request, timeout=30
        ) as response:
            if response.status < 200 or response.status >= 300:
                raise RequestError("The HTTPS endpoint returned an unexpected status.")
            body = response.read(MAX_RESPONSE + 1)
    except urllib.error.HTTPError as error:
        status = error.code
        message = f"HTTP request failed with status {status}"
        try:
            if parsed.netloc == urllib.parse.urlsplit(ORIGIN).netloc:
                message += ": " + registry_error_reason(error)
        finally:
            error.close()
        raise RequestError(message) from None
    except (urllib.error.URLError, TimeoutError, OSError):
        raise RequestError("HTTPS request failed") from None
    if len(body) > MAX_RESPONSE:
        raise RequestError("HTTP response exceeds limit")
    try:
        result = json.loads(body, object_pairs_hook=json_object)
    except (ValueError, UnicodeError):
        raise RequestError("The HTTPS endpoint returned invalid JSON.") from None
    if not isinstance(result, dict):
        raise RequestError("HTTP response must be a JSON object")
    return result


def mask(token, environ):
    if environ.get("GITHUB_ACTIONS") == "true":
        print("::add-mask::" + token, flush=True)


def oidc_token(audience, environ, provider="github"):
    if provider == "gitlab":
        token = environ.get("REGISTRY_ID_TOKEN", "")
    elif provider == "github":
        endpoint = environ.get("ACTIONS_ID_TOKEN_REQUEST_URL", "")
        if not endpoint:
            raise RequestError(
                "The GitHub OIDC endpoint is missing. Grant the job id-token: write permission."
            )
        try:
            parsed = urllib.parse.urlsplit(endpoint)
            port = parsed.port
        except ValueError:
            raise RequestError(
                "The GitHub OIDC endpoint is invalid or unsupported."
            ) from None
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or not parsed.hostname.endswith(".actions.githubusercontent.com")
            or port not in (None, 443)
            or parsed.username
            or parsed.password
            or parsed.fragment
        ):
            raise RequestError("The GitHub OIDC endpoint is invalid or unsupported.")
        query = [
            (key, value)
            for key, value in urllib.parse.parse_qsl(parsed.query)
            if key != "audience"
        ]
        query.append(("audience", audience))
        endpoint = urllib.parse.urlunsplit(
            parsed._replace(query=urllib.parse.urlencode(query))
        )
        request_token = environ.get("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
        if re.fullmatch(r"[A-Za-z0-9._~+/-]+=*", request_token) is None:
            raise RequestError("GitHub OIDC request credential unavailable")
        mask(request_token, environ)
        token = request_json(
            endpoint, request_token, operation="GitHub OIDC token request"
        ).get("value")
    else:
        raise ValueError("unsupported identity provider")
    if (
        not isinstance(token, str)
        or re.fullmatch(r"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", token)
        is None
        or len(token) > 16384
    ):
        raise RequestError("OIDC provider returned an invalid token")
    mask(token, environ)
    return token


def uuid_value(value):
    if (
        not isinstance(value, str)
        or re.fullmatch(
            r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}",
            value,
        )
        is None
    ):
        raise ValueError("invalid submission or lease identifier")
    return value


def output(name, value):
    if "\n" in value or "\r" in value:
        raise ValueError("invalid workflow output")
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as stream:
            stream.write(f"{name}={value}\n")
