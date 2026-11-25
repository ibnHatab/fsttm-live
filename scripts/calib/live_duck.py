"""The ducking, tested live on the running Nina (fsttm-live scripts/calib, 2026-11-25).

Each trial: she says a long line (the panel's "say"), the person (a voice from the laptop's
speaker, not in the echo reference) speaks over her; the speakers' feed is recorded from
PulseAudio (the array sink's monitor: her voice and the radio), the radio alone from its own
stream, and her dialog's events read from her log. Measured:

  her level      dBFS RMS of her speech on the feed before the duck (the calibrated target:
                 audio.level + Gain(radius))
  duck           dB her voice drops from barge_duck to the cut or the unduck (target: -12)
  react          ms from the person's first sound on the laptop to barge_duck
  music          dB of the radio: alone, while she speaks, while the person speaks
  music-her      dB between the radio and her voice, normal and ducked

  live_duck.py OUT.json TRIALS [music]
"""
import json, os, sqlite3, subprocess, sys, threading, time, urllib.request
import numpy as np
sys.path.insert(0, os.path.dirname(__file__))
import sig

PANEL = "http://127.0.0.1:8770"
FEED = "alsa_output.usb-SEEED_ReSpeaker_4_Mic_Array__UAC1.0_-00.analog-stereo.monitor"
LAPTOP = "alsa_output.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp__sink"
DB = os.path.expanduser("~/.local/share/nina/nina.db")
SIG = os.path.join(os.path.dirname(os.path.abspath(__file__)), "../../runs/calib/sig")
LINE = ("Here is a longer thought about the evening. The moon rises a little after six, "
        "almost full, low in the east over the rooftops, and the clouds should thin out "
        "by nine, so the light on the balcony will be soft and silver for an hour or two.")
PHRASES = {"backchannel": "Mm-hm, yes.", "interrupt": "Sorry, wait, what was that about the clouds?"}
R = 16000


def api(path, body=None):
    req = urllib.request.Request(PANEL + path, data=json.dumps(body).encode() if body else None,
                                 headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=10))


