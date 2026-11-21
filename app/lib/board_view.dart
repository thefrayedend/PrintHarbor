// PrintHarbor's own web UI (library, slicer, printers, queue, settings) inside
// the app. The app is a native shell around these pages — it sets a
// "PrintHarborApp" user-agent so the web UI adapts for the phone (web/app.js
// reacts to it), and keeps the hardware things (notifications, discovery) native.
import 'dart:collection';

import 'package:flutter/material.dart';
import 'package:flutter_inappwebview/flutter_inappwebview.dart';

import 'app_model.dart';

/// Lets the rest of the app steer the web page (open a tab, a deep link, …).
class BoardNav {
  static InAppWebViewController? controller;
  static bool loaded = false;

  /// The view waiting to be shown once the page has finished starting up.
  static String? pending;

  /// Show one of PrintHarbor's pages: 'library', 'slicer', 'printers', 'queue'.
  /// The web UI switches pages with its own show() function (it only reads the
  /// address hash once, when it starts), so call that. If the page is still
  /// loading, the script waits until PrintHarbor has started before switching.
  static Future<void> open(String view) async {
    final c = controller;
    if (c == null || app.boardUrl.isEmpty) {
      pending = view;
      return;
    }
    final url = await c.getUrl();
    if (url == null || !url.toString().startsWith(app.boardUrl)) {
      pending = view;
      await c.loadUrl(urlRequest: URLRequest(url: WebUri('${app.boardUrl}/')));
      return;
    }
    pending = null;
    await c.evaluateJavascript(source: showScript(view));
  }

  /// JavaScript that shows [view] as soon as the web UI is ready. The UI starts
  /// on 'projects' internally and moves to its first real page when it has
  /// loaded, so that move is the sign it is ready.
  static String showScript(String view) => """
(function (v) {
  var n = 0;
  (function go() {
    var ready = false;
    try { ready = typeof show === 'function' && state.info && state.info.version && state.view !== 'projects'; } catch (e) {}
    if (ready || n++ > 100) { try { show(v); } catch (e) {} return; }
    setTimeout(go, 150);
  })();
})(${_js(view)});""";

  static String _js(String s) => "'${s.replaceAll('\\', '\\\\').replaceAll("'", "\\'")}'";
}

class BoardView extends StatefulWidget {
  const BoardView({super.key});
  @override
  State<BoardView> createState() => _BoardViewState();
}

class _BoardViewState extends State<BoardView> {
  String _shown = '';
  double _progress = 0;
  String? _loadError;

  @override
  void initState() {
    super.initState();
    app.addListener(_onApp);
  }

  @override
  void dispose() {
    app.removeListener(_onApp);
    super.dispose();
  }

