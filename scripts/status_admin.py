"""Authorize a registry status dispatcher before constructing its audit identity."""

import json
import os
import re
import subprocess
import sys
from pathlib import Path


def actor_identity(env, permission):
    actor = env["GITHUB_ACTOR"]
    actor_id = env["GITHUB_ACTOR_ID"]
    repository = env["GITHUB_REPOSITORY"]
    workflow = env["GITHUB_WORKFLOW_REF"]
    if (
        env["GITHUB_EVENT_NAME"] != "workflow_dispatch"
        or env["GITHUB_REF"] != "refs/heads/master"
        or env["GITHUB_TRIGGERING_ACTOR"] != actor
        or workflow != f"{repository}/.github/workflows/status.yml@refs/heads/master"
        or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]{0,38}", actor)
        or not re.fullmatch(r"[1-9][0-9]*", actor_id)
    ):
        raise ValueError(
            "Status changes require the original dispatcher on trusted master."
        )
    user = permission.get("user", {})
    if (
        str(user.get("id")) != actor_id
        or user.get("login", "").lower() != actor.lower()
        or not (
            permission.get("permission") == "admin"
            or (
                permission.get("role_name") == "maintain"
                and permission.get("permission") == "write"
            )
        )
    ):
        raise ValueError(
            "Status changes require registry admin or maintain permission."
        )
    run_id = f"{env['GITHUB_RUN_ID']}:{env['GITHUB_RUN_ATTEMPT']}"
    if not re.fullmatch(r"[1-9][0-9]*:[1-9][0-9]*", run_id):
        raise ValueError("Invalid workflow run identity.")
    return {
        "kind": "registry-admin",
        "id": actor_id,
        "name": actor,
        "workflow": workflow,
        "run_id": run_id,
    }


def main():
    repository = os.environ["GITHUB_REPOSITORY"]
    actor = os.environ["GITHUB_ACTOR"]
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("Invalid registry repository.")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]{0,38}", actor):
        raise ValueError("Invalid dispatcher.")
    permission = subprocess.run(
        ["gh", "api", f"repos/{repository}/collaborators/{actor}/permission"],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
    )
    identity = actor_identity(os.environ, json.loads(permission.stdout))
    Path(sys.argv[1]).write_text(json.dumps(identity) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
