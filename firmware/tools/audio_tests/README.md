# Audio integration host tests

Run from the repository root:

```sh
python3 firmware/tools/audio_tests/run.py
```

Requires Python 3 and a host C compiler with AddressSanitizer and
UndefinedBehaviorSanitizer (Clang or GCC). Set `CC` to select a compiler.
The runner uses C11, `-Wall -Wextra -Werror`, ASan and UBSan. It suppresses
only unused static functions because the single stub header deliberately
contains a delay trap even though production playback must never call it.
Compilation products and include shims live in an automatically removed
temporary directory. No ESP-IDF build, download, hardware, or source rewrite
is needed.

`test_audio.c` includes the production `firmware/main/audio/audio.c` directly
and links the production `echo_stream.c`. The real protocol and audio headers
are used. `host.h` supplies just the peripheral surface needed by that unit:
bounded copying queues, a deterministic clock, registered I2S callbacks, codec
configuration interfaces, programmable PA errors, aligned allocation, and a
512-sample AEC observer. The observer records both native inputs and produces
a distinctive processed signal; it does not simulate acoustic cancellation.

The tests drive the registered TX/RX callbacks with distinct real DMA buffers
and stereo mic samples. Queue waits advance the fake clock and dispatch ISR
events; tests can therefore exercise `audio_capture()` synchronously without
threads or pre-filling a queue that capture reset will discard. Both interrupt
orders are exercised. Native input and network output are checked sample by
sample.

Covered behavior:

- References reflect DMA PCM that has completed playback, before callback
  refill, rather than queued PCM. Software volume, clamping, stereo expansion,
  short-frame zero padding, underrun silence, and delayed render credits are
  checked independently.
- Start/abort/restart perform no queue waits or task delays. Cancellation
  invalidates both queued and DMA-resident old-generation completion credits.
  Render polling never enables PA. Four new completions are required after
  restart; timeout, PA-off failure, and PA-on failure cannot enable playback.
- PA changes are reflected at their sample positions within a DMA block.
  Repeated abort preserves the original PA-off transition timestamp.
- Alternating RX/TX interrupt order, 320-sample capture, 512-sample AEC, and
  320-sample reads preserve sample order and mic/reference pairing. Receive
  sequence wrap is included.
- Missing reference, receive sequence gaps, receive intervals outside
  10..30ms, skipped TX, and extra TX EOF with a changed nearest sequence
  reject capture without returning raw or modifying the caller output.
  Queue timeout and AEC creation failure are included.
- Explicit capture reset discards queued output, incomplete native input,
  queued RX and ISR-held pre-reset RX. ISR events dispatched during AEC
  creation verify that the fresh-capture cutoff is set after creation.

The harness is deterministic and single threaded. It validates state-machine
behavior and buffer ownership; real interrupt scheduling, codec/AEC quality,
and electrical PA behavior require device validation.
