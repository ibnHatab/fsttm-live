"""Test signals for the acoustic calibration (fsttm-live scripts/calib, 2026-11-25).

All at 16 kHz s16le mono: the ReSpeaker's own rate (its USB playback is 16 kHz; anything else
is resampled by plughw on the way in, and its echo reference, channel 5, is at 16 kHz).
"""
import json, os, socket, struct, sys, wave
import numpy as np

R = 16000


def write(path, x):
    x = np.clip(np.asarray(x, float), -1, 1)
    with wave.open(path, "wb") as w:
        w.setnchannels(1); w.setsampwidth(2); w.setframerate(R)
        w.writeframes((x * 32767).astype("<i2").tobytes())


def read(path):
    with wave.open(path) as w:
        n, r, c = w.getnframes(), w.getframerate(), w.getnchannels()
        x = np.frombuffer(w.readframes(n), "<i2").astype(float) / 32768
    if c > 1:
        x = x.reshape(-1, c)[:, 0]
    if r != R:  # linear resample: these are test signals, not music
        t = np.arange(int(len(x) * R / r)) * r / R
        x = np.interp(t, np.arange(len(x)), x)
    return x


def pink(n, rng):
    """Pink noise (1/f power), unit RMS: speech-like spread over the band."""
    f = np.fft.rfftfreq(n, 1 / R)
    s = rng.normal(size=len(f)) + 1j * rng.normal(size=len(f))
    s[1:] /= np.sqrt(f[1:]); s[0] = 0
    s[f < 80] = 0; s[f > 7500] = 0
    x = np.fft.irfft(s, n)
    return x / np.sqrt(np.mean(x ** 2))


def sweep(d=3.0, f0=100, f1=7500):
    """Exponential sine sweep (Farina): its inverse gives the impulse response."""
    t = np.arange(int(d * R)) / R
    k = np.log(f1 / f0)
    x = np.sin(2 * np.pi * f0 * d / k * (np.exp(t * k / d) - 1))
    fade = np.minimum(1, np.minimum(t / 0.02, (d - t) / 0.02))
    return x * fade


def db(x):
    return 20 * np.log10(max(x, 1e-9))


def paradee(text, voice, speed=1.0):
    """A sentence in a Paradee voice (Pascal's TTS socket): 24 kHz PCM → 16 kHz float."""
    c = socket.socket(socket.AF_UNIX)
    c.connect(os.path.join(os.environ["XDG_RUNTIME_DIR"], "paradee-tts.sock"))
    c.sendall(f"{speed:.2f} {voice}\n{text}".encode())
    c.shutdown(socket.SHUT_WR)
    b = b""
    while True:
        d = c.recv(65536)
        if not d:
            break
        b += d
    i = b.find(b"data") + 8
    x = np.frombuffer(b[i:len(b) - (len(b) - i) % 2], "<i2").astype(float) / 32768
    t = np.arange(int(len(x) * R / 24000)) * 24000 / R
    return np.interp(t, np.arange(len(x)), x)


def norm(x, dbfs):
    """x scaled to an RMS of dbfs (over its voiced part, as speech levels are measured)."""
    a = np.abs(x)
    v = x[a > 0.02 * a.max()] if a.max() > 0 else x
    return x * (10 ** (dbfs / 20) / np.sqrt(np.mean(v ** 2)))


NINA = ("Good afternoon. The living room is at twenty one degrees, and the ventilation is on its "
        "normal setting. I put some quiet jazz on the radio for you. Tomorrow looks cloudy in the "
        "morning, with a little sun after lunch. Would you like me to dim the bedroom light later, "
        "when it gets dark outside?")
HUMAN = ["Nina, stop.", "What is the weather tomorrow?", "Turn the ventilation to boost.",
         "Nina, wait a moment, I have a question.", "Play something calm."]

if __name__ == "__main__":
    out = sys.argv[1]
    os.makedirs(out, exist_ok=True)
    rng = np.random.default_rng(7)
    write(f"{out}/pink.wav", pink(R * 2, rng) * 10 ** (-12 / 20))
    write(f"{out}/sweep.wav", sweep() * 10 ** (-6 / 20))
    nina = paradee(NINA, "af_heart")
    write(f"{out}/nina.wav", norm(nina, -18))
    hum = [paradee(h, "am_puck") for h in HUMAN]
    gap = np.zeros(int(1.2 * R))
    write(f"{out}/human.wav", norm(np.concatenate([np.concatenate([h, gap]) for h in hum]), -18))
    meta = {"nina_s": len(nina) / R, "human_s": [len(h) / R for h in hum]}
    json.dump(meta, open(f"{out}/signals.json", "w"), indent=1)
    print(meta)
