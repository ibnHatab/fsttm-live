"""Sleep and wake, tested live (fsttm-live scripts/calib, 2026-11-26; the user: "from the laptop, where
you have TTS running in Pascal, … decide this go to sleep, wake up stuff").

A Nina machine (pero) is put to sleep; "the person" is Paradee's voice from Pascal's own speaker (not
in that machine's echo reference); her panel's event stream says what happened and when:

  name → kws (the spotter's hit) → bare_name (woken at the end) → turn → first_audio (her voice) → reply
  "Go to sleep." → her goodbye → asleep

Per phrase: the time from the end of the phrase (paplay done, minus the clip's trailing silence) to
her first audio, the words she answered, and whether she ended awake or asleep as she should.

  wake_cycle.py [PANEL http://pero.local:8770] [CYCLES 3] [VOICE am_puck]
"""
import json, os, subprocess, sys, threading, time, urllib.request
import numpy as np
sys.path.insert(0, os.path.dirname(__file__))
import sig

PANEL = sys.argv[1] if len(sys.argv) > 1 else "http://pero.local:8770"
CYCLES = int(sys.argv[2]) if len(sys.argv) > 2 else 3
VOICE = sys.argv[3] if len(sys.argv) > 3 else "am_puck"
LAPTOP = "alsa_output.pci-0000_00_1f.3-platform-skl_hda_dsp_generic.HiFi__hw_sofhdadsp__sink"
D = os.path.join(os.path.dirname(os.path.abspath(__file__)), "../../runs/calib/wake")
KINDS = {"kws", "bare_name", "turn", "first_audio", "reply_done", "attention", "heard", "asleep_local",
         "voice_connected", "voice_warm_failed", "voice_resume_failed", "mind_closed_asleep", "phantom_name",
         "asleep_ignored", "oww_peak"}

events, lock = [], threading.Lock()


def stream():
    while True:
        try:
            r = urllib.request.urlopen(PANEL + "/api/events", timeout=600)
            kind = None
            for raw in r:
                line = raw.decode().rstrip("\n")
                if line.startswith("event:"):
                    kind = line[6:].strip()
                elif line.startswith("data:") and kind == "ev":
                    e = json.loads(line[5:])
                    if e.get("ev") in KINDS:
                        with lock:
                            events.append((time.time(), e))
        except Exception:
            time.sleep(1)


def do(action, arg=None):
    body = {"action": action}
    if arg is not None:
        body["arg"] = arg
    req = urllib.request.Request(PANEL + "/api/do", data=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=10).read()


def state():
    return json.load(urllib.request.urlopen(PANEL + "/api/state", timeout=10))["status"]


def clip(text):
    os.makedirs(D, exist_ok=True)
    f = os.path.join(D, f"{VOICE}-{text.replace(' ', '_').replace(',', '').replace('.', '').replace('?', '')}.wav")
    if not os.path.exists(f):
        x = sig.paradee(text, VOICE)
        sig.write(f, sig.norm(x, -18))
    x = sig.read(f)
    voiced = np.flatnonzero(np.abs(x) > 10 ** (-40 / 20) * np.abs(x).max())
    tail = (len(x) - voiced[-1]) / 16000 if len(voiced) else 0
    return f, tail


def say(text):
    f, tail = clip(text)
    subprocess.run(["paplay", "-d", LAPTOP, f])
    return time.time() - tail  # the phrase's end


def idle(limit=30):
    end = time.time() + limit
    while time.time() < end:
        st = state()
        if not st.get("replying") and not st.get("speaking") and not st.get("pending"):
            return st
        time.sleep(0.2)
    return state()


def phrase(text, want_awake, wait=14):
    idle()
    time.sleep(1.5)
    with lock:
        n0 = len(events)
    t_end = say(text)
    deadline = time.time() + wait
    while time.time() < deadline:
        with lock:
            got = [e for _, e in events[n0:]]
        if any(e["ev"] == "reply_done" for e in got) or (not want_awake and any(e["ev"] in ("asleep_local", "asleep_ignored") for e in got)):
            break
        time.sleep(0.1)
    time.sleep(0.5)
    with lock:
        got = events[n0:]
    first = lambda k: next(((t, e) for t, e in got if e["ev"] == k), (None, None))
    tf, _ = first("first_audio")
    _, rd = first("reply_done")
    _, kws = first("kws")
    peaks = [round(e.get("peak", 0), 2) for _, e in got if e["ev"] == "oww_peak"]
    st = idle()
    r = {"said": text, "kws": bool(kws), "peak": max(peaks) if peaks else None, "bare": bool(first("bare_name")[1]),
         "to_voice_s": round(tf - t_end, 2) if tf else None, "reply": (rd or {}).get("said"), "awake": st.get("awake"),
         "ok": st.get("awake") == want_awake and (rd is not None or not want_awake),
         "connects": [(e.get("ms"), e.get("resumed"), e.get("warm")) for _, e in got if e["ev"] == "voice_connected"]}
    print(json.dumps(r), flush=True)
    return r


if __name__ == "__main__":
    threading.Thread(target=stream, daemon=True).start()
    time.sleep(1.5)
    do("sleep")
    idle()
    res = []
    for i in range(CYCLES):
        res.append(phrase("Nina.", True))
        res.append(phrase("Go to sleep.", False))
    res.append(phrase("Hey Nina.", True))
    res.append(phrase("Go to sleep.", False))
    res.append(phrase("Nina, what time is it?", True))
    res.append(phrase("Thank you, go to sleep.", False))
    ok = sum(r["ok"] for r in res)
    lat = [r["to_voice_s"] for r in res if r["to_voice_s"] is not None and r["said"].startswith(("Nina", "Hey"))]
    print(f"{ok}/{len(res)} as expected; name → her voice: {lat}")
