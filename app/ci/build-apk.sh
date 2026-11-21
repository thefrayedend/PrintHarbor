#!/usr/bin/env bash
# Builds the PrintHarbor Android app and publishes it as the release "latest-app".
# Run by .github/workflows/build-app.yml from the repo root, after Java 17 and
# Flutter are set up. Kept here (not in the workflow) so the build steps travel
# with the app's source.
#   env: GH_TOKEN, GITHUB_REPOSITORY, RUN_NUMBER
#   optional: ANDROID_KEYSTORE_BASE64 / ANDROID_KEYSTORE_PASSWORD (your own signing key)
#   NO_BUILD=1 stops after the code check (used to test this script without the Android SDK)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="1.0.${RUN_NUMBER:-0}"
OUT="${OUT:-$PWD/../app-out}"

echo "== complete the Android project (our own files are kept)"
flutter --version
flutter create --platforms=android --org io.printharbor --project-name printharbor_app .
rm -f test/widget_test.dart   # the template's sample test is for the template's app

echo "== sign with the app's fixed key, so each build installs over the last"
if [ -n "${ANDROID_KEYSTORE_BASE64:-}" ]; then
  echo "$ANDROID_KEYSTORE_BASE64" | base64 -d > android/app/release.jks
  STORE=release.jks; PASS="${ANDROID_KEYSTORE_PASSWORD:-}"
else
  STORE=printharbor-sideload.jks; PASS=printharbor-sideload
fi
if [ -f android/app/build.gradle.kts ]; then
  cat >> android/app/build.gradle.kts <<GRADLE

// --- added by ci/build-apk.sh ---
android {
    // flutter_local_notifications needs core library desugaring
    compileOptions {
        isCoreLibraryDesugaringEnabled = true
    }
    signingConfigs {
        create("printharbor") {
            storeFile = file("$STORE")
            storePassword = "$PASS"
            keyAlias = "printharbor"
            keyPassword = "$PASS"
        }
    }
    buildTypes {
        getByName("release") {
            signingConfig = signingConfigs.getByName("printharbor")
        }
    }
}
dependencies {
    coreLibraryDesugaring("com.android.tools:desugar_jdk_libs:2.1.4")
}
GRADLE
else
  cat >> android/app/build.gradle <<GRADLE

// --- added by ci/build-apk.sh ---
android {
    compileOptions {
        coreLibraryDesugaringEnabled true
    }
    signingConfigs {
        printharbor {
            storeFile file("$STORE")
            storePassword "$PASS"
            keyAlias "printharbor"
            keyPassword "$PASS"
        }
    }
    buildTypes {
        release {
            signingConfig signingConfigs.printharbor
        }
    }
}
dependencies {
    coreLibraryDesugaring "com.android.tools:desugar_jdk_libs:2.1.4"
}
GRADLE
fi

echo "== check the code"
flutter pub get
flutter analyze --no-fatal-infos --no-fatal-warnings
flutter test

if [ -n "${NO_BUILD:-}" ]; then echo "== NO_BUILD set: stopping before the Android build"; exit 0; fi

echo "== build"
flutter build apk --release --build-name "$VERSION" --build-number "${RUN_NUMBER:-1}"
mkdir -p "$OUT"
cp build/app/outputs/flutter-apk/app-release.apk "$OUT/printharbor.apk"
echo "{\"version\": \"$VERSION\", \"apk\": \"printharbor.apk\"}" > "$OUT/printharbor-app.json"
ls -lh "$OUT"

if [ -n "${GH_TOKEN:-}" ]; then
  echo "== publish as the release latest-app"
  gh release delete latest-app --yes --cleanup-tag --repo "$GITHUB_REPOSITORY" || true
  gh release create latest-app "$OUT"/* --repo "$GITHUB_REPOSITORY" \
    --title "PrintHarbor Android app $VERSION" \
    --notes "Open this page on your phone, tap printharbor.apk and install it. To update, install the new one over the old."
fi
