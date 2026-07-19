package dash

// indexHTML is the whole viewport UI: session list on the left, live ledger
// tail on the right, kill switch per running session. Poll-based on purpose —
// boring beats clever in an audit surface.
const indexHTML = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AgentOS</title>
<style>
  :root { color-scheme: dark; }
  body { margin: 0; font: 14px/1.5 ui-monospace, monospace; background: #0d1117; color: #d0d7de; display: flex; height: 100vh; }
  aside { width: 340px; border-right: 1px solid #30363d; overflow-y: auto; }
  main { flex: 1; overflow-y: auto; padding: 12px 16px; }
  h1 { font-size: 14px; padding: 12px 16px; margin: 0; border-bottom: 1px solid #30363d; color: #e6edf3; }
  .sess { padding: 10px 16px; border-bottom: 1px solid #21262d; cursor: pointer; }
  .sess:hover, .sess.active { background: #161b22; }
  .sess .id { color: #e6edf3; }
  .badge { font-size: 11px; padding: 1px 7px; border-radius: 9px; margin-left: 6px; }
  .running  { background: #1f6feb33; color: #58a6ff; }
  .finished { background: #23863633; color: #3fb950; }
  .unknown  { background: #6e768133; color: #8b949e; }
  .kill { float: right; background: #da36331a; color: #f85149; border: 1px solid #f8514966; border-radius: 5px; cursor: pointer; font: inherit; font-size: 11px; padding: 0 8px; }
  .ev { white-space: pre-wrap; word-break: break-all; padding: 2px 0; border-bottom: 1px solid #161b22; }
  .ev .t { color: #8b949e; margin-right: 8px; }
  .ev .ty { color: #d2a8ff; margin-right: 8px; }
  .ev.denied .ty, .ev.exceeded .ty { color: #f85149; }
  .empty { color: #8b949e; padding: 24px; }
  .esc { background: #bb800926; border: 1px solid #bb800966; border-radius: 6px; padding: 8px 12px; margin: 8px 0; }
  .esc .host { color: #d29922; }
  .esc button { font: inherit; font-size: 11px; border-radius: 5px; cursor: pointer; padding: 1px 10px; margin-left: 8px; }
  .esc .ok { background: #23863633; color: #3fb950; border: 1px solid #3fb95066; }
  .esc .no { background: #da36331a; color: #f85149; border: 1px solid #f8514966; }
</style>
<body>
<aside>
  <h1>AgentOS sessions</h1>
  <div id="list"><div class="empty">loading…</div></div>
</aside>
<main>
  <div id="approvals"></div>
  <div id="ledger"><div class="empty">select a session</div></div>
</main>
<script>
let current = null, offset = 0;

async function refreshList() {
  const res = await fetch('api/sessions');
  const { sessions } = await res.json();
  const list = document.getElementById('list');
  list.innerHTML = '';
  if (!sessions.length) list.innerHTML = '<div class="empty">no sessions yet</div>';
  for (const s of sessions) {
    const div = document.createElement('div');
    div.className = 'sess' + (s.id === current ? ' active' : '');
    div.innerHTML = '<span class="id">' + s.id + '</span>' +
      '<span class="badge ' + s.status + '">' + s.status + '</span>' +
      (s.status === 'running' ? '<button class="kill">kill</button>' : '');
    div.onclick = () => select(s.id);
    const kill = div.querySelector('.kill');
    if (kill) kill.onclick = async (e) => {
      e.stopPropagation();
      if (confirm('SIGTERM session ' + s.id + '?'))
        await fetch('api/sessions/' + s.id + '/kill', { method: 'POST' });
      refreshList();
    };
    list.appendChild(div);
  }
}

function select(id) {
  current = id; offset = 0;
  document.getElementById('ledger').innerHTML = '';
  refreshList(); tail(); refreshApprovals();
}

async function refreshApprovals() {
  const pane = document.getElementById('approvals');
  if (!current) { pane.innerHTML = ''; return; }
  const res = await fetch('api/sessions/' + current + '/escalations');
  if (!res.ok) return;
  const { escalations } = await res.json();
  pane.innerHTML = '';
  for (const e of escalations.filter(e => e.status === 'pending')) {
    const div = document.createElement('div');
    div.className = 'esc';
    div.innerHTML = 'capability request: egress to <span class="host">' + e.host +
      '</span> — "' + (e.reason || 'no reason given') + '"' +
      '<button class="ok">approve</button><button class="no">deny</button>';
    const decide = (approve) => async () => {
      await fetch('api/sessions/' + current + '/escalations/' + e.host, {
        method: 'POST', body: JSON.stringify({ approve }) });
      refreshApprovals();
    };
    div.querySelector('.ok').onclick = decide(true);
    div.querySelector('.no').onclick = decide(false);
    pane.appendChild(div);
  }
}

async function tail() {
  if (!current) return;
  const res = await fetch('api/sessions/' + current + '/ledger?offset=' + offset);
  if (!res.ok) return;
  const { events, next } = await res.json();
  const pane = document.getElementById('ledger');
  for (const e of events) {
    const div = document.createElement('div');
    const cls = e.type.includes('denied') ? ' denied' : e.type.includes('exceeded') ? ' exceeded' : '';
    div.className = 'ev' + cls;
    div.innerHTML = '<span class="t">' + e.ts + '</span><span class="ty">' + e.type + '</span>' +
      (e.data ? JSON.stringify(e.data) : '');
    pane.appendChild(div);
  }
  if (events.length) pane.scrollTop = pane.scrollHeight;
  offset = next;
}

refreshList();
setInterval(refreshList, 3000);
setInterval(tail, 1500);
setInterval(refreshApprovals, 2000);
</script>
</body>`
