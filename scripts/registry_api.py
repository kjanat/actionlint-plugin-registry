"""Send authenticated HTTPS requests with size limits and without exposing credentials."""

import json
import os
import re
import urllib.error
import urllib.parse
import urllib.request

ORIGIN = "https://plugins.actionlint.kjanat.dev"
MAX_RESPONSE = 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise ValueError("authenticated HTTP redirects are forbidden")


def json_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON member")
        result[key] = value
    return result


def request_json(url, token, payload=None):
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
            "Accept": "application/json",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.build_opener(NoRedirect).open(
            request, timeout=30
        ) as response:
            if response.status < 200 or response.status >= 300:
                raise ValueError("unexpected HTTP response status")
            body = response.read(MAX_RESPONSE + 1)
    except urllib.error.HTTPError as error:
        status = error.code
        error.close()
        raise ValueError(f"HTTP request failed with status {status}") from None
    except (urllib.error.URLError, TimeoutError, OSError):
        raise ValueError("HTTPS request failed") from None
    if len(body) > MAX_RESPONSE:
        raise ValueError("HTTP response exceeds limit")
    result = json.loads(body, object_pairs_hook=json_object)
    if not isinstance(result, dict):
        raise TypeError("HTTP response must be a JSON object")
    return result


def mask(token, environ):
    if environ.get("GITHUB_ACTIONS") == "true":
        print("::add-mask::" + token, flush=True)


def oidc_token(audience, environ, provider="github"):
    if provider == "gitlab":
        token = environ.get("REGISTRY_ID_TOKEN", "")
    elif provider == "github":
        endpoint = environ.get("ACTIONS_ID_TOKEN_REQUEST_URL", "")
        parsed = urllib.parse.urlsplit(endpoint)
        if (
            parsed.scheme != "https"
            or not parsed.hostname
            or not parsed.hostname.endswith(".actions.githubusercontent.com")
            or parsed.port not in (None, 443)
            or parsed.username
            or parsed.password
            or parsed.fragment
        ):
            raise ValueError(
                "The GitHub OIDC endpoint is unavailable. Grant the job id-token: write permission."
            )
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
            raise ValueError("GitHub OIDC request credential unavailable")
        mask(request_token, environ)
        token = request_json(endpoint, request_token).get("value")
    else:
        raise ValueError("unsupported identity provider")
    if (
        not isinstance(token, str)
        or re.fullmatch(r"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", token)
        is None
        or len(token) > 16384
    ):
        raise ValueError("OIDC provider returned an invalid token")
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
