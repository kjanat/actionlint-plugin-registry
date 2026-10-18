"""Claim authenticated submissions and complete their run-bound leases."""

import argparse
import json
import os
import sys
from pathlib import Path

from registry_api import (
    ORIGIN,
    json_object,
    oidc_token,
    output,
    request_json,
    uuid_value,
)


def read_object(path):
    with open(path, "rb") as stream:
        data = stream.read(1024 * 1024 + 1)
    if len(data) > 1024 * 1024:
        raise ValueError("file exceeds limit")
    value = json.loads(data, object_pairs_hook=json_object)
    if not isinstance(value, dict):
        raise TypeError("JSON object required")
    return value


def claim(directory):
    token = oidc_token(ORIGIN + "/validator", os.environ)
    response = request_json(ORIGIN + "/v1/claims", token, {})
    if set(response) != {"claim"}:
        raise ValueError("unexpected claim response")
    item = response["claim"]
    if item is None:
        output("claimed", "false")
        return
    if not isinstance(item, dict) or set(item) != {"receipt", "lease_id"}:
        raise ValueError("invalid claim")
    receipt = item["receipt"]
    if (
        not isinstance(receipt, dict)
        or receipt.get("schema_version") != 1
        or not isinstance(receipt.get("submission"), dict)
        or not isinstance(receipt.get("identity"), dict)
    ):
        raise ValueError("invalid authenticated receipt")
    identifier = uuid_value(receipt.get("id"))
    lease = {"id": identifier, "lease_id": uuid_value(item["lease_id"])}
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "receipt.json").write_text(
        json.dumps(receipt) + "\n", encoding="utf-8"
    )
    (directory / "lease.json").write_text(json.dumps(lease) + "\n", encoding="utf-8")
    output("claimed", "true")


def complete(lease_path, status):
    lease = read_object(lease_path)
    if set(lease) != {"id", "lease_id"}:
        raise ValueError("invalid lease")
    identifier = uuid_value(lease["id"])
    lease_id = uuid_value(lease["lease_id"])
    token = oidc_token(ORIGIN + "/validator", os.environ)
    result = request_json(
        ORIGIN + "/v1/claims/" + identifier + "/complete",
        token,
        {"lease_id": lease_id, "status": status},
    )
    if result.get("id") != identifier or result.get("status") != status:
        raise ValueError("unexpected completion response")


def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    claim_parser = sub.add_parser("claim")
    claim_parser.add_argument("--directory", type=Path, default=Path("handoff"))
    complete_parser = sub.add_parser("complete")
    complete_parser.add_argument("--lease", type=Path, required=True)
    complete_parser.add_argument(
        "--status", choices=("published", "rejected"), required=True
    )
    arguments = parser.parse_args()
    if arguments.command == "claim":
        claim(arguments.directory)
    else:
        complete(arguments.lease, arguments.status)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, TypeError, KeyError, OSError):
        sys.exit(
            "Registry consumer request failed; the lease remains eligible for retry."
        )
