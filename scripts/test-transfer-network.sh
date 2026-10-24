#!/usr/bin/env bash
# Exercise real 30-second stalls outside the six-minute correctness suite.
set -euo pipefail
exec "$(dirname "$0")/test-integration.sh" -tags=integration,network \
  -run '^TestDownloadNetwork' -timeout=3m "$@"
