#!/usr/bin/env bash
# Build fsttm-live's Android app (APK) with no Android Studio or Gradle: gomobile bind, then the SDK's
# build tools (aapt2, javac, d8, zipalign, apksigner). The persona is bound in by its Go package:
#   android/build.sh [persona Go packages...]     → android/out/fsttm-live.apk
# Env: ANDROID_HOME (~/Android/Sdk), ANDROID_NDK_HOME (its newest ndk), GOWORK / CGO_CFLAGS /
# CGO_LDFLAGS for the persona's native libraries (a persona's own script sets them), MODULE_DIR the
# module gomobile runs in (default: this repo; a persona's, to bind its packages).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Android/Sdk}"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$(ls -d "$ANDROID_HOME"/ndk/* | sort -V | tail -1)}"
BT=$(ls -d "$ANDROID_HOME"/build-tools/* | grep -v rc | sort -V | tail -1)
JAR=$(ls -d "$ANDROID_HOME"/platforms/android-* | sort -V | tail -1)/android.jar
API=25
out="$here/out"; rm -rf "$out"; mkdir -p "$out/classes" "$out/apk/lib/armeabi-v7a"
gomobile=$(command -v gomobile || echo "$HOME/go/bin/gomobile")

echo "== gomobile bind (android/arm, API $API)"
(cd "${MODULE_DIR:-$here/..}" && PATH="$HOME/go/bin:$PATH" "$gomobile" bind -target=android/arm -androidapi $API \
  -javapkg live.fsttm -o "$out/bridge.aar" github.com/ibnHatab/fsttm-live/pkg/mobile "$@")
(cd "$out" && unzip -q -o bridge.aar classes.jar 'jni/armeabi-v7a/*' && cp jni/armeabi-v7a/*.so apk/lib/armeabi-v7a/)

echo "== manifest (aapt2)"
"$BT/aapt2" link --manifest "$here/AndroidManifest.xml" -I "$JAR" --min-sdk-version $API --target-sdk-version $API \
  -o "$out/base.apk"

echo "== java (javac, d8)"
javac -nowarn -Xlint:-options -source 8 -target 8 -bootclasspath "$JAR" -cp "$out/classes.jar" -d "$out/classes" \
  $(find "$here/src" -name '*.java')
"$BT/d8" --min-api $API --lib "$JAR" --output "$out/apk" $(find "$out/classes" -name '*.class') "$out/classes.jar"

echo "== apk (zip, zipalign, apksigner)"
cp "$out/base.apk" "$out/unsigned.apk"
(cd "$out/apk" && zip -q -r "$out/unsigned.apk" classes.dex lib)
"$BT/zipalign" -f -p 4 "$out/unsigned.apk" "$out/aligned.apk"
ks="${KEYSTORE:-$HOME/.android/debug.keystore}"
"$BT/apksigner" sign --ks "$ks" --ks-pass pass:android --key-pass pass:android --out "$out/fsttm-live.apk" "$out/aligned.apk"
ls -la "$out/fsttm-live.apk"
