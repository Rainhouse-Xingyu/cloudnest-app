#!/usr/bin/env bash
# Search protocol/ACL tests with unavailable models, using disposable DB/Redis.
set -euo pipefail
exec "$(dirname "$0")/test-integration.sh" -tags=integration,aisearch -run '^TestAIHybrid' -timeout=3m "$@"
