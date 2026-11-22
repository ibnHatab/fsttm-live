// The panel: Nina's preferences, state and conversation, from nina-live's web plugin.
import { toggleSwitch, keyMatch } from './vendor/switch.js';

const $ = id => document.getElementById(id);
const api = async (path, body) => {
  const r = await fetch(path, body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  if (!r.ok) throw new Error(await r.text());
  return r.json();
};
const act = (action, arg = '') => api('/api/do', { action, arg }).catch(e => event_('error: ' + e.message, true));
const setPref = patch => api('/api/prefs', patch).then(showPrefs).catch(e => event_('error: ' + e.message, true));

// ── the voice card: [● mic ⟦switch⟧ ● voice ⟦switch⟧], F9 the mic, Shift+8 the voice ──
const mic = toggleSwitch({ label: 'mic', title: 'the microphone: Nina listens while on (F9)', onChange: on => setPref({ mic: on }) });
const voice = toggleSwitch({ label: 'voice', title: 'her sound: off shows her replies without playing them (Shift+8)', onChange: on => setPref({ voice: on }) });
$('voicebar').append(mic.el, voice.el);
const typing = t => /^(INPUT|TEXTAREA|SELECT)$/.test(t?.tagName ?? '');
document.addEventListener('keydown', e => {
  if (e.repeat || typing(e.target)) return;
  if (e.key === 'F9') { e.preventDefault(); setPref({ mic: !mic.on }); }
  else if (keyMatch('Shift+8', e)) { e.preventDefault(); setPref({ voice: !voice.on }); }
});

$('talk').onclick = () => act('talk');
$('stop').onclick = () => act('stop');
$('sleep').onclick = () => act('sleep');
$('voice_name').onchange = e => setPref({ voice_name: e.target.value });
$('local').onchange = e => setPref({ local: e.target.value });
$('rhvoice').onchange = e => setPref({ rhvoice: e.target.value });
$('paradee').onchange = e => setPref({ paradee: e.target.value });
$('say').onsubmit = e => {
  e.preventDefault();
  const t = $('text').value.trim();
  if (!t) return;
  $('text').value = '';
  const m = t.match(/^\/note\s+(\S+)\s+(.+)$/);
  if (m) act('note', `${m[1]} ${m[2]}`);
  else if (t.startsWith('/say ')) act('say', t.slice(5));
  else act('type', t);
};

let prefs = {};
function options(sel, values, label = v => v || '(the model\'s)') {
  sel.replaceChildren(...values.map(v => Object.assign(document.createElement('option'), { value: v, textContent: label(v) })));
}
function knob(levels) {
  $('narration').replaceChildren(...levels.map(l => {
    const b = Object.assign(document.createElement('button'), { type: 'button', textContent: l });
    b.setAttribute('role', 'radio');
    b.title = l === 'off' ? 'no notes spoken' : l === 'urgent' ? 'only urgent notes (also when asleep)' : `${l} and above`;
    b.onclick = () => setPref({ narration: l });
    return b;
  }));
}
function showPrefs(p) {
  prefs = p;
  mic.set(p.mic ? 'on' : 'off');
  voice.set(p.voice ? 'on' : 'off');
  $('voice_name').value = p.voice_name;
  $('local').value = p.local;
  $('rhvoice').value = p.rhvoice;
  $('paradee').value = p.paradee;
  $('rhbox').hidden = p.local !== 'rhvoice';
  $('pdbox').hidden = p.local !== 'paradee';
  for (const b of $('narration').children) b.setAttribute('aria-checked', String(b.textContent === p.narration));
}
function showStatus(s) {
  $('att').textContent = s.awake ? 'awake' : 'asleep';
  $('att').className = 'chip' + (s.awake ? ' on' : '');
  $('floor').textContent = s.floor + (s.pending ? ` · ${s.pending} note${s.pending > 1 ? 's' : ''}` : '');
  $('floor').className = 'chip' + (/SYSTEM|BOTHs/.test(s.floor) ? ' sys' : /USER|BOTHu/.test(s.floor) ? ' usr' : '');
  $('mind').textContent = s.mind ? 'session open' : 'session closed';
  $('mind').className = 'chip' + (s.mind ? ' on' : '');
  $('talk').textContent = s.replying ? 'Stop her' : s.awake ? 'Talk' : 'Wake';
  $('talk').classList.toggle('live', s.hearing || s.speaking);
  if (s.hearing && mic.on) mic.ping(400);
}

// ── the conversation ──
const log = $('log');
const atBottom = () => log.scrollHeight - log.scrollTop - log.clientHeight < 40;
function add(el) {
  const stick = atBottom();
  log.append(el);
  while (log.children.length > 300) log.firstChild.remove();
  if (stick) log.scrollTop = log.scrollHeight;
  return el;
}
const msg = (cls, text) => add(Object.assign(document.createElement('div'), { className: 'msg ' + cls, textContent: text }));
function event_(text, err = false) {
  add(Object.assign(document.createElement('div'), { className: 'ev' + (err ? ' err' : ''), textContent: text }));
}
let ninaMsg = null, said = '';
const handlers = {
  interim: e => { $('interim').textContent = e.text ? `hearing: ${e.text}` : ''; },
  heard: e => { $('interim').textContent = ''; if (e.tentative && !e.text) return; },
  typed: e => msg('user', e.text),
  turn: e => {
    if (e.note) msg('note', e.text.replace(/^NOTE \((\w+)\): /, 'note ($1): '));
    else if (!e.typed_shown) msg('user', e.text.replace(/^\(You were interrupted[^)]*\)\n/, ''));
    ninaMsg = null; said = '';
  },
  said: e => { said += e.text; if (!ninaMsg) ninaMsg = msg('nina', ''); ninaMsg.textContent = said.trim(); },
  reply_done: e => { if (!ninaMsg && e.said) ninaMsg = msg('nina', e.said); else if (ninaMsg) ninaMsg.textContent = e.said || said.trim(); ninaMsg = null; },
  cut: () => { ninaMsg?.classList.add('cut'); ninaMsg = null; },
  echo: e => event_(`her own voice, ignored: “${e.text}”`),
  aside: e => event_(`not for her (no “Nina”, ${Math.round(e.since_spoke_s)} s after she spoke): “${e.text}”`),
  outlasted: e => event_(`said over her: “${e.kept}”`),
  phantom_name: e => event_(`“${e.text}” from ${e.voiced_s.toFixed(2)} s of sound: not someone calling her, ignored`),
  vocab_echo: e => event_(`the transcriber's word list, ignored: “${e.text}”`),
  asleep_ignored: e => event_(`asleep, ignored: “${e.text}”`),
  attention: e => event_(e.awake ? 'awake' : 'asleep'),
  barge_in: e => event_(`interrupted (${(e.novel || []).join(' ')})`),
  note_filtered: e => event_(`note (${e.level}) not spoken: ${e.text}`),
  note_queued: e => event_(`note (${e.level}) waiting for the floor`),
  tool: e => event_(`${e.name}(${Object.values(e.args || {}).join(', ')}) → ${e.result?.state ?? e.result?.error ?? JSON.stringify(e.result)}`),
  prefs: e => showPrefs(e.prefs),
  mind_error: e => event_(`voice session: ${e.err}`, true),
  no_reply: () => event_('no reply', true),
  plugin_error: e => event_(`${e.name}: ${e.err}`, true),
};
// a typed line shows as typed; its turn would show it twice
let lastTyped = '';
const typedH = handlers.typed, turnH = handlers.turn;
handlers.typed = e => { lastTyped = e.text; typedH(e); };
handlers.turn = e => { if (e.text === lastTyped) { e.typed_shown = true; lastTyped = ''; } turnH(e); };

async function start() {
  const s = await api('/api/state');
  options($('voice_name'), s.choices.voice_name);
  options($('rhvoice'), s.choices.rhvoice, v => v);
  options($('paradee'), s.choices.paradee, v => v);
  knob(s.choices.narration);
  showPrefs(s.prefs);
  showStatus(s.status);
  const es = new EventSource('/api/events');
  es.addEventListener('status', m => showStatus(JSON.parse(m.data)));
  es.addEventListener('ev', m => { const e = JSON.parse(m.data); handlers[e.ev]?.(e); });
  es.onerror = () => { $('mind').textContent = 'no connection'; $('mind').className = 'chip'; };
  es.onopen = () => api('/api/state').then(s => { showPrefs(s.prefs); showStatus(s.status); });
}
start().catch(e => event_('error: ' + e.message, true));
