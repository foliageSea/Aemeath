#!/usr/bin/env bash
# Build a self-contained MyGO macOS app and DMG, including hardware sensors.
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "DMG packaging requires macOS, Xcode Command Line Tools and hdiutil." >&2
  exit 1
fi
project_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$project_dir"
platform="${1:-darwin/universal}"
if [[ $# -gt 0 ]]; then shift; fi
case "$platform" in
  darwin/arm64|darwin/amd64|darwin/universal) ;;
  *) echo "Usage: $0 [darwin/universal|darwin/arm64|darwin/amd64] [mygo build flags...]" >&2; exit 2 ;;
esac
for tool in go xcrun hdiutil codesign; do
  command -v "$tool" >/dev/null || { echo "Missing tool: $tool" >&2; exit 1; }
done
xcrun --find clang >/dev/null
# Keep cgo enabled: Aemeath uses IOKit temperature and GPU sensors on macOS.
export CGO_ENABLED=1
# Match the bundle minimum; otherwise clang targets the build machine's macOS.
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=13.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=13.0"
go run ./scripts/icon
args=(build -platform "$platform")
if [[ -n "${AEMEATH_SIGN_IDENTITY:-}" ]]; then
  args+=(-sign "$AEMEATH_SIGN_IDENTITY")
fi
go run github.com/egoist/mygo/cmd/mygo "${args[@]}" "$@"
echo "Build complete. See the MyGO artifact paths above."
