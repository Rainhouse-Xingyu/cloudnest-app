#!/usr/bin/env python3
"""Download the fixed, attributed natural-photo corpus to a NEW private directory.

No model calls, metadata edits, generated captions or embeddings are involved.
Original bytes are hash-pinned; the Go evaluator strips metadata before upload.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import time
from urllib.parse import urlsplit
from urllib.request import Request, build_opener, HTTPRedirectHandler


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path, help='New absolute output directory')
    args = parser.parse_args()
    destination = args.directory
    if not destination.is_absolute() or destination.exists() or destination.is_symlink():
        parser.error('directory must be a new absolute path')
    manifest = Path(__file__).resolve().parents[1] / 'cloudnest_go/integration/testdata/retrieval/dataset-photos-v1.json'
    sources = json.loads(manifest.read_text())['sources']
    # Validate the complete input before creating any output or contacting a host.
    for source in sources:
        url = urlsplit(source['download_url'])
        if (url.scheme != 'https' or url.netloc != 'upload.wikimedia.org' or
                url.query or url.fragment or not url.path.startswith('/wikipedia/commons/') or
                not re.fullmatch(r'[a-z][a-z0-9-]{0,63}', source['id']) or
                not re.fullmatch(r'[0-9a-f]{64}', source['sha256']) or
                not 0 < source['bytes'] <= 16 * 1024**2):
            parser.error('invalid fixed source manifest')
    destination.mkdir(mode=0o700)
    opener = build_opener(NoRedirect)
    for index, source in enumerate(sources):
        if index:
            time.sleep(5)  # Respect the public source's request budget.
        request = Request(source['download_url'], headers={'User-Agent': 'CloudNest-Research/1.0 (educational retrieval evaluation)'})
        with opener.open(request, timeout=60) as response:
            body = response.read(source['bytes'] + 1)
        if len(body) != source['bytes'] or hashlib.sha256(body).hexdigest() != source['sha256']:
            raise RuntimeError('Source bytes changed: ' + source['id'])
        path = destination / (source['id'] + '.jpg')
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, 'wb') as output:
            output.write(body)
            output.flush()
            os.fsync(output.fileno())
        print(source['id'], len(body), source['sha256'], flush=True)
    print('CLOUDNEST_AI_PHOTO_ROOT=' + str(destination))


if __name__ == '__main__':
    main()
