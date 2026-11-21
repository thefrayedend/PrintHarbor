// Finding PrintHarbor on the local network, the way FlashBridge's app did:
// resolve its mDNS name (harbor.local), and if that fails, probe the phone's
// own /24 subnet for a host that answers /api/info like PrintHarbor.
import 'dart:async';
import 'dart:io';

import 'package:flutter/services.dart';

import 'package:multicast_dns/multicast_dns.dart';

import 'board.dart';

const _native = MethodChannel('printharbor/app');

/// Android only delivers multicast answers to an app that holds the Wi-Fi
/// multicast lock, so hold it while looking a name up (MainActivity.kt).
Future<void> _multicastLock(bool on) async {
  try {
    await _native.invokeMethod('multicastLock', {'on': on});
  } catch (_) {
    // not Android, or no Wi-Fi: the lookup just finds nothing
  }
}

/// Resolve a ".local" name to an IP via mDNS. Null if nothing answered.
Future<String?> resolveLocal(String name) async {
  final host = name.endsWith('.local') ? name : '$name.local';
  final client = MDnsClient(
    // Android cannot share the mDNS port with reusePort; without this the
    // lookup throws and the name is never found.
    rawDatagramSocketFactory: (dynamic host, int port, {bool reuseAddress = true, bool reusePort = true, int ttl = 1}) =>
        RawDatagramSocket.bind(host, port, reuseAddress: true, reusePort: false, ttl: ttl),
  );
  await _multicastLock(true);
  try {
    await client.start();
    await for (final rec in client.lookup<IPAddressResourceRecord>(
      ResourceRecordQuery.addressIPv4(host),
      timeout: const Duration(seconds: 3),
    )) {
      return rec.address.address;
    }
  } catch (_) {
    // mDNS can be unavailable on some networks; fall through to a scan.
  } finally {
    client.stop();
    await _multicastLock(false);
  }
  return null;
}

/// Probe the phone's /24 for PrintHarbor on [port]. Returns base URLs that
/// answered /api/info with a version. Bounded and parallel so it's quick.
Future<List<String>> findBoards(int port) async {
  final selfIp = await wifiIp();
  if (selfIp == null) return const [];
  final prefix = selfIp.substring(0, selfIp.lastIndexOf('.') + 1);
  final found = <String>[];
  final futures = <Future<void>>[];
  for (var i = 1; i < 255; i++) {
    final url = 'http://$prefix$i:$port';
    futures.add(() async {
      try {
        final info = await Board(url).info().timeout(const Duration(milliseconds: 1200));
        if (info['version'] != null) found.add(url);
      } catch (_) {
        // not PrintHarbor, or nothing there
      }
    }());
  }
  await Future.wait(futures);
  return found;
}

/// The phone's own Wi-Fi IPv4 address (to know which subnet to scan).
Future<String?> wifiIp() async {
  try {
    for (final iface in await NetworkInterface.list(type: InternetAddressType.IPv4)) {
      for (final addr in iface.addresses) {
        if (!addr.isLoopback && addr.address.startsWith(RegExp(r'10\.|172\.(1[6-9]|2\d|3[01])\.|192\.168\.'))) {
          return addr.address;
        }
      }
    }
  } catch (_) {}
  return null;
}
