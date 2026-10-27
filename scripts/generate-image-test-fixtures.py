#!/usr/bin/env python3
"""Deterministic text-only synthetic invoice; beach pixels come from imagegen."""
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont
out = Path(__file__).resolve().parents[1] / 'cloudnest_go/integration/testdata/images'
out.mkdir(parents=True, exist_ok=True)
im = Image.new('RGB', (1100,480), 'white')
draw = ImageDraw.Draw(im)
font = ImageFont.load_default(size=30)
for i,line in enumerate(['CLOUDNEST TEST INVOICE','Item: Laptop Computer','Total: CNY 5999.00','Server IP: 192.0.2.42','Synthetic fixture - no personal data']):
    draw.text((35,30+i*82),line,font=font,fill='black')
im.save(out/'invoice-synthetic.png')
