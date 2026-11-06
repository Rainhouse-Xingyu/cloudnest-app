#!/usr/bin/env bash
# Only pinned official FRP assets and disposable database containers are used.
set -euo pipefail
cd "$(dirname "$0")/.."
case "$(uname -s)/$(uname -m)" in
  Darwin/arm64) platform=darwin_arm64; digest=d15d0324b99e14619c9aa46e37f6db588c06a09be4bc6f7948890b6b61077842 ;;
  Darwin/x86_64) platform=darwin_amd64; digest=8acb59b6fb998cc8d209d1373d90bf70fa5ec3efeb4dfbc7bda6ce3abd78cb45 ;;
  Linux/aarch64|Linux/arm64) platform=linux_arm64; digest=8edf590b0c396fa42f9c147121a76af83cfc2002b08f5aef0ea1008c43482019 ;;
  Linux/x86_64) platform=linux_amd64; digest=1d9875210972a5d1a62b2dc4c48df45ab0af05d3ca1b5e494a014428259df803 ;;
  *) echo 'Unsupported official FRP test platform.' >&2; exit 1 ;;
esac
probe_dir=$(mktemp -d "${TMPDIR:-/tmp}/cloudnest-agent-probe.XXXXXX")
trap 'rm -rf "$probe_dir"' EXIT
archive="frp_0.71.1_${platform}.tar.gz"
curl --fail --location --retry 2 --max-time 120 \
  "https://github.com/fatedier/frp/releases/download/v0.71.1/$archive" -o "$probe_dir/$archive"
python3 - "$probe_dir/$archive" "$digest" <<'PY'
import hashlib, pathlib, sys
if hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest() != sys.argv[2]:
    raise SystemExit('Official FRP archive SHA-256 mismatch; refusing to execute.')
PY
tar -xzf "$probe_dir/$archive" -C "$probe_dir"
export CLOUDNEST_TEST_FRP_BIN_DIR="$probe_dir/frp_0.71.1_${platform}"
go -C cloudnest_go test -tags=frpnative -race -count=1 -v ./internal/frpagent
./scripts/test-integration.sh -tags=integration,frpnative -run '^TestAgentNative(CLIThreeTunnelsRestartAndReconnect|RetirementStopsTunnels)$'
