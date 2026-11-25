# Acoustic calibration (2026-11-25)

The room: the speakers on the ReSpeaker's jack, about 20 cm from the array, their knob at 50 %.
The person: a voice played from the laptop's speaker (not in the echo canceller's reference).
The objectives: the echo canceller works, the ducking works, barge-in works.

## The steps (the panel to come runs them in this order)

1. **Floor**: 4 s of the room (`rec.py floor.npz 4`): the raw microphones' noise (−65 dBFS here).
2. **Path**: a sine sweep and pink noise in 6 dB steps (`rec.py`, `ana.py levels|delay`): the
   reference (channel 5) is the feed bit for bit; the microphones hear it 7.7 dB under the feed,
   3.6 ms later; linear up to −12 dBFS RMS (peaks −7.5 dBFS on the microphones: no clipping).
3. **Canceller**: her speech alone, the person alone, both (`sweep.py`), scored on the echo removed
   (ERLE), the processed channel's loudest 1 % while she speaks, the utterances Nina's endpointer
   opens on her own echo (`bin/calib-vad`, the same endpointer as the dialogue's), and how much of
   the person survives double talk.
4. **Level**: the same at falling output levels: the calibrated minimum is the level at which she
   arrives at the array as loud as the person does (`audio.level`, bubble 1 m).
5. **Music**: the radio on the same jack at rising volumes, alone and under the person's voice.

## Results

| setting | ERLE | echo 99 % | false utterances | person kept |
|---|---|---|---|---|
| AGC on (the firmware's default) | ~8 dB *apparent* | | | its gain lifts the echo with the voice |
| AGC off, AECNORM 2 / 4 / 8 | 25 / 25 / 24 dB | −39 dBFS | 7–8 | −0.6 dB |
| NLATTENONOFF 1 | 19 dB | −34 | 5 | −4.9 dB (worse) |
| her speech at −30 dBFS RMS (+12 dB, bubble 4 m) | 28 dB | −50 | **0** | −0.3 |
| −36 dBFS RMS (+6 dB, bubble 2 m) | 24 dB | −50 | **0** | −0.7 |
| **−36 dBFS RMS: the minimum, bubble 1 m** (−18 test-relative) | 26 dB | −58 | **0** | 0.0 |
| −42 dBFS RMS | 25 dB | −65 (the floor) | 0 | −0.6 |
| −18 dBFS RMS (+18 dB) | 25 dB | −39 | 7 | −0.6 |

(The test file is −18 dBFS RMS; rows name the feed's level. The person: −39 dBFS median on the
processed channel. The canceller removes a constant ~25 dB; what decides is how loud she is.)

Music (MPD's software volume, the stream at −19 dBFS RMS at 100 %): alone, 0 utterances at 30,
50, 76, 100 %; the person over it: 5 of 5 phrases at 50 and 76 %, at 100 % each held open longer.

## The algorithm

- **The firmware**: AGC off (`AGCONOFF 0`, `AGCGAIN 1`): its gain follows the residual and the
  noise as much as the voice. The rest stays at the defaults (the canceller's 25 dB is level
  independent; the non-linear attenuation costs the person 5 dB for nothing).
- **Her level**: `audio.level` is her speech's RMS on the feed at the minimum (−36 dBFS here). The
  speaker learns her stream's own level (a slow average over voiced chunks) and scales it to
  `level + 20·log10(radius)`.
- **The bubble** (`prefs.bubble`, metres, the knob): 1 m is the minimum, each doubling +6 dB, up to
  4 m (+12 dB: her echo stays 10 dB under the person; at 8 m it opens utterances). The radar's
  distance aims her at the person within the bubble, never past it.
- **The duck**: the radio is brought to 10 dB under her voice while anyone talks, through MPD's
  exponential curve ((e^(4v) − 1)/(e^4 − 1)), wherever it was; quieter music is left alone. (The old
  "0.3 of the volume" took 76 % to 23 %: 22 dB, the music gone.)
- **Routing**: her voice and the radio both on the array's jack (PulseAudio's sink for the array,
  profile `output:analog-stereo`; the capture stays on `hw:` for the 6 channels), so the canceller
  has both in its reference.

## Files

`sig.py` (test signals), `rec.py` (a take: 6 channels + the laptop's microphone), `xvf.py` (the
XVF-3000's parameters over USB; root, or a udev rule for 2886:0018), `ana.py` (levels, delay,
timeline, the endpointer over a channel), `sweep.py` (settings × takes → scores). Takes in
`runs/calib/` (not in git).

## Live (2026-11-25, `live_duck.py`: the running Nina, the feed and the radio recorded from PulseAudio)

Her line said through the panel, the person (laptop speaker) over it 3 s in: a backchannel
("Mm-hm, yes": she must come back) and an interruption ("Sorry, wait, what was that…": cut).
3 rounds each, bubble 1.5 m (target −32.5 dBFS).

| | without music | with music (final) | target |
|---|---|---|---|
| her level | −31.7 to −33.5 dBFS | −31.5 to −32.9 | −32.5 |
| duck event | 6 / 6 | 6 / 6 | |
| her duck | −11.6 to −15.7 dB | −10.0 to −15.9 (mean −12.2) | −12 (the gain is exact; words differ) |
| reaction | 586–720 ms after the sound starts | 602–775 ms | |
| backchannel → her back | 3 / 3, at her level | 3 / 3 | |
| interruption → cut | 3 / 3 | 3 / 3 | |
| radio under her voice | | −10.7 to −16.1 dB | −10 |
| radio under her ducked voice | | −9.1 to −16.0 dB | −10 |

Fixed on the way: backchannels cut her ("Mam, yes"); the normaliser averaged dB (3 dB loud:
now energies); the radio never linked (the core rebuilds its music host); the radio stayed over her
ducked voice (Loudness includes the duck, the music follows it at once); the radio lagged a second
behind her duck (MPD's software mixer acts before its output buffer: now `mixer_type hardware`,
PulseAudio's cubic curve, `music.curve: pulse`).
