package io.printharbor.printharbor_app

import android.content.Context
import android.net.wifi.WifiManager
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

/**
 * The app's one screen (Flutter draws it). This side does the one thing only
 * Android can: hold the Wi-Fi multicast lock while the app looks for
 * PrintHarbor by name. Without the lock most phones never deliver the answer
 * to "who is harbor.local?", and the name is not found.
 */
class MainActivity : FlutterActivity() {
    private var multicastLock: WifiManager.MulticastLock? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "printharbor/app").setMethodCallHandler { call, result ->
            when (call.method) {
                "multicastLock" -> {
                    try {
                        if (call.argument<Boolean>("on") == true) {
                            if (multicastLock == null) {
                                val wm = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
                                multicastLock = wm.createMulticastLock("printharbor").apply { setReferenceCounted(false); acquire() }
                            }
                        } else {
                            multicastLock?.release()
                            multicastLock = null
                        }
                    } catch (e: Exception) {
                        // no Wi-Fi: the search simply finds nothing
                    }
                    result.success(true)
                }
                else -> result.notImplemented()
            }
        }
    }

    override fun onDestroy() {
        multicastLock?.release()
        multicastLock = null
        super.onDestroy()
    }
}
