# Paxve Android app

A Capacitor 8 Android shell around https://paxve.com: the whole system (accounting, billing,
collectors, purchasing, HR, reports, portal…) runs inside it. Native code covers what an Android
WebView cannot do on its own:

- **classic Bluetooth (SPP) receipt printers** — most cheap 58/80 mm printers; browsers only reach BLE (`PaxvePrinter`)
- **Print buttons** (invoices, receipts, bulk bills, payslips, supplier bills, portal) → Android print dialog: printer or *Save as PDF* (`PaxveApp.print`)
- **file downloads** (CSV templates, any `<a download>` blob/data link) → `Downloads/Paxve` on Android 10+, share sheet on 7–9 (`PaxveApp.saveFile`)
- **external links** — `wa.me`, `whatsapp:`, `tel:` (opens the dialer), `mailto:`, `sms:`, `intent:`, other websites → the matching app
- **update notice** — the app compares its `versionCode` with `/download/version.json` and offers the new APK
- **Android back button** — closes an open dialog/menu, else goes back, on the first screen asks "press back again to exit"
- **offline page** (`www/offline.html`, EN/AR/FR) when paxve.com is unreachable and nothing is cached; it retries automatically.
  Once the site has loaded once, the service worker serves the app (collector screen + outbox) offline.
- brand colour status bar / splash, sign-in cookie flushed on pause (stays signed in), `<input type=file>` uses the Android file picker (CSV import, bank statements)

The app loads the live site (`server.url` in `capacitor.config.json`), so every web release reaches
the app instantly. A new APK is only needed when native code, permissions or the icon change.
The web side detects the app with `isNativeApp()` and uses `web/src/native.ts`
(`printPage()`, `saveFile()`, `openExternal()`, `installNativeHooks()`); in browsers every helper
falls back to normal web behaviour. APKs built before the `PaxveApp` plugin existed keep working
(they just lack these extras) and are told to update.

## Layout

```
capacitor.config.json        app id com.paxve.app, loads https://paxve.com/#/login, errorPath offline.html
android/                     generated Capacitor project (committed)
  app/src/main/java/com/paxve/app/PaxvePrinterPlugin.java   classic Bluetooth printing
  app/src/main/java/com/paxve/app/PaxveAppPlugin.java       print, save/share files, external links, app info
  app/src/main/java/com/paxve/app/MainActivity.java         registers plugins, back button, status bar colour
scripts/icons.sh             launcher icons + splash images from the Paxve logo
Dockerfile.build             JDK 21 + Android SDK 36 + Node build image
build-apk.sh                 builds, signs and publishes the APK + downloads/version.json
www/                         offline fallback pages
```

## Plugin API — `PaxveApp`

| Method | Arguments | Result |
|---|---|---|
| `getAppInfo()` | – | `{ versionCode, versionName, android, sdk }` |
| `print({ title })` | – | prints the current page (CSS `@media print` applies) |
| `saveFile({ name, mime, data, share })` | base64 `data`; `share: true` opens the share sheet | `{ saved, uri, shared }` |
| `openExternal({ url })` | any URL | opens it in the right app |
| `exitApp()` | – | closes the app |

## Plugin API — `PaxvePrinter`

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
The script also writes `downloads/version.json` (`versionCode`, `versionName`, `url`, `sha256`, `size`),
served with `Cache-Control: no-store`; apps with an older `versionCode` show an update notice.

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
8. Open an invoice → **Print** → the Android print dialog opens (choose a printer or *Save as PDF*).
9. Import → *Download template* → a toast shows `Downloads/Paxve/…csv`; *Choose file* opens the Android file picker.
10. Customer → WhatsApp button / phone number → WhatsApp / the dialer opens, the app stays signed in.
11. Back button: closes a dialog, then goes back, then *Press back again to exit*.
12. First launch with no internet → the offline page (EN/AR/FR) with *Try again*; it reconnects by itself.
13. Install an older APK, open it → *New version of the app* → *Download update* opens the browser download.

The web-side behaviour (update notice, native print/save/external-link routing, back handling, hidden
download links) is checked with a mocked Capacitor bridge in Playwright; the native side needs a real phone
(the server has no KVM for an emulator).
