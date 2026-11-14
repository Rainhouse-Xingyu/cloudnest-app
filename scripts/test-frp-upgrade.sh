#!/usr/bin/env bash
# Compile the real pre-ledger server and exercise migration in disposable MySQL.
set -euo pipefail
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/cloudnest-frp-upgrade.XXXXXX")
trap 'rm -rf -- "$work"' EXIT
baseline=50ac977d416823487ee8ba2bdb63462610ad930c
mkdir "$work/baseline"
git archive "$baseline" cloudnest_go | tar -xf - -C "$work/baseline"
go -C "$work/baseline/cloudnest_go" build -o "$work/cloudnest-before" ./cmd/server
go -C cloudnest_go build -o "$work/cloudnest-after" ./cmd/server
export CLOUDNEST_TEST_FRP_BEFORE="$work/cloudnest-before"
export CLOUDNEST_TEST_FRP_AFTER="$work/cloudnest-after"
./scripts/test-integration.sh -tags=integration,frpupgrade -run '^TestFRPUpgradeFromPreLedgerRelease$'
