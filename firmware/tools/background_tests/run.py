#!/usr/bin/env python3
"""Production storage/format + codec/runtime/HTTP boundary checks, ASan/UBSan."""
import os
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
FIRMWARE = HERE.parents[1]
with tempfile.TemporaryDirectory(prefix="background-host-") as directory:
    temporary = Path(directory)
    common = [os.environ.get("CC", "cc"), "-std=c11", "-Wall", "-Wextra", "-Werror",
              "-g", "-fsanitize=address,undefined", "-fno-omit-frame-pointer",
              "-I" + str(HERE / "stubs"), "-I" + str(FIRMWARE / "main"),
              "-I" + str(FIRMWARE / "managed_components/espressif__esp_new_jpeg/include")]
    tests = {
        "store": [HERE / "test_store.c", FIRMWARE / "main/background_store.c", FIRMWARE / "main/background_format.c"],
        "runtime": [HERE / "test_runtime.c", FIRMWARE / "main/background_store.c", FIRMWARE / "main/background_format.c"],
    }
    for name, sources in tests.items():
        executable = temporary / name
        subprocess.run(common + list(map(str, sources)) + ["-o", str(executable)], check=True)
        environment = dict(os.environ)
        environment["UBSAN_OPTIONS"] = environment.get("UBSAN_OPTIONS", "") + ":halt_on_error=1"
        subprocess.run([str(executable)], check=True, env=environment)