def rec(cmd, buf, stop):
    """Timestamped 100 ms chunks; cmd is a list, or a function giving one (None: not now), run
    again when it ends (MPD makes a new stream each time it plays)."""
    while not stop.is_set():
        c = cmd() if callable(cmd) else cmd
        if c is None:
            time.sleep(0.2); continue
        p = subprocess.Popen(c, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        while not stop.is_set():
            b = p.stdout.read(3200)
            if len(b) < 3200:
                break
            buf.append((time.time() - 0.05, b))  # the chunk's middle
        p.terminate()
        if not callable(cmd):
            return


def series(buf):
    """(times, dBFS) of 100 ms chunks: each by its own stamp, so two recordings line up."""
    t = np.array([a for a, _ in buf])
    db = np.array([sig.db(np.sqrt(np.mean((np.frombuffer(b, "<i2").astype(float) / 32768) ** 2))) for _, b in buf])
    return t, db, None


def mpd_input():
    out = subprocess.run(["pactl", "list", "sink-inputs"], capture_output=True, text=True).stdout
    cur = None
    for line in out.splitlines():
        if line.startswith("Sink Input #"):
            cur = line.split("#")[1].strip()
        if 'application.name = "Music Player Daemon"' in line:
            return cur
    return None


def events(t0, t1):
    c = sqlite3.connect(DB)
    rows = c.execute("select at, kind, data from events where stream='dialog' and at between ? and ? order by seq",
                     (t0, t1)).fetchall()
    return [(a, k, json.loads(d)) for a, k, d in rows]


def level(t, db, a, b, floor=-60):
    """Energy-mean dBFS of the frames in [a, b] above floor (speech, not its gaps)."""
    m = (t >= a) & (t <= b) & (db > floor)
    if m.sum() < 3:
        return None
    return round(float(10 * np.log10(np.mean(10 ** (db[m] / 10)))), 1)


def idle(quiet=2.0, limit=60):
    """Until she has the floor free, no reply, no voice, for quiet seconds."""
    t_ok, end = None, time.time() + limit
    while time.time() < end:
        st = api("/api/state")["status"]
        free = st.get("floor", "").startswith("FREE") and not st.get("replying") and not st.get("speaking") and not st.get("pending")
        if free:
            t_ok = t_ok or time.time()
            if time.time() - t_ok >= quiet:
                return True
        else:
            t_ok = None
        time.sleep(0.1)
    return False


def trial(kind, music):
    if not idle():
        return {"kind": kind, "error": "never idle"}
    api("/api/do", {"action": "wake"})
    time.sleep(0.3)
    t0 = time.time()
    api("/api/do", {"action": "say", "arg": LINE})
    # her voice starts: wait for it on her status
    deadline = time.time() + 20
    while not api("/api/state")["status"].get("speaking"):
        if time.time() > deadline:
            return {"kind": kind, "error": "she never spoke"}
        time.sleep(0.05)
    t_spoke = time.time()
    time.sleep(3.0)  # her level settles, the music ducks under her
    t_person = time.time()
    subprocess.run(["paplay", "-d", LAPTOP, f"{SIG}/person-{kind}.wav"])
    deadline = time.time() + 30
    while api("/api/state")["status"].get("speaking") and time.time() < deadline:
        time.sleep(0.1)
    time.sleep(2.5)
    idle(1.0)
    return {"kind": kind, "t0": t0, "t_spoke": t_spoke, "t_person": t_person, "t_end": time.time()}


def analyse(tr, feed, mus):
    if "error" in tr:
        return tr
    ev = events(tr["t0"] - 1, tr["t_end"] + 1)
    first = lambda k, after=0: next((a for a, kk, d in ev if kk == k and a >= after), None)
    duck = first("barge_duck")
    end = min([x for x in (first("cut", duck or 0), first("barge_unduck", duck or 0)) if x] or [None], key=lambda x: x or 9e18)
    how = next((kk for a, kk, d in ev if kk in ("cut", "barge_unduck") and duck and a >= duck), None)
    ft, fdb, _ = feed
    out = {"kind": tr["kind"], "duck_event": bool(duck), "ended_by": how}
    # the person's sound reaches the laptop's speaker ~0.1 s after paplay starts
    if duck:
        out["react_ms"] = round((duck - tr["t_person"]) * 1000)
    if mus is not None:
        mt, mdb0, _ = mus
        # the radio at the feed's times (no radio recorded there: silent); her voice is the feed
        # minus the radio (energies)
        mdb = np.interp(ft, mt, mdb0, left=-120, right=-120)
        gap = np.min(np.abs(mt[:, None] - ft[None, :]), axis=0) if len(mt) else np.full(len(ft), 9)
        mdb[gap > 0.15] = -120
        her = 10 * np.log10(np.maximum(10 ** (fdb / 10) - 10 ** (mdb / 10), 1e-12))
        out["music_alone"] = level(ft, mdb, tr["t0"] - 1.5, tr["t0"], -90)
        out["music_under_her"] = level(ft, mdb, tr["t_spoke"] + 1.5, tr["t_person"], -90)
        if duck and end:
            out["music_in_duck"] = level(ft, mdb, duck + 0.1, end - 0.05, -90)
        fdb = her
    pre_a, pre_b = (duck or tr["t_person"]) - 2.0, (duck or tr["t_person"]) - 0.1
    out["her_level"] = level(ft, fdb, pre_a, pre_b)
    if duck and end and end - duck > 0.25:
        out["her_ducked"] = level(ft, fdb, duck + 0.12, end - 0.05)
        if out["her_level"] is not None and out["her_ducked"] is not None:
            out["duck_db"] = round(out["her_ducked"] - out["her_level"], 1)
        out["duck_s"] = round(end - duck, 2)
    if how == "barge_unduck":
        out["her_after"] = level(ft, fdb, end + 0.15, end + 1.5)
    if mus is not None and out.get("her_level") is not None and out.get("music_under_her") is not None:
        out["music_vs_her"] = round(out["music_under_her"] - out["her_level"], 1)
        if out.get("her_ducked") is not None and out.get("music_in_duck") is not None:
            out["music_vs_her_ducked"] = round(out["music_in_duck"] - out["her_ducked"], 1)
    return out


if __name__ == "__main__":
    out, n, music = sys.argv[1], int(sys.argv[2]), len(sys.argv) > 3 and sys.argv[3] == "music"
    for k, text in PHRASES.items():
        f = f"{SIG}/person-{k}.wav"
        if not os.path.exists(f):
            sig.write(f, sig.norm(sig.paradee(text, "am_puck"), -18))
    st = api("/api/state")
    assert st["prefs"]["mic"], "her mic is off"
    api("/api/do", {"action": "wake"})
    if music:  # the radio as in use: she puts it on (her music host sets its level from her voice's)
        idle()
        api("/api/do", {"action": "type", "arg": "Nina, play something calm."})
        time.sleep(3); idle(3.0)
    stop = threading.Event()
    fb, mb = [], []
    th = [threading.Thread(target=rec, args=(["parec", "-d", FEED, "--rate=16000", "--channels=1", "--format=s16le",
                                              "--latency-msec=20"], fb, stop), daemon=True)]
    if music:
        mcmd = lambda: (lambda i: i and ["parec", f"--monitor-stream={i}", "--rate=16000", "--channels=1",
                                         "--format=s16le", "--latency-msec=20"])(mpd_input())
        th.append(threading.Thread(target=rec, args=(mcmd, mb, stop), daemon=True))
    for x in th:
        x.start()
    time.sleep(1)
    trials = []
    try:
        for i in range(n):
            for k in PHRASES:
                trials.append(trial(k, music))
    finally:
        time.sleep(0.5)
        stop.set()
        for x in th:
            x.join(2)
        if music:
            subprocess.run(["mpc", "-q", "stop"])
    feed = series(fb)
    mus = series(mb) if mb else None
    res = [analyse(t, feed, mus) for t in trials]
    for r in res:
        print(json.dumps(r))
    json.dump({"music": music, "trials": res}, open(out, "w"), indent=1)
