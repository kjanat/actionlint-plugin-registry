#!/usr/bin/env bash
set -euo pipefail

# Guest-authored errors are log data, never runner workflow commands.
# Keep the unpredictable resume token out of the validator's environment.
command_token="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
printf '::stop-commands::%s\n' "$command_token"
trap 'printf "::%s::\n" "$command_token"' EXIT
"$@"
