#!/bin/sh
set -eu

runtime="${CONTAINER_RUNTIME:-podman}"
image="docker.io/dpokidov/imagemagick:7.1.1-47@sha256:dac6411fae7541908d9881c0c27c205d07010d896184e27b52d4cfc2261e4803"
root="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
master="assets/brand/canonical/zestmem-master.png"
mode="${1:-write}"

[ -f "$root/$master" ] || { echo "missing $master" >&2; exit 2; }
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/assets/brand/icons" "$tmp/assets/brand/wordmark" \
  "$tmp/assets/brand/readme" "$tmp/assets/brand/social"

convert_image() {
  "$runtime" run --rm --entrypoint magick \
    -v "$root:/work:ro" -v "$tmp:/out" "$image" "$@"
}

# Separate the reviewed stacked master into stable mark and wordmark regions.
convert_image "/work/$master" -crop 692x430+160+131 +repage -trim +repage \
  -strip /out/mark.png
convert_image "/work/$master" -crop 692x145+160+565 +repage -trim +repage \
  -strip /out/type.png

# Canonical square icon with transparent padding.
convert_image /out/mark.png -resize 704x704 -background none -gravity center \
  -extent 768x768 -strip /out/icon.png
for size in 512 192 180 64 32 16; do
  convert_image /out/icon.png -filter Lanczos -resize "${size}x${size}" -strip \
    "/out/assets/brand/icons/zestmem-${size}.png"
done
convert_image /out/icon.png -strip /out/assets/brand/icons/zestmem-icon.png

# Recompose the existing artwork horizontally without redrawing it.
convert_image /out/mark.png -resize x240 -background none -gravity center \
  -extent 440x280 -strip /out/mark-horizontal.png
convert_image /out/type.png -resize 650x150 -background none -gravity center \
  -extent 700x280 -strip /out/type-horizontal.png
convert_image /out/mark-horizontal.png /out/type-horizontal.png +append -trim +repage \
  -bordercolor none -border 24 -strip \
  /out/assets/brand/wordmark/zestmem-horizontal.png
convert_image /out/assets/brand/wordmark/zestmem-horizontal.png -resize 960x \
  -background none -gravity center -extent 1200x320 -strip \
  /out/assets/brand/readme/zestmem-header.png
convert_image -size 1280x640 xc:'#07111f' \
  /out/assets/brand/wordmark/zestmem-horizontal.png -resize 1040x \
  -gravity center -composite -strip /out/assets/brand/social/zestmem-social.png

files="
assets/brand/icons/zestmem-icon.png
assets/brand/icons/zestmem-512.png
assets/brand/icons/zestmem-192.png
assets/brand/icons/zestmem-180.png
assets/brand/icons/zestmem-64.png
assets/brand/icons/zestmem-32.png
assets/brand/icons/zestmem-16.png
assets/brand/wordmark/zestmem-horizontal.png
assets/brand/readme/zestmem-header.png
assets/brand/social/zestmem-social.png"

for file in $files; do
  if [ "$mode" = "--check" ]; then
    cmp "$tmp/$file" "$root/$file" || {
      echo "brand derivative drift: $file" >&2
      exit 1
    }
  else
    mkdir -p "$root/$(dirname "$file")"
    cp "$tmp/$file" "$root/$file"
  fi
done
