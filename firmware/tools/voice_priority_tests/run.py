#!/usr/bin/env python3
"""Compile the production voice and WebSocket units with bounded host queues.

Run: python3 firmware/tools/voice_priority_tests/run.py
Only a host C compiler and the already vendored cJSON sources are required.
ASan/UBSan check ownership and malformed envelope handling.
"""
import os
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
FIRMWARE = HERE.parents[1]
CJSON = FIRMWARE / "managed_components/espressif__cjson/cJSON"
STUBS = ["esp_err.h", "esp_log.h", "esp_timer.h", "esp_random.h", "esp_heap_caps.h",
         "esp_http_server.h", "freertos/FreeRTOS.h", "freertos/queue.h",
         "freertos/task.h", "freertos/idf_additions.h", "ui/voice_page.h", "ui/dashboard.h"]
with tempfile.TemporaryDirectory(prefix="voice-priority-") as tmp:
    tmp = Path(tmp)
    for name in STUBS:
        path = tmp / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#pragma once\n")
    for name in ("voice", "ws"):
        output = tmp / name
        subprocess.run([os.environ.get("CC", "cc"), "-std=c11", "-D_POSIX_C_SOURCE=200809L",
                        "-g", "-fsanitize=address,undefined", "-fno-omit-frame-pointer",
                        "-Wall", "-Wextra", "-Wno-unused-function", "-Wno-unused-variable",
                        "-Wno-sign-compare", "-include", str(HERE / "host.h"),
                        "-I" + str(tmp), "-I" + str(FIRMWARE / "main"), "-I" + str(CJSON),
                        str(HERE / ("test_" + name + ".c")),
                        str(FIRMWARE / "main/voice/protocol.c"), str(CJSON / "cJSON.c"),
                        "-lm", "-o", str(output)], check=True)
        subprocess.run([str(output)], check=True)
