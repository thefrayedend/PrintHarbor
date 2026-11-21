// The app's own settings: which PrintHarbor, and whether to send phone
// notifications. Everything else lives in PrintHarbor's own Settings page (the
// Printers tab inside the app), so this stays short.
import 'package:flutter/material.dart';

import 'app_model.dart';
import 'app_update.dart';
import 'notifications.dart';

class SettingsView extends StatefulWidget {
  const SettingsView({super.key});
  @override
  State<SettingsView> createState() => _SettingsViewState();
}

class _SettingsViewState extends State<SettingsView> {
  late final TextEditingController _host = TextEditingController(text: app.host);
  String _version = '';
  String _updateNote = '';
  bool _checking = false;

  @override
  void initState() {
    super.initState();
    app.addListener(_changed);
    AppUpdate.installedVersion().then((v) {
      if (mounted) setState(() => _version = v);
    });
  }

  Future<void> _checkUpdate() async {
    setState(() {
      _checking = true;
      _updateNote = 'Checking…';
    });
    String note;
    try {
      final u = await AppUpdate.check();
      if (u == null) {
        note = 'This is the latest version.';
      } else {
        note = 'Version ${u.version} is available.';
        if (mounted) await offerUpdate(context, u);
      }
    } catch (e) {
      note = 'Could not check for updates: $e';
    }
    if (mounted) {
      setState(() {
        _checking = false;
        _updateNote = note;
      });
    }
  }

  @override
  void dispose() {
    app.removeListener(_changed);
    _host.dispose();
    super.dispose();
  }

  void _changed() {
    if (mounted && _host.text != app.host) _host.text = app.host;
    if (mounted) setState(() {});
  }

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context).textTheme;
    return ListView(padding: const EdgeInsets.all(16), children: [
      Text('PrintHarbor', style: t.titleLarge),
      const SizedBox(height: 8),
      Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(crossAxisAlignment: CrossAxisAlignment.start, children: [
            TextField(
              controller: _host,
              decoration: const InputDecoration(
                labelText: 'Address or name',
                hintText: 'harbor.local',
                helperText: 'harbor.local, an IP like 192.168.1.50, or a full http://… address.',
              ),
              onSubmitted: (v) => app.setHost(v),
            ),
            const SizedBox(height: 12),
            Row(children: [
              FilledButton(onPressed: () => app.setHost(_host.text), child: const Text('Connect')),
              const SizedBox(width: 12),
              if (app.connecting) const SizedBox(width: 20, height: 20, child: CircularProgressIndicator(strokeWidth: 2)),
              if (!app.connecting && app.connected)
                Text('Connected · v${app.version}', style: t.bodyMedium?.copyWith(color: Colors.green)),
              if (!app.connecting && !app.connected && app.error != null)
                Expanded(child: Text(app.error!, style: t.bodySmall?.copyWith(color: Theme.of(context).colorScheme.error))),
            ]),
          ]),
        ),
      ),
      const SizedBox(height: 8),
      Card(
        child: SwitchListTile(
          value: app.watch,
          onChanged: !app.connected
              ? null
              : (v) async {
                  if (v) await Notifier.requestPermission();
                  await app.setWatch(v);
                  Notifier.sync();
                },
          title: const Text('Print notifications'),
          subtitle: const Text(
              'Alerts you when a print finishes, is paused (incl. the AI failure detection), fails or loses contact. '
              'Works while the app is open on the same network. For away-from-home push, also set up the server’s notify plugin with ntfy.'),
        ),
      ),
      const SizedBox(height: 8),
      Card(
        child: ListTile(
          title: Text(_version.isEmpty ? 'PrintHarbor app' : 'PrintHarbor app $_version'),
          subtitle: Text(_updateNote.isEmpty ? 'The app looks for a newer version each time it opens.' : _updateNote),
          trailing: FilledButton.tonal(onPressed: _checking ? null : _checkUpdate, child: const Text('Check for updates')),
        ),
      ),
    ]);
  }
}

/// Asks whether to install a newer version, and does it when the answer is yes.
Future<void> offerUpdate(BuildContext context, AvailableUpdate u) async {
  final yes = await showDialog<bool>(
    context: context,
    builder: (c) => AlertDialog(
      title: const Text('Update PrintHarbor?'),
      content: Text('Version ${u.version} is available (you have ${u.current}). Download and install it now?'),
      actions: [
        TextButton(onPressed: () => Navigator.pop(c, false), child: const Text('Not now')),
        FilledButton(onPressed: () => Navigator.pop(c, true), child: const Text('Install')),
      ],
    ),
  );
  if (yes != true || !context.mounted) return;
  final progress = ValueNotifier<double?>(0);
  showDialog<void>(
    context: context,
    barrierDismissible: false,
    builder: (c) => AlertDialog(
      title: Text('Downloading version ${u.version}…'),
      content: ValueListenableBuilder<double?>(valueListenable: progress, builder: (_, v, __) => LinearProgressIndicator(value: v)),
    ),
  );
  final note = await AppUpdate.install(u, progress: (f) => progress.value = f);
  if (!context.mounted) return;
  Navigator.of(context, rootNavigator: true).pop(); // the download box
  ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(note), duration: const Duration(seconds: 8)));
}
