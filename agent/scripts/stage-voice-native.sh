#!/usr/bin/env bash
# Stage only the official native runtime; models/reference audio remain in user cache.
set -euo pipefail
project_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$project_dir"
platform="${1:-}"
case "$platform" in
  darwin/universal) targets=(darwin-arm64 darwin-amd64); module=macos ;;
  darwin/arm64) targets=(darwin-arm64); module=macos ;;
  darwin/amd64) targets=(darwin-amd64); module=macos ;;
  windows/amd64) targets=(windows-amd64); module=windows ;;
  windows/arm64) targets=(windows-arm64); module=windows ;;
  *) echo "Usage: $0 darwin/{universal,arm64,amd64}|windows/{amd64,arm64}" >&2; exit 2 ;;
esac
module_path="github.com/k2-fsa/sherpa-onnx-go-$module/v2"
module_dir="$(go list -mod=readonly -m -f '{{.Dir}}' "$module_path")"
version="$(go list -mod=readonly -m -f '{{.Version}}' "$module_path")"
[[ "$version" == v2.0.1 && -d "$module_dir/lib" ]] || {
  echo "Expected downloaded $module_path v2.0.1; run go mod download first." >&2; exit 1;
}
for target in "${targets[@]}"; do
  case "$target" in
    darwin-arm64) triple=aarch64-apple-darwin ;;
    darwin-amd64) triple=x86_64-apple-darwin ;;
    windows-amd64) triple=x86_64-pc-windows-gnu ;;
    windows-arm64) triple=aarch64-pc-windows-gnu ;;
  esac
  if [[ "$module" == macos ]]; then
    libraries=(libsherpa-onnx-c-api.dylib libonnxruntime.dylib)
  else
    libraries=(sherpa-onnx-c-api.dll onnxruntime.dll)
  fi
  mkdir -p "resources/native/$target" "resources/$target"
  # These directories contain generated runtime files only.
  printf '*\n!.gitignore\n' > "resources/$target/.gitignore"
  for library in "${libraries[@]}"; do
    [[ -f "$module_dir/lib/$triple/$library" ]] || { echo "Missing native library: $library" >&2; exit 1; }
    # Module-cache files are read-only; install replaces a prior staged copy
    # instead of making the next build fail while overwriting its permissions.
    install -m 755 "$module_dir/lib/$triple/$library" "resources/native/$target/$library"
    ln -sfn "../native/$target/$library" "resources/$target/$library"
  done
  install -m 644 "$module_dir/LICENSE" "resources/native/$target/sherpa-onnx-LICENSE"
  ln -sfn "../native/$target/sherpa-onnx-LICENSE" "resources/$target/sherpa-onnx-LICENSE"
done
printf 'Staged sherpa-onnx %s for %s (no models, reference audio or credentials).\n' "$version" "$platform"
