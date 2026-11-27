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

### Her face (acasa's nina-card, `shell.html?remote`), same day

At rest, the app's CPU and the WebView's frame time, by setting:

| Setting | CPU at rest | Frame 50th |
|---|---|---|
| parallax shader 30/15 fps, blur over her face (the card's defaults) | 29–40 % | 31 ms |
| CSS sway instead of the shader (`parallax=0`) | 37–59 % | 48 ms |
| shader 20/8 fps (`fps=20,8`) | 19–32 % | 32 ms |
| + no blur (`lite`) | 17–25 % | 15 ms |
| + 0.7 resolution (`scale=0.7`): **Nina's setting** | 14–22 % | 15 ms |

During a reply at Nina's setting: 41–55 % CPU, 7 % janky frames, 50th 12 ms (the plain panel before the
batching: 27 % janky). The blur (`backdrop-filter`) was half of every frame: each new frame of her face
re-blurs what lies under the bubbles.
