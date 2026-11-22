# fsttm-live

Nina's dialog runtime on the Gemini Live API, in Go, for small machines (a Raspberry Pi 4 with
the ReSpeaker 4 Mic Array and a monitor in kiosk mode). The FSTTM floor machine (Raux &
Eskenazi, N09-1071) owns the floor; Google's free tier does the hearing, the talking and the
reasoning; Metis and the home's other SOURCES own the truth.

Status: **phase 2, the floor** (`cmd/nina-live`): the floor machine drives both Live sessions
end to end, acoustically, on a desktop with the ReSpeaker. Phase 1 was the spike (`cmd/livespike`).

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
- The voice: the Live session's own audio (unlimited on the free tier; Gemini's TTS models allow
  10 requests a day). On a small machine RHVoice is the local voice (`-local rhvoice`): it speaks
  the session's transcript sentence by sentence, and says the local phrases when the network is
  down. Whisper is not used.

## The floor (`internal/dialog`)

FSTTM's floor machine and attention (`github.com/ibnHatab/fsttm/go/pkg/fsm`, `pkg/attention`),
driven by our endpointer, the ears and the voice:

| event | floor | |
|---|---|---|
| speech starts (local VAD) | user G: FREE → USER | while Nina holds the floor: *tentative* (her echo until proven otherwise) |
| speech ends (700 ms hang) | user R: USER → FREEu | |
| final text, addressed | system G: FREEu → SYSTEM | the text is a turn for the voice |
| reply played | system R: SYSTEM → FREEs | attention touched; the follow-up window opens |
| words Nina is not saying, while SYSTEM | barge-in: user G (BOTHs), system R (USER) | speaker cut locally, the turn's tail dropped; the next turn says what was heard |
| NOTE from SOURCES | system G from FREE only | urgent at once, others after 2 s of free floor; the knob filters levels |

- **Addressed**: asleep, only an utterance with her name (it wakes her); awake, one with her name
  or within the follow-up window (12 s) after she spoke; anything else is an aside. Asleep again
  after 90 s without a turn, or "Nina, go to sleep".
- **Echo without a canceller**: the ears transcribe Nina too; an utterance that started while she
  held the floor is compared with her output transcript: her words are echo, two new words (or
  "stop", "wait"…) are a barge-in, and only the new words reach the voice.
- **Ears**: the transcribe model sends only interim text, accumulated over its session without
  separators; the ears cut out each utterance and call it final at sentence punctuation that held
  200 ms, or 1.5 s after our end.
- **Voice**: turns are tracked by id: a turn sent while the previous one is still generating
  interrupts it, and every event is attributed to its own turn (a cut reply's tail is dropped).

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

Phase 2, `nina-live` acoustically (Pascal: the person is a WAV from the PC's speaker, Nina plays
from the same speaker; `testdata/scripts/basic.txt`):

| test | result |
|---|---|
| asleep: "Is the bedroom ventilation running?", "What's on the living room TV?" | ignored |
| wake, two commands (tools right), ambiguous command ("the lab or the bedroom?"), sleep | right; final text 0.41–0.45 s after our end; first audio 0.8–1.3 s after the turn (1.2–1.7 s after the end of speech) |
| Nina's echo (no canceller): 7 replies, each heard in full by the ears | 7 of 7 rejected by the transcript comparison |
| an aside 20 s after she spoke | ignored |
| NOTE (important) while the person had the floor | waited; narrated when the floor was free ("I've noticed the CO2 in the bedroom is getting high…") |
| "Nina, stop." over a long reply, **no canceller** (same speaker, same level) | **missed**: the ears heard only Nina; barge-in needs echo cancellation |
| the same through PulseAudio's WebRTC canceller (`scripts/pascal-aec.sh`, ~10 dB off the echo, person 0 dB) | cut **90 ms after the person's last word**; next turn "Okay." with what was heard |
| `-local rhvoice` | works; first audio 1.6–2.3 s (a sentence of transcript first); its robotic echo transcribes worse (one false barge-in) |

The ReSpeaker's hardware canceller is not tested yet: nothing audible is plugged into its jack
(channel 5 carries the reference when Nina plays to `plughw:ArrayUAC10`, the raw mics stay at
noise). With a speaker on the jack: `-out plughw:ArrayUAC10`.

Open: the ReSpeaker's own AEC (a speaker on its jack); the Metis reasoning channel (tools and
the narration port); the Raspberry Pi build (cgo for the WebRTC VAD: build on the Pi or with an
aarch64 cross compiler).

## Config, preferences, plugins

One file (`-config`, default `~/.config/fsttm-live/config.yaml`, created from
`internal/config/default.yaml` when missing; `testdata/config-pascal.yaml` is Pascal's):

- the machine's settings: the key's file, the charter, the models, the sound devices, the dialog's
  timing;
- `prefs`: what the person changes while Nina runs (mic, voice, the Live voice, the local voice,
  the narration level), changed by any plugin and written back into the file, its comments kept;
- `plugins`: one block each, `enabled` or not; several run at once (set her up on the panel, drive
  her with the buttons). Each gets the same host: preferences, status, events, actions (talk, wake,
  sleep, stop, mic, voice, type, say, note).

| plugin | |
|---|---|
| `web` | the panel (`:8770`): the mic/voice card (F9, Shift+8), Talk/Stop/Sleep, the Live voice, the local voice (RHVoice, Paradee), the narration knob (off · urgent · important · info), the floor and attention, the conversation (her echo, asides and ignored lines shown dimmed). A kiosk's screen on the Pi. |
| `gpio` | a Pi's buttons and lights through `/dev/gpiochipN` (go-gpiocdev, no root): per button a press and a long-press action (default: talk / sleep, mic, voice); per light a state (awake, speaking, hearing, replying, mic, voice). |

The Live API has no sleep: asleep (her goodbye, the Sleep action, or `awake_for` without a turn),
Nina's voice session is closed; a wake or an urgent NOTE resumes it with its handle, the
conversation's context intact (measured: after a close, "what did I ask you to turn on earlier?"
→ "You asked me to turn on the bedroom ventilation"). A change of the Live voice takes effect by
resuming the session with the new voice. The mic switched off closes the ears' session.

Local voices (`prefs.local`): `rhvoice` for small machines; `paradee` on a desktop running
xsel-to-speech's Paradee service (`paradee-tts.socket`, model kept loaded: 7 s of speech in
0.13 s). Both speak her transcript sentence by sentence (first audio ~2.4 s, against ~1 s for her
own audio).

## Run

```sh
go build -o bin/nina-live ./cmd/nina-live
bin/nina-live -config testdata/config-pascal.yaml            # the panel on :8770; type lines on stdin too
bin/nina-live -config testdata/config-pascal.yaml -q -log runs/x.jsonl -script testdata/scripts/basic.txt
scripts/pascal-aec.sh up    # then audio.device pulse:nina_ec_source, channels 1, ref -1, out pulse:nina_ec_sink
```

Typed lines: plain text is the person's words; `/note important …` a NOTE; `/say …` verbatim.
