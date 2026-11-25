"""The software echo canceller's calibration: a separate microphone (the UGREEN, KT USB Audio) and
separate speakers, PulseAudio's WebRTC canceller between them (module-echo-cancel) instead of
the array's own (fsttm-live scripts/calib, 2026-11-25).

Nina plays to the canceller's sink (its reference), the person from the laptop's speaker; the
raw microphone and the canceller's output are recorded together, then scored as sweep.py scores
the array: erle, echo99, false (Nina's endpointer on the output), keep, near50.

  softaec.py OUT.json NAME='{"aec": "analog_gain_control=0 digital_gain_control=0", "gain": -18, "mic": 88}' ...
"""
import json, os, subprocess, sys, threading, time
import numpy as np
sys.path.insert(0, os.path.dirname(__file__))
import sig, ana, sweep

D = os.path.dirname(os.path.abspath(__file__))
SIG = os.path.join(D, "../../runs/calib/sig")
MIC = "alsa_input.usb-KTMicro_KT_USB_Audio_2021-06-07-0000-0000-0000--00.mono-fallback"
MIC_CARD = "Audio"  # ALSA card name of the KT USB Audio (its Mic Capture Volume)
SPK = "alsa_output.usb-SEEED_ReSpeaker_4_Mic_Array__UAC1.0_-00.analog-stereo"  # the speakers (the array only as a sound card)
LAPTOP = "alsa_output.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp__sink"
R = 16000


def sh(*a):
    return subprocess.run(a, capture_output=True, text=True).stdout


def ec_up(aec):
    ec_down()
    sh("pactl", "load-module", "module-echo-cancel", f"source_master={MIC}", f"sink_master={SPK}",
       "source_name=calib_ec_source", "sink_name=calib_ec_sink", "aec_method=webrtc", "rate=16000", "channels=1",
       f'aec_args="{aec}"', "source_properties=device.description=calib_ec", "sink_properties=device.description=calib_ec")
    time.sleep(1)


def ec_down():
    for line in sh("pactl", "list", "modules", "short").splitlines():
        if "calib_ec_source" in line:
            sh("pactl", "unload-module", line.split()[0])


def reader(p, buf):
    while True:
        b = p.stdout.read(4096)
        if not b:
            return
        buf.append(b)


def take(out, secs, plays):
    """Record the raw microphone and the canceller's output while plays run: (sink, start, file, gain)."""
    recs = [subprocess.Popen(["parec", "-d", s, "--rate=16000", "--channels=1", "--format=s16le", "--latency-msec=20"],
                             stdout=subprocess.PIPE) for s in (MIC, "calib_ec_source")]
    bufs = [[], []]
    for p, b in zip(recs, bufs):
        threading.Thread(target=reader, args=(p, b), daemon=True).start()
    t0, ps = time.time(), []
    for sink, start, path, gain in sorted(plays, key=lambda e: e[1]):
        time.sleep(max(0, t0 + start - time.time()))
        f = path
        if gain:
            f = f"/tmp/calib-soft-{len(ps)}.wav"
            sig.write(f, sig.read(path) * 10 ** (gain / 20))
        ps.append(subprocess.Popen(["paplay", "-d", sink, f]))
    time.sleep(max(0, t0 + secs - time.time()))
    for p in ps:
        p.wait()
    for p in recs:
        p.terminate(); p.wait()
    raw, ec = [np.frombuffer(b"".join(b)[: len(b"".join(b)) // 2 * 2], "<i2").astype(float) / 32768 for b in bufs]
    np.savez_compressed(out, raw=raw, ec=ec)
    return raw, ec


def score(tag, out, gain):
    nina, hum = sig.read(f"{SIG}/nina.wav"), sig.read(f"{SIG}/human.wav")
    meta = json.load(open(f"{SIG}/signals.json"))
    take(f"/tmp/calib-soft-warm.npz", 6, [("calib_ec_sink", 0.3, f"{SIG}/nina.wav", gain)])
    er, ee = take(f"{out}/{tag}-echo.npz", 21, [("calib_ec_sink", 0.5, f"{SIG}/nina.wav", gain)])
    nr, ne = take(f"{out}/{tag}-near.npz", 13, [(LAPTOP, 0.5, f"{SIG}/human.wav", 0)])
    br, be = take(f"{out}/{tag}-dt.npz", 21, [("calib_ec_sink", 0.5, f"{SIG}/nina.wav", gain), (LAPTOP, 4.0, f"{SIG}/human.wav", 0)])
    o = lambda x, s: sweep.offset(x, s)
    oe, ob, on = o(er, nina), o(br, nina), o(nr, hum)
    # the canceller's output lags the raw microphone by its own latency: found on the near-end take
    lag = sweep.offset(ne, nr[on:on + len(hum)]) - on if len(ne) > on else 0
    spans, t = [], 0
    for L in meta["human_s"]:
        spans.append((int(t * R), int((t + L) * R))); t += L + 1.2
    sh_ = lambda sp, d: [(a + d, b + d) for a, b in sp]
    her = [(oe, oe + len(nina))]
    hb = sh_(spans, ob + int(3.5 * R))
    he = sh_(hb, oe - ob)
    E = sweep.energy
    erle = 10 * np.log10(E(er, her) / max(E(ee, sh_(her, lag)), 1e-12))
    seg = ee[oe + lag:oe + lag + len(nina)]
    fr = ana.env(seg, 0.05)
    rr = ana.env(er[oe:oe + len(nina)], 0.05)[:len(fr)]
    fr = fr[:len(rr)]
    echo99 = float(np.percentile(fr[rr > -55], 99)) if np.any(rr > -55) else float("nan")
    keep = 10 * np.log10(max(E(be, sh_(hb, lag)) - E(ee, sh_(he, lag)), 1e-12) / E(ne, sh_(spans, on + lag)))
    nf, nrw = ana.env(ne, 0.05), ana.env(nr, 0.05)
    m = min(len(nf), len(nrw))
    near50 = float(np.median(nf[:m][nrw[:m] > -55])) if np.any(nrw[:m] > -55) else float("nan")
    pk = float(sig.db(np.abs(er).max()))
    return {"erle": round(float(erle), 1), "echo99": round(echo99, 1), "false": sweep.utts(seg), "keep": round(float(keep), 1),
            "near50": round(near50, 1), "raw_echo": round(sig.db(ana.rms(er[oe:oe + len(nina)])), 1), "raw_peak": round(pk, 1),
            "lag_ms": round(lag / 16, 1)}


if __name__ == "__main__":
    out = sys.argv[1]
    os.makedirs(os.path.dirname(os.path.abspath(out)), exist_ok=True)
    res = json.load(open(out)) if os.path.exists(out) else {}
    try:
        for a in sys.argv[2:]:
            name, conf = a.split("=", 1)
            conf = json.loads(conf)
            if "mic" in conf:
                sh("amixer", "-c", MIC_CARD, "sset", "Mic", f"{conf['mic']}%")
            ec_up(conf.get("aec", "analog_gain_control=0 digital_gain_control=0"))
            s = score(name, os.path.dirname(os.path.abspath(out)), conf.get("gain", -18))
            res[name] = {"conf": conf, **s}
            print(name, s, flush=True)
            json.dump(res, open(out, "w"), indent=1)
    finally:
        ec_down()
