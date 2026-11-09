#!/usr/bin/env python3
"""Build the OFL-licensed voice font from the checked-in static subset.

Default: Python stdlib + npm (lv_font_conv pinned to 1.5.3).
--prepare upstream.ttf: additionally requires fonttools==4.61.1 and creates the
static 400-weight source subset. Upstream URL and SHA are recorded in metadata.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
FONTS = ROOT / "main/assets/fonts"
SOURCE = FONTS / "source/VoiceSC-Regular.ttf"
UPSTREAM = "https://raw.githubusercontent.com/google/fonts/main/ofl/notosanssc/NotoSansSC%5Bwght%5D.ttf"


def symbols():
    # GB2312 supplies 6,763 common simplified Chinese characters and punctuation;
    # add ASCII/curly quotes and all Chinese text used on the voice page.
    chars = {chr(i) for i in range(32, 127)} | set("“”‘’…—爱弥斯")
    for high in range(0xa1, 0xf8):
        for low in range(0xa1, 0xff):
            try:
                chars.update(bytes((high, low)).decode("gb2312"))
            except UnicodeDecodeError:
                pass
    return "".join(sorted(chars))


def prepare(upstream):
    from fontTools.ttLib import TTFont
    from fontTools.varLib.instancer import instantiateVariableFont
    from fontTools import subset
    font = TTFont(upstream, recalcTimestamp=False)
    font = instantiateVariableFont(font, {"wght": 400}, inplace=True)
    opts = subset.Options()
    opts.recalc_timestamp = False
    opts.layout_features = []
    sub = subset.Subsetter(options=opts)
    sub.populate(text=symbols())
    sub.subset(font)
    # Name the modified subset explicitly; keep original copyright/license IDs.
    for record in font["name"].names:
        if record.nameID in (1, 4, 6):
            record.string = ("Aemeath Voice SC" if record.nameID != 6 else "AemeathVoiceSC-Regular").encode(record.getEncoding())
        elif record.nameID == 2:
            record.string = "Regular".encode(record.getEncoding())
    font.save(SOURCE)
    available = font.getBestCmap()
    supported = "".join(c for c in symbols() if ord(c) in available)
    (FONTS / "source/symbols.txt").write_text(supported, encoding="utf-8")
    metadata = dict(upstream=UPSTREAM, upstream_sha256=hashlib.sha256(upstream.read_bytes()).hexdigest(),
                    subset_sha256=hashlib.sha256(SOURCE.read_bytes()).hexdigest(),
                    fonttools="4.61.1", weight=400, glyphs=len(supported),
                    license="SIL Open Font License 1.1 (OFL.txt)", converter="lv_font_conv@1.5.3",
                    size=20, bpp=4, kerning=False, compression=False)
    (FONTS / "source/manifest.json").write_text(json.dumps(metadata, indent=2) + "\n")


def build(check):
    supported = (FONTS / "source/symbols.txt").read_text(encoding="utf-8")
    meta = json.loads((FONTS / "source/manifest.json").read_text())
    assert hashlib.sha256(SOURCE.read_bytes()).hexdigest() == meta["subset_sha256"]
    assert meta["compression"] is False, "parallel drawing requires plain glyph bitmaps"
    # Use stable repository-relative command arguments in generated comments.
    with tempfile.TemporaryDirectory(prefix="avatar-font-") as tmp:
        output = Path(tmp) / "voice_font_20.c"
        args = ["npm", "exec", "--yes", "--package=lv_font_conv@1.5.3", "--", "lv_font_conv",
                "--size", "20", "--bpp", "4", "--format", "lvgl", "--font",
                str(SOURCE.relative_to(ROOT)), "--symbols", supported,
                # LVGL 9.6 compressed glyph RLE cursor is global, not per draw worker.
                # Plain 4bpp glyphs avoid that decoder race without serializing drawing.
                "--no-compress", "--no-kerning", "--lv-font-name", "voice_font_20", "-o", str(output)]
        subprocess.run(args, cwd=ROOT, check=True)
        data = output.read_text()
        # Converter includes command line in its header: normalize temp path.
        data = data.replace(str(output), "main/assets/fonts/voice_font_20.c")
        # Keep generated output reproducible without a whitespace-error EOF.
        data = data.rstrip() + "\n"
        data = "// Noto Sans SC derivative; Copyright Adobe. SIL OFL 1.1: see OFL.txt.\n" + data
        assert re.search(r"\.bitmap_format\s*=\s*0,", data), "converter must emit plain font"
        target = FONTS / "voice_font_20.c"
        bitmap = data.split("glyph_bitmap[] = {", 1)[1].split("};", 1)[0]
        bitmap_bytes = len(re.findall(r"0x[0-9a-fA-F]+", bitmap))
        if bitmap_bytes > 1 << 20:
            # Default LVGL descriptors have only 20 bits for bitmap_index. Fail
            # loudly instead of wrapping the final Chinese glyphs at 1 MiB.
            data = data.replace("#if VOICE_FONT_20\n", "#if VOICE_FONT_20\n"
                "#if !LV_FONT_FMT_TXT_LARGE\n"
                '#error "voice_font_20 requires LV_FONT_FMT_TXT_LARGE for bitmap offsets above 1 MiB"\n'
                "#endif\n", 1)
        previous = target.read_text() if target.exists() else ""
        old_bitmap = previous.split("glyph_bitmap[] = {", 1)[1].split("};", 1)[0] if previous else ""
        old_bytes = len(re.findall(r"0x[0-9a-fA-F]+", old_bitmap))
        if check:
            assert target.read_text() == data, "font output differs"
        else:
            target.write_text(data)
    print(("Verified" if check else "Generated") + f" plain 20px/4bpp Chinese font, {len(supported)} characters; "
          f"bitmap {bitmap_bytes:,} bytes ({bitmap_bytes - old_bytes:+,} vs previous)")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", type=Path)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if args.prepare:
        prepare(args.prepare)
    build(args.check)


if __name__ == "__main__":
    main()
