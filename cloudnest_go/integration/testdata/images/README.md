# Synthetic image fixtures

No personal photographs, real invoices, model responses or vectors are stored here.

- `beach-synthetic.png`: generated with the built-in `image_gen` tool, copied into this repository and visually inspected. It depicts sand, ocean waves, rocks and sky without visible text. The live test uploads it under an unrelated filename and supplies pixels to the real vision model.
- `invoice-synthetic.png`: generated deterministically with Pillow by `scripts/generate-image-test-fixtures.py`. It contains Laptop Computer, CNY 5999.00 and documentation IP 192.0.2.42. It has no hidden OCR text or capture date.

The test inserts **synthetic** PNG eXIf metadata into the beach image at runtime: June 15 of the preceding year, 10:20:30, +08:00. This tests parser/storage/calendar behavior; it is not evidence of a real photographic capture date. Both base PNGs are kept without this test date.

Exact built-in generation prompt (no CLI fallback was used):

> Create one synthetic photorealistic landscape image for a software vision-search test fixture. Quiet empty sandy beach by the ocean in daylight, small waves breaking on sand, blue sea, pale blue sky with a few white clouds, a few rocks to the left. Clear natural realistic textures, wide view, no humans, no buildings, no boats, no text, no logo, no watermark. Square 1024x1024 composition. This is synthetic test data, not a documentary photograph.

Two fixtures establish a functional smoke test only. They do not measure general vision/OCR accuracy, Chinese handwriting, difficult lighting or search Recall/MRR.

| File | Pixels | SHA-256 |
| --- | --- | --- |
| beach-synthetic.png | 1254 × 1254 | 46fb75e9a0db7d4dbe339bd25f10829280182f955b75b77190e4ba27f44d1dd2 |
| invoice-synthetic.png | 1100 × 480 | 651e5ea7e9b0de70197a8d697de442692d2aa1025e3369dce0131e17651c7d1f |

Both images were visually inspected; the invoice text fits within the canvas.
