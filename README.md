# fsttm-live

Nina's dialog runtime on the Gemini Live API, in Go, for small machines (a Raspberry Pi 4 with
the ReSpeaker 4 Mic Array and a monitor in kiosk mode). The FSTTM floor machine (Raux &
Eskenazi, N09-1071) owns the floor; Google's free tier does the hearing, the talking and the
reasoning; Metis and the home's other SOURCES own the truth.

Status: **phase 1, the spike** (`cmd/livespike`). The design below is what the spike's
measurements support.

## Roles (the same words in every prompt)

- **USER**: the person. Only USER speaks to Nina.
- **SYSTEM**: Nina, one voice, speaking only when the floor is hers.
- **SOURCES**: the systems Nina draws on (the Metis runtime first, any other service). They never
  speak; they answer tools and send narration through a port quantised by urgency.

## The design the spike supports

```
ReSpeaker ch0 ─▶ local VAD ─▶ FSTTM (endpointing, barge-in) ─▶ attention (awake/asleep, addressed?)
                    │ activityStart/End + audio                     │ text turns (USER: …, NARRATE (level): …)
                    ▼                                               ▼
        Live "ears": gemini-3.5-transcribe-live          Live "voice+mind": gemini-3.1-flash-live-preview
        (TEXT, manual activity, customVocabulary,        (AUDIO, tools → Metis/SOURCES, 24 kHz PCM out)
         interim transcripts every ~0.5 s)                          │
                                                                    ▼
                                                        speaker (cut locally on barge-in)
```

- The ears run in **our** endpointing (manual activity). The model is never asked whether it was
  addressed: our attention machine decides, and only addressed text reaches the voice session.
- Narration: semantic world → Metis infers → narration port (urgent / important / info) → a
  `NARRATE (level): …` turn when the floor machine gives the floor and the UI knob lets the level
  through; it stays in the voice session's context ("why did you mention the bedroom?" works).
- Paradee/RHVoice and Whisper are not used: everything is on the free tier.

## Measurements (Pascal, 2026-11-22; `runs/` is not in git)

Free tier of the project's key (AI Studio rate limits): Live models unlimited requests (3.8 Live and
3 Flash Live 65K tokens/min, 3.5 Transcribe Live 20K); Flash-Lite 3.1/3.5 15/min and 500/day each;
**every TTS model 3/min and 10/day** (unusable for a voice).

| test | result |
|---|---|
| 3.8 Live, manual activity (our endpointing) | **no response, no transcript**: 3.8 accepts only its own VAD |
| 3.8 Live, automatic VAD | transcript right, first audio 118 ms after the server's end of speech |
| 3.1 Flash Live, manual activity, 9 utterances (digital) | all transcribed right; first audio 0.63–1.18 s after our activityEnd; tools right; asked "which one?" |
| 3.1 Flash Live, not addressed | cannot stay silent (no proactive audio): it said "stay silent" aloud → addressedness is ours |
| text turns (transcript, NARRATE, SAY EXACTLY) on 3.1 / 3.8 | first audio 0.49–1.06 s / 0.45–1.79 s; verbatim exact; narration natural |
| 3.8 Live tools | asynchronous: turnComplete comes before the spoken result (bookkeeping differs) |
| 3.5 Transcribe Live, manual activity, **acoustic** (speaker → room → ReSpeaker ch0, WebRTC VAD, hang 700 ms) | 6 of 6 exact (incl. "lab Prana in night mode"); text ready 80–330 ms after our end; one transcript arrived ~5 s late |
| 3.1 Flash Live tokens | ~2.2K prompt tokens per turn after 9 turns |

Open: echo (Nina heard by the array) until the speaker is on the ReSpeaker's jack (its channel 5,
the echo reference, is silent on Pascal); session limits (10 min transcribe, ~10 min connection)
with resumption; the charter must not leak its labels ("USER, just so you know").

## Spike

```sh
go build -o bin/livespike ./cmd/livespike
bin/livespike -model gemini-3.1-flash-live-preview -sys testdata/charter-v1.txt testdata/utt/*.wav   # digital, manual activity
bin/livespike -model gemini-3.1-flash-live-preview -sys testdata/charter-v1.txt testdata/turns-v0.txt # text turns
bin/livespike -mic -model gemini-3.5-transcribe-live -text -tools=false testdata/utt/*.wav          # acoustic, ReSpeaker
```

The key is read from `~/.config/fsttm/gemini.key`. `testdata/utt/` are test utterances spoken by
Paradee's `am_puck` voice.
