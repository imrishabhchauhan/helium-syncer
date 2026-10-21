"""Turns real Helium captures into the pictures the setup window shows.

    python testing/lab/make-pictures.py <windows-captures> <mac-captures> internal/ui/img

Captures come from testing/lab/capture-helium.ps1 (Windows) and
testing/lab/capture-helium-mac.sh (macOS): window, history, settings and
extensions, each as <page>-<light|dark>.png, 1760 x 1040 (2x).

Output: <page>-win-<theme>.webp and <page>-mac-<theme>.webp. A Mac page
that is missing is left out; the page then uses the Windows picture.
"""
import os
import sys

from PIL import Image

win_dir, mac_dir, out = sys.argv[1], sys.argv[2], sys.argv[3]
os.makedirs(out, exist_ok=True)

# The window inside each capture. On Windows the left, right and bottom
# edges are the resize border, which is not part of the window.
WINDOW = {"win": (12, 0, 1748, 1028), "mac": (0, 0, 1760, 1040)}
WIDTH = 1300  # shown at about 650 px: sharp on 2x screens


def save(img, name):
    img = img.resize((WIDTH, round(img.height * WIDTH / img.width)), Image.LANCZOS)
    path = os.path.join(out, name + ".webp")
    img.save(path, "WEBP", quality=86, method=6)
    print("saved", path, img.size, os.path.getsize(path) // 1024, "KB")


for tag, src in (("win", win_dir), ("mac", mac_dir)):
    for theme in ("light", "dark"):
        for page in ("window", "history", "settings", "extensions"):
            path = os.path.join(src, f"{page}-{theme}.png")
            if not os.path.exists(path):
                print("missing", path)
                continue
            save(Image.open(path).convert("RGB").crop(WINDOW[tag]), f"{page}-{tag}-{theme}")
