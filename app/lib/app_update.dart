// Keeping the app up to date: it looks at the "latest-app" release on GitHub
// (where the build publishes each new APK), and when that is newer than the
// installed version it asks before downloading it and handing it to Android's
// installer (Updater.kt). Android asks once to allow installs from this app.
import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';

/// Where GitHub Actions publishes the app (app/ci/build-apk.sh).
const appReleaseBase = 'https://github.com/thefrayedend/PrintHarbor/releases/download/latest-app';

const _native = MethodChannel('printharbor/app');

/// "1.2.10" > "1.2.9"; anything that is not a number counts as 0.
bool isNewerVersion(String latest, String current) {
  List<int> parts(String v) => v.split(RegExp(r'[.+-]')).map((s) => int.tryParse(s) ?? 0).toList();
  final a = parts(latest), b = parts(current);
  for (var i = 0; i < (a.length > b.length ? a.length : b.length); i++) {
    final x = i < a.length ? a[i] : 0, y = i < b.length ? b[i] : 0;
    if (x != y) return x > y;
  }
  return false;
}

/// A newer version waiting on GitHub.
class AvailableUpdate {
  final String version, current, apk;
  AvailableUpdate(this.version, this.current, this.apk);
}

class AppUpdate {
  /// The installed version ("1.0.4"), or '' when it can't be read.
  static Future<String> installedVersion() async {
    try {
      return '${await _native.invokeMethod('appVersion') ?? ''}';
    } catch (_) {
      return '';
    }
  }

  /// Looks on GitHub. Returns the update when there is a newer one, null when
  /// this is the latest; throws a message for the Settings page on failure.
  static Future<AvailableUpdate?> check() async {
    final c = HttpClient()..connectionTimeout = const Duration(seconds: 10);
    try {
      final current = await installedVersion();
      final req = await c.getUrl(Uri.parse('$appReleaseBase/printharbor-app.json'));
      final res = await req.close().timeout(const Duration(seconds: 20));
      if (res.statusCode != 200) {
        await res.drain<void>();
        throw 'GitHub answered ${res.statusCode}.';
      }
      final j = jsonDecode(await utf8.decodeStream(res)) as Map<String, dynamic>;
      final latest = '${j['version'] ?? ''}';
      if (current.isEmpty || !isNewerVersion(latest, current)) return null;
      return AvailableUpdate(latest, current, '${j['apk'] ?? 'printharbor.apk'}');
    } on String {
      rethrow;
    } catch (_) {
      throw 'Could not reach GitHub (is the phone online?).';
    } finally {
      c.close(force: true);
    }
  }

  /// Downloads the update and hands it to Android. Returns a line to show.
  static Future<String> install(AvailableUpdate u, {void Function(double? fraction)? progress}) async {
    final c = HttpClient()..connectionTimeout = const Duration(seconds: 15);
    try {
      final dir = Directory('${await _native.invokeMethod('cacheDir')}/update');
      if (await dir.exists()) await dir.delete(recursive: true); // old downloads
      await dir.create(recursive: true);
      final apk = File('${dir.path}/printharbor-${u.version}.apk');
      final req = await c.getUrl(Uri.parse('$appReleaseBase/${u.apk}'));
      final res = await req.close();
      if (res.statusCode != 200) {
        await res.drain<void>();
        return 'The download failed (GitHub answered ${res.statusCode}).';
      }
      final total = res.contentLength;
      var got = 0;
      final out = apk.openWrite();
      await for (final chunk in res) {
        out.add(chunk);
        got += chunk.length;
        progress?.call(total > 0 ? got / total : null);
      }
      await out.close();
      final r = '${await _native.invokeMethod('installApk', {'path': apk.path})}';
      if (r == 'permission') {
        return 'Allow "Install unknown apps" for PrintHarbor (Android just opened that switch), then come back and tap Install again.';
      }
      if (r.startsWith('error')) return 'The update could not be installed ($r).';
      return 'Android is installing version ${u.version}…';
    } catch (_) {
      return 'The update download failed (is the phone online?).';
    } finally {
      c.close(force: true);
    }
  }
}
