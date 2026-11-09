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
export CGO_ENABLED=0
go run ./scripts/icon
go run github.com/egoist/mygo/cmd/mygo build -platform "$platform" "$@"
echo "Build complete. See the MyGO artifact paths above."
