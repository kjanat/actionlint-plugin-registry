"""Authenticate the publisher with OIDC and submit release metadata without executing the plugin."""

import os
import re
import sys

from registry_api import (
    ORIGIN,
    RequestError,
    failure_message,
    oidc_token,
    output,
    request_json,
    uuid_value,
)


def submission(environ):
    provider = environ.get("PLUGIN_PROVIDER", "github")
    if provider not in ("github", "gitlab"):
        raise ValueError("unsupported provider")
    repository = environ["PLUGIN_REPOSITORY"].lower()
    segments = repository.split("/")
    if (
        len(segments) < 2
        or (provider == "github" and len(segments) != 2)
        or len(repository) > 255
        or any(
            re.fullmatch(r"[a-z0-9][a-z0-9._-]{0,99}", segment) is None
            for segment in segments
        )
    ):
        raise ValueError("invalid repository path")
    tag = environ["PLUGIN_TAG"]
    asset = environ["PLUGIN_ASSET"]
    name_pattern = r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}"
    if re.fullmatch(name_pattern, tag) is None:
        raise ValueError("invalid release tag")
    if re.fullmatch(name_pattern, asset) is None or not asset.endswith(
        (".wasm", ".wasm.gz")
    ):
        raise ValueError("asset must be a .wasm or .wasm.gz release filename")
    digest = environ["PLUGIN_SHA256"].lower()
    if re.fullmatch(r"[0-9a-f]{64}", digest) is None:
        raise ValueError("sha256 must contain exactly 64 hexadecimal characters")
    kind = environ["PLUGIN_KIND"]
    if kind not in ("core", "component"):
        raise ValueError("kind must be core or component")
    return {
        "schema_version": 1,
        "provider": provider,
        "repository": repository,
        "tag": tag,
        "asset": asset,
        "sha256": digest,
        "kind": kind,
    }


def main():
    entry = submission(os.environ)
    token = oidc_token(ORIGIN, os.environ, entry["provider"])
    response = request_json(
        ORIGIN + "/v1/submissions", token, entry, operation="Registry submission"
    )
    identifier = uuid_value(response.get("id"))
    status = response.get("status")
    if status not in ("pending", "validating", "published", "rejected", "failed"):
        raise ValueError("unexpected submission status")
    output("submission-id", identifier)
    output("submission-status", status)
    print(f"Registry submission {identifier}: {status}.")
    print("Check status: " + ORIGIN + "/v1/submissions/" + identifier)
    if status in ("rejected", "failed"):
        raise RequestError(f"Registry submission {identifier} is {status}.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, TypeError, KeyError, OSError) as error:
        sys.exit(
            failure_message(
                error,
                "The submission failed. Check the release metadata, OIDC permissions, and registry availability.",
            )
        )
