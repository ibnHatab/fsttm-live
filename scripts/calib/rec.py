"""One calibration take: the ReSpeaker's 6 channels and the laptop's microphone recorded while
signals play from the array's jack (the speakers: what Nina says, the echo canceller's
reference) and from the laptop's speaker (the person: not in the reference).

  python3 rec.py OUT.npz SECONDS [array|laptop:START:FILE[:GAIN_DB]] ...

Channels of the array (6-channel firmware): 0 processed (AEC, beamformer, NS, AGC), 1-4 the raw
microphones, 5 the playback reference.
"""
import subprocess, sys, threading, time, os
import numpy as np
sys.path.insert(0, os.path.dirname(__file__))
import sig

ARRAY_IN = "hw:ArrayUAC10"
ARRAY_OUT = "plughw:ArrayUAC10"
ARRAY_SINK = "alsa_output.usb-SEEED_ReSpeaker_4_Mic_Array__UAC1.0_-00.analog-stereo"  # PulseAudio holds the jack (her voice and the music mixed): its volume at 100%
LAPTOP_SRC = "alsa_input.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp_6__source"
LAPTOP_SINK = "alsa_output.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp__sink"


def reader(p, buf):
    while True:
        b = p.stdout.read(4096)
        if not b:
            return
        buf.append(b)


def take(out, secs, events):
    a = subprocess.Popen(["arecord", "-q", "-D", ARRAY_IN, "-c", "6", "-r", "16000", "-f", "S16_LE", "-t", "raw"],
                         stdout=subprocess.PIPE)
    l = subprocess.Popen(["parec", "-d", LAPTOP_SRC, "--rate=16000", "--channels=1", "--format=s16le",
                          "--latency-msec=20"], stdout=subprocess.PIPE)
    ab, lb = [], []
    for p, b in ((a, ab), (l, lb)):
        threading.Thread(target=reader, args=(p, b), daemon=True).start()
    t0 = time.time()
    plays = []
    for ev in sorted(events, key=lambda e: e[1]):
        where, start, path, gain = ev
        time.sleep(max(0, t0 + start - time.time()))
        f = path
        if gain:
            f = f"/tmp/calib-{os.getpid()}-{len(plays)}.wav"
            sig.write(f, sig.read(path) * 10 ** (gain / 20))
        cmd = (["paplay", "-d", ARRAY_SINK, f] if where == "array"
               else ["paplay", "-d", LAPTOP_SINK, f])
        plays.append(subprocess.Popen(cmd))
    time.sleep(max(0, t0 + secs - time.time()))
    for p in plays:
        p.wait()
    a.terminate(); l.terminate(); a.wait(); l.wait()
    time.sleep(0.1)
    x = np.frombuffer(b"".join(ab)[: len(b"".join(ab)) // 12 * 12], "<i2").reshape(-1, 6)
    y = np.frombuffer(b"".join(lb)[: len(b"".join(lb)) // 2 * 2], "<i2")
    np.savez_compressed(out, array=x, laptop=y, events=np.array([str(e) for e in events]))
    return x, y


def parse(s):
    p = s.split(":")
    return (p[0], float(p[1]), p[2], float(p[3]) if len(p) > 3 else 0.0)


if __name__ == "__main__":
    x, y = take(sys.argv[1], float(sys.argv[2]), [parse(s) for s in sys.argv[3:]])
    r = lambda v: sig.db(np.sqrt(np.mean((v.astype(float) / 32768) ** 2)))
    print(f"{len(x)/16000:.1f}s  ch0 {r(x[:,0]):.1f}  raw {r(x[:,1]):.1f}  ref {r(x[:,5]):.1f}  laptop {r(y):.1f} dBFS"
          f"  peak raw {np.abs(x[:,1:5]).max()}  ref {np.abs(x[:,5]).max()}")
