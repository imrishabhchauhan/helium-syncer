"""Turns the real Helium captures (testing/lab/capture-helium.ps1) into the
pictures the setup window shows. Usage:

    python testing/lab/make-pictures.py <captures-dir> internal/ui/img

Each picture is a whole Helium window. The setup window marks the part
that matters (tab strip, bookmarks bar, ...) with a frame.

The Mac window is the same Helium window with the macOS window buttons.
Helium's own interface is the same on both systems.
"""
import os
import sys

from PIL import Image, ImageDraw

src, out = sys.argv[1], sys.argv[2]
os.makedirs(out, exist_ok=True)

# The window inside a 2x capture (1760x1040). The left, right and bottom
# edges are the Windows resize border, which is not part of the window.
WINDOW = (12, 0, 1748, 1028)
WIDTH = 1300  # shown at about 650 px: sharp on 2x screens


def save(img, name):
    h = round(img.height * WIDTH / img.width)
    img = img.resize((WIDTH, h), Image.LANCZOS)
    path = os.path.join(out, name + ".webp")
    img.save(path, "WEBP", quality=86, method=6)
    print("saved", path, img.size, os.path.getsize(path) // 1024, "KB")


def traffic_lights(strip):
    """Paint the macOS window buttons on a 2x tab strip (supersampled)."""
    ss = 4
    w, h = strip.size
    layer = Image.new("RGBA", (w * ss, h * ss), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    for i, (fill, ring) in enumerate([((255, 95, 87), (226, 70, 62)), ((254, 188, 46), (223, 158, 26)), ((40, 200, 64), (26, 170, 46))]):
        cx, cy, r = (40 + 40 * i) * ss, 36 * ss, 12 * ss
        d.ellipse((cx - r, cy - r, cx + r, cy + r), fill=ring)
        d.ellipse((cx - r + ss, cy - r + ss, cx + r - ss, cy + r - ss), fill=fill)
    strip.alpha_composite(layer.resize((w, h), Image.LANCZOS))


for theme in ("light", "dark"):
    for page in ("window", "history", "settings", "extensions"):
        img = Image.open(os.path.join(src, f"{page}-{theme}.png")).convert("RGBA").crop(WINDOW)
        save(img, f"{page}-{theme}")
        if page == "window":
            # Mac: macOS buttons on the left, no Windows buttons on the right.
            w = img.width
            strip = Image.new("RGBA", (w, 70), img.getpixel((w - 330, 8)))
            strip.paste(img.crop((0, 3, 1400, 70)), (150, 3))  # rows 0-2: the Windows border line
            traffic_lights(strip)
            mac = img.copy()
            mac.paste(strip, (0, 0))
            save(mac, f"window-mac-{theme}")