  void _onApp() {
    if (app.boardUrl.isNotEmpty && app.boardUrl != _shown && BoardNav.controller != null) {
      _shown = app.boardUrl;
      BoardNav.loaded = false;
      BoardNav.controller!.loadUrl(urlRequest: URLRequest(url: WebUri('${app.boardUrl}/')));
    }
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    if (app.boardUrl.isEmpty) {
      BoardNav.controller = null;
      BoardNav.loaded = false;
      return _Message(
        icon: Icons.wifi_find,
        text: app.connecting ? 'Looking for PrintHarbor…' : (app.error ?? 'No PrintHarbor yet. Find it in Settings.'),
        busy: app.connecting,
        action: app.connecting ? null : FilledButton(onPressed: () => app.connect(), child: const Text('Search again')),
      );
    }
    return Stack(children: [
      InAppWebView(
        initialUrlRequest: URLRequest(url: WebUri('${app.boardUrl}/')),
        // Runs before PrintHarbor's own scripts. The web UI treats this app like
        // its desktop window (window.__phApp) and would tell a window with no
        // build number to "close and reopen to finish the update"; that message
        // is for the desktop program only, so give the phone a build number
        // that is never out of date.
        initialUserScripts: UnmodifiableListView<UserScript>([
          UserScript(
            source: 'window.__phPhone = true; window.__phAppBuild = Number.MAX_SAFE_INTEGER;',
            injectionTime: UserScriptInjectionTime.AT_DOCUMENT_START,
          ),
        ]),
        initialSettings: InAppWebViewSettings(
          javaScriptEnabled: true,
          domStorageEnabled: true,
          useOnDownloadStart: true,
          useShouldOverrideUrlLoading: true,
          mediaPlaybackRequiresUserGesture: false,
          supportZoom: false,
          transparentBackground: true,
          supportMultipleWindows: false,
          mixedContentMode: MixedContentMode.MIXED_CONTENT_ALWAYS_ALLOW,
          // Tells PrintHarbor's pages they run inside the phone app, so they can
          // adapt (web/app.js: adds the `in-app mobile-app` class on this UA).
          applicationNameForUserAgent: 'PrintHarborApp',
        ),
        onWebViewCreated: (c) {
          BoardNav.controller = c;
          _shown = app.boardUrl;
        },
        onLoadStart: (c, url) => setState(() {
          BoardNav.loaded = false;
          _loadError = null;
        }),
        onLoadStop: (c, url) {
          setState(() => BoardNav.loaded = true);
          final v = BoardNav.pending;
          if (v != null) {
            BoardNav.pending = null;
            c.evaluateJavascript(source: BoardNav.showScript(v));
          }
        },
        onProgressChanged: (c, p) => setState(() => _progress = p / 100),
        onReceivedError: (c, req, err) {
          if (req.isForMainFrame ?? true) setState(() => _loadError = err.description);
        },
        // Downloads (sliced files, backups) and off-site links go to the phone browser.
        onDownloadStartRequest: (c, req) => _external(req.url.toString()),
        shouldOverrideUrlLoading: (c, action) async {
          final u = action.request.url;
          if (u == null || action.isForMainFrame == false) return NavigationActionPolicy.ALLOW;
          final s = u.toString();
          if (s.startsWith(app.boardUrl) || s.startsWith('about:') || s.startsWith('data:') || s.startsWith('blob:')) {
            return NavigationActionPolicy.ALLOW;
          }
          _external(s);
          return NavigationActionPolicy.CANCEL;
        },
      ),
      if (_progress < 1) LinearProgressIndicator(value: _progress, minHeight: 2),
      if (_loadError != null)
        Positioned.fill(
          child: ColoredBox(
            color: Theme.of(context).colorScheme.surface,
            child: _Message(
              icon: Icons.cloud_off,
              text: 'PrintHarbor did not answer ($_loadError).\nIs it running and on the same Wi-Fi?',
              action: FilledButton(
                onPressed: () async {
                  setState(() => _loadError = null);
                  if (await app.connect()) {
                    BoardNav.controller?.loadUrl(urlRequest: URLRequest(url: WebUri('${app.boardUrl}/')));
                  }
                },
                child: const Text('Try again'),
              ),
            ),
          ),
        ),
    ]);
  }

  Future<void> _external(String url) async {
    // Open in the phone's browser; InAppBrowser or url_launcher can be wired here.
    await InAppBrowser.openWithSystemBrowser(url: WebUri(url));
  }
}

class _Message extends StatelessWidget {
  final IconData icon;
  final String text;
  final Widget? action;
  final bool busy;
  const _Message({required this.icon, required this.text, this.action, this.busy = false});

  @override
  Widget build(BuildContext context) => Center(
        child: Padding(
          padding: const EdgeInsets.all(32),
          child: Column(mainAxisSize: MainAxisSize.min, children: [
            busy
                ? const SizedBox(width: 40, height: 40, child: CircularProgressIndicator())
                : Icon(icon, size: 48, color: Theme.of(context).colorScheme.primary),
            const SizedBox(height: 16),
            Text(text, textAlign: TextAlign.center, style: Theme.of(context).textTheme.bodyLarge),
            if (action != null) ...[const SizedBox(height: 20), action!],
          ]),
        ),
      );
}
