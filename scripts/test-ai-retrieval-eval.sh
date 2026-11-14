#!/usr/bin/env bash
# Synthetic corpus, actual local models, fresh disposable databases; never .env.
set -euo pipefail
: "${CLOUDNEST_AI_EVAL_REPORT:?Set an absolute, previously nonexistent JSON report path}"
: "${CLOUDNEST_TEST_POPPLER_BIN:?Set the native Poppler executable directory}"
export CLOUDNEST_TEST_AI_MODEL=1
export CLOUDNEST_TEST_AI_EVAL=1
exec "$(dirname "$0")/test-integration.sh" -tags=integration,aimodel,aieval -run '^TestAIRetrievalEvaluation$' -timeout=15m "$@"
