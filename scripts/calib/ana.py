"""Analysis of calibration takes (rec.py's .npz).

  ana.py levels TAKE.npz      per pink step: ref, raw mics, processed, laptop; linearity, ERLE
  ana.py delay TAKE.npz       the reference → microphone delay and the echo path's gain
  ana.py timeline TAKE.npz    100 ms levels of every channel (and the VAD's view)
"""
import sys, os
import numpy as np
sys.path.insert(0, os.path.dirname(__file__))
import sig

R = 16000


def load(p):
    z = np.load(p)
    return z["array"].astype(float) / 32768, z["laptop"].astype(float) / 32768


def rms(x):
    return float(np.sqrt(np.mean(x ** 2))) if len(x) else 0.0


def env(x, win=0.1):
    """dB per window."""
    n = int(win * R)
    m = len(x) // n
    return np.array([sig.db(rms(x[i * n:(i + 1) * n])) for i in range(m)])


def segments(ref, thr_db=-50, minlen=0.5):
    """Where the reference is on: (start, end) in samples."""
    e = env(ref, 0.05) > thr_db
    out, s = [], None
    for i, v in enumerate(np.append(e, False)):
        if v and s is None:
            s = i
        if not v and s is not None:
            if (i - s) * 0.05 >= minlen:
                out.append((s * 800, i * 800))
            s = None
    return out


def levels(p):
    a, l = load(p)
    floor = {k: rms(v) for k, v in (("ch0", a[:4000, 0]), ("raw", a[:4000, 1]), ("lap", l[:4000]))}
    print(f"{'ref':>7} {'raw':>7} {'raw-ref':>7} {'ch0':>7} {'ERLE':>6} {'laptop':>7} {'rawpk':>6}")
    for s, e in segments(a[:, 5]):
        s, e = s + 4000, e - 1600  # skip the onset (the AEC's convergence is a separate question)
        ref, raw = rms(a[s:e, 5]), np.mean([rms(a[s:e, c]) for c in range(1, 5)])
        c0, lap = rms(a[s:e, 0]), rms(l[s:e]) if len(l) > e else 0
        pk = np.abs(a[s:e, 1:5]).max()
        print(f"{sig.db(ref):7.1f} {sig.db(raw):7.1f} {sig.db(raw)-sig.db(ref):7.1f} {sig.db(c0):7.1f} "
              f"{sig.db(raw)-sig.db(c0):6.1f} {sig.db(lap):7.1f} {sig.db(pk):6.1f}")
    print("floor:", {k: round(sig.db(v), 1) for k, v in floor.items()})


def xcorr_delay(x, y, maxlag):
    n = 1 << int(np.ceil(np.log2(len(x) + len(y))))
    c = np.fft.irfft(np.fft.rfft(y, n) * np.conj(np.fft.rfft(x, n)), n)
    c = np.concatenate([c[-maxlag:], c[:maxlag]])
    i = int(np.argmax(np.abs(c)))
    return i - maxlag, c


def delay(p):
    a, l = load(p)
    ref = a[:, 5]
    for ch in (1, 2, 3, 4, 0):
        d, c = xcorr_delay(ref, a[:, ch], 4000)
        g = np.abs(c).max() / np.sum(ref ** 2)
        print(f"ch{ch}: delay {d} samples = {d/R*1000:.1f} ms, path gain {sig.db(g):.1f} dB")
    if len(l) > len(ref) // 2:
        m = min(len(l), len(ref))
        d, c = xcorr_delay(ref[:m], l[:m], 8000)
        print(f"laptop mic: {d/R*1000:.1f} ms after the reference")


def timeline(p, step=0.1):
    a, l = load(p)
    cols = {"ref": a[:, 5], "raw": a[:, 1], "ch0": a[:, 0], "lap": l}
    E = {k: env(v, step) for k, v in cols.items()}
    m = min(len(v) for v in E.values())
    print("   t " + " ".join(f"{k:>6}" for k in E))
    for i in range(m):
        print(f"{i*step:4.1f} " + " ".join(f"{E[k][i]:6.1f}" for k in E))




def vad(p, ch=0, *args):
    """Nina's endpointer over one channel (bin/calib-vad): the utterances it would open."""
    import subprocess
    a, l = load(p)
    x = (a[:, int(ch)] if ch != "lap" else l) * 32768
    exe = os.path.join(os.path.dirname(__file__), "../../bin/calib-vad")
    r = subprocess.run([exe, *args], input=x.astype("<i2").tobytes(), capture_output=True)
    print(r.stdout.decode(), end="")


if __name__ == "__main__":
    {"levels": levels, "delay": delay, "timeline": timeline, "vad": vad}[sys.argv[1]](*sys.argv[2:])
