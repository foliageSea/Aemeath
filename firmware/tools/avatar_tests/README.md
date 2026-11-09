# Avatar host validation

This harness builds the actual managed LVGL software renderer, generated indexed
avatar descriptors, Chinese font, voice UI and dashboard. No ESP-IDF build folder
is used. Transport actions, screen power, metrics and the unrelated quota/NVS page
are stubbed. The test translation units include the production UI source so tests
can assert animation frames and LVGL object geometry without adding firmware APIs.

The Chinese font is deliberately **uncompressed 4bpp**. In the managed LVGL 9.6,
compressed glyph decoding uses the shared `LV_GLOBAL_DEFAULT()->font_fmt_rle`
input/cursor state, while software draw workers can decode different labels at
the same time. Plain glyphs avoid this path without reducing animation refresh
or serializing the renderer. This source-grounded race is a credible explanation
for intermittent corrupted Chinese text; these host tests do not reproduce every
physical panel/PSRAM timing condition.

The font bitmap exceeds 1 MiB, so the firmware must enable
`CONFIG_LV_FONT_FMT_TXT_LARGE=y` (the default descriptor has only a 20-bit offset).
The generated C has a compile-time guard against truncating high-index glyphs.
`CONFIG_LV_BIN_DECODER_RAM_LOAD=y` is still needed to expand indexed avatar frames
into a bounded per-frame buffer for scaling. The host harness enables large font
offsets, explicitly disables compressed fonts, and checks visible text/sprite
pixels in every snapshot to catch silent decoder failures.

Run from the repository root (CMake, a host C/C++ compiler and make required):

```sh
cmake -S firmware/tools/avatar_tests -B firmware/build-avatar-host -G "Unix Makefiles" -DCMAKE_BUILD_TYPE=Debug
cmake --build firmware/build-avatar-host -j 8
ctest --test-dir firmware/build-avatar-host --output-on-failure
python3 firmware/tools/avatar_tests/render.py firmware/build-avatar-host
python3 firmware/tools/avatar_convert.py --check --preview firmware/build-avatar-host/avatar-contact-sheet.png
python3 firmware/tools/avatar_font.py --check

# Also exercise two real pthread software draw workers, matching the device.
cmake -S firmware/tools/avatar_tests -B firmware/build-avatar-host/font-stability -G "Unix Makefiles" -DCMAKE_BUILD_TYPE=Debug -DAVATAR_PARALLEL_DRAW=ON
cmake --build firmware/build-avatar-host/font-stability -j 8
ctest --test-dir firmware/build-avatar-host/font-stability --output-on-failure
```

Use Python with Pillow 12.3.0 for conversion checks and rendering. Font regeneration
uses npm and the pinned lv_font_conv@1.5.3 converter. The checked-in source font is
an OFL-licensed Noto Sans SC subset covering 7,541 characters. The asset converter
checks all 57 original frames, their row order, transparency masks, hashes and
byte-identical generated C/header/blob/manifest output.

CTest validates wake-to-listening coalescing and the greeting duration, listening
and speaking levels, silent decay, Chinese glyph availability, failure playback
frame by frame followed by a held final frame, UI commands and disconnected errors,
new-connection mute reset, UTF-8-safe truncation, and 100 duplicate long-caption
snapshots causing no label text writes (including ellipsized labels). A separate
font test compares 120,000 concurrent glyph decodes across four pthreads with
serial golden bitmaps, checks 4bpp alpha levels and offsets above 1 MiB, and rejects
compressed output. Dashboard checks cover a full
3-second dwell, idempotent repeated finish, cancellation by begin, explicit and swipe
navigation priority, and the uint32 tick wraparound boundary. Assertions stay enabled
even in release builds.

CTest writes native 480×480 PPM snapshots of six states using lv_snapshot_take().
The renderer script losslessly converts those PPMs to PNG and assembles a labeled
overview without resizing or repainting the widgets. These are real host LVGL
renders of the voice tile, not photographs or captures from the ESP32 LCD. The
contact sheet is separately labeled by its filename as asset conversion output.

Hardware-only behavior (panel power, touch controller, audio input/output and IDF
integration) remains part of the lead firmware build and physical-device checks.
