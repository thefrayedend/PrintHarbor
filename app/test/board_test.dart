// The app's HTTP client against a stand-in PrintHarbor server that answers the
// way the real one does: /api/info is an object with a version, /api/printers
// and /api/alerts are plain lists.
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:printharbor_app/app_update.dart';
import 'package:printharbor_app/board.dart';
import 'package:printharbor_app/board_view.dart';

void main() {
  late HttpServer server;
  late Board board;

  setUp(() async {
    server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    server.listen((req) {
      Object? body;
      switch (req.uri.path) {
        case '/api/info':
          body = {'version': '0.4.2', 'build_number': 76};
        case '/api/printers':
          body = [
            {'id': 'a1', 'name': 'Printer One', 'bed_clear': true, 'has_empty_bed': false, 'status': {'state': 'idle'}}
          ];
        case '/api/alerts':
          final since = int.tryParse(req.uri.queryParameters['since'] ?? '') ?? 0;
          body = [
            for (var i = 1; i <= 3; i++)
              if (i > since) {'id': i, 'level': 'info', 'printer_name': 'Printer One', 'text': 'alert $i'}
          ];
        default:
          req.response.statusCode = 404;
      }
      if (body != null) req.response.headers.contentType = ContentType.json;
      req.response.write(body == null ? '' : jsonEncode(body));
      req.response.close();
    });
    board = Board('http://127.0.0.1:${server.port}');
  });

  tearDown(() => server.close(force: true));

  test('info returns the version', () async {
    expect((await board.info())['version'], '0.4.2');
  });

  test('printers reads a plain list', () async {
    final p = await board.printers();
    expect(p.single['name'], 'Printer One');
  });

  test('alerts honours since', () async {
    expect((await board.alerts(0)).length, 3);
    expect((await board.alerts(2)).single['id'], 3);
  });

  test('a server that is not PrintHarbor is reported, not crashed on', () async {
    expect(() => Board('http://127.0.0.1:${server.port}/nope').info(), throwsA(isA<BoardException>()));
  });

  test('the page-switch script quotes its view safely', () {
    final js = BoardNav.showScript("li'brary");
    expect(js, contains(r"'li\'brary'"));
    expect(js, contains('show(v)'));
  });

  test('version comparison', () {
    expect(isNewerVersion('1.0.10', '1.0.9'), true);
    expect(isNewerVersion('1.0.4', '1.0.4'), false);
    expect(isNewerVersion('1.0.3', '1.0.4'), false);
    expect(isNewerVersion('1.1', '1.0.99'), true);
  });
}
