// PrintHarbor companion app: PrintHarbor's own web UI in a native shell, with
// network discovery and phone notifications. Architecture mirrors FlashBridge's
// app — a WebView of the server's pages plus native glue — adapted to
// PrintHarbor. See app/README.md.
import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'app_model.dart';
import 'app_update.dart';
import 'board_view.dart';
import 'notifications.dart';
import 'settings_view.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await Notifier.init();
  runApp(const PrintHarborApp());
}

class PrintHarborApp extends StatelessWidget {
  const PrintHarborApp({super.key});

  // PrintHarbor's own accent (web style.css --primary), so app and pages match.
  static ThemeData _theme(Brightness b) => ThemeData(
        useMaterial3: true,
        colorScheme: ColorScheme.fromSeed(
          seedColor: b == Brightness.dark ? const Color(0xFF4C9AFF) : const Color(0xFF1A73E8),
          brightness: b,
        ),
      );

  @override
  Widget build(BuildContext context) => MaterialApp(
        title: 'PrintHarbor',
        debugShowCheckedModeBanner: false,
        theme: _theme(Brightness.light),
        darkTheme: _theme(Brightness.dark),
        home: const Shell(),
      );
}

/// Tabs open PrintHarbor's own pages (Library, Slicer, Printers, Queue), plus a
/// native Settings screen for the app itself.
class Shell extends StatefulWidget {
  const Shell({super.key});
  @override
  State<Shell> createState() => _ShellState();
}

class _ShellState extends State<Shell> {
  int _tab = 0;
  static const _hashes = ['library', 'slicer', 'printers', 'queue'];

  @override
  void initState() {
    super.initState();
    app.addListener(_changed);
    _start();
  }

  @override
  void dispose() {
    app.removeListener(_changed);
    super.dispose();
  }

  void _changed() {
    if (mounted) setState(() {});
    Notifier.sync();
  }

  Future<void> _start() async {
    await app.load();
    final ok = await app.connect();
    if (!ok && mounted) setState(() => _tab = _hashes.length); // land on Settings to set the address
    Notifier.sync();
    // Look on GitHub for a newer app and ask before installing it.
    try {
      final u = await AppUpdate.check();
      if (u != null && mounted) await offerUpdate(context, u);
    } catch (_) {
      // offline, or GitHub unreachable: Settings has "Check for updates"
    }
  }

  void _select(int i) {
    setState(() => _tab = i);
    if (i < _hashes.length) BoardNav.open(_hashes[i]);
  }

  // Android back: step the web history first, then fall back to the first tab.
  Future<void> _back() async {
    if (_tab < _hashes.length) {
      final c = BoardNav.controller;
      if (c != null && await c.canGoBack()) {
        await c.goBack();
        return;
      }
    }
    if (_tab != 0) {
      _select(0);
      return;
    }
    await SystemNavigator.pop();
  }

  int get _stackIndex => _tab < _hashes.length ? 0 : 1;

  @override
  Widget build(BuildContext context) {
    return PopScope(
      canPop: false,
      onPopInvokedWithResult: (didPop, _) {
        if (!didPop) _back();
      },
      child: Scaffold(
        body: SafeArea(
          bottom: false,
          child: IndexedStack(index: _stackIndex, children: const [
            BoardView(),
            SettingsView(),
          ]),
        ),
        bottomNavigationBar: NavigationBar(
          selectedIndex: _tab,
          onDestinationSelected: _select,
          destinations: const [
            NavigationDestination(icon: Icon(Icons.collections_bookmark_outlined), selectedIcon: Icon(Icons.collections_bookmark), label: 'Library'),
            NavigationDestination(icon: Icon(Icons.view_in_ar_outlined), selectedIcon: Icon(Icons.view_in_ar), label: 'Slicer'),
            NavigationDestination(icon: Icon(Icons.print_outlined), selectedIcon: Icon(Icons.print), label: 'Printers'),
            NavigationDestination(icon: Icon(Icons.format_list_bulleted), selectedIcon: Icon(Icons.format_list_bulleted), label: 'Queue'),
            NavigationDestination(icon: Icon(Icons.settings_outlined), selectedIcon: Icon(Icons.settings), label: 'Settings'),
          ],
        ),
      ),
    );
  }
}
