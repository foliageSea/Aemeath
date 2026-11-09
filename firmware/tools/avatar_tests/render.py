#!/usr/bin/env python3
"""Convert real host LVGL PPM snapshots to PNG and a native-size overview.

Run after ctest: python3 firmware/tools/avatar_tests/render.py firmware/build-avatar-host
Requires Pillow; this only packages LVGL output, it does not recreate UI widgets.
"""
import argparse
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    font_path = Path(__file__).resolve().parents[2] / "main/assets/fonts/source/VoiceSC-Regular.ttf"
    font = ImageFont.truetype(str(font_path), 20)
    states = ["idle", "wake", "listening", "thinking", "speaking", "error"]
    overview = Image.new("RGB", (480 * 3, 520 * 2 + 42), "#202936")
    draw = ImageDraw.Draw(overview)
    draw.text((16, 8), "Actual host LVGL snapshots | native 480×480 | device hardware not captured", font=font, fill="white")
    for i, state in enumerate(states):
        path = args.directory / f"voice-{state}.ppm"
        image = Image.open(path).convert("RGB")
        assert image.size == (480, 480)
        # Guard against a blank or failed image/font decode in the preview.
        assert len(image.getcolors(480 * 480)) > 100
        image.save(path.with_suffix(".png"))
        x, y = (i % 3) * 480, (i // 3) * 520 + 42
        draw.text((x + 16, y + 8), state.capitalize(), font=font, fill="white")
        overview.paste(image, (x, y + 40))
    target = args.directory / "avatar-lvgl-preview.png"
    overview.save(target)
    print(f"Saved six native 480×480 LVGL PNG snapshots and {target}")


if __name__ == "__main__":
    main()
