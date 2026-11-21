// The app's shared state: which PrintHarbor server, whether it answers, and the
// "something is happening" banner. Mirrors how FlashBridge's app_model worked,
// adapted to PrintHarbor (harbor.local:7621, /api/info + /api/printers).
import 'package:flutter/foundation.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'board.dart';
import 'discovery.dart';

class Busy {
  final String text;
  final double? progress; // 0..1, null = indeterminate
  Busy(this.text, [this.progress]);
}

class AppModel extends ChangeNotifier {
  /// What the user typed (or the default): a name like harbor.local or an address.
  String host = 'harbor.local';

  /// The resolved base URL the app uses, e.g. http://192.168.1.50:7621.
  String boardUrl = '';

  /// Watch the printers and raise phone notifications.
  bool watch = false;

  Map<String, dynamic>? info; // from /api/info; non-null once connected
  String? error;
  bool connecting = false;
  final busy = ValueNotifier<Busy?>(null);

  Board get board => Board(boardUrl);
  bool get connected => info != null;
  String get version => '${info?['version'] ?? ''}';

  static const _defaultPort = 7621;

  Future<void> load() async {
    final p = await SharedPreferences.getInstance();
    host = (p.getString('host') ?? '').trim().isEmpty ? 'harbor.local' : p.getString('host')!.trim();
    boardUrl = p.getString('board') ?? '';
    watch = p.getBool('watch') ?? false;
    notifyListeners();
  }

  Future<void> _save() async {
    final p = await SharedPreferences.getInstance();
    await p.setString('host', host);
    await p.setString('board', boardUrl);
    await p.setBool('watch', watch);
  }

  /// Finds and checks PrintHarbor: the last known address first, then the name
  /// (mDNS / DNS), then a scan of the Wi-Fi. True when a server answered.
  Future<bool> connect({bool search = true}) async {
    connecting = true;
    error = null;
    notifyListeners();
    try {
      final tries = <String>[];
      if (boardUrl.isNotEmpty) tries.add(boardUrl);
      final h = host.trim();
      if (h.isNotEmpty) {
        if (h.startsWith('http://') || h.startsWith('https://')) {
          tries.add(_withPort(h));
        } else if (RegExp(r'^\d+\.\d+\.\d+\.\d+(:\d+)?$').hasMatch(h)) {
          tries.add(_withPort('http://$h'));
        } else {
          // A name, with or without ":port" on the end (harbor.local:7621).
          final m = RegExp(r'^(.*?)(?::(\d+))?$').firstMatch(h)!;
          final name = m.group(1)!, port = m.group(2) ?? '$_defaultPort';
          final ip = await resolveLocal(name);
          if (ip != null) tries.add('http://$ip:$port');
          tries.add('http://$name:$port'); // a router that knows the name
        }
      }
      for (final url in tries.toSet()) {
        if (await _try(url)) return true;
      }
      var scanned = '';
      if (search) {
        final self = await wifiIp();
        final found = await findBoards(_defaultPort);
        if (found.length == 1 && await _try(found.first)) return true;
        if (found.length > 1) {
          error = 'Found ${found.length} PrintHarbor servers — type the one you want in Settings.';
          return false;
        }
        scanned = self == null
            ? ' The phone does not seem to be on Wi-Fi, so the network could not be searched.'
            : ' Nothing on this Wi-Fi (${self.substring(0, self.lastIndexOf('.'))}.x) answered as PrintHarbor on port $_defaultPort either.';
      }
      // Say plainly what failed and what to do, instead of the last raw error.
      final named = h.isNotEmpty && !h.startsWith('http') && !RegExp(r'^\d+\.\d+\.\d+\.\d+(:\d+)?$').hasMatch(h);
      error = (named ? 'The name "$h" was not found from this phone.' : 'PrintHarbor did not answer at "$h".') +
          scanned +
          ' Check that PrintHarbor is running, that the phone is on the same Wi-Fi as the PC (not a guest network), and that '
              'Windows Firewall on the PC allows PrintHarbor. Typing the PC\'s IP address here also works.';
      return false;
    } finally {
      connecting = false;
      notifyListeners();
    }
  }

  String _withPort(String url) {
    final u = Uri.parse(url);
    return u.hasPort ? url : '${u.scheme}://${u.host}:$_defaultPort';
  }

  Future<bool> _try(String url) async {
    try {
      final i = await Board(url).info();
      if (i['version'] == null) return false; // not PrintHarbor
      info = i;
      if (url != boardUrl) {
        boardUrl = url;
        await _save();
      }
      return true;
    } on BoardException catch (e) {
      error = e.message;
      return false;
    }
  }

  Future<void> setHost(String h) async {
    host = h.trim();
    boardUrl = '';
    await _save();
    await connect();
  }

  Future<void> setWatch(bool on) async {
    watch = on;
    await _save();
    notifyListeners();
  }
}

final app = AppModel();
