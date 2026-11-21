package io.printharbor.printharbor_app

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.net.wifi.WifiManager
import android.os.Build
import android.provider.Settings
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import java.io.File

/**
 * The app's one screen (Flutter draws it). This side does what only Android
 * can: hold the Wi-Fi multicast lock while the app looks for PrintHarbor by
 * name (without it most phones never deliver the answer to "who is
 * harbor.local?"), and hand a downloaded update to Android's installer.
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
                "cacheDir" -> result.success(cacheDir.absolutePath)
                "appVersion" -> result.success(
                    try {
                        packageManager.getPackageInfo(packageName, 0).versionName ?: ""
                    } catch (e: Exception) {
                        ""
                    }
                )
                "installApk" -> {
                    val path = call.argument<String>("path")
                    if (path == null) {
                        result.success("error: no file")
                    } else if (Build.VERSION.SDK_INT >= 26 && !packageManager.canRequestPackageInstalls()) {
                        // once: Android asks to allow "Install unknown apps" for PrintHarbor
                        try {
                            startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")))
                        } catch (e: Exception) {
                        }
                        result.success("permission")
                    } else {
                        Thread {
                            val r = try {
                                Updater.install(this, File(path))
                                "started"
                            } catch (e: Exception) {
                                "error: ${e.message}"
                            }
                            runOnUiThread { result.success(r) }
                        }.start()
                    }
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
