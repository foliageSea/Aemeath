#!/usr/bin/env bash
# Run the native MyGO app with file watching and terminal restart/quit commands.
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Native desktop development requires macOS and Xcode Command Line Tools." >&2
  exit 1
fi
for tool in go xcrun; do
  command -v "$tool" >/dev/null || { echo "Missing tool: $tool" >&2; exit 1; }
done
xcrun --find clang >/dev/null

project_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$project_dir"
unset GOOS GOARCH
export CGO_ENABLED=1
export GOFLAGS="${GOFLAGS:-} -mod=readonly"
export CC="\"$project_dir/scripts/macos-cc.sh\""
export MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=13.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=13.0 -Wl,-rpath,@executable_path/../Resources"
bash ./scripts/stage-voice-native.sh "darwin/$(go env GOARCH)"

echo "Aemeath 开发模式：r + 回车重新构建并重启；q + 回车退出；Ctrl+C 退出。"
exec go run github.com/egoist/mygo/cmd/mygo dev "$@"
