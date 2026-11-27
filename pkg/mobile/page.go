package mobile

const pageHTML = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Nina proof</title>
<style>
body{font:16px/1.4 sans-serif;margin:0;padding:16px;background:#12161d;color:#e3e8f0}
h1{font-size:20px;margin:0 0 12px}
.meter{height:22px;background:#2c3442;border-radius:4px;overflow:hidden;margin:6px 0 4px}
.bar{height:100%;width:0;background:#6cc28f;transition:width .1s}
button{font-size:16px;padding:10px 14px;margin:6px 6px 0 0;border:0;border-radius:6px;background:#2f5fd0;color:#fff}
pre{white-space:pre-wrap;background:#1a2029;padding:10px;border-radius:6px;font-size:13px;min-height:3em}
.muted{color:#9aa4b5;font-size:13px}
</style></head><body>
<h1>Nina on Android: proof</h1>
<div>Mic <span id="st" class="muted"></span></div>
<div class="meter"><div class="bar" id="bar"></div></div>
<div class="muted" id="mem"></div>
<div><button onclick="echo()">Echo 3 s</button><span id="checks"></span></div>
<pre id="out">Speak: the bar moves. Echo records 3 seconds and plays them back.</pre>
<script>
const $=id=>document.getElementById(id);
async function poll(){try{const l=await (await fetch('/api/level')).json();
 $('bar').style.width=Math.min(100,l.rms*400)+'%';
 $('st').textContent=l.live?'listening':'off (app in background or no permission)';
 $('mem').textContent='Go heap '+l.heap_mb.toFixed(1)+' MB, from the OS '+l.sys_mb.toFixed(1)+' MB, '+l.goroutines+' goroutines';}catch(e){}
 setTimeout(poll,150)}
async function echo(){$('out').textContent='Recording 3 s...';
 const r=await fetch('/api/echo');$('out').textContent=r.ok?'Played back '+(await r.json()).played_s+' s':await r.text()}
async function check(n){$('out').textContent='Running '+n+'...';
 const r=await fetch('/api/check?name='+n);$('out').textContent=r.ok?(await r.json()).result:await r.text()}
fetch('/api/checks').then(r=>r.json()).then(ns=>{$('checks').innerHTML=ns.map(n=>'<button onclick="check(\''+n+'\')">'+n+'</button>').join('')});
poll();
</script></body></html>`
