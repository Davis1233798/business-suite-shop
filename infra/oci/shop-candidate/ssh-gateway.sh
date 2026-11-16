#!/usr/bin/env bash
set -Eeuo pipefail

readonly deployer=/usr/local/lib/business-suite-shop-candidate/deploy-release.sh
command_line=${SSH_ORIGINAL_COMMAND:-}

if [[ $command_line =~ ^deploy\ ([0-9a-f]{40})\ (sha256:[0-9a-f]{64})$ ]]; then
  [[ -x $deployer ]] || { echo 'Shop candidate deployer is not installed' >&2; exit 3; }
  exec "$deployer" "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
fi

echo 'This SSH key may only deploy an immutable Shop candidate image by commit digest' >&2
exit 2
