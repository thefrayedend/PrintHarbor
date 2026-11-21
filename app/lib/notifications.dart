// Print notifications on the phone. While the app is running and "Print
// notifications" is on, this polls PrintHarbor's alert feed (/api/alerts, which
// already carries finished / paused / failed / lost-contact / bed-cleared) and
// raises a local notification for each new one.
//
// This covers the common case (phone on the same network, app recently open).
// For always-on, away-from-home push, pair it with the server's `notify` plugin
// pointed at ntfy — see app/README.md. A true always-on foreground service is a
// documented next step.
import 'dart:async';

import 'package:flutter_local_notifications/flutter_local_notifications.dart';

import 'app_model.dart';
import 'board.dart';

class Notifier {
  static final _plugin = FlutterLocalNotificationsPlugin();
  static Timer? _timer;
  static int _since = 0;
  static bool _primed = false;

  static Future<void> init() async {
    const android = AndroidInitializationSettings('@mipmap/ic_launcher');
    const ios = DarwinInitializationSettings();
    await _plugin.initialize(const InitializationSettings(android: android, iOS: ios));
  }

  static Future<void> requestPermission() async {
    await _plugin
        .resolvePlatformSpecificImplementation<AndroidFlutterLocalNotificationsPlugin>()
        ?.requestNotificationsPermission();
    await _plugin
        .resolvePlatformSpecificImplementation<IOSFlutterLocalNotificationsPlugin>()
        ?.requestPermissions(alert: true, badge: true, sound: true);
  }

  /// Start/stop the watcher to match the app's "watch" setting and connection.
  static void sync() {
    final want = app.watch && app.connected;
    if (want && _timer == null) {
      _primed = false; // the first poll just learns the current point, no backlog spam
      _timer = Timer.periodic(const Duration(seconds: 6), (_) => _poll());
      _poll();
    } else if (!want && _timer != null) {
      _timer!.cancel();
      _timer = null;
    }
  }

  static Future<void> _poll() async {
    if (!app.connected) return;
    List<Map<String, dynamic>> alerts;
    try {
      alerts = await app.board.alerts(_since);
    } on BoardException {
      return; // the server blipped; try again next tick
    }
    if (alerts.isEmpty) return;
    for (final a in alerts) {
      final id = (a['id'] as num?)?.toInt() ?? 0;
      if (id > _since) _since = id;
      if (!_primed) continue; // don't replay what was already there when we started
      await _show(a);
    }
    _primed = true;
  }

  static Future<void> _show(Map<String, dynamic> a) async {
    final level = '${a['level'] ?? 'info'}';
    final printer = '${a['printer_name'] ?? ''}';
    final text = '${a['text'] ?? ''}';
    final importance = level == 'error'
        ? Importance.high
        : level == 'warning'
            ? Importance.defaultImportance
            : Importance.low;
    final details = NotificationDetails(
      android: AndroidNotificationDetails(
        'prints', 'Print notifications',
        channelDescription: 'Finished, failed and paused prints',
        importance: importance,
        priority: level == 'error' ? Priority.high : Priority.defaultPriority,
      ),
      iOS: const DarwinNotificationDetails(),
    );
    await _plugin.show(
      (a['id'] as num?)?.toInt() ?? DateTime.now().millisecondsSinceEpoch ~/ 1000,
      printer.isEmpty ? 'PrintHarbor' : printer,
      text,
      details,
    );
  }
}
