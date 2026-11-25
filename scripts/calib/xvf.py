"""The ReSpeaker's XVF-3000 parameters over USB (Seeed's usb_4_mic_array tuning.py protocol).
Needs access to the USB device (root, or the udev rule in scripts/calib/README).

  xvf.py                 all parameters
  xvf.py NAME [VALUE]    read, or write then read back
  xvf.py -f FILE.json    write a set
"""
import json, struct, sys
import usb.core, usb.util

P = {  # name: (id, offset, int?)
    'AECFREEZEONOFF': (18, 7, 1), 'AECNORM': (18, 19, 0), 'AECPATHCHANGE': (18, 25, 1), 'RT60': (18, 26, 0),
    'HPFONOFF': (18, 27, 1), 'RT60ONOFF': (18, 28, 1), 'AECSILENCELEVEL': (18, 30, 0), 'AECSILENCEMODE': (18, 31, 1),
    'AGCONOFF': (19, 0, 1), 'AGCMAXGAIN': (19, 1, 0), 'AGCDESIREDLEVEL': (19, 2, 0), 'AGCGAIN': (19, 3, 0),
    'AGCTIME': (19, 4, 0), 'CNIONOFF': (19, 5, 1), 'FREEZEONOFF': (19, 6, 1), 'STATNOISEONOFF': (19, 8, 1),
    'GAMMA_NS': (19, 9, 0), 'MIN_NS': (19, 10, 0), 'NONSTATNOISEONOFF': (19, 11, 1), 'GAMMA_NN': (19, 12, 0),
    'MIN_NN': (19, 13, 0), 'ECHOONOFF': (19, 14, 1), 'GAMMA_E': (19, 15, 0), 'GAMMA_ETAIL': (19, 16, 0),
    'GAMMA_ENL': (19, 17, 0), 'NLATTENONOFF': (19, 18, 1), 'NLAEC_MODE': (19, 20, 1), 'SPEECHDETECTED': (19, 22, 1),
    'FSBUPDATED': (19, 23, 1), 'FSBPATHCHANGE': (19, 24, 1), 'TRANSIENTONOFF': (19, 29, 1), 'VOICEACTIVITY': (19, 32, 1),
    'STATNOISEONOFF_SR': (19, 33, 1), 'NONSTATNOISEONOFF_SR': (19, 34, 1), 'GAMMA_NS_SR': (19, 35, 0),
    'GAMMA_NN_SR': (19, 36, 0), 'MIN_NS_SR': (19, 37, 0), 'MIN_NN_SR': (19, 38, 0), 'GAMMAVAD_SR': (19, 39, 0),
    'DOAANGLE': (21, 0, 1),
}


def dev():
    d = usb.core.find(idVendor=0x2886, idProduct=0x0018)
    if d is None:
        raise SystemExit("no ReSpeaker")
    return d


def read(d, n):
    i, off, isint = P[n]
    cmd = 0x80 | off | (0x40 if isint else 0)
    a, b = struct.unpack("ii", d.ctrl_transfer(0xC0, 0, cmd, i, 8, 100000))
    return a if isint else a * 2.0 ** b


def write(d, n, v):
    i, off, isint = P[n]
    pl = struct.pack("iii", off, int(v), 1) if isint else struct.pack("ifi", off, float(v), 0)
    d.ctrl_transfer(0x40, 0, 0, i, pl, 100000)


if __name__ == "__main__":
    d, a = dev(), sys.argv[1:]
    if not a:
        print(json.dumps({n: read(d, n) for n in P}, indent=1))
    elif a[0] == "-f":
        conf = json.load(sys.stdin if a[1] == "-" else open(a[1]))
        for n, v in conf.items():
            write(d, n, v)
        print(json.dumps({n: read(d, n) for n in conf}))
    elif len(a) == 1:
        print(read(d, a[0]))
    else:
        write(d, a[0], a[1]); print(read(d, a[0]))
