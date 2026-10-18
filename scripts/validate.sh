#!/usr/bin/env bash
set -euo pipefail

# Treat errors produced by plugins as log text, so they cannot execute runner workflow commands.
# Keep the unpredictable resume token out of the validator's environment.
command_token="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
printf '::stop-commands::%s\n' "$command_token"
trap 'printf "::%s::\n" "$command_token"' EXIT
"$@"
