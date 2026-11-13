# Background host validation

Run from the repository root:

~~~sh
python3 firmware/tools/background_tests/run.py
~~~

The runner compiles the production background_store.c and background_format.c
with -Wall -Wextra -Werror and ASan/UBSan in a temporary directory. Fake flash
injects failures and partial writes at every erase/write/read/hash point of a
replacement and deletion, plus cancellation gates. It checks slot recovery,
committed EMPTY tombstones, payload bounds, no-op writes and uint64 exhaustion.
Format checks cover strict ETags/digests, JPEG geometry, progressive/truncated
input, marker bounds and deterministic fuzz inputs.

A second executable includes the actual production background_codec.c,
background.c and net/background_http.c. Only ESP-IDF/PSA/JPEG/HTTPD/RTOS boundary
calls are stubbed. It checks PSRAM/alignment contracts, decode failures and
cleanup, persisted restore, deletion after a failed display swap and a no-write
retry, exact uint64 JSON, response-loss recovery state, body preflight/error
codes, worker-only receive/send, busy admission and matching async completion.
The JPEG fixture in this boundary test is structural, not a photographic decode;
the ESP32-specific decoder and cryptographic implementation are not executable
or cryptographically revalidated by these host stubs.

Real shared-layer rendering is exercised by the existing avatar host harness:

~~~sh
cmake -S firmware/tools/avatar_tests -B firmware/build-background-host -G "Unix Makefiles" -DCMAKE_BUILD_TYPE=Debug -DAVATAR_PARALLEL_DRAW=ON
cmake --build firmware/build-background-host -j 8
ctest --test-dir firmware/build-background-host --output-on-failure
~~~

It tests default plain pixels on all three tiles, 90 replacements with immediate
old-source release, deletion, source cache retirement, provisioning-overlay
opacity and layer order, in addition to existing real avatar/font/navigation
checks. Quota/NVS transport remains stubbed, as documented by that harness.
For full renderer memory checks use a separate build directory and pass both
CMAKE_C_FLAGS and CMAKE_CXX_FLAGS as
-fsanitize=address,undefined -fno-omit-frame-pointer.

The firmware must be built with the updated partition table (background data at
0x610000, size 0x80000); installing only the app onto an old partition table
leaves background support unavailable rather than aborting monitoring/voice.
No default background image/data partition binary is shipped. Flash/NVS/app
addresses are unchanged. Host snapshots and builds are not real LCD captures;
actual JPEG decode, Flash/audio timing, PSRAM pressure, touch, panel output and
physical power interruption still require separate hardware validation. Do not
flash a board or replace release artifacts merely to run these checks.
