# fsttm-live's Android app

A WebView on the voice's panel, the mic fed to Go while the app is in front (no wake word), her voice
streamed back; the persona is bound in by its Go package (`pkg/mobile` is the bridge). No Android Studio
or Gradle: `android/build.sh [persona packages]` (gomobile bind, aapt2, javac, d8, zipalign, apksigner).
Nina's build and install: nina-live `scripts/android-build.sh [--install]`.

## Measured (Fire 7 2017 "austin", LineageOS 14.1, MT8127, armeabi-v7a, 1 GB; 2026-11-27)

Nina's dialogue running (Gemini Live, pi on Gemini Flash), turns spoken from Pascal's speakers (Paradee):

| What | Value |
|---|---|
| Cold start to the activity displayed | 976 ms (`am start -W`) |
| First audio after a turn | 569, 583, 588, 614, 990 ms (5 turns) |
| Memory | 75 MB PSS in all (Dalvik 2.5 MB; Go heap 3–17 MB), 114 MB RSS |
| CPU, the app | median 8 %, max 26 % of one core while she speaks (2 cores online) |
| CPU, audioserver | median 17 % (the voice-communication capture: the platform's echo canceller) |
| WebView frames | 101 frames, 27 % janky, 50th 11 ms, 90th 26 ms, 99th 40 ms (the panel's bubble updated per word) |
| Thermal zone 0 | 39 °C |

The jank is the conversation bubble re-rendered on every streamed word; batching those updates (one
render per animation frame) is the first thing to try if the panel feels slow.
