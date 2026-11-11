#!/usr/bin/env bash
# Build the same versioned artifacts locally and in GitHub Actions.
set -euo pipefail
repo_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$repo_dir"
if [[ $# -ne 1 ]]; then
  echo "Usage: $0 windows-x64|macos-arm64|firmware" >&2
  exit 2
fi
case "${1:-}" in
  windows-x64|macos-arm64|firmware) target="$1" ;;
  *) echo "Usage: $0 windows-x64|macos-arm64|firmware" >&2; exit 2 ;;
esac
version="$(python3 scripts/version.py --check)"
mkdir -p dist
case "$target" in
  windows-x64)
    (cd agent && go mod download)
    bash agent/scripts/build-windows-voice.sh windows/amd64
    cp "agent/build/windows-amd64/aemeath-setup-${version}-amd64.exe" "dist/Aemeath-Agent-${version}-windows-x64-setup.exe"
    ;;
  macos-arm64)
    (cd agent && go mod download)
    bash agent/scripts/build-dmg.sh darwin/arm64
    bash agent/scripts/verify-macos-voice.sh "$repo_dir/agent/build/darwin-arm64/Aemeath.app"
    cp "agent/build/darwin-arm64/aemeath-${version}-arm64.dmg" "dist/Aemeath-Agent-${version}-macos-arm64.dmg"
    ;;
  firmware)
    bash firmware/scripts/build-release.sh
    ;;
esac
