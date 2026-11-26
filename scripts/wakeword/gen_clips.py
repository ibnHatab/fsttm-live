"""Nina's wake word: the training clips from Paradee's voices (fsttm-live scripts/wakeword, 2026-11-26).

The user's choice: openWakeWord trained on the voices of the TTS the xsel utilities use (Paradee-8M:
af_heart, am_puck, bf_isabella, bm_fable), at varying speeds. openWakeWord's train.py generates its
clips with Piper (--generate_clips); this writes the same four folders instead, so train.py runs from
--augment_clips on:

  positive_train, positive_test   her name: "Nina" (., !, ?, ,), "Hey / Okay / Hi Nina", "Neena"
  negative_train, negative_test   names and words that sound close (a curated list and openWakeWord's
                                  own phonetic neighbours of "nina", without the name itself)

Each clip: a voice, a speed (0.7–1.4) and a pitch shift (resampling, ±12 %: more speakers than four),
the silence trimmed to 0.1 s, 16 kHz mono 16-bit. Needs Pascal's Paradee socket (paradee-tts.socket).

  python gen_clips.py OUT_DIR [N_TRAIN 8000] [N_TEST 1000]
"""
import os, random, socket, sys, uuid
import numpy as np
import scipy.io.wavfile as wf
from scipy.signal import resample_poly

VOICES = ["af_heart", "am_puck", "bf_isabella", "bm_fable"]
R = 16000

POSITIVE = (
    [(w, "Nina" + p) for w, p in [(6, "."), (4, "!"), (4, "?"), (2, ","), (1, "...")]] +
    [(2, "Hey Nina."), (1, "Hey, Nina!"), (1, "Okay Nina."), (1, "Hi Nina."), (1, "Ah, Nina."),
     (1, "Nina, Nina."), (2, "Neena."), (1, "Neena?"), (1, "Nina, please.")]
)
NEAR = ["Tina", "Lena", "Gina", "Mina", "Dina", "Lina", "Rina", "Sina", "Zina", "Vina", "Kina", "Nita",
        "Nima", "Nisha", "Nia", "Nikki", "Nino", "Nine", "nine a.m.", "Anna", "Nana", "Nanny", "Neil",
        "Ninja", "dinner", "meaner", "leaner", "Selena", "Irina", "Marina", "Christina", "arena",
        "Hey Tina", "Hey Lena", "Okay Gina", "Hey Anna", "Hey Nino", "Hi Dina", "Hey, nine!", "Tina?",
        "Lena!", "Mina.", "keener", "Venus", "Edina", "Regina", "Katrina", "Nicky", "nearer", "Naomi"]


def paradee(text, voice, speed):
    c = socket.socket(socket.AF_UNIX)
    c.connect(os.path.join(os.environ["XDG_RUNTIME_DIR"], "paradee-tts.sock"))
    c.sendall(f"{speed:.2f} {voice}\n{text}".encode())
    c.shutdown(socket.SHUT_WR)
    b = b""
    while d := c.recv(65536):
        b += d
    i = b.find(b"data") + 8
    return np.frombuffer(b[i:len(b) - (len(b) - i) % 2], "<i2").astype(np.float32) / 32768  # 24 kHz


def clip(text, rng):
    x = paradee(text, rng.choice(VOICES), rng.uniform(0.7, 1.4))
    # 24 kHz → 16 kHz, with a pitch (and tempo) shift: resampled as if recorded at f·24 kHz
    f = rng.uniform(0.88, 1.12)
    up = 160
    down = int(round(240 * f))
    y = resample_poly(x, up, down)
    # trim the silence to 0.1 s each side
    env = np.abs(y) > 10 ** (-40 / 20) * max(np.abs(y).max(), 1e-6)  # -40 dB under the peak: the soft N kept
    idx = np.flatnonzero(env)
    if len(idx) == 0:
        return None
    a, b = max(0, idx[0] - R // 10), min(len(y), idx[-1] + R // 10)
    y = y[a:b]
    y = y / max(np.abs(y).max(), 1e-6) * 10 ** (rng.uniform(-12, -1) / 20)
    return (y * 32767).astype(np.int16)


def fill(folder, texts, n, rng):
    os.makedirs(folder, exist_ok=True)
    have = len(os.listdir(folder))
    weights = np.array([w for w, _ in texts], float)
    weights /= weights.sum()
    for i in range(have, n):
        t = texts[rng.choice(len(texts), p=weights)][1]
        y = clip(t, rng)
        if y is not None:
            wf.write(os.path.join(folder, uuid.uuid4().hex + ".wav"), R, y)
        if i % 500 == 0:
            print(folder, i, flush=True)


if __name__ == "__main__":
    out = sys.argv[1]
    n_train = int(sys.argv[2]) if len(sys.argv) > 2 else 8000
    n_test = int(sys.argv[3]) if len(sys.argv) > 3 else 1000
    rng = np.random.default_rng(26)
    from openwakeword.data import generate_adversarial_texts
    adv = generate_adversarial_texts(input_text="nina", N=1500, include_partial_phrase=1.0, include_input_words=0.0)
    adv = [a for a in adv if "nina" not in a.lower()]
    negative = [(3, w) for w in NEAR] + [(1, a) for a in sorted(set(adv))]
    fill(os.path.join(out, "positive_train"), POSITIVE, n_train, rng)
    fill(os.path.join(out, "positive_test"), POSITIVE, n_test, rng)
    fill(os.path.join(out, "negative_train"), negative, n_train, rng)
    fill(os.path.join(out, "negative_test"), negative, n_test, rng)
