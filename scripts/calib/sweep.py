"""The echo canceller's settings, tried one after another on the same takes (fsttm-live
scripts/calib, 2026-11-25). For each: Nina alone (the echo), the person alone (near end), both
(double talk). Scores:

  erle      dB of her echo removed: raw microphones over the processed channel, while she speaks
  echo99    dBFS: the processed channel's loudest 1% (50 ms frames) while she speaks alone
  false     utterances Nina's endpointer opens on her own echo (each: the floor taken, music ducked)
  keep      dB of the person kept while she speaks over him (0: all of him, -6: half)
  near50    dBFS: the person's median level on the processed channel, alone

  sweep.py OUT.json NAME='{"AECNORM": 8}' ...       (each on top of the base: AGC off)
"""
import json, os, subprocess, sys, time
import numpy as np
sys.path.insert(0, os.path.dirname(__file__))
import sig, rec, ana

D = os.path.dirname(os.path.abspath(__file__))
SIG = os.path.join(D, "../../runs/calib/sig")
BASE = {"AGCONOFF": 0, "AGCGAIN": 1.0, "AECNORM": 2.0, "NLATTENONOFF": 0, "NLAEC_MODE": 0, "GAMMA_E": 1.0,
        "GAMMA_ETAIL": 1.0, "GAMMA_ENL": 1.0, "ECHOONOFF": 1, "TRANSIENTONOFF": 1, "AECFREEZEONOFF": 0}
R = 16000


def xvf(conf):
    subprocess.run(["sudo", "-n", "python3", os.path.join(D, "xvf.py"), "-f", "-"],
                   input=json.dumps(conf).encode(), check=True, capture_output=True)


def offset(x, s):
    """Where signal s starts in x (samples)."""
    d, _ = ana.xcorr_delay(s, x, len(x) - 1)
    return d


def energy(x, spans):
    return float(sum(np.sum(x[max(0, a):b] ** 2) for a, b in spans))


def utts(x):
    r = subprocess.run([os.path.join(D, "../../bin/calib-vad")], input=(x * 32768).astype("<i2").tobytes(),
                       capture_output=True)
    return int(r.stdout.decode().strip().split("\n")[-1].split()[1])


def score(tag, out, gain=0.0):
    nina, hum = sig.read(f"{SIG}/nina.wav"), sig.read(f"{SIG}/human.wav")
    meta = json.load(open(f"{SIG}/signals.json"))
    take = lambda n, secs, ev: (rec.take(f"{out}/{tag}-{n}.npz", secs, ev), ana.load(f"{out}/{tag}-{n}.npz"))[1]
    rec.take("/tmp/calib-warm.npz", 6, [("array", 0.3, f"{SIG}/nina.wav", gain)])  # the canceller adapts to the new settings
    e, _ = take("echo", 21, [("array", 0.5, f"{SIG}/nina.wav", gain)])
    n, _ = take("near", 13, [("laptop", 0.5, f"{SIG}/human.wav", 0)])
    b, _ = take("dt", 21, [("array", 0.5, f"{SIG}/nina.wav", gain), ("laptop", 4.0, f"{SIG}/human.wav", 0)])
    # where she starts (the reference is her, bit for bit) and where he does (on a raw microphone)
    oe, ob = offset(e[:, 5], nina), offset(b[:, 5], nina)
    her = lambda o: [(o, o + len(nina))]
    on = offset(n[:, 1], hum)
    # his phrases, from the generator's lengths (each followed by a 1.2 s gap)
    spans, t = [], 0
    for L in meta["human_s"]:
        spans.append((int(t * R), int((t + L) * R))); t += L + 1.2
    hn = [(a + on, b + on) for a, b in spans]
    hb_start = ob + int(3.5 * R)  # he starts 3.5 s after her in the double-talk take
    hb = [(a + hb_start, b + hb_start) for a, b in spans]
    he = [(a + oe - ob, b + oe - ob) for a, b in hb]  # the same windows of her, in the echo take
    erle = 10 * np.log10(np.mean([energy(e[:, c], her(oe)) for c in range(1, 5)]) / energy(e[:, 0], her(oe)))
    fr = ana.env(e[oe:oe + len(nina), 0], 0.05)
    rf = ana.env(e[oe:oe + len(nina), 5], 0.05)
    echo99 = float(np.percentile(fr[rf > -55], 99))
    near_dt = energy(b[:, 0], hb) - energy(e[:, 0], he)
    keep = 10 * np.log10(max(near_dt, 1e-12) / energy(n[:, 0], hn))
    nf = ana.env(n[:, 0], 0.05)
    near50 = float(np.median(nf[ana.env(n[:, 1], 0.05) > -55]))
    return {"erle": round(erle, 1), "echo99": round(echo99, 1), "false": utts(e[oe:oe + len(nina), 0]),
            "keep": round(keep, 1), "near50": round(near50, 1)}


if __name__ == "__main__":
    out = sys.argv[1]
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    res = json.load(open(out)) if os.path.exists(out) else {}
    for a in sys.argv[2:]:
        name, conf = a.split("=", 1)
        conf = json.loads(conf)
        gain = conf.pop("_gain", 0.0)
        xvf({**BASE, **conf})
        time.sleep(1)
        s = score(name, os.path.dirname(os.path.abspath(out)), gain)
        res[name] = {"conf": conf, "gain": gain, **s}
        print(name, s, flush=True)
        json.dump(res, open(out, "w"), indent=1)
