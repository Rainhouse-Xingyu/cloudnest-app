#!/usr/bin/env python3
"""Generate deterministic, synthetic PDFs with real reportlab/Pillow/pypdf."""
from pathlib import Path
from reportlab.pdfgen.canvas import Canvas
from reportlab.lib.utils import ImageReader
from PIL import Image, ImageDraw, ImageFont
from pypdf import PdfReader, PdfWriter

out = Path(__file__).resolve().parents[1] / 'cloudnest_go/integration/testdata/pdf'
out.mkdir(parents=True, exist_ok=True)
text = out / 'migration.pdf'
c = Canvas(str(text), pagesize=(612, 792), invariant=1)
for title, lines in [
    ('Database migration plan', ['CloudNest synthetic fixture - no personal data.',
     'Migrate MySQL data after a verified backup.', 'Validate row counts and retain a rollback snapshot.',
     'The staging server address is 192.0.2.42.']),
    ('Spring Boot permissions', ['Grant access through explicit read and write permissions.',
     'Test authorization before deploying the Spring Boot service.'])]:
    c.setFont('Helvetica-Bold', 20)
    c.drawString(48, 730, title)
    c.setFont('Helvetica', 12)
    for i, line in enumerate(lines):
        c.drawString(48, 685-i*25, line)
    c.showPage()
c.save()
# Pixels only: this PDF has no invisible text layer. The amount/IP must be read
# by a genuine OCR/vision provider, never manufactured by fixture metadata.
im = Image.new('RGB', (1100, 480), 'white')
d = ImageDraw.Draw(im)
font = ImageFont.load_default(size=30)
for i, line in enumerate(['CLOUDNEST TEST INVOICE', 'Item: Laptop Computer',
                         'Total: CNY 5999.00', 'Server IP: 192.0.2.42',
                         'Synthetic fixture - no personal data']):
    d.text((35, 30+i*82), line, font=font, fill='black')
c = Canvas(str(out/'scanned-invoice.pdf'), pagesize=(660, 288), invariant=1)
c.drawImage(ImageReader(im), 0, 0, width=660, height=288)
c.showPage(); c.save()
reader = PdfReader(text)
w = PdfWriter()
w.add_page(reader.pages[0])
w.encrypt('synthetic-fixture-password', algorithm='RC4-128')
with (out/'encrypted.pdf').open('wb') as f: w.write(f)
w = PdfWriter()
for i in range(65): w.add_page(reader.pages[0])
with (out/'too-many-pages.pdf').open('wb') as f: w.write(f)
for p in sorted(out.glob('*.pdf')): print(p.name, p.stat().st_size)
