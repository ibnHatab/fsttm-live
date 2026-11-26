# Nina's wake word (openWakeWord)

- `melspectrogram.onnx`, `embedding_model.onnx`: openWakeWord's shared models, release v0.5.1
  (github.com/dscripka/openWakeWord, Apache-2.0; the embedding is Google's speech_embedding).
- `nina.onnx`: Nina's own classifier, trained 2026-11-26 with openWakeWord's train.py on clips of
  Paradee's four voices (`scripts/wakeword`: gen_clips.py, nina.yml). On the night's tests: the
  person's tries recorded on pero 5 of 5 (through the echo canceller), 1 false wake in 10.7 h of
  openWakeWord's validation audio at threshold 0.5.

Used by `dialog.wake.engine: oww` (internal/oww), `model: ~/repo/fsttm-live/models/oww`.

Tried and kept out (2026-11-26): 200,000 steps with a 64-wide layer learned the four voices (9 of 10
synthetic names) and lost the person (4 of 5) and the quiet (7 false wakes an hour): more voices
would be the way to a better model, not more training on these.
