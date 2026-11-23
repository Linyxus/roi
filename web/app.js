'use strict';
// roi web app. No build step, no dependencies.

const $app = document.getElementById('app');
const S = {
  agents: [], host: '', online: false, loaded: false, filter: 'all',
  view: null,          // {name, id}
  detail: null,        // last /api/agents/:id payload
  stamp: '',
  open: new Set(),     // expanded step groups, steps and diffs
  inboxTop: 0, inboxChips: 0, dockKey: '', showKeys: false, modeMenu: false, modeBusy: false, missing: 0,
  term: { fit: true, ctrl: false, alt: false, cols: 80 },
  form: null, settings: null,
};
let pollTimer = null;

// ---------- helpers ----------
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const sv = (d, s = 20, w = 1.75) => `<svg width="${s}" height="${s}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="${w}" stroke-linecap="round" stroke-linejoin="round">${d}</svg>`;
const I = {
  back: sv('<path d="M15 5l-7 7 7 7"/>', 24),
  chev: sv('<path d="M9 6l6 6-6 6"/>', 16),
  plus: sv('<path d="M12 5v14M5 12h14"/>', 22, 2),
  send: sv('<path d="M12 19V5M6 11l6-6 6 6"/>', 20, 2),
  term: sv('<rect x="3" y="4.5" width="18" height="15" rx="3"/><path d="M7.5 10l3 2.5-3 2.5M13 15h3.5"/>'),
  check: sv('<path d="M5 12.5l4.5 4.5L19 7.5"/>', 11, 3.2),
  stop: sv('<rect x="6.5" y="6.5" width="11" height="11" rx="2.5" fill="currentColor" stroke="none"/>', 18),
  x: sv('<path d="M6 6l12 12M18 6L6 18"/>', 22),
  folder: sv('<path d="M3.5 7.5a2 2 0 012-2h3.6l2 2.3h7.4a2 2 0 012 2v7.2a2 2 0 01-2 2h-13a2 2 0 01-2-2z"/>', 20),
  up: sv('<path d="M12 19V5M6 11l6-6 6 6"/>', 18),
  keys: sv('<rect x="3" y="6.5" width="18" height="11" rx="2.5"/><path d="M7 10.5h.01M11 10.5h.01M15 10.5h.01M8 14h8"/>'),
  pick: sv('<path d="M8 9.5l4-4 4 4M8 14.5l4 4 4-4"/>', 16),
  badge: sv('<path d="M5 12.5l4.5 4.5L19 7.5"/>', 10, 3.4),
  shield: sv('<path d="M12 3l7 3v5.5c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6z"/>', 16, 2),
  ask: sv('<path d="M9 9.2a3 3 0 115 2.2c-.9.8-2 1.3-2 2.8M12 17.6h.01"/>', 16, 2),
};

// A project's hue and two-letter monogram, both derived from its name.
// The hues skip the yellow-green range, which turns muddy at this lightness.
const HUES = [10, 35, 60, 140, 160, 180, 200, 220, 240, 255, 270, 285, 300, 320, 340, 355];
function hueOf(name) {
  let h = 2166136261;
  for (const c of String(name || '')) { h ^= c.codePointAt(0); h = Math.imul(h, 16777619); }
  h ^= h >>> 15; h = Math.imul(h, 2246822507); h ^= h >>> 13;
  return HUES[(h >>> 0) % HUES.length];
}
function monogram(name) {
  const w = String(name || '').split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  return (w.length > 1 ? [...w[0]][0] + [...w[1]][0] : [...(w[0] || '?')].slice(0, 2).join('')).toUpperCase();
}
const avatar = (project, state, cls = '') => `<span class="av ${cls}">${esc(monogram(project))}${state ? `<span class="bd ${state}">${state === 'done' ? I.badge : state === 'working' ? '<i class="spin"></i>' : '<i></i>'}</span>` : ''}</span>`;
const themeMeta = document.querySelector('meta[name="theme-color"]');
// The browser's top bar takes the colour of the screen under it.
const tint = () => { const v = $app.querySelector('.view'); if (v && themeMeta) themeMeta.content = getComputedStyle(v).backgroundColor; };

async function api(path, body) {
  const opt = body === undefined ? {} : { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Roi': '1' }, body: JSON.stringify(body) };
  const r = await fetch(path, opt);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || `Request failed (${r.status})`);
  return j;
}

function ago(iso) {
  if (!iso) return '';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return 'now';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}
function since(iso) {
  if (!iso) return '';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + ' min';
  if (s < 86400) return Math.floor(s / 3600) + ' h';
  return Math.floor(s / 86400) + ' d';
}
const kindName =(k) => (k === 'codex' ? 'Codex' : 'Claude');
const stateName = { need: 'Waiting on you', done: 'Finished', working: 'Working', idle: 'Idle' };
// The idle key row has six keys, so its mode button uses the short names.
const modeShort = { default: 'Ask first', acceptEdits: 'Edits', plan: 'Plan', bypassPermissions: 'Bypass', auto: 'Auto' };
const modeOrder = ['default', 'acceptEdits', 'plan', 'auto', 'bypassPermissions'];
const modeName = { default: 'Ask first', acceptEdits: 'Auto-accept edits', plan: 'Plan mode', bypassPermissions: 'Bypass permissions', auto: 'Auto mode' };

function toast(msg) {
  document.querySelector('.toast')?.remove();
  const t = document.createElement('div');
  t.className = 'toast'; t.textContent = msg;
  $app.appendChild(t);
  setTimeout(() => t.remove(), 3500);
}

