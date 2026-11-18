#!/usr/bin/env bash
# Builds the signed release APK inside Docker and publishes it as $DOWNLOADS_DIR/paxve.apk
# (served at https://paxve.com/download/paxve.apk by Caddy).
#
# The signing keystore is created once and kept ONLY on the server (never in git):
#   /srv/paxve-secrets/android.keystore   (chmod 600)
#   /srv/paxve-secrets/android.env        (KEYSTORE_PASSWORD, KEY_ALIAS; chmod 600)
# Keep a safe copy of both: Android refuses updates signed with a different key.
set -euo pipefail
cd "$(dirname "$0")"

SECRETS=${SECRETS_DIR:-/srv/paxve-secrets}
OUT=${DOWNLOADS_DIR:-/srv/paxve/downloads}
VERSION_CODE=${VERSION_CODE:-$(date +%y%m%d%H)}   # must grow for every release
VERSION_NAME=${VERSION_NAME:-1.0.$(date +%Y%m%d)}

mkdir -p "$SECRETS" "$OUT"
chmod 700 "$SECRETS"

docker build -q -t paxve-android-build -f Dockerfile.build . >/dev/null

if [ ! -f "$SECRETS/android.keystore" ]; then
  PASS=$(openssl rand -hex 20)
  umask 077
  printf 'KEYSTORE_PASSWORD=%s\nKEY_ALIAS=paxve\n' "$PASS" > "$SECRETS/android.env"
  docker run --rm -v "$SECRETS":/secrets paxve-android-build \
    keytool -genkeypair -keystore /secrets/android.keystore -alias paxve -keyalg RSA -keysize 4096 -validity 10000 \
      -storepass "$PASS" -keypass "$PASS" -dname "CN=Paxve, O=Paxve, C=LB" >/dev/null 2>&1
  chmod 600 "$SECRETS/android.keystore" "$SECRETS/android.env"
  echo "created signing key in $SECRETS"
fi

docker run --rm \
  -e VERSION_CODE="$VERSION_CODE" -e VERSION_NAME="$VERSION_NAME" \
  -v "$PWD":/work -v "$SECRETS":/secrets:ro -v "$OUT":/out -v paxve-gradle-cache:/root/.gradle \
  paxve-android-build bash -c '
    set -euo pipefail
    set -a; . /secrets/android.env; set +a
    npm ci --no-audit --no-fund --loglevel=error
    bash scripts/icons.sh
    npx cap sync android
    cd android
    ./gradlew --no-daemon -q assembleRelease -PversionCode="$VERSION_CODE" -PversionName="$VERSION_NAME"
    BT=/opt/android-sdk/build-tools/36.0.0
    $BT/zipalign -f -p 4 app/build/outputs/apk/release/app-release-unsigned.apk /tmp/aligned.apk
    $BT/apksigner sign --ks /secrets/android.keystore --ks-key-alias "$KEY_ALIAS" \
      --ks-pass env:KEYSTORE_PASSWORD --key-pass env:KEYSTORE_PASSWORD --out /tmp/paxve.apk /tmp/aligned.apk
    $BT/apksigner verify /tmp/paxve.apk
    cp /tmp/paxve.apk /out/paxve.apk
    chmod 644 /out/paxve.apk
  '
ls -la "$OUT/paxve.apk"
