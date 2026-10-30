#!/usr/bin/env bash
# Real local Ollama + newly created disposable MySQL/Redis; synthetic text only.
set -euo pipefail
export CLOUDNEST_TEST_AI_MODEL=1
exec "$(dirname "$0")/test-integration.sh" -tags=integration,aimodel -run '^TestAITextLive' -timeout=6m "$@"
