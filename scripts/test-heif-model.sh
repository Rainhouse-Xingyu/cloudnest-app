#!/usr/bin/env bash
# Native HEIF helper + real local Ollama + newly created disposable DB/Redis.
set -euo pipefail
: "${CLOUDNEST_TEST_HEIF_HELPER:?Set the absolute path to a built cloudnest-heif helper}"
[[ "$CLOUDNEST_TEST_HEIF_HELPER" == /* && -x "$CLOUDNEST_TEST_HEIF_HELPER" ]] || exit 1
export CLOUDNEST_TEST_AI_MODEL=1
exec "$(dirname "$0")/test-integration.sh" -tags=integration,aisearch,aimodel,heifmodel \
  -run '^TestAI(TextLiveHEIF|ImageHEIF|ImageFailures)' -timeout=6m "$@"
