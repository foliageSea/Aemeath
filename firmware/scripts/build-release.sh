#!/usr/bin/env bash
# Run after activating ESP-IDF v6.0.2 (source "$IDF_PATH/export.sh").
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
firmware_dir="$(cd -- "$script_dir/.." && pwd)"
repo_dir="$(cd -- "$firmware_dir/.." && pwd)"
helper="$script_dir/package-release.py"

if (( $# != 0 )); then
    echo "Usage: $0 (activate ESP-IDF v6.0.2 first)" >&2
    exit 2
fi
if ! command -v idf.py >/dev/null 2>&1 || ! command -v python >/dev/null 2>&1; then
    echo "Activate ESP-IDF v6.0.2 before running this script." >&2
    exit 1
fi
if [[ "$(idf.py --version)" != "ESP-IDF v6.0.2" ]]; then
    echo "This release requires ESP-IDF v6.0.2." >&2
    exit 1
fi
version="$(python "$helper" version "$repo_dir/VERSION")"
build_dir="$firmware_dir/build-release"
dist_dir="$repo_dir/dist"
stem="Aemeath-Firmware-${version}-esp32s3"
mkdir -p -- "$build_dir" "$dist_dir"

# A fresh config on every invocation prevents both local sdkconfig and a stale
# release config from overriding the checked-in defaults. Only our mktemp files
# are removed; the local build directory and user configuration are untouched.
release_sdkconfig="$(mktemp "$build_dir/sdkconfig.release.XXXXXX")"
cleanup() {
    rm -f -- "$release_sdkconfig" "$release_sdkconfig.old"
}
trap cleanup EXIT
unset SDKCONFIG SDKCONFIG_DEFAULTS IDF_TARGET
idf=(idf.py -C "$firmware_dir" -B "$build_dir"
    -D "SDKCONFIG=$release_sdkconfig"
    -D "SDKCONFIG_DEFAULTS=$firmware_dir/sdkconfig.defaults"
    -D "IDF_TARGET=esp32s3")
"${idf[@]}" reconfigure build
"${idf[@]}" merge-bin -o "$dist_dir/$stem.bin"
python "$helper" package --build-dir "$build_dir" --version-file "$repo_dir/VERSION" \
    --output "$dist_dir/$stem.zip"
printf "Release artifacts:\n  %s\n  %s\n" "$dist_dir/$stem.bin" "$dist_dir/$stem.zip"
