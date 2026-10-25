#!/usr/bin/env bash
# Explicit opt-in; uses the same disposable database/container safeguards.
set -euo pipefail
exec "$(dirname "$0")/test-integration.sh" -tags=integration,performance \
  -run '^TestTransferGiBRestartAndRange$' -timeout=15m "$@"
