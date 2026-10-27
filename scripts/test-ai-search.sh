#!/usr/bin/env bash
# Search and index control protocol/ACL tests using disposable DB/Redis.
set -euo pipefail
exec "$(dirname "$0")/test-integration.sh" -tags=integration,aisearch -run '^TestAI(Hybrid|IndexControl|IndexIncremental|Office|PDF)' -timeout=3m "$@"
