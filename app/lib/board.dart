// Talking to a PrintHarbor server over its HTTP API. Everything the native side
// of the app needs — a reachability/version probe, the printer list, and the
// alert feed the notifier watches — comes from here. The UI itself is the
// server's own web pages (see board_view.dart), so this stays small.
import 'dart:convert';

import 'package:http/http.dart' as http;

class BoardException implements Exception {
  final String message;
  BoardException(this.message);
  @override
  String toString() => message;
}

/// One PrintHarbor server, addressed by base URL (e.g. http://harbor.local:7621).
class Board {
  final String base;
  Board(this.base);

  Uri _u(String path) => Uri.parse('$base$path');

  Future<Map<String, dynamic>> _getJson(String path, {Duration timeout = const Duration(seconds: 6)}) async {
    http.Response r;
    try {
      r = await http.get(_u(path)).timeout(timeout);
    } catch (e) {
      throw BoardException('Could not reach PrintHarbor ($e).');
    }
    if (r.statusCode != 200) throw BoardException('PrintHarbor returned ${r.statusCode}.');
    final v = jsonDecode(r.body);
    if (v is! Map<String, dynamic>) throw BoardException('Unexpected reply from PrintHarbor.');
    return v;
  }

  /// The reachability/version probe. A real PrintHarbor answers /api/info with a
  /// version — discovery uses this to confirm a host is PrintHarbor.
  Future<Map<String, dynamic>> info() => _getJson('/api/info');

  /// Live printers with their status.
  Future<List<Map<String, dynamic>>> printers() async {
    final r = await http.get(_u('/api/printers')).timeout(const Duration(seconds: 6));
    if (r.statusCode != 200) throw BoardException('PrintHarbor returned ${r.statusCode}.');
    final v = jsonDecode(r.body);
    final list = v is Map ? (v['printers'] ?? v['items']) : v;
    if (list is! List) return const [];
    return [for (final p in list) Map<String, dynamic>.from(p as Map)];
  }

  /// New alerts since the given id (0 = all recent). PrintHarbor's alert feed
  /// already carries exactly what we notify on: finished, paused (incl. the AI
  /// failure detection), failed, lost contact, bed cleared.
  Future<List<Map<String, dynamic>>> alerts(int since) async {
    final r = await http.get(_u('/api/alerts?since=$since')).timeout(const Duration(seconds: 6));
    if (r.statusCode != 200) throw BoardException('PrintHarbor returned ${r.statusCode}.');
    final v = jsonDecode(r.body);
    final list = v is Map ? v['alerts'] : v;
    if (list is! List) return const [];
    return [for (final a in list) Map<String, dynamic>.from(a as Map)];
  }
}
