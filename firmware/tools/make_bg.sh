#!/bin/sh
# Builds the 480x480 dashboard background from a source image:
# square crop (y offset centres the subject), downscale, darken for text contrast,
# then raw little-endian RGB565 for LVGL plus a PNG preview.
# Usage: tools/make_bg.sh [source.png] [crop_y]
set -e
cd "$(dirname "$0")/.."
SRC=${1:-assets/bg_source.png}
CROP_Y=${2:-100}
FILTER="crop=min(iw\,ih):min(iw\,ih):(iw-min(iw\,ih))/2:${CROP_Y},scale=480:480:flags=lanczos,colorchannelmixer=rr=0.42:gg=0.42:bb=0.46,vignette=PI/5"
ffmpeg -loglevel error -y -i "$SRC" -vf "$FILTER" -pix_fmt rgb565le -f rawvideo main/assets/bg_480.rgb565
ffmpeg -loglevel error -y -i "$SRC" -vf "$FILTER" assets/bg_preview.png
ls -l main/assets/bg_480.rgb565
