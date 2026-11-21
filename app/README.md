# PrintHarbor companion app

Your workshop in your pocket: PrintHarbor's own web UI in a native phone app,
with network discovery and print notifications. The design follows FlashBridge's
app — a thin native shell around the web UI plus native glue — so the app never
drifts out of sync with the web, and most of the work is the few things a web
page can't do on a phone.

## What it does

- **Library, Slicer, Printers, Queue** tabs are PrintHarbor's own pages in a WebView.
  The app sends a `PrintHarborApp` user-agent, so the web UI adapts for the phone
  (`web/app.js` adds an `in-app mobile-app` class on it).
- **Finds PrintHarbor on the Wi-Fi**: the last address it used, then the
  `harbor.local` mDNS name, then a scan of the phone's subnet for a host that
  answers `/api/info` like PrintHarbor (port 7621).
- **Print notifications**: while the app is open and notifications are on, it
  polls `/api/alerts` and raises a local notification when a print finishes, is
  paused (incl. the AI failure detection), fails, or loses contact.
- **Settings**: the address, and the notifications switch. Everything else is in
  PrintHarbor's own Settings (the Printers tab).

## Architecture

```
lib/
  main.dart           the shell: bottom nav (Library/Slicer/Printers/Queue/Settings), back handling
  board_view.dart     the WebView of PrintHarbor's pages (UA = PrintHarborApp)
  app_model.dart      shared state: which server, connected?, the busy banner
  discovery.dart      mDNS (harbor.local) + subnet scan to find the server
  board.dart          the HTTP client: /api/info, /api/printers, /api/alerts
  notifications.dart  the alert watcher → local phone notifications
  settings_view.dart  address + notifications
```

The server side needs nothing new for this: the app uses `/api/info` (probe +
version), `/api/printers` (status), and `/api/alerts?since=N` (notifications),
which already exist. The only server change for the app is the UA detection in
`web/app.js`.

## Building it

The APK is built by GitHub Actions: **Actions -> "Build Android app" -> Run
workflow**. When it finishes, the APK is on the repo's **Releases** page under
`latest-app`: open that page on the phone, tap `printharbor.apk` and install it.

The steps are in `ci/build-apk.sh`. It completes the Android project with
`flutter create` (the manifest, launcher icons and signing key in `android/` are
ours and are kept), adds signing and the desugaring setting the notifications
plugin needs, runs `flutter analyze` and `flutter test`, then builds.

To build on your own machine (needs Flutter and the Android SDK):

```sh
RUN_NUMBER=1 bash app/ci/build-apk.sh     # APK lands in app-out/printharbor.apk
```

Signing: builds are signed with the sideload key in
`android/app/printharbor-sideload.jks`, so each build installs over the last.
That key is public, like the rest of the source. To sign with a private key of
your own, set the repo secrets `ANDROID_KEYSTORE_BASE64` and
`ANDROID_KEYSTORE_PASSWORD` (key alias `printharbor`).

The phone must be able to reach PrintHarbor: same Wi-Fi, and the PC's firewall
must allow incoming connections on PrintHarbor's port (7621).

## Notifications: the two layers

- **In-app watcher (here):** works when the app is open on the same network, no
  cloud, no setup. Good for "I'm at home with the app up."
- **Away-from-home push:** install PrintHarbor's **notify** plugin on the server
  and point it at an [ntfy](https://ntfy.sh) topic, then subscribe to that topic
  in the ntfy phone app. That path pushes anywhere, through the same event hooks.

A true always-on foreground service (watching even when the app is closed,
without ntfy) is the natural next step — it slots into `notifications.dart` as an
Android foreground service / iOS background task.

## Status

The Dart code passes `flutter analyze` and its tests (`test/board_test.dart`),
and the page switching was checked against a running PrintHarbor in a
phone-sized browser. The Android build itself runs in GitHub Actions.
