#!/usr/bin/env bash
# sherpa's cgo directive embeds its module-cache rpath. Remove that linker
# argument before linking, so MyGO signs an already relocatable executable.
set -euo pipefail
args=()
for arg in "$@"; do
  case "$arg" in
    -Wl,-rpath,*sherpa-onnx-go-macos*) ;;
    *) args+=("$arg") ;;
  esac
done
exec xcrun clang "${args[@]}"
