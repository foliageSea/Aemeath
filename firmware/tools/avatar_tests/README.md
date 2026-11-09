# Avatar host validation

This harness builds the actual managed LVGL software renderer, generated indexed
avatar descriptors, Chinese font, voice UI and dashboard. No ESP-IDF build folder
is used. Transport actions, screen power, metrics and the unrelated quota/NVS page
are stubbed. The test translation units include the production UI source so tests
can assert animation frames and LVGL object geometry without adding firmware APIs.

The firmware configuration must enable CONFIG_LV_USE_FONT_COMPRESSED=y and
CONFIG_LV_BIN_DECODER_RAM_LOAD=y. The first decodes the compressed Chinese font;
the second expands indexed frames into a bounded per-frame buffer for scaling.
The host harness explicitly enables both and checks visible text/sprite pixels in
every snapshot to catch silent decoder failures.

Run from the repository root (CMake, a host C/C++ compiler and make required):

```sh
cmake -S firmware/tools/avatar_tests -B firmware/build-avatar-host -G "Unix Makefiles" -DCMAKE_BUILD_TYPE=Debug
cmake --build firmware/build-avatar-host -j 8
ctest --test-dir firmware/build-avatar-host --output-on-failure
python3 firmware/tools/avatar_tests/render.py firmware/build-avatar-host
python3 firmware/tools/avatar_convert.py --check --preview firmware/build-avatar-host/avatar-contact-sheet.png
python3 firmware/tools/avatar_font.py --check
```

Use Python with Pillow 12.3.0 for conversion checks and rendering. Font regeneration
uses npm and the pinned lv_font_conv@1.5.3 converter. The checked-in source font is
an OFL-licensed Noto Sans SC subset covering 7,541 characters. The asset converter
checks all 57 original frames, their row order, transparency masks, hashes and
byte-identical generated C/header/blob/manifest output.

CTest validates wake-to-listening coalescing and the greeting duration, listening
and speaking levels, silent decay, Chinese glyph availability, failure playback
frame by frame followed by a held final frame, UI commands and disconnected errors,
new-connection mute reset, and UTF-8-safe truncation. Dashboard checks cover a full
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
