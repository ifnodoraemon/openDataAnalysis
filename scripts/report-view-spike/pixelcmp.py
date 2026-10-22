#!/usr/bin/env python3
"""Verify that cropping one full-page screenshot equals per-slice clip captures.

The spike claims the two strategies are pixel-identical, which is what makes the
production implementation cheap: one screenshot per capture, slicing done
server-side in Go.

Usage:
    python3 pixelcmp.py <out_dir>

Expects <out_dir>/full.png, <out_dir>/clip-*.png and <out_dir>/slices.json
produced by slice_compare.js.
"""
import json
import sys
from pathlib import Path

import numpy as np
from PIL import Image


def main() -> int:
    out = Path(sys.argv[1] if len(sys.argv) > 1 else "/spike/out")
    meta = json.loads((out / "slices.json").read_text())
    full = np.asarray(Image.open(out / "full.png").convert("RGB"))
    print(f"full page: {full.shape[1]}x{full.shape[0]}")

    identical = 0
    failures = []
    for s in meta["slices"]:
        y, h = s["y"], s["h"]
        crop = full[y : y + h, 0 : full.shape[1]]
        name = f"clip-{s['block_id']}-{s['part'].replace('/', 'of')}.png"
        clip = np.asarray(Image.open(out / name).convert("RGB"))
        if clip.shape != crop.shape:
            failures.append((name, f"shape {clip.shape} != {crop.shape}"))
            continue
        diff = np.abs(clip.astype(int) - crop.astype(int))
        if diff.max() == 0:
            identical += 1
        else:
            failures.append((name, f"maxdiff={diff.max()}"))
    print(f"identical: {identical}/{len(meta['slices'])}")
    for name, why in failures:
        print(f"  FAIL {name}: {why}")
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
