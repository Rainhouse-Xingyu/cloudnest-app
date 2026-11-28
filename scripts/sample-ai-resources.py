#!/usr/bin/env python3
"""Record bounded, read-only loopback Ollama/process observations to private JSONL.

Reported model VRAM and sampled resident sets are observations, not hard memory
bounds. Process names are collected without arguments or environment variables.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import time
from urllib.request import ProxyHandler, HTTPRedirectHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--samples', type=int, default=300)
    parser.add_argument('--interval', type=float, default=2.0, help='Seconds between sample start times')
    parser.add_argument('--model', action='append', default=[], help='Model tag to include; repeat for several')
    args = parser.parse_args()
    if (not args.output.is_absolute() or not args.output.parent.is_dir() or
            not 1 <= args.samples <= 1800 or not 0.5 <= args.interval <= 10 or
            args.samples * args.interval > 3600):
        parser.error('new absolute output path, 1..1800 samples and nominal duration at most one hour required')
    wanted = set(args.model)
    opener = build_opener(ProxyHandler({}), NoRedirect)
    descriptor = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    started = time.monotonic()
    with os.fdopen(descriptor, 'w', encoding='utf-8') as output:
        for index in range(args.samples):
            if index:
                time.sleep(max(0, started + index * args.interval - time.monotonic()))
            row = {'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   'index': index, 'elapsed_seconds': time.monotonic() - started}
            if index == 0:
                row['sampler'] = {'version': 1, 'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                                  'python': platform.python_version(), 'platform': platform.platform(),
                                  'requested_models': sorted(wanted), 'interval_seconds': args.interval,
                                  'samples': args.samples}
            try:
                request = Request('http://127.0.0.1:11434/api/ps')
                with opener.open(request, timeout=3) as response:
                    raw = response.read(512 * 1024 + 1)
                if len(raw) > 512 * 1024:
                    raise ValueError('model inventory exceeds observation budget')
                models = json.loads(raw)['models']
                if not isinstance(models, list) or any(not isinstance(m, dict) for m in models):
                    raise ValueError('invalid model inventory')
                row['models'] = [m for m in models if not wanted or m.get('name') in wanted]
                row['other_loaded_model_count'] = sum(bool(wanted) and m.get('name') not in wanted for m in models)
            except Exception as error:
                row['model_error'] = type(error).__name__
            try:
                result = subprocess.run(['ps', '-axo', 'pid,rss,comm'], capture_output=True, text=True, timeout=3, check=True)
                row['processes'] = [line.strip() for line in result.stdout.splitlines()
                                    if 'ollama' in line.lower() or 'llama-server' in line.lower()
                                    or 'integration.test' in line or ' integration.tes' in line]
            except Exception as error:
                row['process_error'] = type(error).__name__
            output.write(json.dumps(row, ensure_ascii=False, allow_nan=False) + '\n')
            output.flush()


if __name__ == '__main__':
    main()
