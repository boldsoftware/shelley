package server

import "net/http"

func (s *Server) handleDebugConversationStreamPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Conversation stream debug</title>
<style>
:root { color-scheme: light dark; }
body { font-family: system-ui, sans-serif; margin: 24px; }
header { display: flex; gap: 16px; align-items: baseline; flex-wrap: wrap; }
.badge { border: 1px solid #8886; border-radius: 999px; padding: 2px 10px; font-family: ui-monospace, monospace; font-size: 12px; }
.badge.error { color: #c00; }
main { display: grid; grid-template-columns: minmax(0, 1fr) minmax(420px, 0.55fr); gap: 20px; align-items: start; }
pre { margin: 0; padding: 16px; border: 1px solid #8884; border-radius: 8px; overflow: auto; max-height: 78vh; background: #8881; }
table { width: 100%; border-collapse: collapse; font-size: 12px; font-family: ui-monospace, monospace; }
th, td { border-bottom: 1px solid #8884; padding: 6px 8px; text-align: left; vertical-align: top; }
td code { word-break: break-all; }
details > summary { cursor: pointer; }
@media (max-width: 900px) { main { grid-template-columns: 1fr; } }
</style>
</head>
<body>
<header>
<h1>Conversation stream debug</h1>
<span class="badge" id="status">connecting</span>
<span class="badge">hash <code id="hash">none</code></span>
<span class="badge">items <code id="items">0</code></span>
</header>
<main>
<section>
<h2>Current JSON</h2>
<pre id="json">[]</pre>
</section>
<section>
<h2>Patch history</h2>
<table>
<thead><tr><th>Old → new</th><th>Ops</th><th>At</th></tr></thead>
<tbody id="history"></tbody>
</table>
</section>
</main>
<script>
let state = [];
let hash = new URLSearchParams(location.search).get('old_hash') || '';
const statusEl = document.getElementById('status');
const hashEl = document.getElementById('hash');
const itemsEl = document.getElementById('items');
const jsonEl = document.getElementById('json');
const historyEl = document.getElementById('history');

function unescapePointerToken(t) { return t.replace(/~1/g, '/').replace(/~0/g, '~'); }
function parsePointer(p) {
  if (p === '') return [];
  if (p[0] !== '/') throw new Error('bad pointer ' + p);
  return p.slice(1).split('/').map(unescapePointerToken);
}
function walk(doc, parts) {
  for (const p of parts) {
    if (Array.isArray(doc)) doc = doc[+p];
    else if (doc && typeof doc === 'object') doc = doc[p];
    else throw new Error('cannot traverse');
  }
  return doc;
}
function setAt(doc, path, value, mustExist) {
  const parts = parsePointer(path);
  if (parts.length === 0) return value;
  const last = parts[parts.length - 1];
  const parent = walk(doc, parts.slice(0, -1));
  if (Array.isArray(parent)) {
    const idx = +last;
    if (mustExist) parent[idx] = value;
    else parent.splice(idx, 0, value);
  } else { parent[last] = value; }
  return doc;
}
function removeAt(doc, path) {
  const parts = parsePointer(path);
  const last = parts[parts.length - 1];
  const parent = walk(doc, parts.slice(0, -1));
  if (Array.isArray(parent)) parent.splice(+last, 1);
  else delete parent[last];
  return doc;
}
function applyOp(doc, op) {
  switch (op.op) {
    case 'replace': return op.path === '' ? op.value : setAt(doc, op.path, op.value, true);
    case 'add': return setAt(doc, op.path, op.value, false);
    case 'remove': return removeAt(doc, op.path);
    case 'move': {
      const v = walk(doc, parsePointer(op.from));
      doc = removeAt(doc, op.from);
      return setAt(doc, op.path, v, false);
    }
    default: throw new Error('unsupported op ' + op.op);
  }
}
function applyPatch(doc, patch) {
  for (const op of patch) doc = applyOp(doc, op);
  return doc;
}
function short(s) { return s ? s.slice(0, 10) : 'null'; }
function summarizeOps(ops) {
  return ops.map(op => op.op + ' ' + (op.path || '/') + (op.from ? ' ← ' + op.from : '')).join(', ');
}
function addHistory(ev, prepend = true) {
  const tr = document.createElement('tr');
  const reset = ev.reset ? ' (reset)' : '';
  const summary = summarizeOps(ev.patch);
  tr.innerHTML = '<td><code>' + short(ev.old_hash) + '</code> → <code>' + short(ev.new_hash) + '</code>' + reset + '</td>'
    + '<td><details><summary>' + ev.patch.length + ' op' + (ev.patch.length === 1 ? '' : 's') + '</summary><div>' + summary + '</div></details></td>'
    + '<td>' + new Date(ev.at).toLocaleTimeString() + '</td>';
  if (prepend) historyEl.prepend(tr); else historyEl.append(tr);
}
function onPatch(ev) {
  state = applyPatch(state, ev.patch);
  hash = ev.new_hash;
  hashEl.textContent = short(hash);
  itemsEl.textContent = Array.isArray(state) ? state.length : '?';
  jsonEl.textContent = JSON.stringify(state, null, 2);
  addHistory(ev);
}
async function loadInitialHistory() {
  const res = await fetch('/debug/conversation-stream/history');
  const events = await res.json();
  historyEl.textContent = '';
  for (const ev of events) addHistory(ev, false);
}
function connect() {
  statusEl.textContent = 'connecting';
  statusEl.className = 'badge';
  const url = '/api/stream' + (hash ? '?conversation_list_hash=' + encodeURIComponent(hash) : '');
  const es = new EventSource(url);
  es.addEventListener('open', () => { statusEl.textContent = 'connected'; });
  es.onmessage = e => {
    try {
      const data = JSON.parse(e.data);
      if (data.conversation_list_patch) onPatch(data.conversation_list_patch);
    } catch (err) { console.error(err); }
  };
  es.addEventListener('error', () => { statusEl.textContent = 'disconnected'; statusEl.className = 'badge error'; });
}
loadInitialHistory();
connect();
</script>
</body>
</html>`))
}
