#!/usr/bin/env python3
"""Run real audio.c + echo_stream.c against deterministic host peripherals."""
import os
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
FIRMWARE = HERE.parents[1]
STUBS = [
    "esp_err.h", "esp_aec.h", "esp_timer.h", "board.h",
    "driver/i2s_std.h", "esp_codec_dev_defaults.h", "esp_log.h",
    "esp_heap_caps.h", "freertos/idf_additions.h",
    "freertos/FreeRTOS.h", "freertos/queue.h", "freertos/task.h",
]
with tempfile.TemporaryDirectory(prefix="audio-host-") as directory:
    temporary = Path(directory)
    for name in STUBS:
        path = temporary / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#pragma once\n")
    executable = temporary / "audio_test"
    command = [os.environ.get("CC", "cc"), "-std=c11", "-Wall", "-Wextra",
               "-Werror", "-Wno-unused-function", "-g",
               "-fsanitize=address,undefined", "-fno-omit-frame-pointer",
               "-I" + str(temporary), "-I" + str(FIRMWARE / "main"),
               str(HERE / "test_audio.c"),
               str(FIRMWARE / "main/audio/echo_stream.c"),
               "-o", str(executable)]
    subprocess.run(command, check=True)
    subprocess.run([str(executable)], check=True)
