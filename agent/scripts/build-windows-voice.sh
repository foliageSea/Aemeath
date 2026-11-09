#!/usr/bin/env bash
# Voice requires a Windows-targeting C compiler (MinGW-w64 / llvm-mingw).
# Example on macOS: CC=x86_64-w64-mingw32-gcc bash scripts/build-windows-voice.sh
set -euo pipefail
export AEMEATH_WINDOWS_VOICE=1
exec bash "$(dirname -- "$0")/build-windows.sh" "$@"