// ---------- markdown (escapes first, then adds a small safe subset) ----------
function inline(s) {
  const codes = [];
  s = esc(s).replace(/`([^`\n]+)`/g, (_, c) => { codes.push(c); return `\u0000${codes.length - 1}\u0000`; });
  s = s.replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
    .replace(/(^|[\s(])\*([^*\s][^*\n]*?)\*(?=[\s).,;:!?]|$)/g, '$1<em>$2</em>')
    .replace(/(^|[\s(])_([^_\s][^_\n]*?)_(?=[\s).,;:!?]|$)/g, '$1<em>$2</em>');
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${codes[+i]}</code>`);
}
function md(src) {
  const lines = String(src).replace(/\r/g, '').split('\n');
  const out = [];
  let i = 0;
  const isRow = (l) => /^\s*\|.*\|\s*$/.test(l);
  const cells = (l) => l.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
  while (i < lines.length) {
    const l = lines[i];
    let m;
    if (/^\s*```/.test(l)) {
      const buf = []; i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) buf.push(lines[i++]);
      i++;
      out.push(`<pre><code>${esc(buf.join('\n'))}</code></pre>`);
    } else if ((m = l.match(/^(#{1,4})\s+(.*)$/))) {
      out.push(`<h${m[1].length}>${inline(m[2])}</h${m[1].length}>`); i++;
    } else if (/^\s*([-*_])\s*\1\s*\1[\s\-*_]*$/.test(l)) {
      out.push('<hr>'); i++;
    } else if (isRow(l) && i + 1 < lines.length && /^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(lines[i + 1])) {
      const head = cells(l); i += 2;
      const rows = [];
      while (i < lines.length && isRow(lines[i])) rows.push(cells(lines[i++]));
      out.push(`<div class="tbl"><table><thead><tr>${head.map((c) => `<th>${inline(c)}</th>`).join('')}</tr></thead><tbody>${rows.map((r) => `<tr>${r.map((c) => `<td>${inline(c)}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`);
    } else if (/^\s*>\s?/.test(l)) {
      const buf = [];
      while (i < lines.length && /^\s*>\s?/.test(lines[i])) buf.push(lines[i++].replace(/^\s*>\s?/, ''));
      out.push(`<blockquote>${inline(buf.join(' '))}</blockquote>`);
    } else if (/^\s*([-*+]|\d+[.)])\s+/.test(l)) {
      const ordered = /^\s*\d/.test(l);
      const items = [];
      while (i < lines.length && (/^\s*([-*+]|\d+[.)])\s+/.test(lines[i]) || (/^\s{2,}\S/.test(lines[i]) && items.length))) {
        if (/^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) items.push(lines[i].replace(/^\s*([-*+]|\d+[.)])\s+/, ''));
        else items[items.length - 1] += ' ' + lines[i].trim();
        i++;
      }
      const tag = ordered ? 'ol' : 'ul';
      out.push(`<${tag}>${items.map((x) => `<li>${inline(x)}</li>`).join('')}</${tag}>`);
    } else if (!l.trim()) {
      i++;
    } else {
      const buf = [];
      while (i < lines.length && lines[i].trim() && !/^\s*(```|#{1,4}\s|>|([-*+]|\d+[.)])\s)/.test(lines[i]) && !(isRow(lines[i]) && buf.length)) buf.push(lines[i++]);
      if (!buf.length) buf.push(lines[i++]);
      out.push(`<p>${buf.map(inline).join('<br>')}</p>`);
    }
  }
  return out.join('');
}

// ---------- routing ----------
function route() {
  const h = location.hash.replace(/^#\/?/, '');
  const p = h.split('/');
  let v = { name: 'inbox' };
  if (p[0] === 'a' && p[1]) v = { name: p[2] === 'term' ? 'term' : 'session', id: p[1] };
  else if (p[0] === 'new') v = { name: 'new' };
  else if (p[0] === 'settings') v = { name: 'settings' };
  saveInboxScroll();
  const changed = !S.view || S.view.name !== v.name || S.view.id !== v.id;
  S.view = v;
  clearInterval(pollTimer); pollTimer = null;
  if (v.name === 'inbox') renderInbox();
  if (v.name === 'session') {
    if (changed) { S.detail = null; S.stamp = ''; S.dockKey = ''; S.missing = 0; S.showKeys = false; S.modeMenu = S.modeBusy = false; }
    renderSession(true); loadDetail();
    pollTimer = setInterval(loadDetail, 1500);
  }
  if (v.name === 'term') { renderTerm(); loadScreen(); pollTimer = setInterval(loadScreen, 1000); }
  if (v.name === 'new') openNew();
  if (v.name === 'settings') openSettings();
}
const go = (h) => { location.hash = h; };
window.addEventListener('hashchange', route);

// ---------- data ----------
async function loadAgents() {
  try {
    const j = await api('/api/agents');
    S.agents = j.agents || []; S.host = j.host; S.loaded = true; S.online = true;
  } catch (_) { S.online = false; }
  if (S.view?.name === 'inbox') renderInbox();
}
function connect() {
  const es = new EventSource('/api/events');
  es.onmessage = () => { S.online = true; loadAgents(); };
  es.onerror = () => { S.online = false; if (S.view?.name === 'inbox') renderInbox(); };
}
setInterval(() => { if (!S.online || document.visibilityState === 'visible') loadAgents(); }, 15000);
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') { loadAgents(); if (S.view?.name === 'session') loadDetail(); } });

// ---------- inbox ----------
// The inbox's own scroll position, kept while another screen is showing.
function saveInboxScroll() {
  const v = $app.querySelector('.inboxview');
  if (v) { S.inboxTop = v.querySelector('.scroll').scrollTop; S.inboxChips = v.querySelector('.chips').scrollLeft; }
}
function renderInbox() {
  // Chips filter by project; the list itself is grouped by state.
  const projects = new Map();
  for (const a of S.agents) projects.set(a.project, (projects.get(a.project) || 0) + 1);
  if (S.filter !== 'all' && !projects.has(S.filter)) S.filter = 'all';
  const list = S.agents.filter((a) => S.filter === 'all' || a.project === S.filter);
  const need = S.agents.filter((a) => a.state === 'need').length;
  const chips = `<button class="chip ${S.filter === 'all' ? 'on' : ''}" data-act="filter" data-v="all">All <i>${S.agents.length}</i></button>`
    + [...projects].sort((x, y) => y[1] - x[1] || String(x[0]).localeCompare(y[0])).map(([p, n]) => `<button class="chip hue ${S.filter === p ? 'on' : ''}" style="--h:${hueOf(p)}" data-act="filter" data-v="${esc(p)}"><span class="dot"></span>${esc(p)} <i>${n}</i></button>`).join('');
  saveInboxScroll();
  const keep = S.inboxTop, keepChips = S.inboxChips;
  let rows;
  if (!S.loaded) rows = `<div class="empty">${S.online === false && S.loaded === false ? 'Connecting to your Mac…' : ''}</div>`;
  else if (!list.length) rows = '<div class="empty"><b>No agents running</b>Start one with the plus button, or open Claude Code or Codex in a Rex pane.</div>';
  else {
    rows = [['need', 'Needs you'], ['done', 'Finished'], ['working', 'Working'], ['idle', 'Idle']].map(([st, name]) => {
      const xs = list.filter((a) => a.state === st);
      if (!xs.length) return '';
      return `<div class="sec ${st}">${name}<span>${xs.length}</span></div>` + xs.map((a) => `
      <button class="item hue ${a.state}" style="--h:${hueOf(a.project)}" data-act="open" data-id="${esc(a.id)}">
        ${avatar(a.project, a.state)}
        <span class="grow">
          <span class="row"><span class="t clip1 grow">${esc(a.title)}</span><span class="time">${ago(a.updated)}</span></span>
          <span class="l clip1"><b>${esc(a.project)}</b> ${esc(a.line)}</span>
        </span>
      </button>`).join('');
    }).join('');
  }
  $app.innerHTML = `
    <div class="view inboxview">
      <div class="top">
        <div class="hd">
          <h1>Agents</h1>
          <button class="hostbtn" data-act="settings"><span class="pill"><span class="dot" style="color:var(--${S.online ? 'done' : 'need'})"></span>${S.online ? 'Mac online' : 'Mac unreachable'}</span></button>
        </div>
        <div class="chips">${chips}</div>
      </div>
      <div class="sheet"><div class="scroll" style="padding-bottom:104px">${rows}</div></div>
      <button class="fab" data-act="new" aria-label="New agent">${I.plus}</button>
    </div>`;
  $app.querySelector('.scroll').scrollTop = keep;
  $app.querySelector('.chips').scrollLeft = keepChips;
  tint();
  document.title = need ? `(${need}) roi` : 'roi';
  if (navigator.setAppBadge) (need ? navigator.setAppBadge(need) : navigator.clearAppBadge()).catch(() => {});
}

// ---------- session ----------
async function loadDetail() {
  const id = S.view?.id;
  if (!id || S.view.name !== 'session') return;
  try {
    const j = await api(`/api/agents/${encodeURIComponent(id)}?stamp=${S.stamp}`);
    if (S.view?.id !== id) return;
    S.missing = 0;
    if (j.unchanged) return;
    const first = !S.detail;
    S.detail = j; S.stamp = j.stamp;
    renderSession(first);
    if (j.agent.unread) api(`/api/agents/${encodeURIComponent(id)}/seen`, {}).catch(() => {});
  } catch (e) {
    if (S.view?.id !== id) return;
    S.missing++;
    if (!S.detail && S.missing > 12) { $app.querySelector('.body').innerHTML = `<div class="empty"><b>Agent not found</b>${esc(e.message)}</div>`; }
    if (S.detail && S.missing === 3) toast('Lost contact with this agent.');
  }
}

function stepWord(items) {
  const c = { cmd: 0, read: 0, edit: 0 };
  for (const t of items) {
    if (t.name === 'Bash') c.cmd++;
    else if (/^(Read|Grep|Glob)$/.test(t.name)) c.read++;
    else if (/^(Edit|Write|MultiEdit|NotebookEdit)$/.test(t.name)) c.edit++;
  }
  const parts = [];
  if (c.cmd) parts.push(`${c.cmd} command${c.cmd > 1 ? 's' : ''}`);
  if (c.read) parts.push(`${c.read} read${c.read > 1 ? 's' : ''}`);
  if (c.edit) parts.push(`${c.edit} edit${c.edit > 1 ? 's' : ''}`);
  return parts.join(' · ');
}

function renderDiff(t) {
  const d = t.diff, key = 'd:' + t.id, open = S.open.has(key);
  const name = d.file.split('/').slice(-2).join('/');
  const body = open ? `<pre>${d.lines.map((l) => `<span class="${l[0] === '+' ? 'a' : l[0] === '-' ? 'd' : l === '@@' ? 'h' : ''}">${l === '@@' ? '⋯' : esc(l ? l[0] + ' ' + l.slice(1) : ' ')}</span>`).join('')}${d.more ? `<span class="h">${d.more} more lines on the Mac</span>` : ''}</pre>` : '';
  return `<div class="diff ${open ? 'open' : ''}"><button class="diff-h" data-act="toggle" data-k="${esc(key)}"><span class="grow clip1">${esc(name)}</span><span class="plus">+${d.added}</span><span class="minus">−${d.removed}</span>${I.chev}</button>${body}</div>`;
}

function renderGroup(tools, live) {
  const key = 'g:' + tools[0].id, open = S.open.has(key);
  const words = stepWord(tools);
  let h = `<div class="steps ${open ? 'open' : ''}"><button class="steps-h" data-act="toggle" data-k="${esc(key)}">${I.chev}<span class="grow clip1"><b>${tools.length} step${tools.length > 1 ? 's' : ''}</b>${words ? ' · ' + words : ''}</span></button>`;
  if (open) {
    h += tools.map((t) => {
      const sk = 's:' + t.id, so = S.open.has(sk);
      const detail = so ? `<pre>${esc(t.input || t.summary || '')}</pre>${t.output ? `<pre>${esc(t.output)}</pre>` : t.pending ? '' : '<pre>(no output)</pre>'}` : '';
      return `<button class="step ${t.error ? 'err' : ''}" data-act="toggle" data-k="${esc(sk)}"><div class="n">${esc(t.name)}${t.error ? ' · failed' : t.pending ? ' · running' : ''}</div><div class="s ${so ? '' : 'clip1'}">${esc(t.summary || '')}</div>${detail}</button>`;
    }).join('');
  }
  h += '</div>';
  h += tools.filter((t) => t.diff).map(renderDiff).join('');
  const last = tools[tools.length - 1];
  if (live && last.pending) h += `<div class="now"><i class="spin"></i><span class="grow clip1">${last.name === 'Bash' ? 'Running' : esc(last.name)} <b>${esc(last.summary || '')}</b></span></div>`;
  return h;
}

function renderItems(d) {
  const items = d.items || [];
  const live = d.agent.state === 'working';
  const out = [];
  if (d.truncated) out.push('<div class="note">Earlier messages are on the Mac</div>');
  let group = [];
  const flush = (isLast) => { if (group.length) { out.push(renderGroup(group, live && isLast)); group = []; } };
  items.forEach((it) => {
    if (it.kind === 'tool') { group.push(it); return; }
    flush(false);
    if (it.kind === 'user') out.push(`<div class="you">${esc(it.text)}</div>`);
    else if (it.kind === 'assistant') out.push(`<div class="agent">${md(it.text)}</div>`);
    else out.push(`<div class="note">${esc(it.text)}</div>`);
  });
  flush(true);
  if (!items.length) out.push(d.noTranscript ? '<div class="empty"><b>No conversation to show</b>roi could not find this agent\'s session file. The terminal view still works.</div>' : '<div class="empty">No messages yet.</div>');
  return out.join('');
}

function renderPanels(d) {
  let h = '';
  const plan = d.plan || [];
  if (plan.length && d.agent.state !== 'idle') {
    const done = plan.filter((t) => t.status === 'completed').length;
    h += `<div class="panel"><div class="label">Plan · ${done} of ${plan.length}</div>${plan.map((t) => `<div class="todo ${esc(t.status)}"><span class="bx">${t.status === 'completed' ? I.check : ''}</span><span>${esc(t.text)}</span></div>`).join('')}</div>`;
  }
  const ws = d.agent.workers || [];
  if (ws.length && d.agent.state !== 'idle') {
    h += `<div class="panel"><div class="label">Workers</div>${ws.map((w) => `<div class="worker"><span class="mark ${w.state === 'working' ? 'working' : w.state === 'blocked' ? 'need' : 'done'}"></span><span class="grow clip1" style="${w.state === 'working' ? 'font-weight:600' : ''}">${esc(w.title)}</span><span class="time">${w.progress != null ? w.progress + '%' : w.state === 'done' ? 'done' : ''}</span></div>`).join('')}</div>`;
  }
  return h;
}

function dockHTML(d) {
  const a = d.agent;
  const modes = d.modes?.length ? modeOrder.filter((m) => d.modes.includes(m)) : modeOrder;
  const mode = a.kind === 'claude' && d.mode ? `<button class="key mode" data-act="mode-menu" aria-haspopup="menu" aria-expanded="${S.modeMenu}" ${S.modeBusy ? 'disabled' : ''}><span class="clip1">${S.modeBusy ? 'Switching…' : esc((a.state === 'working' ? modeName : modeShort)[d.mode] || d.mode)}</span>${S.modeBusy ? '<i class="spin"></i>' : I.pick}</button>` : '';
  const menu = mode && S.modeMenu ? `<button class="shade" data-act="mode-close" aria-label="Close"></button><div class="menu" role="menu"><div class="label">Permission mode</div>${modes.map((m) => `<button class="mi ${m === d.mode ? 'on' : ''}" role="menuitemradio" aria-checked="${m === d.mode}" data-act="mode-set" data-v="${m}"><span class="grow">${modeName[m]}</span>${m === d.mode ? I.check : ''}</button>`).join('')}</div>` : '';
  if (a.state === 'need' && d.prompt) {
    const p = d.prompt;
    const opts = (p.options || []).map((o) => `<button class="opt" data-act="answer" data-n="${o.n}"><span class="num">${o.n}</span><span class="grow"><div class="ol">${esc(o.label)}</div>${o.description ? `<div class="od">${esc(o.description)}</div>` : ''}</span></button>`).join('');
    const fallback = opts ? '' : `<div class="hint" style="margin:10px 0">roi could not read the choices from the screen. Answer in the terminal.</div><button class="btn pri wide" data-act="term" style="margin-bottom:10px">${I.term} Open terminal</button>`;
    return `<div class="prompt">
      <div class="k">${p.kind === 'question' ? I.ask + 'Question' : p.kind === 'permission' ? I.shield + 'Permission' : 'Waiting on you'}</div>
      <div class="q">${esc(p.title)}</div>
      ${p.body ? `<div class="cmd">${esc(p.body)}</div>` : ''}
      <div class="opts ${esc(p.kind)}">${opts}</div>${fallback}
      <div class="field"><textarea rows="1" placeholder="Or type a reply" data-role="answer"></textarea><button class="send" data-act="answer-text" aria-label="Send">${I.send}</button></div>
    </div>`;
  }
  const working = a.state === 'working';
  const keys = working
    ? `<div class="keys"><button class="key stop" data-act="key" data-keys="escape">${I.stop}Stop</button><button class="key" data-act="key" data-keys="ctrl+c">⌃C</button>${mode}</div>`
    : S.showKeys ? `<div class="keys"><button class="key" data-act="key" data-keys="escape">esc</button><button class="key" data-act="key" data-keys="ctrl+c">⌃C</button><button class="key" data-act="key" data-keys="up">↑</button><button class="key" data-act="key" data-keys="down">↓</button><button class="key" data-act="key" data-keys="enter">⏎</button>${mode}</div>` : '';
  return `${menu}${keys}<div class="field"><textarea rows="1" placeholder="${working ? 'Steer it. Read at the next step' : 'Reply'}" data-role="send"></textarea>${working ? '' : `<button class="iconbtn" data-act="keys" aria-label="Keys" style="color:var(--ink2);width:40px">${I.keys}</button>`}<button class="send" data-act="send" aria-label="Send">${I.send}</button></div>`;
}

function renderSession(fresh) {
  const d = S.detail;
  const a = d?.agent || S.agents.find((x) => x.id === S.view.id) || { title: '', project: '', kind: 'claude', state: 'idle' };
  if (fresh || !$app.querySelector('.sessionview')) {
    $app.innerHTML = `
      <div class="view sessionview hue">
        <div class="top">
          <div class="nav">
            <button class="iconbtn" data-act="home" aria-label="Back">${I.back}</button>
            <span data-slot="av"></span>
            <div class="grow who"><div class="ttl clip1" data-slot="title"></div><div class="sub clip1" data-slot="sub"></div></div>
            <button class="iconbtn" data-act="term" aria-label="Terminal">${I.term}</button>
          </div>
          <div class="status" data-slot="status"></div>
        </div>
        <div class="sheet"><div class="scroll"><div class="panels" data-slot="panels"></div><div class="body" data-slot="body"></div></div></div>
        <div class="dock" data-slot="dock"></div>
      </div>`;
    S.dockKey = '';
  }
  const q = (s) => $app.querySelector(`[data-slot="${s}"]`);
  $app.querySelector('.sessionview').style.setProperty('--h', hueOf(a.project));
  tint();
  q('av').innerHTML = a.project ? avatar(a.project, '', 'sm') : '';
  q('title').textContent = a.title || 'Starting…';
  q('sub').textContent = a.project ? `${a.project} · ${kindName(a.kind)}` : '';
  const steps = d ? (d.items || []).filter((it) => it.kind === 'tool').length : 0;
  const right = !d ? '' : a.state === 'need' && d.prompt?.kind ? d.prompt.kind
    : a.state === 'working' ? (steps ? `${steps} step${steps > 1 ? 's' : ''}` : '')
    : a.kind === 'claude' && d.mode ? (modeName[d.mode] || d.mode).toLowerCase() : '';
  const when = a.updated ? since(a.updated) : '';
  q('status').innerHTML = `<span class="st ${a.state}">${a.state === 'working' ? '<i class="spin"></i>' : '<i class="dot"></i>'}<b>${stateName[a.state] || ''}${when ? ' · ' + when : ''}</b></span><span class="grow"></span><span class="rt mono">${esc(right)}</span>`;
  if (!d) { if (fresh) q('body').innerHTML = '<div class="empty">Loading…</div>'; return; }

  const sc = $app.querySelector('.scroll');
  const atBottom = fresh || sc.scrollHeight - sc.scrollTop - sc.clientHeight < 80;
  const top = sc.scrollTop;
  q('panels').innerHTML = renderPanels(d);
  q('body').innerHTML = renderItems(d);

  // Rebuild the dock only when its shape changes, so typed text survives refreshes.
  const key = a.state === 'need' ? 'need:' + JSON.stringify(d.prompt) : `${a.state === 'working'}:${S.showKeys}:${d.mode}:${a.title}:${S.modeMenu}:${S.modeBusy}:${d.modes}`;
  if (key !== S.dockKey) {
    const old = $app.querySelector('.dock textarea');
    const text = old ? old.value : '', role = old?.dataset.role, focused = document.activeElement === old;
    q('dock').innerHTML = dockHTML(d);
    const ta = $app.querySelector('.dock textarea');
    if (ta && ta.dataset.role === role) { ta.value = text; autosize(ta); if (focused) ta.focus(); }
    S.dockKey = key;
  }
  sc.scrollTop = atBottom ? sc.scrollHeight : top;
}

function autosize(ta) { ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'; }

async function act(fn, okMsg) {
  try { await fn(); if (okMsg) toast(okMsg); setTimeout(loadDetail, 500); } catch (e) { toast(e.message); }
}

// ---------- terminal ----------
function renderTerm() {
  const a = S.agents.find((x) => x.id === S.view.id) || S.detail?.agent || { title: 'Terminal' };
  const k = (label, keys, cls = '') => `<button class="key grow ${cls}" data-act="tkey" data-keys="${keys}">${label}</button>`;
  $app.innerHTML = `
    <div class="view termview">
      <div class="nav">
        <button class="iconbtn" data-act="back-session" aria-label="Back">${I.back}</button>
        <div class="grow"><div class="ttl clip1">${esc(a.title)}</div><div class="sub clip1" data-slot="tsub">terminal</div></div>
        <button class="key ui ${S.term.fit ? 'on' : ''}" data-act="fit" style="margin-right:8px">Fit width</button>
      </div>
      <div class="termout"><pre data-slot="screen"></pre></div>
      <div class="dock">
        <div class="keys">${k('esc', 'escape')}${k('tab', 'tab')}<button class="key grow ${S.term.ctrl ? 'on' : ''}" data-act="mod" data-m="ctrl">⌃</button><button class="key grow ${S.term.alt ? 'on' : ''}" data-act="mod" data-m="alt">⌥</button>${k('⇧⇥', 'shift+tab')}${k('⏎', 'enter')}</div>
        <div class="keys">${k('←', 'left')}${k('↓', 'down')}${k('↑', 'up')}${k('→', 'right')}${k('⌫', 'backspace')}${k('space', 'space')}</div>
        <div class="field"><input type="text" placeholder="Type, then send" autocapitalize="off" autocorrect="off" autocomplete="off" spellcheck="false" data-role="tinput"><button class="send" data-act="tsend" aria-label="Send">${I.send}</button></div>
      </div>
    </div>`;
  tint();
}
async function loadScreen() {
  const id = S.view?.id;
  if (S.view?.name !== 'term') return;
  try {
    const j = await api(`/api/agents/${encodeURIComponent(id)}/screen`);
    if (S.view?.name !== 'term' || S.view.id !== id) return;
    const pre = $app.querySelector('[data-slot="screen"]'), box = $app.querySelector('.termout');
    const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40 || !pre.innerHTML;
    S.term.cols = j.cols || 80;
    if (pre.dataset.h !== j.html) { pre.innerHTML = j.html; pre.dataset.h = j.html; }
    fitTerm();
    $app.querySelector('[data-slot="tsub"]').textContent = `terminal · ${j.cols} × ${j.rows} · live`;
    if (atBottom) box.scrollTop = box.scrollHeight;
  } catch (e) { $app.querySelector('[data-slot="tsub"]').textContent = e.message; }
}
function fitTerm() {
  const pre = $app.querySelector('[data-slot="screen"]'), box = $app.querySelector('.termout');
  if (!pre) return;
  const size = S.term.fit ? Math.max(5, Math.min(14, (box.clientWidth - 16) / (S.term.cols * 0.6))) : 12.5;
  pre.style.fontSize = size.toFixed(2) + 'px';
}
window.addEventListener('resize', fitTerm);
function termKey(key) {
  const mods = (S.term.ctrl ? 'ctrl+' : '') + (S.term.alt ? 'alt+' : '');
  S.term.ctrl = S.term.alt = false;
  $app.querySelectorAll('[data-act="mod"]').forEach((b) => b.classList.remove('on'));
  api(`/api/agents/${encodeURIComponent(S.view.id)}/key`, { keys: [mods + key] }).then(() => setTimeout(loadScreen, 150)).catch((e) => toast(e.message));
}

// ---------- new agent ----------
async function openNew() {
  if (!S.form) S.form = { kind: 'claude', prompt: '', cwd: '', browse: null, projects: [], kinds: ['claude', 'codex'], busy: false };
  renderNew();
  try {
    const j = await api('/api/projects');
    S.form.projects = j.projects; S.form.kinds = j.kinds; S.form.home = j.home;
    if (!S.form.cwd && j.projects.length) S.form.cwd = j.projects[0].cwd;
    if (S.view.name === 'new') renderNew();
  } catch (e) { toast(e.message); }
}
function renderNew() {
  const f = S.form;
  const ta = $app.querySelector('.area'); if (ta) f.prompt = ta.value;
  const short = (p) => (f.home && p.startsWith(f.home) ? '~' + p.slice(f.home.length) : p);
  const name = (p) => p.split('/').filter(Boolean).pop() || '/';
  let where;
  if (f.browse) {
    const b = f.browse;
    where = `<div class="label">Where · ${esc(short(b.path))}</div><div class="list">
      ${b.path !== '/' ? `<button class="pick" data-act="dir" data-p="${esc(b.parent)}">${I.up}<span class="grow pn">Up to ${esc(name(b.parent))}</span></button>` : ''}
      ${b.dirs.map((d) => `<button class="pick" data-act="dir" data-p="${esc(b.path.replace(/\/$/, '') + '/' + d)}">${I.folder}<span class="grow pn clip1">${esc(d)}</span>${I.chev}</button>`).join('') || '<div class="pick hint">No folders here</div>'}
    </div><button class="btn wide" data-act="usedir" style="margin-top:12px">Use ${esc(name(b.path))}</button>`;
  } else {
    const known = f.projects.some((p) => p.cwd === f.cwd);
    const pick = (pn, cwd, running) => `<button class="pick hue ${cwd === f.cwd ? 'on' : ''}" style="--h:${hueOf(pn)}" data-act="cwd" data-p="${esc(cwd)}">${avatar(pn, '', 'sm')}<span class="grow"><div class="pn clip1">${esc(pn)}</div><div class="pp clip1">${esc(short(cwd))}${running ? ` · ${running} running` : ''}</div></span><span class="radio">${cwd === f.cwd ? I.check : ''}</span></button>`;
    const rows = f.projects.map((p) => pick(p.name, p.cwd, p.running)).join('');
    const custom = f.cwd && !known ? pick(name(f.cwd), f.cwd, 0) : '';
    where = `<div class="label">Where</div><div class="list">${custom}${rows}<button class="pick" data-act="browse">${I.folder}<span class="grow pn" style="color:var(--ink2)">Browse folders on the Mac</span>${I.chev}</button></div>`;
  }
  $app.innerHTML = `
    <div class="view ${f.cwd ? 'hue' : ''}" style="--h:${hueOf(f.projects.find((p) => p.cwd === f.cwd)?.name || name(f.cwd || ''))}">
      <div class="sheet-h"><h2>New agent</h2><button class="iconbtn" data-act="home" aria-label="Close">${I.x}</button></div>
      <div class="sheet"><div class="scroll"><div class="form">
        <div class="seg">${f.kinds.map((k) => `<button class="${f.kind === k ? 'on' : ''}" data-act="kind" data-v="${esc(k)}">${k === 'claude' ? 'Claude Code' : k === 'codex' ? 'Codex' : esc(k)}</button>`).join('')}</div>
        <div><div class="label">What should it do</div><textarea class="area" placeholder="Describe the task. Leave empty to start a blank session.">${esc(f.prompt)}</textarea></div>
        <div>${where}</div>
      </div></div></div>
      <div class="dock"><button class="btn pri wide" data-act="spawn" ${f.cwd && !f.busy ? '' : 'disabled'}>${f.busy ? 'Starting…' : f.cwd ? 'Start in ' + esc(name(f.cwd)) : 'Choose a folder'}</button></div>
    </div>`;
  tint();
}
async function browse(path) {
  try { S.form.browse = await api('/api/dirs?path=' + encodeURIComponent(path || '')); renderNew(); } catch (e) { toast(e.message); }
}

// ---------- settings ----------
async function openSettings() {
  S.settings = S.settings || { devices: null, key: '', perm: 'default' };
  renderSettings();
  try { const j = await api('/api/push'); S.settings.devices = j.devices; S.settings.key = j.key; } catch (_) {}
  if (S.view.name === 'settings') renderSettings();
}
function pushSupport() {
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) {
    const ios = /iPhone|iPad|iPod/.test(navigator.userAgent);
    return ios ? 'On iPhone, notifications work once roi is on your Home Screen: tap Share, then Add to Home Screen, and open it from there.' : 'This browser does not support notifications.';
  }
  return '';
}
function renderSettings() {
  const why = pushSupport();
  const perm = why ? 'unsupported' : Notification.permission;
  const n = { need: 0, done: 0, working: 0, idle: 0 };
  for (const a of S.agents) n[a.state]++;
  $app.innerHTML = `
    <div class="view">
      <div class="sheet-h"><h2>Your Mac</h2><button class="iconbtn" data-act="home" aria-label="Close">${I.x}</button></div>
      <div class="sheet"><div class="scroll"><div class="form" style="gap:0;padding-top:6px">
        <div class="kv"><span>Mac</span><span>${esc(S.host || '')}</span></div>
        <div class="kv"><span>Connection</span><span>${S.online ? 'Online' : 'Unreachable'}</span></div>
        <div class="kv"><span>Agents</span><span>${n.need} waiting · ${n.done} finished · ${n.working} working · ${n.idle} idle</span></div>
        <div class="kv"><span>Notifications</span><span>${perm === 'granted' ? 'On for this device' : perm === 'denied' ? 'Blocked in browser settings' : 'Off'}</span></div>
        <p class="hint" style="margin:14px 0">${why || 'roi notifies you when an agent is waiting on you, and 30 seconds after one finishes. Working and idle agents never notify.'}</p>
        ${why ? '' : `<button class="btn pri wide" data-act="push-on">${perm === 'granted' ? 'Send a test notification' : 'Turn on notifications'}</button>`}
        <p class="hint" style="margin:22px 0 0">The Mac must stay awake for agents to keep running and for roi to be reachable.</p>
      </div></div></div>
    </div>`;
  tint();
}
function b64key(s) {
  const pad = '='.repeat((4 - (s.length % 4)) % 4);
  const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}
async function enablePush() {
  try {
    const perm = await Notification.requestPermission();
    if (perm !== 'granted') { toast('Notifications were not allowed.'); return renderSettings(); }
    const reg = await navigator.serviceWorker.register('/sw.js');
    await navigator.serviceWorker.ready;
    let sub = await reg.pushManager.getSubscription();
    if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64key(S.settings.key) });
    await api('/api/push/subscribe', sub.toJSON());
    const j = await api('/api/push/test', {});
    toast(j.delivered ? 'Test notification sent.' : 'Subscribed, but the test was not accepted.');
  } catch (e) { toast('Could not turn on notifications: ' + e.message); }
  renderSettings();
}

// ---------- events ----------
$app.addEventListener('click', (ev) => {
  const el = ev.target.closest('[data-act]');
  if (!el) return;
  const id = S.view?.id, base = id ? `/api/agents/${encodeURIComponent(id)}` : '';
  const ta = () => $app.querySelector('.dock textarea, .dock input');
  switch (el.dataset.act) {
    case 'filter': S.filter = el.dataset.v; renderInbox(); break;
    case 'open': go('/a/' + el.dataset.id); break;
    case 'home': go('/'); break;
    case 'new': S.form = null; go('/new'); break;
    case 'settings': go('/settings'); break;
    case 'term': go(`/a/${id}/term`); break;
    case 'back-session': go('/a/' + id); break;
    case 'keys': S.showKeys = !S.showKeys; renderSession(false); break;
    case 'toggle': { const k = el.dataset.k; if (window.getSelection().toString()) break; S.open.has(k) ? S.open.delete(k) : S.open.add(k); renderSession(false); break; }
    case 'mode-menu': S.modeMenu = !S.modeMenu; renderSession(false); break;
    case 'mode-close': S.modeMenu = false; renderSession(false); break;
    case 'mode-set': {
      S.modeMenu = false;
      if (el.dataset.v === S.detail?.mode) { renderSession(false); break; }
      S.modeBusy = true; renderSession(false);
      api(base + '/mode', { mode: el.dataset.v }).catch((e) => toast(e.message)).then(() => loadDetail()).then(() => { S.modeBusy = false; if (S.view?.name === 'session') renderSession(false); });
      break;
    }
    case 'key': act(() => api(base + '/key', { keys: el.dataset.keys.split(' ') })); break;
    case 'send': { const t = ta(); const text = t.value.trim(); if (!text) break; t.value = ''; autosize(t); act(() => api(base + '/send', { text })); break; }
    case 'answer': el.disabled = true; act(() => api(base + '/answer', { option: +el.dataset.n })); break;
    case 'answer-text': { const t = ta(); const text = t.value.trim(); if (!text) break; t.value = ''; act(() => api(base + '/answer', { text })); break; }
    case 'fit': S.term.fit = !S.term.fit; el.classList.toggle('on', S.term.fit); fitTerm(); break;
    case 'mod': S.term[el.dataset.m] = !S.term[el.dataset.m]; el.classList.toggle('on', S.term[el.dataset.m]); break;
    case 'tkey': termKey(el.dataset.keys); break;
    case 'tsend': { const t = ta(); const text = t.value; if (!text) { termKey('enter'); break; } t.value = ''; api(base + '/key', { text, keys: ['enter'] }).then(() => setTimeout(loadScreen, 150)).catch((e) => toast(e.message)); break; }
    case 'kind': S.form.kind = el.dataset.v; renderNew(); break;
    case 'cwd': S.form.cwd = el.dataset.p; renderNew(); break;
    case 'browse': browse(S.form.cwd || S.form.home); break;
    case 'dir': browse(el.dataset.p); break;
    case 'usedir': S.form.cwd = S.form.browse.path; S.form.browse = null; renderNew(); break;
    case 'spawn': {
      const f = S.form; f.prompt = $app.querySelector('.area').value; f.busy = true; renderNew();
      api('/api/spawn', { kind: f.kind, cwd: f.cwd, prompt: f.prompt })
        .then((j) => { S.form = null; go('/a/' + j.id); })
        .catch((e) => { f.busy = false; renderNew(); toast(e.message); });
      break;
    }
    case 'push-on': enablePush(); break;
  }
});
$app.addEventListener('input', (ev) => {
  const t = ev.target;
  if (t.matches('.dock textarea')) autosize(t);
  // In the terminal, a held ⌃ or ⌥ applies to the next typed character.
  if (t.dataset.role === 'tinput' && (S.term.ctrl || S.term.alt) && /^[a-z0-9]$/i.test(t.value)) { const c = t.value.toLowerCase(); t.value = ''; termKey(c); }
});
$app.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Enter') return;
  const t = ev.target;
  if (t.dataset.role === 'tinput') { ev.preventDefault(); $app.querySelector('[data-act="tsend"]').click(); }
  else if (t.matches('.dock textarea') && (ev.metaKey || ev.ctrlKey)) { ev.preventDefault(); $app.querySelector('.dock .send').click(); }
});
// The 256-colour terminal palette that Rex's HTML refers to by number.
(() => {
  const base = ['#1d1f21', '#cc6666', '#b5bd68', '#f0c674', '#81a2be', '#b294bb', '#8abeb7', '#c5c8c6', '#666a6c', '#e06c75', '#c3d17a', '#f5d28a', '#96b9d6', '#c8a8d2', '#9fd4cc', '#ffffff'];
  const v = [0, 95, 135, 175, 215, 255], css = [];
  for (let i = 0; i < 256; i++) {
    let c;
    if (i < 16) c = base[i];
    else if (i < 232) { const n = i - 16; c = `rgb(${v[Math.floor(n / 36)]},${v[Math.floor(n / 6) % 6]},${v[n % 6]})`; }
    else { const g = 8 + (i - 232) * 10; c = `rgb(${g},${g},${g})`; }
    css.push(`--vt-palette-${i}:${c}`);
  }
  const st = document.createElement('style');
  st.textContent = `.termout{${css.join(';')}}`;
  document.head.appendChild(st);
})();

if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
route();
loadAgents();
connect();
