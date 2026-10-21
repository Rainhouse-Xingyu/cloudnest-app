#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../cloudnest_go"
tool_dir=$(mktemp -d)
trap 'rm -rf "$tool_dir"' EXIT
# Build the scanner separately so its newer compiler does not silently replace
# the application's toolchain during analysis of standard-library vulnerabilities.
GOBIN="$tool_dir" go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
"$tool_dir/govulncheck" ./...
