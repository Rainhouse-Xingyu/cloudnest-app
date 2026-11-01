#!/usr/bin/env python3
"""Reproduce synthetic HEIC sources with macOS ImageIO; writes a NEW directory."""
import argparse
import hashlib
from pathlib import Path
import platform
import struct
import subprocess
import zlib
from PIL import Image

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
if platform.system() != 'Darwin' or not args.output.is_absolute():
    parser.error('requires macOS and a new absolute output directory')
args.output.mkdir(exist_ok=False)
root = Path(__file__).resolve().parents[1]
source = root / 'cloudnest_go/integration/testdata/images/invoice-synthetic.png'
exif = bytearray(97)
exif[:2] = b'II'
struct.pack_into('<HIH', exif, 2, 42, 8, 2)
def entry(at, tag, kind, count, value):
    struct.pack_into('<HHII', exif, at, tag, kind, count, value)
entry(10, 0x112, 3, 1, 1)
entry(22, 0x8769, 4, 1, 38)
struct.pack_into('<H', exif, 38, 2)
entry(40, 0x9003, 2, 20, 70)
entry(52, 0x9011, 2, 7, 90)
exif[70:90] = b'2025:06:15 10:20:30\0'
exif[90:97] = b'+08:00\0'
chunk = b'eXIf' + exif
raw = source.read_bytes()
annotated = args.output / 'invoice-exif.png'
annotated.write_bytes(raw[:33] + struct.pack('>I', len(exif)) + chunk + struct.pack('>I', zlib.crc32(chunk)) + raw[33:])
large = args.output / 'large-synthetic.png'
Image.new('RGB', (2000, 1000), (40, 120, 200)).save(large)
for name, image in [('invoice-synthetic', source), ('invoice-exif', annotated), ('large-synthetic', large)]:
    target = args.output / (name + '.heic')
    subprocess.run(['/usr/bin/sips', '-s', 'format', 'heic', str(image), '--out', str(target)], check=True)
    print(name, hashlib.sha256(target.read_bytes()).hexdigest())
