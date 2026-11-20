#!/usr/bin/env bash
# Generates the Android launcher icons from the Paxve logo (needs rsvg-convert; present in Dockerfile.build).
set -euo pipefail
cd "$(dirname "$0")/.."
RES=android/app/src/main/res
TMP=$(mktemp -d)

# square icon (same as web favicon)
cat > "$TMP/icon.svg" <<'EOF'
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#0B1F3A"/><g transform="translate(5.5 5.5) scale(0.828)"><path d="M12 4h20a8 8 0 0 1 8 8v8h-8a12 12 0 0 0-12 12v8H12a8 8 0 0 1-8-8V12a8 8 0 0 1 8-8z" fill="#fff"/><path d="M32 24h20a8 8 0 0 1 8 8v20a8 8 0 0 1-8 8H32a8 8 0 0 1-8-8V32a8 8 0 0 1 8-8z" fill="#5EEAD4"/></g></svg>
EOF
# round icon
cat > "$TMP/round.svg" <<'EOF'
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><circle cx="32" cy="32" r="32" fill="#0B1F3A"/><g transform="translate(9.6 9.6) scale(0.7)"><path d="M12 4h20a8 8 0 0 1 8 8v8h-8a12 12 0 0 0-12 12v8H12a8 8 0 0 1-8-8V12a8 8 0 0 1 8-8z" fill="#fff"/><path d="M32 24h20a8 8 0 0 1 8 8v20a8 8 0 0 1-8 8H32a8 8 0 0 1-8-8V32a8 8 0 0 1 8-8z" fill="#5EEAD4"/></g></svg>
EOF
# adaptive foreground (108dp canvas, glyph inside the 66dp safe zone)
cat > "$TMP/fg.svg" <<'EOF'
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 108 108"><g transform="translate(54 54) scale(1.05) translate(-32 -32)"><path d="M12 4h20a8 8 0 0 1 8 8v8h-8a12 12 0 0 0-12 12v8H12a8 8 0 0 1-8-8V12a8 8 0 0 1 8-8z" fill="#fff"/><path d="M32 24h20a8 8 0 0 1 8 8v20a8 8 0 0 1-8 8H32a8 8 0 0 1-8-8V32a8 8 0 0 1 8-8z" fill="#5EEAD4"/></g></svg>
EOF

for d in mdpi:48:108 hdpi:72:162 xhdpi:96:216 xxhdpi:144:324 xxxhdpi:192:432; do
  IFS=: read -r name size fg <<<"$d"
  dir="$RES/mipmap-$name"
  mkdir -p "$dir"
  rsvg-convert -w "$size" -h "$size" "$TMP/icon.svg" -o "$dir/ic_launcher.png"
  rsvg-convert -w "$size" -h "$size" "$TMP/round.svg" -o "$dir/ic_launcher_round.png"
  rsvg-convert -w "$fg" -h "$fg" "$TMP/fg.svg" -o "$dir/ic_launcher_foreground.png"
done

# launch screen (Android 7–11; Android 12+ uses the system splash with the icon + brand colour, see styles.xml)
splash() { # w h
  cat <<EOF
<svg xmlns="http://www.w3.org/2000/svg" width="$1" height="$2" viewBox="0 0 $1 $2"><rect width="$1" height="$2" fill="#0B1F3A"/><g transform="translate($(( $1 / 2 )) $(( $2 / 2 ))) scale(4) translate(-32 -32)"><path d="M12 4h20a8 8 0 0 1 8 8v8h-8a12 12 0 0 0-12 12v8H12a8 8 0 0 1-8-8V12a8 8 0 0 1 8-8z" fill="#fff"/><path d="M32 24h20a8 8 0 0 1 8 8v20a8 8 0 0 1-8 8H32a8 8 0 0 1-8-8V32a8 8 0 0 1 8-8z" fill="#5EEAD4"/></g></svg>
EOF
}
splash 720 1280 > "$TMP/port.svg"
splash 1280 720 > "$TMP/land.svg"
rsvg-convert "$TMP/port.svg" -o "$RES/drawable/splash.png"
for d in "$RES"/drawable-port-*; do rsvg-convert "$TMP/port.svg" -o "$d/splash.png"; done
for d in "$RES"/drawable-land-*; do rsvg-convert "$TMP/land.svg" -o "$d/splash.png"; done

mkdir -p "$RES/values"
cat > "$RES/values/ic_launcher_background.xml" <<'EOF'
<?xml version="1.0" encoding="utf-8"?>
<resources>
    <color name="ic_launcher_background">#0B1F3A</color>
</resources>
EOF
rm -rf "$TMP"
echo "icons generated"
