#!/usr/bin/env python3
"""Encode synthetic codec fixtures; no real photos or model output are used."""
from pathlib import Path
from PIL import Image, features
import hashlib

root = Path(__file__).resolve().parents[1]
out = root / 'cloudnest_go/internal/imageprep/testdata'
out.mkdir(exist_ok=True)
im = Image.new('RGBA', (3, 2))
im.putdata([(255, 0, 0, 255), (0, 255, 0, 128), (0, 0, 255, 255),
            (255, 255, 255, 255), (0, 0, 0, 0), (255, 255, 0, 255)])
files = []
for name, source, lossless in [('lossless', im, True), ('lossy', im.convert('RGB'), False), ('alpha', im, False)]:
    path = out / (name + '.webp')
    source.save(path, format='WEBP', lossless=lossless, quality=90, method=6)
    files.append(path)
invoice = root / 'cloudnest_go/integration/testdata/images/invoice-synthetic.webp'
with Image.open(invoice.with_suffix('.png')) as image:
    image.save(invoice, format='WEBP', lossless=True, method=6)
files.append(invoice)
print('libwebp', features.version('webp'))
for path in files:
    print(path.relative_to(root), hashlib.sha256(path.read_bytes()).hexdigest())
