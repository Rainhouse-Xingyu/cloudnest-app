#!/usr/bin/env bash
# Prepare an isolated, foreground-only frps fixture. Never edits an existing
# service, firewall, trust store or FRP installation; never prints credentials.
set -euo pipefail
umask 077
if [[ $# != 1 && $# != 5 ]]; then
  echo 'Usage: prepare-public-frps-test.sh PUBLIC_IPV4 [CONTROL HTTP HTTPS TCP]' >&2
  exit 2
fi
if [[ "$(uname -s)/$(uname -m)" != Linux/x86_64 ]]; then
  echo 'This fixture targets Linux x86_64 (Ubuntu 24.04).' >&2
  exit 2
fi
for dependency in curl sha256sum tar openssl python3; do
  command -v "$dependency" >/dev/null || { echo "Missing command: $dependency" >&2; exit 2; }
done
probe_ip=$1
control_port=${2:-17000}
http_port=${3:-18080}
https_port=${4:-18443}
tcp_port=${5:-20080}
python3 - "$probe_ip" "$control_port" "$http_port" "$https_port" "$tcp_port" <<'PY'
import ipaddress, socket, sys
try:
    address = ipaddress.ip_address(sys.argv[1])
    ports = [int(value) for value in sys.argv[2:]]
    if address.version != 4 or not address.is_global:
        raise ValueError('expected a public IPv4 address')
    if len(set(ports)) != 4 or any(port < 1024 or port > 65535 for port in ports):
        raise ValueError('four distinct unprivileged TCP ports are required')
except ValueError as error:
    raise SystemExit(str(error))
listeners = []
try:
    for port in ports:
        listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        listeners.append(listener)
        try:
            listener.bind(('0.0.0.0', port))
        except OSError as error:
            raise SystemExit(f'TCP port {port} unavailable; existing services were not changed: {error}')
finally:
    for listener in listeners:
        listener.close()
PY
probe_dir=$(mktemp -d "${HOME}/cloudnest-frps-test.XXXXXX")
archive="$probe_dir/frp.tar.gz"
curl --fail --location --retry 2 --connect-timeout 10 --max-time 120 --silent --show-error \
  https://github.com/fatedier/frp/releases/download/v0.71.1/frp_0.71.1_linux_amd64.tar.gz \
  -o "$archive"
printf '%s  %s\n' 1d9875210972a5d1a62b2dc4c48df45ab0af05d3ca1b5e494a014428259df803 "$archive" | sha256sum --check --status
tar -xzf "$archive" -C "$probe_dir" frp_0.71.1_linux_amd64/frps
openssl rand -hex 32 > "$probe_dir/token"
openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 7 \
  -keyout "$probe_dir/server.key" -out "$probe_dir/ca.pem" \
  -subj '/CN=CloudNest isolated FRP test' \
  -addext "subjectAltName=IP:$probe_ip" \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -addext 'extendedKeyUsage=serverAuth' > "$probe_dir/certificate-setup.log" 2>&1
python3 - "$probe_dir" "$control_port" "$http_port" "$https_port" "$tcp_port" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
control, http, https, tcp = map(int, sys.argv[2:])
quote = lambda name: json.dumps(str(root / name))
(root / 'frps.toml').write_text(f'''bindAddr = "0.0.0.0"
proxyBindAddr = "0.0.0.0"
bindPort = {control}
vhostHTTPPort = {http}
vhostHTTPSPort = {https}
allowPorts = [{{ single = {tcp} }}]
maxPortsPerClient = 3
detailedErrorsToClient = false
auth.method = "token"
auth.tokenSource.type = "file"
auth.tokenSource.file.path = {quote('token')}
transport.tls.force = true
transport.tls.certFile = {quote('ca.pem')}
transport.tls.keyFile = {quote('server.key')}
log.to = "console"
log.level = "warn"
log.disablePrintColor = true
''')
(root / 'run.sh').write_text('''#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")"
exec ./frp_0.71.1_linux_amd64/frps -c ./frps.toml
''')
(root / 'run.sh').chmod(0o700)
PY
"$probe_dir/frp_0.71.1_linux_amd64/frps" verify -c "$probe_dir/frps.toml"
printf '\nPrepared private test directory: %s\n' "$probe_dir"
printf 'Allow inbound TCP for the test machine on: %s %s %s %s\n' "$control_port" "$http_port" "$https_port" "$tcp_port"
printf 'Start in a dedicated terminal: %q\n' "$probe_dir/run.sh"
printf 'Stop only this test instance with Ctrl-C in that terminal.\n'
printf 'Copy ca.pem and token securely to the test Mac; do not paste token in chat.\n'
printf 'Keep server.key on this server. No service or firewall was changed.\n'
