#!/usr/bin/env python3
"""ASan/UBSan checks of production page transport, no device or Go files needed."""
import os
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
FIRMWARE = HERE.parents[1]
with tempfile.TemporaryDirectory(prefix="page-state-") as directory:
    temporary = Path(directory)
    for name in ("esp_http_server.h", "freertos/FreeRTOS.h", "freertos/task.h"):
        path = temporary / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("#pragma once\n")
    executable = temporary / "page_checks"
    subprocess.run([os.environ.get("CC", "cc"), "-std=c11", "-Wall", "-Wextra", "-Werror",
                    "-g", "-fsanitize=address,undefined", "-fno-omit-frame-pointer", "-pthread",
                    "-I" + str(temporary), "-I" + str(FIRMWARE / "main"),
                    str(HERE / "test_page.c"), "-o", str(executable)], check=True)
    subprocess.run([str(executable)], check=True,
                   env=dict(os.environ, UBSAN_OPTIONS="halt_on_error=1"))
