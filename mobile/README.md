# Paxve Android app

A thin Capacitor 8 Android shell around https://paxve.com. It exists for one reason: most cheap
58/80 mm receipt printers used by collectors are **classic Bluetooth (SPP)**, which browsers cannot
reach (Web Bluetooth is BLE-only). The app adds a small native plugin, `PaxvePrinter`, that sends
the ESC/POS bytes the web app already builds.

The app loads the live site (`server.url` in `capacitor.config.json`), so every web release reaches
the app instantly. A new APK is only needed when native code, permissions or the icon change.

## Layout

```
capacitor.config.json        app id com.paxve.app, loads https://paxve.com/#/login
android/                     generated Capacitor project (committed)
  app/src/main/java/com/paxve/app/PaxvePrinterPlugin.java   classic Bluetooth printing
  app/src/main/java/com/paxve/app/MainActivity.java         registers the plugin
scripts/icons.sh             launcher icons from the Paxve logo
Dockerfile.build             JDK 21 + Android SDK 36 + Node build image
build-apk.sh                 builds, signs and publishes the APK
www/                         offline fallback page only
```

## Plugin API (JavaScript, `window.Capacitor.Plugins.PaxvePrinter`)

| Method | Arguments | Result |
|---|---|---|
| `listPaired()` | – | `{ devices: [{ name, address }] }` (asks for Bluetooth permission on Android 12+) |
| `connect({ address })` | MAC address | `{ name, address }` – SPP UUID `00001101-0000-1000-8000-00805F9B34FB`, falls back to insecure socket / channel 1 |
| `write({ data })` | base64 ESC/POS bytes | `{ bytes }` (sent in 1 KB chunks) |
| `status()` | – | `{ connected, name, address }` |
| `disconnect()` | – | – |

The web side lives in `web/src/bt.ts` (`isNativeApp()`, `listPairedPrinters()`,
`connectNativePrinter()`, `printLines()`); browsers keep using Web Bluetooth.

## Build (on the server)

```sh
cd /srv/paxve/mobile
./build-apk.sh            # ~3 min first time (Gradle cache is kept in a Docker volume)
```

The script builds `Dockerfile.build`, generates icons, runs `cap sync`, `gradlew assembleRelease`,
then `zipalign` + `apksigner`, and copies the result to `/srv/paxve/downloads/paxve.apk`, which Caddy
serves at **https://paxve.com/download/paxve.apk**
(`Content-Type: application/vnd.android.package-archive`).

`versionCode` is the build date/hour (`yyMMddHH`), so each build installs over the previous one.

### Signing key — keep it safe

Created once by `build-apk.sh`, stored **only on the server** and never in git:

- `/srv/paxve-secrets/android.keystore` (RSA 4096, alias `paxve`, chmod 600)
- `/srv/paxve-secrets/android.env` (`KEYSTORE_PASSWORD`, `KEY_ALIAS`, chmod 600)

Back both up somewhere safe. If the key is lost, phones cannot install updates over the existing
app (they would have to uninstall first).

## Manual test on a phone

1. On Android 7+ open https://paxve.com/download/paxve.apk, allow "install unknown apps" for the browser, install.
2. Turn the printer on and pair it in **Settings → Bluetooth** (PIN usually `0000` or `1234`).
3. Open Paxve, sign in as a collector (e.g. a user with the *collector* role).
4. Tap the Bluetooth icon at the top → allow "Nearby devices" → choose the printer from the list.
5. Collect a payment → **Print (Bluetooth)**. The receipt (Arabic included) prints as an image.
6. Switch the printer off and on, print again: the app reconnects to the saved printer automatically.
7. Airplane mode: the collector screen still opens (service worker + cached data); payments queue and
   sync when back online.
