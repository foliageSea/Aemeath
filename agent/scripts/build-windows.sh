#!/usr/bin/env bash
# Cross-compile the MyGO desktop app and its NSIS Windows installer.
set -euo pipefail

usage() {
  echo "Usage: $0 [windows/amd64|windows/arm64] [mygo build flags...]"
}
if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
platform="windows/amd64"
if [[ $# -gt 0 && "$1" != -* ]]; then
  platform="$1"
  shift
fi
case "$platform" in
  windows/amd64|windows/arm64) ;;
  *) usage >&2; exit 2 ;;
esac
command -v go >/dev/null || { echo "Missing tool: Go 1.27.3 or newer." >&2; exit 1; }
if ! command -v makensis >/dev/null; then
  echo "Missing tool: makensis (NSIS 3)." >&2
  echo "macOS: brew install nsis" >&2
  echo "Debian/Ubuntu: sudo apt-get install nsis" >&2
  exit 1
fi

project_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$project_dir"
# Run the icon generator and MyGO CLI on the host. MyGO sets the target itself.
unset GOOS GOARCH
export GOFLAGS="${GOFLAGS:-} -mod=readonly"
export CGO_ENABLED=0
go run ./scripts/icon
if [[ "${AEMEATH_WINDOWS_VOICE:-0}" == 1 ]]; then
  # Build the CLI on the host before exporting the target C compiler.
  voice_cc="${CC:-}"
  if [[ -z "$voice_cc" ]]; then
    case "$platform" in
      windows/amd64) voice_cc=x86_64-w64-mingw32-gcc ;;
      windows/arm64) voice_cc=aarch64-w64-mingw32-gcc ;;
    esac
  fi
  command -v "$voice_cc" >/dev/null || {
    echo "Voice build requires a Windows-targeting C compiler; set CC to its executable path." >&2; exit 1;
  }
  # Avoid extra MinGW C/C++ runtime DLL dependencies in the produced app.
  export CGO_LDFLAGS="${CGO_LDFLAGS:-} -static-libgcc -static-libstdc++"
  mkdir -p build/voice-tools
  go build -o build/voice-tools/mygo github.com/egoist/mygo/cmd/mygo
  bash ./scripts/stage-voice-native.sh "$platform"
  CGO_ENABLED=1 CC="$voice_cc" ./build/voice-tools/mygo build -platform "$platform" "$@"
else
  # A previous voice build may have staged DLLs; keep telemetry packages lean.
  target="windows-${platform#*/}"
  for library in sherpa-onnx-c-api.dll onnxruntime.dll sherpa-onnx-LICENSE; do
    link="$project_dir/resources/$target/$library"
    if [[ -L "$link" && "$(readlink "$link")" == "../native/$target/$library" ]]; then
      unlink "$link"
    elif [[ -e "$link" ]]; then
      echo "Refusing to remove unexpected resource: $link" >&2; exit 1
    fi
  done
  go run github.com/egoist/mygo/cmd/mygo build -platform "$platform" "$@"
fi
echo "Build complete. See the MyGO artifact paths above."
