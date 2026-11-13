#!/usr/bin/env bash
# Read-only validation of the final bundle, including every architecture slice.
set -euo pipefail
app="${1:?Usage: verify-macos-voice.sh /path/to/Aemeath.app}"
executable="$(/usr/libexec/PlistBuddy -c 'Print CFBundleExecutable' "$app/Contents/Info.plist")"
if ! microphone_description="$(/usr/libexec/PlistBuddy -c 'Print NSMicrophoneUsageDescription' "$app/Contents/Info.plist" 2>/dev/null)" || [[ -z "${microphone_description//[[:space:]]/}" ]]; then
  echo "Missing microphone usage description" >&2
  exit 1
fi
if ! audio_input_entitlement="$(codesign -d --entitlements :- "$app" 2>/dev/null | plutil -extract 'com\.apple\.security\.device\.audio-input' raw -o - - 2>/dev/null)" || [[ "$audio_input_entitlement" != "true" ]]; then
  echo "Missing signed audio-input entitlement" >&2
  exit 1
fi
resources="$app/Contents/Resources"
for binary in "$app/Contents/MacOS/$executable" "$resources/libsherpa-onnx-c-api.dylib" "$resources/libonnxruntime.dylib"; do
  [[ -f "$binary" ]] || { echo "Missing bundled native code: $binary" >&2; exit 1; }
  echo "$binary"
  lipo -archs "$binary"
  # Absolute library paths may only name Apple system libraries/frameworks.
  otool -arch all -L "$binary" | awk '
    /compatibility version/ {
      path=$1
      if (path !~ /^@rpath\// && path !~ /^@loader_path\// && path !~ /^@executable_path\// && path !~ /^\/usr\/lib\// && path !~ /^\/System\/Library\//) {
        print "Nonportable dependency: " path > "/dev/stderr"; failed=1
      }
    }
    END { exit failed }
  '
  otool -arch all -l "$binary" | awk '
    $1 == "cmd" { rpath=($2 == "LC_RPATH") }
    rpath && $1 == "path" && $2 !~ /^@/ {
      print "Nonportable rpath: " $2 > "/dev/stderr"; failed=1
    }
    END { exit failed }
  '
done
otool -arch all -l "$app/Contents/MacOS/$executable" | awk '
  $1 == "path" && $2 == "@executable_path/../Resources" { found=1 }
  END { if (!found) { print "Missing bundled-library rpath" > "/dev/stderr"; exit 1 } }
'
codesign --verify --deep --strict "$app"
echo "Verified microphone permission description and audio-input entitlement, bundled sherpa/onnx linkage and code signatures."
