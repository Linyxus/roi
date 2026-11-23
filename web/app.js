'use strict';
// roi web app. No build step, no dependencies.
//
// Four rules shape this file. A control answers the finger before any request
// is made. The result of an action is shown at once and the Mac confirms
// behind it. The screen is updated in place, so what stays keeps its place
// and what is new can arrive. And a working agent looks like it is working.

const $app = document.getElementById('app');
const root = document.documentElement;
const S = {
  agents: [], host: '', online: null, loaded: false, filter: 'all',
  view: null,          // {name, id}
  open: new Set(),     // expanded step groups, steps and diffs
  was: new Set(),      // those opened at least once, which keep their content while closing
  inboxTop: 0, inboxChips: 0, showKeys: false, modeMenu: false, sheet: null, ans: 0,
  fresh: false,        // a render that should not animate what it adds
  painted: false,      // the open session has shown its conversation
  stick: true,         // the conversation follows its newest line
  unseen: false,       // something arrived below while scrolled up
  reveal: true,        // new agent text is laid down word by word
  tapAt: 0,            // when the last in-app navigation was tapped
  lists: {}, effs: {}, // model lists by agent, effort levels by model
  term: { fit: true, ctrl: false, alt: false, cols: 80 },
  form: null, settings: null,
};
let pollTimer = null, uid = 0;

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
  chip: sv('<rect x="6.5" y="6.5" width="11" height="11" rx="2.5"/><path d="M10 3.5v3M14 3.5v3M10 17.5v3M14 17.5v3M3.5 10h3M3.5 14h3M17.5 10h3M17.5 14h3"/>', 18),
  badge: sv('<path d="M5 12.5l4.5 4.5L19 7.5"/>', 10, 3.4),
  shield: sv('<path d="M12 3l7 3v5.5c0 4.2-2.9 7.6-7 9-4.1-1.4-7-4.8-7-9V6z"/>', 16, 2),
  ask: sv('<path d="M9 9.2a3 3 0 115 2.2c-.9.8-2 1.3-2 2.8M12 17.6h.01"/>', 16, 2),
  down: sv('<path d="M12 19V5M6 11l6-6 6 6"/>', 16, 2.2),
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
const modeOrder = ['default', 'acceptEdits', 'plan', 'auto', 'bypassPermissions'];
const modeName = { default: 'Ask first', acceptEdits: 'Auto-accept edits', plan: 'Plan mode', bypassPermissions: 'Bypass permissions', auto: 'Auto mode' };

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

// ---------- motion ----------
// Durations and curves live in app.css; scripts read the same values.
const css = getComputedStyle(root);
const ms = (v) => parseFloat(css.getPropertyValue(v)) || 0;
const D1 = ms('--d1'), D2 = ms('--d2'), D3 = ms('--d3'), D4 = ms('--d4');
const EASE = css.getPropertyValue('--ease').trim() || 'ease-out', SPRING = css.getPropertyValue('--spring').trim() || EASE;
const calm = matchMedia('(prefers-reduced-motion: reduce)');
const sleep = (t) => new Promise((r) => setTimeout(r, t));
function anim(el, kf, d = D2, easing = SPRING, extra = {}) {
  if (!el || !el.animate) return null;
  if (calm.matches) kf = kf.map(({ transform, ...k }) => k);   // fades only
  try { return el.animate(kf, { duration: d, easing, ...extra }); } catch (_) { return el.animate(kf, { duration: d, ...extra }); }
}
const rise = (el, extra) => anim(el, [{ opacity: 0, transform: 'translateY(12px)' }, { opacity: 1, transform: 'none' }], D3, SPRING, extra);
const fade = (el, d = D2) => anim(el, [{ opacity: 0 }, { opacity: 1 }], d, EASE);
const pop = (el) => anim(el, [{ opacity: 0, transform: 'scale(.3)' }, { opacity: 1, transform: 'none' }], D3);

// ---------- updating the screen in place ----------
// kids makes `from` hold what `to` holds, keeping every element it can: same
// key, or same tag in the same place. Kept elements keep their state, their
// running animations and their place under the finger.
function kids(from, to, onNew) {
  const keyed = new Map();
  for (const c of from.children) if (c.dataset.key) keyed.set(c.dataset.key, c);
  const wanted = new Set();
  for (const c of to.children) if (c.dataset.key) wanted.add(c.dataset.key);
  let cur = from.firstChild;
  for (const n of [...to.childNodes]) {
    const el = n.nodeType === 1, key = el && n.dataset.key;
    let m = null;
    if (key) { m = keyed.get(key) || null; keyed.delete(key); }
    else {
      while (cur && cur.nodeType === 1 && cur.dataset.key && !wanted.has(cur.dataset.key)) { const nx = cur.nextSibling; cur.remove(); cur = nx; }
      if (cur && cur.nodeType === n.nodeType && cur.nodeName === n.nodeName && !(cur.nodeType === 1 && cur.dataset.key)) m = cur;
    }
    if (m) {
      if (m === cur) cur = cur.nextSibling; else from.insertBefore(m, cur);
      if (el) morph(m, n, onNew);
      else if (m.nodeValue !== n.nodeValue) { m.nodeValue = n.nodeValue; if (onNew && from.dataset.fx !== undefined) anim(from, [{ opacity: 0.2 }, { opacity: 1 }], D3, EASE); }
    } else { from.insertBefore(n, cur); if (el) { if (key) n._src = n.outerHTML; if (onNew) onNew(n); } }
  }
  while (cur) { const nx = cur.nextSibling; cur.remove(); cur = nx; }
}
function morph(from, to, onNew) {
  if (from.dataset.key) { const src = to.outerHTML; if (from._src === src) return; from._src = src; }
  for (const { name } of [...from.attributes]) if (!to.hasAttribute(name)) from.removeAttribute(name);
  for (const { name, value } of [...to.attributes]) if (from.getAttribute(name) !== value) from.setAttribute(name, value);
  if (from.tagName !== 'TEXTAREA' && from.tagName !== 'INPUT') kids(from, to, onNew);
}
function put(el, html) {
  const t = el.cloneNode(false);
  t.innerHTML = html;
  kids(el, t, S.fresh ? null : born);
}
// A whole screen: built once, then updated in place while it stays the same screen.
function mount(cls, html) {
  const same = $app.firstElementChild?.classList.contains(cls);
  if (!same) { $app.innerHTML = html; return; }
  put($app, html);
}
// What a newly arrived element does on its way in.
function born(el) {
  if (el.matches('.agent')) return S.reveal ? reveal(el) : rise(el);
  if (el.matches('.you')) return anim(el, [{ opacity: 0, transform: 'translateY(34px) scale(.8)' }, { transform: 'none' }], D4);
  if (el.matches('.prompt')) return [...el.children].forEach((c, i) => rise(c, { delay: 60 + i * 45, fill: 'backwards' }));
  if (el.matches('.menu')) return anim(el, [{ opacity: 0, transform: 'scale(.86) translateY(8px)' }, { opacity: 1, transform: 'none' }], D2);
  if (el.matches('.bd > *, .tick, .mi > svg, .radio > svg')) return pop(el);
  if (el.matches('.item, .steps, .diff, .now, .note, .keys, .opt, .field, .panel, .pick')) return rise(el);
  if (el.matches('.step, .sec, .mrow, .chip, .todo, .worker, pre')) return fade(el, D3);
}
// Rows that stay glide to their new place.
function flip(box, fn) {
  const at = new Map([...box.children].map((e) => [e, e.getBoundingClientRect().top]));
  fn();
  for (const [e, y] of at) { if (!e.isConnected) continue; const dy = y - e.getBoundingClientRect().top; if (Math.abs(dy) > 1) anim(e, [{ transform: `translateY(${dy}px)` }, { transform: 'none' }], D4); }
}
// New agent text arrives as a whole message. It is laid down at reading
// speed, and faster when more is waiting, so it never falls behind.
function reveal(el) {
  if (calm.matches) return fade(el, D3);
  const units = [];
  const walk = (node, owners) => {
    for (const c of [...node.childNodes]) {
      if (c.nodeType === 3) {
        if (!c.nodeValue.trim()) continue;
        const frag = document.createDocumentFragment();
        for (const part of c.nodeValue.split(/(\s+)/)) {
          if (!part) continue;
          if (/^\s+$/.test(part)) { frag.append(part); continue; }
          const s = document.createElement('span'); s.className = 'w'; s.textContent = part; frag.append(s); units.push([s, owners]);
        }
        c.replaceWith(frag);
      } else if (c.nodeType !== 1) continue;
      else if (c.tagName === 'PRE' || c.tagName === 'HR' || c.classList.contains('tbl')) { c.classList.add('hid'); units.push([c, owners]); }
      else if (c.tagName === 'CODE') { c.classList.add('w'); units.push([c, owners]); }
      else if (/^(P|LI|UL|OL|H\d|BLOCKQUOTE)$/.test(c.tagName)) { c.classList.add('hid'); walk(c, [...owners, c]); }
      else walk(c, owners);
    }
  };
  walk(el, []);
  let i = 0, acc = 1, last = performance.now();
  const step = (now) => {
    if (!el.isConnected) return;
    acc += ((now - last) / 1000) * Math.max(26, (units.length - i) / 2.5); last = now;
    while (acc >= 1 && i < units.length) {
      acc--;
      const [u, owners] = units[i++];
      for (const o of owners) o.classList.remove('hid');
      if (u.classList.contains('hid')) { u.classList.remove('hid'); anim(u, [{ opacity: 0, transform: 'translateY(8px)' }, { opacity: 1, transform: 'none' }], D3, EASE); } else u.classList.add('in');
    }
    if (i < units.length) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

function toast(msg) {
  document.querySelector('.toast')?.remove();
  const t = document.createElement('div');
  t.className = 'toast'; t.textContent = msg;
  document.body.appendChild(t);
  anim(t, [{ opacity: 0, transform: 'translateY(18px) scale(.94)' }, { opacity: 1, transform: 'none' }], D3);
  setTimeout(() => { const a = anim(t, [{ opacity: 1 }, { opacity: 0, transform: 'translateY(10px)' }], D2, EASE); a ? a.finished.then(() => t.remove(), () => t.remove()) : t.remove(); }, 3500);
}

// ---------- what the phone keeps ----------
// The last agent list and the sessions opened lately stay on the phone, so
// the app opens with content and brings it up to date in place.
const LS = {
  get(k) { try { return JSON.parse(localStorage.getItem(k)); } catch (_) { return null; } },
  set(k, v) { try { localStorage.setItem(k, typeof v === 'string' ? v : JSON.stringify(v)); return true; } catch (_) { return false; } },
  del(k) { try { localStorage.removeItem(k); } catch (_) {} },
};
const SS = new Map();
// One session as the phone holds it: a window of the transcript (items from
// position `from`, at revision `rev` of reading `tr`), plus what you have
// done that the Mac has not confirmed yet.
function sess(id) {
  let st = SS.get(id);
  if (!st) {
    st = { id, loaded: false, tr: '', rev: -1, from: 0, total: 0, items: [], stamp: '', full: {}, fetching: {}, pend: [], notes: [], alias: {}, opt: {}, chain: Promise.resolve() };
    const c = LS.get('roi:s:' + id);
    if (c && Array.isArray(c.items) && c.agent) Object.assign(st, c, { loaded: true, cached: true });
    SS.set(id, st);
  }
  return st;
}
function persist(st) {
  clearTimeout(st.saveT);
  st.saveT = setTimeout(() => {
    const items = st.items.slice(-80);
    const text = JSON.stringify({ tr: st.tr, rev: st.rev, from: items.length ? items[0].seq : st.from, total: st.total, items, agent: st.agent, mode: st.mode, modes: st.modes, model: st.model, plan: st.plan, truncated: st.truncated, noTranscript: st.noTranscript });
    const recent = [st.id, ...(LS.get('roi:recent') || []).filter((x) => x !== st.id)];
    for (const old of recent.splice(8)) LS.del('roi:s:' + old);
    if (text.length > 400000 || !LS.set('roi:s:' + st.id, text)) { LS.del('roi:s:' + st.id); recent.shift(); }
    LS.set('roi:recent', recent);
  }, 1200);
}
// Take in what the Mac sent: a first page, or only the items that changed.
function apply(st, j) {
  const fresh = !j.delta || j.tr !== st.tr || !st.loaded;
  let grew = 0, said = 0;
  if (fresh) { st.items = j.items || []; st.full = {}; st.from = j.from || 0; st.alias = {}; }
  else {
    const at = new Map(st.items.map((it, i) => [it.seq, i]));
    for (const it of j.items || []) {
      const i = at.get(it.seq);
      if (i === undefined) { st.items.push(it); grew++; if (it.kind === 'assistant') said++; } else { st.items[i] = it; delete st.full[it.seq]; }
    }
    if (grew) st.items.sort((a, b) => a.seq - b.seq);
  }
  for (const k of ['agent', 'mode', 'modes', 'model', 'prompt', 'plan', 'truncated', 'noTranscript', 'stamp', 'tr', 'rev', 'total']) st[k] = j[k];
  st.loaded = true; st.cached = false;
  settle(st);
  persist(st);
  return { fresh, grew, said };
}
const topSeq = (st) => (st.items.length ? st.items[st.items.length - 1].seq : st.from - 1);
const have = (st) => (st.loaded ? `tr=${encodeURIComponent(st.tr)}&since=${st.rev}&from=${st.from}&stamp=${st.cached ? '' : st.stamp}` : '');

function load(id) {
  const st = sess(id);
  if (st.loading) return st.loading;
  st.loading = api(`/api/agents/${encodeURIComponent(id)}?${have(st)}`)
    .then((j) => { if (!j.unchanged) arrived(st, apply(st, j)); })
    .finally(() => { st.loading = null; });
  return st.loading;
}
// A session changed. Show it if it is the one on screen.
function arrived(st, r) {
  if (S.view?.name !== 'session' || S.view.id !== st.id) return;
  if (!S.stick && r.grew) S.unseen = true;
  S.reveal = !r.fresh && r.said <= 2;
  renderSession(false, r.fresh);
  S.reveal = true;
  if (st.agent.unread) api(`/api/agents/${encodeURIComponent(st.id)}/seen`, {}).catch(() => {});
  connect();
}
// Everything typed at an agent goes in order: a message sent while a model
// switch is still pressing keys in its picker waits for the switch to end.
function queue(st, fn) {
  const p = st.chain.then(fn);
  st.chain = p.catch(() => {});
  return p;
}
// Your own unconfirmed action, shown on top of what the Mac last said. It
// steps back by itself if the Mac never agrees.
function optimistic(st, o, wait = 9000) {
  const turn = (st.turn = (st.turn || 0) + 1);
  Object.assign(st.opt, o);
  for (const k of Object.keys(o)) st.opt['#' + k] = turn;
  setTimeout(() => {
    let any = false;
    for (const k of Object.keys(o)) if (st.opt['#' + k] === turn && k in st.opt) { delete st.opt[k]; any = true; }
    if (any) redraw(st.id);
  }, wait);
}
function settle(st) {
  const o = st.opt;
  let matched = false;
  for (const p of [...st.pend]) {
    const it = st.items.find((x) => x.kind === 'user' && x.seq > p.top && !st.alias[x.seq]);
    if (it) { st.alias[it.seq] = p.key; st.pend.splice(st.pend.indexOf(p), 1); matched = true; }
    else if (Date.now() - p.at > 600000) st.pend.splice(st.pend.indexOf(p), 1);
  }
  const state = st.agent?.state;
  if (o.state) {
    if (o.promptJ ? JSON.stringify(st.prompt) !== o.promptJ : state === 'working' || state === 'need' || matched) { delete o.state; delete o.promptJ; }
  }
  if (o.label && state !== 'working') delete o.label;
}
function shown(st) {
  if (!st.loaded) return null;
  const o = st.opt, on = !!o.state;
  return { ...st, agent: on ? { ...st.agent, state: o.state, updated: o.at } : st.agent, mode: o.mode || st.mode, model: o.model || st.model, prompt: on ? null : st.prompt, o };
}
const redraw = (id) => { if (S.view?.name === 'session' && S.view.id === id) renderSession(false); };

// ---------- routing ----------
function parseRoute() {
  const p = location.hash.replace(/^#\/?/, '').split('/');
  if (p[0] === 'a' && p[1]) return { name: p[2] === 'term' ? 'term' : 'session', id: p[1] };
  if (p[0] === 'new') return { name: 'new' };
  if (p[0] === 'settings') return { name: 'settings' };
  return { name: 'inbox' };
}
function show(v) {
  saveInboxScroll();
  const changed = !S.view || S.view.name !== v.name || S.view.id !== v.id;
  S.view = v;
  clearInterval(pollTimer); pollTimer = null;
  if (v.name === 'inbox') renderInbox(true);
  if (v.name === 'session') {
    if (changed) Object.assign(S, { showKeys: false, modeMenu: false, sheet: null, ans: 0, stick: true, unseen: false });
    renderSession(true);
    ensure(v.id);
  }
  if (v.name === 'term') { renderTerm(); loadScreen(); pollTimer = setInterval(loadScreen, 1000); }
  if (v.name === 'new') openNew();
  if (v.name === 'settings') openSettings();
  connect();
}
// Screens change with a transition when you tapped something in the app. The
// browser's own back gesture already animates, so that one switches plainly.
function route() {
  const v = parseRoute(), prev = S.view;
  const sheet = (x) => x.name === 'new' || x.name === 'settings';
  const kind = !prev || (prev.name === v.name && prev.id === v.id) ? ''
    : prev.name === 'inbox' && v.name === 'session' ? 'open' : prev.name === 'session' && v.name === 'inbox' ? 'close'
    : sheet(v) ? 'up' : sheet(prev) ? 'down' : v.name === 'term' ? 'push' : prev.name === 'term' ? 'pop' : '';
  if (!kind || Date.now() - S.tapAt > 700 || !document.startViewTransition || calm.matches) return show(v);
  // Opening a session: the row's tile and title travel to the header, and back.
  const name = (row, on) => { if (!row) return; row.querySelector('.av').style.viewTransitionName = on ? 'av' : ''; row.querySelector('.t').style.viewTransitionName = on ? 'ttl' : ''; };
  const rowOf = (id) => $app.querySelector(`.item[data-id="${CSS.escape(id)}"]`);
  let row = kind === 'open' ? rowOf(v.id) : null;
  name(row, true);
  root.dataset.nav = kind;
  const done = () => { name(row && row.isConnected ? row : null, false); if (root.dataset.nav === kind) delete root.dataset.nav; };
  try {
    const tr = document.startViewTransition(() => { show(v); if (kind === 'close') { row = rowOf(prev.id); name(row, true); } });
    tr.finished.then(done, done);
  } catch (_) { done(); show(v); }
}
const go = (h) => { S.tapAt = Date.now(); location.hash = h; };
window.addEventListener('hashchange', route);

// ---------- data ----------
async function loadAgents() {
  try {
    const j = await api('/api/agents');
    S.agents = j.agents || []; S.host = j.host; S.loaded = true; S.online = true;
    LS.set('roi:agents', { agents: S.agents, host: S.host });
  } catch (_) { S.online = false; }
  if (S.view?.name === 'inbox') renderInbox();
}
// One stream from the Mac. It says when the list changed, and carries each
// change to the open session as it happens.
let es = null, esFor = '';
function connect(force) {
  const id = S.view?.name === 'session' && sess(S.view.id).loaded ? S.view.id : '';
  if (!force && es && es.readyState !== 2 && esFor === id) return;
  if (es) es.close();
  esFor = id;
  const e = (es = new EventSource('/api/events' + (id ? `?watch=${encodeURIComponent(id)}&${have(sess(id))}` : '')));
  e.onmessage = () => { S.online = true; loadAgents(); };
  e.onerror = () => { if (es !== e) return; S.online = false; if (S.view?.name === 'inbox') renderInbox(); };
  e.addEventListener('session', (ev) => { if (es !== e) return; const st = sess(id); arrived(st, apply(st, JSON.parse(ev.data))); });
  e.addEventListener('gone', () => { if (es === e && S.view?.id === id) gone(id, 'That agent is no longer running.'); });
}
// Open a session's data: at once from what the phone has, then from the Mac.
async function ensure(id) {
  const here = () => S.view?.name === 'session' && S.view.id === id;
  for (let n = 0; here(); n++) {
    try { await load(id); return; } catch (e) {
      if (sess(id).loaded) { if (n === 2) toast('Lost contact with this agent.'); }
      else if (n >= 10) return gone(id, e.message);
      await sleep(1500);
    }
  }
}
function gone(id, why) {
  if (S.view?.name !== 'session' || S.view.id !== id) return;
  SS.delete(id); LS.del('roi:s:' + id);
  const body = $app.querySelector('[data-slot="body"]');
  if (body) body.innerHTML = `<div class="empty"><b>Agent not found</b>${esc(why)}</div>`;
}
setInterval(() => {
  if (!S.online || document.visibilityState === 'visible') loadAgents();
  if (S.view?.name === 'session' && document.visibilityState === 'visible' && (!es || es.readyState !== 1)) load(S.view.id).catch(() => {});
}, 15000);
// A phone that was asleep has a dead stream: start a new one, which brings the open session up to date.
document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') { connect(true); loadAgents(); } });

// ---------- inbox ----------
// The inbox's own scroll position, kept while another screen is showing.
function saveInboxScroll() {
  const v = $app.querySelector('.inboxview');
  if (v) { S.inboxTop = v.querySelector('.scroll').scrollTop; S.inboxChips = v.querySelector('.chips').scrollLeft; }
}
function renderInbox(first) {
  // Chips filter by project; the list itself is grouped by state.
  const projects = new Map();
  for (const a of S.agents) projects.set(a.project, (projects.get(a.project) || 0) + 1);
  if (S.filter !== 'all' && !projects.has(S.filter)) S.filter = 'all';
  const list = S.agents.filter((a) => S.filter === 'all' || a.project === S.filter);
  const need = S.agents.filter((a) => a.state === 'need').length;
  const chips = `<button class="chip ${S.filter === 'all' ? 'on' : ''}" data-key="c:" data-act="filter" data-v="all">All <i>${S.agents.length}</i></button>`
    + [...projects].sort((x, y) => y[1] - x[1] || String(x[0]).localeCompare(y[0])).map(([p, n]) => `<button class="chip hue ${S.filter === p ? 'on' : ''}" data-key="c:${esc(p)}" style="--h:${hueOf(p)}" data-act="filter" data-v="${esc(p)}"><span class="dot"></span>${esc(p)} <i>${n}</i></button>`).join('');
  let rows;
  if (!S.loaded) rows = '<div class="empty">Connecting to your Mac…</div>';
  else if (!list.length) rows = '<div class="empty"><b>No agents running</b>Start one with the plus button, or open Claude Code or Codex in a Rex pane.</div>';
  else {
    rows = [['need', 'Needs you'], ['done', 'Finished'], ['working', 'Working'], ['idle', 'Idle']].map(([st, name]) => {
      const xs = list.filter((a) => a.state === st);
      if (!xs.length) return '';
      return `<div class="sec ${st}" data-key="s:${st}">${name}<span>${xs.length}</span></div>` + xs.map((a) => `
      <button class="item hue ${a.state}" data-key="a:${esc(a.id)}" style="--h:${hueOf(a.project)}" data-act="open" data-id="${esc(a.id)}">
        ${avatar(a.project, a.state)}
        <span class="grow">
          <span class="row"><span class="t clip1 grow">${esc(a.title)}</span><span class="time">${ago(a.updated)}</span></span>
          <span class="l clip1" data-fx><b>${esc(a.project)}</b> ${esc(a.line)}</span>
        </span>
      </button>`).join('');
    }).join('');
  }
  saveInboxScroll();
  const build = first || !$app.querySelector('.inboxview');
  if (build) $app.innerHTML = `
    <div class="view inboxview">
      <div class="top">
        <div class="hd">
          <h1>Agents</h1>
          <button class="hostbtn" data-act="settings"><span class="pill" data-slot="host"></span></button>
        </div>
        <div class="chips"></div>
      </div>
      <div class="sheet"><div class="scroll" style="padding-bottom:104px"></div></div>
      <button class="fab" data-act="new" aria-label="New agent">${I.plus}</button>
    </div>`;
  const sc = $app.querySelector('.scroll'), ch = $app.querySelector('.chips');
  S.fresh = build;
  put($app.querySelector('[data-slot="host"]'), `<span class="dot" style="color:var(--${S.online ? 'done' : S.online === false ? 'need' : 'idle'})"></span><span data-fx>${S.online ? 'Mac online' : S.online === false ? 'Mac unreachable' : 'Connecting…'}</span>`);
  put(ch, chips);
  if (build) put(sc, rows); else flip(sc, () => put(sc, rows));
  S.fresh = false;
  if (build) { sc.scrollTop = S.inboxTop; ch.scrollLeft = S.inboxChips; }
  tint();
  document.title = need ? `(${need}) roi` : 'roi';
  if (navigator.setAppBadge) (need ? navigator.setAppBadge(need) : navigator.clearAppBadge()).catch(() => {});
}

// ---------- session ----------
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

const mdMemo = new Map();
function mdOnce(text) {
  let h = mdMemo.get(text);
  if (h === undefined) { if (mdMemo.size > 600) mdMemo.clear(); mdMemo.set(text, (h = md(text))); }
  return h;
}
// Folded content stays in the page once opened, so it can close as smoothly as it opened.
const fold = (key, inner) => `<div class="fold"><div class="fold-in">${S.open.has(key) || S.was.has(key) ? inner() : ''}</div></div>`;
const lines = (n) => Array.from({ length: n }, (_, i) => `<i class="sk" style="width:${[86, 64, 74][i % 3]}%"></i>`).join('');

function renderDiff(st, t) {
  const d = t.diff, key = 'd:' + t.id, open = S.open.has(key);
  const name = d.file.split('/').slice(-2).join('/');
  const body = () => {
    const full = d.lines ? d : st.full[t.seq]?.diff;
    if (!full || !full.lines) return lines(3);
    return `<pre>${full.lines.map((l) => `<span class="${l[0] === '+' ? 'a' : l[0] === '-' ? 'd' : l === '@@' ? 'h' : ''}">${l === '@@' ? '⋯' : esc(l ? l[0] + ' ' + l.slice(1) : ' ')}</span>`).join('')}${full.more ? `<span class="h">${full.more} more lines on the Mac</span>` : ''}</pre>`;
  };
  return `<div class="diff ${open ? 'open' : ''}" data-key="${esc(key)}"><button class="diff-h" data-act="toggle" data-k="${esc(key)}" data-seq="${t.seq}"><span class="grow clip1">${esc(name)}</span><span class="plus">+${d.added}</span><span class="minus">−${d.removed}</span>${I.chev}</button>${fold(key, body)}</div>`;
}

function renderGroup(st, tools, live) {
  const key = 'g:' + tools[0].id, open = S.open.has(key);
  const words = stepWord(tools);
  const steps = () => tools.map((t) => {
    const sk = 's:' + t.id, so = S.open.has(sk);
    const detail = () => {
      const f = t.lazy ? st.full[t.seq] : t;
      if (!f) return lines(2);
      return `<pre>${esc(f.input || t.summary || '')}</pre>${f.output ? `<pre>${esc(f.output)}</pre>` : t.pending ? '' : '<pre>(no output)</pre>'}`;
    };
    return `<button class="step ${t.error ? 'err' : ''} ${so ? 'open' : ''}" data-act="toggle" data-k="${esc(sk)}" data-seq="${t.seq}"><div class="n">${esc(t.name)}${t.error ? ' · failed' : t.pending ? ' · running' : ''}</div><div class="s ${so ? '' : 'clip1'}">${esc(t.summary || '')}</div>${fold(sk, detail)}</button>`;
  }).join('');
  let h = `<div class="steps ${open ? 'open' : ''}" data-key="${esc(key)}"><button class="steps-h" data-act="toggle" data-k="${esc(key)}">${I.chev}<span class="grow clip1"><b>${tools.length} step${tools.length > 1 ? 's' : ''}</b>${words ? ' · ' + words : ''}</span></button>${fold(key, steps)}</div>`;
  h += tools.filter((t) => t.diff).map((t) => renderDiff(st, t)).join('');
  const last = tools[tools.length - 1];
  if (live && last.pending) h += `<div class="now" data-key="now"><i class="spin"></i><span class="grow clip1">${last.name === 'Bash' ? 'Running' : esc(last.name)} <b>${esc(last.summary || '')}</b></span>${last.ts ? `<span class="time" data-t="${Date.parse(last.ts) || ''}"></span>` : ''}</div>`;
  return h;
}

function renderItems(st, d) {
  const items = d.items;
  const live = d.agent.state === 'working';
  const out = [];
  // Older items are fetched as you scroll up to them.
  if (st.from > 0) out.push(`<div class="skel" data-key="older">${lines(2)}</div>`);
  else if (d.truncated) out.push('<div class="note" data-key="trunc">Earlier messages are on the Mac</div>');
  const notes = [...st.notes];
  const note = (n) => out.push(`<div class="note" data-key="${n.key}">${esc(n.text)}</div>`);
  let group = [];
  const flush = (isLast) => { if (group.length) { out.push(renderGroup(st, group, live && isLast)); group = []; } };
  for (const it of items) {
    while (notes.length && notes[0].at < it.seq) { flush(false); note(notes.shift()); }
    if (it.kind === 'tool') { group.push(it); continue; }
    flush(false);
    if (it.kind === 'user') out.push(`<div class="you" data-key="${st.alias[it.seq] || 'm' + it.seq}">${esc(it.text)}</div>`);
    else if (it.kind === 'assistant') out.push(`<div class="agent" data-key="m${it.seq}">${mdOnce(it.text)}</div>`);
    else out.push(`<div class="note" data-key="m${it.seq}">${esc(it.text)}</div>`);
  }
  flush(true);
  notes.forEach(note);
  for (const p of st.pend) out.push(`<div class="you ${p.state}" data-key="${p.key}">${esc(p.text)}</div>`);
  const last = items[items.length - 1];
  if (live && !(last && last.kind === 'tool' && last.pending)) out.push('<div class="now think" data-key="now"><i class="spin"></i><span class="shim">Thinking</span></div>');
  if (!items.length && !st.pend.length) out.push(d.noTranscript ? '<div class="empty"><b>No conversation to show</b>roi could not find this agent\'s session file. The terminal view still works.</div>' : '<div class="empty">No messages yet.</div>');
  return out.join('');
}
const SKEL = `<div class="skel" data-key="skel"><i class="sk" style="align-self:flex-end;width:68%;height:44px;border-radius:20px 20px 6px 20px"></i><i class="sk" style="height:44px;border-radius:14px"></i><i class="sk" style="width:92%;height:14px"></i><i class="sk" style="width:97%;height:14px;margin-top:-4px"></i><i class="sk" style="width:58%;height:14px;margin-top:-4px"></i></div>`;

function renderPanels(d) {
  let h = '';
  const plan = d.plan || [];
  if (plan.length && d.agent.state !== 'idle') {
    const done = plan.filter((t) => t.status === 'completed').length;
    h += `<div class="panel" data-key="plan"><div class="label">Plan · ${done} of ${plan.length}</div>${plan.map((t) => `<div class="todo ${esc(t.status)}"><span class="bx">${t.status === 'completed' ? I.check : ''}</span><span>${esc(t.text)}</span></div>`).join('')}</div>`;
  }
  const ws = d.agent.workers || [];
  if (ws.length && d.agent.state !== 'idle') {
    h += `<div class="panel" data-key="workers"><div class="label">Workers</div>${ws.map((w) => `<div class="worker"><span class="mark ${w.state === 'working' ? 'working' : w.state === 'blocked' ? 'need' : 'done'}"></span><span class="grow clip1" style="${w.state === 'working' ? 'font-weight:600' : ''}">${esc(w.title)}</span><span class="time">${w.progress != null ? w.progress + '%' : w.state === 'done' ? 'done' : ''}</span></div>`).join('')}</div>`;
  }
  return h;
}

function dockHTML(d) {
  const a = d.agent, o = d.o || {};
  const modes = d.modes?.length ? modeOrder.filter((m) => d.modes.includes(m)) : modeOrder;
  // A mode or model you just chose shows at once and shimmers until the Mac agrees.
  const mode = a.kind === 'claude' && d.mode ? `<button class="key mode" data-act="mode-menu" aria-haspopup="menu" aria-expanded="${S.modeMenu}"><span class="clip1 ${o.mode ? 'shim' : ''}">${esc(modeName[d.mode] || d.mode)}</span>${I.pick}</button>` : '';
  const menu = mode && S.modeMenu ? `<button class="shade" data-key="shade" data-act="mode-close" aria-label="Close"></button><div class="menu" data-key="menu" role="menu"><div class="label">Permission mode</div>${modes.map((m) => `<button class="mi ${m === d.mode ? 'on' : ''}" role="menuitemradio" aria-checked="${m === d.mode}" data-act="mode-set" data-v="${m}"><span class="grow">${modeName[m]}</span>${m === d.mode ? I.check : ''}</button>`).join('')}</div>` : '';
  if (a.state === 'need' && d.prompt) {
    const p = d.prompt;
    const opts = (p.options || []).map((x) => `<button class="opt ${S.ans === x.n ? 'chosen' : ''}" data-act="answer" data-n="${x.n}"><span class="num">${x.n}</span><span class="grow"><div class="ol">${esc(x.label)}</div>${x.description ? `<div class="od">${esc(x.description)}</div>` : ''}</span><span class="tick">${sv('<path d="M5 12.5l4.5 4.5L19 7.5"/>', 16, 2.6)}</span></button>`).join('');
    const fallback = opts ? '' : `<div class="hint" style="margin:10px 0">roi could not read the choices from the screen. Answer in the terminal.</div><button class="btn pri wide" data-act="term" style="margin-bottom:10px">${I.term} Open terminal</button>`;
    return `<div class="prompt" data-key="prompt">
      <div class="k">${p.kind === 'question' ? I.ask + 'Question' : p.kind === 'permission' ? I.shield + 'Permission' : 'Waiting on you'}</div>
      <div class="q">${esc(p.title)}</div>
      ${p.body ? `<div class="cmd">${esc(p.body)}</div>` : ''}
      <div class="opts ${esc(p.kind)} ${S.ans ? 'answered' : ''}">${opts}</div>${fallback}
      <div class="field"><textarea rows="1" placeholder="Or type a reply" data-role="answer"></textarea><button class="send" data-act="answer-text" aria-label="Send">${I.send}</button></div>
    </div>`;
  }
  const working = a.state === 'working';
  const keys = working
    ? `<div class="keys" data-key="keys"><button class="key stop" data-act="key" data-keys="escape">${I.stop}Stop</button><button class="key" data-act="key" data-keys="ctrl+c">⌃C</button>${mode}</div>`
    : S.showKeys ? `<div class="keys" data-key="keys">${[['esc', 'escape'], ['⌃C', 'ctrl+c'], ['↑', 'up'], ['↓', 'down'], ['⏎', 'enter']].map(([l, k]) => `<button class="key grow" data-act="key" data-keys="${k}">${l}</button>`).join('')}</div>` : '';
  // While idle, the model and the permission mode sit above the reply field.
  const model = `<button class="key model" data-act="model-open" aria-haspopup="dialog" aria-label="Model${d.model ? ': ' + esc(d.model.name) : ''}. Tap to change.">${I.chip}<span class="clip1 grow ${o.model ? 'shim' : ''}">${d.model ? `<b>${esc(d.model.name)}</b>${d.model.effort ? ' · ' + esc(d.model.effort) : ''}` : 'Model'}</span>${I.pick}</button>`;
  const ctl = working ? '' : `<div class="keys ctl" data-key="ctl">${model}${mode}</div>`;
  return `${menu}${ctl}${keys}<div class="field" data-key="field"><textarea rows="1" placeholder="${working ? 'Steer it. Read at the next step' : 'Reply'}" data-role="send"></textarea>${working ? '' : `<button class="iconbtn" data-key="kb" data-act="keys" aria-label="Keys" style="color:var(--ink2);width:40px">${I.keys}</button>`}<button class="send" data-key="send" data-act="send" aria-label="Send">${I.send}</button></div>`;
}

// renderSession(fresh) builds the screen; later calls update it in place.
// quiet is for content that should simply be there: an older page, a reload.
function renderSession(fresh, quiet) {
  const id = S.view.id, st = sess(id), d = shown(st);
  const a = d?.agent || S.agents.find((x) => x.id === id) || { title: '', project: '', kind: 'claude', state: 'idle' };
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
        <div class="sheet"><div class="scroll"><div class="panels" data-slot="panels"></div><div class="body" data-slot="body"></div></div><button class="latest" data-act="latest">${I.down}Latest</button></div>
        <div class="dock" data-slot="dock"></div>
        <div data-slot="overlay"></div>
      </div>`;
    S.painted = false;
    follow();
    renderSheet();
  }
  const q = (s) => $app.querySelector(`[data-slot="${s}"]`);
  $app.querySelector('.sessionview').style.setProperty('--h', hueOf(a.project));
  tint();
  S.fresh = !S.painted || !!quiet;
  put(q('av'), a.project ? avatar(a.project, '', 'sm') : '');
  q('title').textContent = a.title || 'Starting…';
  q('sub').textContent = a.project ? `${a.project} · ${kindName(a.kind)}` : '';
  const steps = d ? d.items.filter((it) => it.kind === 'tool').length : 0;
  const right = !d ? '' : a.state === 'need' && d.prompt?.kind ? d.prompt.kind
    : a.state === 'working' ? (steps ? `${steps}${st.from > 0 ? '+' : ''} step${steps > 1 ? 's' : ''}` : '')
    : a.kind === 'claude' && d.mode ? (modeName[d.mode] || d.mode).toLowerCase() : '';
  const when = a.updated ? since(a.updated) : '';
  const label = d?.o.label || `${stateName[a.state] || ''}${when ? ' · ' + when : ''}`;
  put(q('status'), `<span class="st ${a.state}">${a.state === 'working' ? '<i class="spin"></i>' : '<i class="dot"></i>'}<b data-fx>${esc(label)}</b></span><span class="grow"></span><span class="rt mono" data-fx>${esc(right)}</span>`);
  const sc = $app.querySelector('.scroll'), dock = q('dock');
  if (!d) {
    // Nothing from the Mac yet: the outline of a conversation, and a reply bar that is already there.
    put(q('body'), SKEL);
    put(dock, dockHTML({ agent: { ...a, state: a.state === 'need' ? 'idle' : a.state }, mode: null, model: null }));
    sendState();
    S.fresh = false;
    return;
  }
  const first = !S.painted, hadSkel = !!q('body').querySelector('[data-key="skel"]');
  put(q('panels'), renderPanels(d));
  put(q('body'), renderItems(st, d));
  // The reply bar changes shape in place, keeping what you have typed.
  const old = dock.querySelector('textarea'), typed = old ? old.value : '', h0 = dock.offsetHeight;
  put(dock, dockHTML(d));
  const ta = dock.querySelector('textarea');
  if (ta && ta !== old && typed && !ta.value) { ta.value = typed; autosize(ta); }
  const h1 = dock.offsetHeight;
  if (!S.fresh && h0 !== h1 && !S.modeMenu) { dock.style.overflow = 'hidden'; const an = anim(dock, [{ height: h0 + 'px' }, { height: h1 + 'px' }], D3); const end = () => { dock.style.overflow = ''; }; an ? an.finished.then(end, end) : end(); }
  S.fresh = false;
  sendState();
  if (first) { S.painted = true; S.stick = true; sc.scrollTop = sc.scrollHeight; if (hadSkel) fade(q('body'), D3); }
  else if (S.stick) sc.scrollTop = sc.scrollHeight;
  $app.querySelector('.latest').classList.toggle('show', S.unseen && !S.stick);
  clock();
  wanted(st);
  older(st);
}
// The view follows new text while you are at the bottom, and offers a way back when you are not.
function follow() {
  const sc = $app.querySelector('.sessionview .scroll'), pill = $app.querySelector('.latest');
  const away = () => sc.scrollHeight - sc.scrollTop - sc.clientHeight;
  const check = () => { if (S.stick) S.unseen = false; pill.classList.toggle('show', S.unseen && away() > 150); };
  S.ro?.disconnect();
  S.ro = new ResizeObserver(() => { if (S.stick) sc.scrollTop = sc.scrollHeight; check(); });
  S.ro.observe(sc); S.ro.observe(sc.querySelector('.body'));
  sc.addEventListener('scroll', () => { S.stick = away() < 60; check(); if (S.view?.name === 'session') older(sess(S.view.id)); }, { passive: true });
}
// Fetch the input, output and diff lines of whatever is open and still missing them.
function wanted(st) {
  for (const it of st.items) {
    if (it.kind !== 'tool' || !it.lazy || st.full[it.seq] || st.fetching[it.seq]) continue;
    if (S.open.has('s:' + it.id) || (it.diff && S.open.has('d:' + it.id))) whole(st, it.seq);
  }
}
function whole(st, seq) {
  if (st.full[seq] || st.fetching[seq] || !st.loaded) return;
  st.fetching[seq] = true;
  api(`/api/agents/${encodeURIComponent(st.id)}/items/${seq}?tr=${encodeURIComponent(st.tr)}`)
    .then((j) => { st.full[seq] = j.item; redraw(st.id); }, () => {})
    .finally(() => { setTimeout(() => { delete st.fetching[seq]; }, 2000); });
}
// Scrolling back: bring in the page before the oldest item on screen, without moving what you are reading.
async function older(st) {
  const sc = $app.querySelector('.sessionview .scroll');
  if (!sc || !st.loaded || st.from <= 0 || st.olderBusy || sc.scrollTop > 500 || Date.now() < (st.olderWait || 0)) return;
  st.olderBusy = true;
  try {
    const j = await api(`/api/agents/${encodeURIComponent(st.id)}/items?tr=${encodeURIComponent(st.tr)}&before=${st.from}&n=60`);
    st.items = [...j.items, ...st.items]; st.from = j.from;
    if (S.view?.name === 'session' && S.view.id === st.id) {
      const box = $app.querySelector('.sessionview .scroll'), below = box.scrollHeight - box.scrollTop, stuck = S.stick;
      renderSession(false, true);
      box.scrollTop = box.scrollHeight - below;
      S.stick = stuck;
    }
  } catch (_) { st.olderWait = Date.now() + 5000; }
  st.olderBusy = false;
  redraw(st.id);
}
// The send button is grey while there is nothing to send.
function sendState() { for (const f of $app.querySelectorAll('.dock .field')) { const b = f.querySelector('.send'), t = f.querySelector('textarea'); if (b && t) b.classList.toggle('off', !t.value.trim()); } }
// The running step shows how long it has been going.
function clock() {
  for (const el of $app.querySelectorAll('[data-t]')) {
    const t = +el.dataset.t;
    if (!t) continue;
    const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
    el.textContent = s >= 3600 ? `${Math.floor(s / 3600)}:${String(Math.floor(s / 60) % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  }
}
setInterval(clock, 1000);

// ---------- model sheet ----------
// The lists come from the agent's own /model picker, read off its screen.
// The sheet opens at once, with the list the phone last saw or its outline.
function renderSheet() {
  const el = $app.querySelector('[data-slot="overlay"]');
  if (!el) return;
  const m = S.sheet;
  if (!m) { el.innerHTML = ''; el.dataset.step = ''; return; }
  const tool = (sess(S.view.id).agent?.kind || 'claude') === 'codex' ? 'Codex' : 'Claude Code';
  const second = m.step === 'efforts';
  const tag = (r) => (r.current ? '<i class="tag on">Current</i>' : r.default ? '<i class="tag">Default</i>' : '');
  const skel = Array.from({ length: 4 }, (_, i) => `<div class="mrow sk-row"><span class="grow"><i class="sk" style="width:${[34, 26, 30, 24][i]}%;height:16px"></i>${second ? '' : `<i class="sk" style="width:${[72, 64, 58, 66][i]}%;height:12px;margin-top:8px"></i>`}</span></div>`).join('');
  let body;
  if (m.error) body = `<div class="bs-msg"><b>That did not work</b><span>${esc(m.error)}</span><button class="btn" data-act="model-retry">Try again</button></div>`;
  else if (m.loading) body = skel;
  else if (second) body = m.efforts.map((r) => `<button class="mrow ${r.current && m.pick.current ? 'on' : ''}" data-act="model-effort" data-v="${esc(r.label)}"><span class="grow"><span class="mr-t">${esc(r.label)}${r.current && m.pick.current ? tag(r) : r.default ? '<i class="tag">Default</i>' : ''}</span>${r.desc ? `<span class="mr-d">${esc(r.desc)}</span>` : ''}</span></button>`).join('');
  else body = m.models.map((r) => `<button class="mrow ${r.current ? 'on' : ''}" data-act="model-pick" data-n="${r.n}"><span class="grow"><span class="mr-t">${esc(r.label)}${tag(r)}</span>${r.desc ? `<span class="mr-d">${esc(r.desc)}</span>` : ''}</span>${I.chev}</button>`).join('');
  const bs0 = el.querySelector('.bsheet'), h0 = bs0?.offsetHeight, same = el.dataset.step === m.step;
  S.fresh = !bs0;
  put(el, `<button class="scrim" data-key="scrim" data-act="model-close" aria-label="Close"></button>
    <div class="bsheet" data-key="bs" role="dialog" aria-label="Model">
      <div class="grab"></div>
      <div class="bs-h ${second ? 'back' : ''}">
        ${second ? `<button class="iconbtn" data-key="back" data-act="model-back" aria-label="Back to models">${I.back}</button>` : ''}
        <div class="grow"><div class="bs-t clip1" data-fx>${second ? esc(m.pick.label) : 'Model'}</div><div class="bs-s" data-fx>${second ? 'Reasoning effort' : tool}</div></div>
        <button class="iconbtn" data-key="x" data-act="model-close" aria-label="Close">${I.x}</button>
      </div>
      <div class="bs-b">${body}</div>
      <div class="bs-f">This session only. Your default stays the same.</div>
    </div>`);
  S.fresh = false;
  el.dataset.step = m.step;
  const bs = el.querySelector('.bsheet');
  if (bs0 && h0 !== bs.offsetHeight) anim(bs, [{ height: h0 + 'px' }, { height: bs.offsetHeight + 'px' }], D3);
  if (bs0 && !same) anim(bs.querySelector('.bs-b'), [{ opacity: 0, transform: `translateX(${second ? 36 : -36}px)` }, { opacity: 1, transform: 'none' }], D3);
}
function closeSheet(from = 0) {
  const bs = $app.querySelector('.bsheet'), done = () => { S.sheet = null; renderSheet(); };
  const a = bs && anim(bs, [{ transform: `translateY(${from}px)` }, { transform: 'translateY(105%)' }], D2 + 40, EASE, { fill: 'forwards' });
  if (!a) return done();
  bs.style.transform = '';
  anim($app.querySelector('.scrim'), [{ opacity: 1 }, { opacity: 0 }], D2, EASE, { fill: 'forwards' });
  a.finished.then(done, done);
}
// Each step checks that the sheet it started in is still the one showing.
async function openModels(fresh) {
  const id = S.view.id, st = sess(id), known = !fresh && S.lists[id];
  const m = (S.sheet = { step: 'models', models: known || null, loading: !known });
  renderSheet();
  try {
    const j = await queue(st, () => api(`/api/agents/${encodeURIComponent(id)}/models${fresh ? '?fresh=1' : ''}`));
    S.lists[id] = j.models;
    if (S.sheet !== m) return;
    S.sheet = { step: 'models', models: j.models };
  } catch (e) { if (S.sheet !== m || known) return; S.sheet = { step: 'models', error: e.message }; }
  renderSheet();
}
async function pickModel(pick, models) {
  const id = S.view.id, st = sess(id), key = `${st.agent.kind}|${pick.label}`, known = S.effs[key];
  const m = (S.sheet = { step: 'efforts', pick, models, efforts: known || null, loading: !known });
  renderSheet();
  if (known) return;
  try {
    const j = await queue(st, () => api(`/api/agents/${encodeURIComponent(id)}/efforts?n=${pick.n}&label=${encodeURIComponent(pick.label)}`));
    S.effs[key] = j.efforts;
    if (S.sheet !== m) return;
    if (!j.efforts.length) return applyModel('');
    S.sheet = { step: 'efforts', pick, models, efforts: j.efforts };
  } catch (e) { if (S.sheet !== m) return; S.sheet = { step: 'efforts', pick, models, error: e.message }; }
  renderSheet();
}
// The sheet leaves at once and the button shows the new model, shimmering
// until the agent's own picker has confirmed it.
function applyModel(effort) {
  const id = S.view.id, st = sess(id), pick = S.sheet.pick;
  const first = (pick.desc || '').split(' · ')[0];
  const name = st.agent.kind === 'claude' && (pick.desc || '').includes(' · ') && !first.startsWith('Use ') ? first : pick.label;
  const guess = { name, effort: effort.toLowerCase() };
  optimistic(st, { model: guess }, 40000);
  closeSheet(); renderSession(false);
  queue(st, () => api(`/api/agents/${encodeURIComponent(id)}/model`, { n: pick.n, label: pick.label, effort })).then((j) => {
    st.model = { name: j.name, effort: j.effort };
    for (const r of S.lists[id] || []) r.current = r.n === pick.n;
  }, (e) => toast(e.message)).finally(() => { if (st.opt.model === guess) delete st.opt.model; redraw(id); });
}
function closeMenu() {
  const menu = $app.querySelector('.menu'), done = () => { S.modeMenu = false; if (S.view?.name === 'session') renderSession(false); };
  const a = menu && anim(menu, [{ opacity: 1, transform: 'none' }, { opacity: 0, transform: 'scale(.92) translateY(6px)' }], D1 + 30, EASE, { fill: 'forwards' });
  a ? a.finished.then(done, done) : done();
}

function autosize(ta) { ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'; }

// ---------- terminal ----------
function renderTerm() {
  const a = S.agents.find((x) => x.id === S.view.id) || SS.get(S.view.id)?.agent || { title: 'Terminal' };
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
  mount('newview', `
    <div class="view newview ${f.cwd ? 'hue' : ''}" style="--h:${hueOf(f.projects.find((p) => p.cwd === f.cwd)?.name || name(f.cwd || ''))}">
      <div class="sheet-h"><h2>New agent</h2><button class="iconbtn" data-act="home" aria-label="Close">${I.x}</button></div>
      <div class="sheet"><div class="scroll"><div class="form">
        <div class="seg">${f.kinds.map((k) => `<button class="${f.kind === k ? 'on' : ''}" data-act="kind" data-v="${esc(k)}">${k === 'claude' ? 'Claude Code' : k === 'codex' ? 'Codex' : esc(k)}</button>`).join('')}</div>
        <div><div class="label">What should it do</div><textarea class="area" placeholder="Describe the task. Leave empty to start a blank session.">${esc(f.prompt)}</textarea></div>
        <div>${where}</div>
      </div></div></div>
      <div class="dock"><button class="btn pri wide" data-act="spawn" ${f.cwd && !f.busy ? '' : 'disabled'}>${f.busy ? 'Starting…' : f.cwd ? 'Start in ' + esc(name(f.cwd)) : 'Choose a folder'}</button></div>
    </div>`);
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
  mount('setview', `
    <div class="view setview">
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
    </div>`);
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

// ---------- touch ----------
// A control shows it was touched on the way down, and loading starts then
// too: by the time the finger lifts, the session or the step is on its way.
let pressed = null;
function down(b) {
  pressed = b; b.classList.add('pr');
  const act = b.dataset.act;
  if (act === 'open') load(b.dataset.id).catch(() => {});
  else if (act === 'toggle' && b.dataset.seq && S.view?.name === 'session') { const st = sess(S.view.id), it = st.items.find((x) => x.seq === +b.dataset.seq); if (it?.lazy) whole(st, it.seq); }
}
function up() { if (pressed) pressed.classList.remove('pr'); pressed = null; }
$app.addEventListener('pointerdown', (ev) => { const b = ev.target.closest('button'); if (b && !b.disabled) down(b); });
for (const e of ['pointerup', 'pointercancel', 'pointerleave']) $app.addEventListener(e, up);
// A scroll that starts on a row is not a press.
$app.addEventListener('scroll', up, { capture: true, passive: true });
// The model sheet can be pulled down by its header.
$app.addEventListener('pointerdown', (ev) => {
  const h = ev.target.closest('.bs-h, .grab');
  if (!h || ev.target.closest('button')) return;
  const bs = h.closest('.bsheet'), y0 = ev.clientY;
  let dy = 0;
  const move = (e) => { dy = Math.max(0, e.clientY - y0); bs.style.transform = `translateY(${dy}px)`; };
  const end = () => {
    removeEventListener('pointermove', move); removeEventListener('pointerup', end); removeEventListener('pointercancel', end);
    if (dy > 90) return closeSheet(dy);
    bs.style.transform = '';
    if (dy) anim(bs, [{ transform: `translateY(${dy}px)` }, { transform: 'none' }], D3);
  };
  addEventListener('pointermove', move); addEventListener('pointerup', end); addEventListener('pointercancel', end);
});

// ---------- events ----------
$app.addEventListener('click', (ev) => {
  const el = ev.target.closest('[data-act]');
  if (!el) return;
  const id = S.view?.id, base = id ? `/api/agents/${encodeURIComponent(id)}` : '';
  const field = () => el.closest('.field')?.querySelector('textarea, input') || $app.querySelector('.dock textarea, .dock input');
  const st = S.view?.name === 'session' ? sess(id) : null;
  const now = () => new Date().toISOString();
  switch (el.dataset.act) {
    case 'filter': S.filter = el.dataset.v; renderInbox(); break;
    case 'open': go('/a/' + el.dataset.id); break;
    case 'home': go('/'); break;
    case 'new': S.form = null; go('/new'); break;
    case 'settings': go('/settings'); break;
    case 'term': go(`/a/${id}/term`); break;
    case 'back-session': go('/a/' + id); break;
    case 'keys': S.showKeys = !S.showKeys; renderSession(false); break;
    case 'toggle': {
      const k = el.dataset.k;
      if (window.getSelection().toString()) break;
      if (S.open.has(k)) S.open.delete(k); else { S.open.add(k); S.was.add(k); }
      S.stick = false;   // the header you tapped stays where it is
      renderSession(false);
      break;
    }
    case 'latest': { const sc = $app.querySelector('.scroll'); S.stick = true; S.unseen = false; sc.scrollTo({ top: sc.scrollHeight, behavior: calm.matches ? 'auto' : 'smooth' }); el.classList.remove('show'); break; }
    case 'model-open': S.modeMenu = false; renderSession(false); openModels(false); break;
    case 'model-close': closeSheet(); break;
    case 'model-back': S.sheet = { step: 'models', models: S.sheet.models }; if (S.sheet.models) renderSheet(); else openModels(false); break;
    case 'model-retry': S.sheet.pick ? pickModel(S.sheet.pick, S.sheet.models) : openModels(true); break;
    case 'model-pick': { const r = S.sheet.models.find((x) => x.n === +el.dataset.n); if (r) pickModel(r, S.sheet.models); break; }
    case 'model-effort': applyModel(el.dataset.v); break;
    case 'mode-menu': if (S.modeMenu) closeMenu(); else { S.modeMenu = true; renderSession(false); } break;
    case 'mode-close': closeMenu(); break;
    case 'mode-set': {
      // The tick moves and the menu leaves at once; the label shimmers until
      // the agent has been stepped round to the mode.
      const m = el.dataset.v;
      if (m !== shown(st).mode) {
        optimistic(st, { mode: m }, 40000);
        queue(st, () => api(base + '/mode', { mode: m })).then((j) => { st.mode = j.mode; }, (e) => toast(e.message)).finally(() => { if (st.opt.mode === m) delete st.opt.mode; redraw(id); });
      }
      renderSession(false);
      setTimeout(closeMenu, 150);
      break;
    }
    case 'key': {
      const keys = el.dataset.keys.split(' ');
      if (st.agent.state === 'working' && (keys[0] === 'escape' || keys[0] === 'ctrl+c')) { optimistic(st, { label: 'Stopping…' }, 6000); renderSession(false); }
      queue(st, () => api(base + '/key', { keys })).catch((e) => { delete st.opt.label; toast(e.message); redraw(id); });
      break;
    }
    case 'send': {
      const t = field(), text = t.value.trim();
      if (!text) break;
      t.value = ''; autosize(t);
      // Your message is on screen in the same frame, slightly pale; it turns
      // solid when the Mac has it, and takes its place when the agent's own
      // record shows it.
      const p = { key: 'p' + ++uid, text, state: 'sending', top: topSeq(st), at: Date.now() };
      st.pend.push(p);
      optimistic(st, { state: 'working', at: now() });
      S.stick = true;
      renderSession(false);
      queue(st, () => api(base + '/send', { text })).then(() => { p.state = ''; redraw(id); }, (e) => {
        const i = st.pend.indexOf(p);
        if (i >= 0) st.pend.splice(i, 1);
        delete st.opt.state;
        redraw(id);
        const f = S.view?.id === id && $app.querySelector('.dock textarea');
        if (f && !f.value) { f.value = text; autosize(f); sendState(); }   // nothing is lost
        toast(e.message);
      });
      break;
    }
    case 'answer': {
      if (S.ans) break;
      // The choice fills and holds for a moment, then the card folds away.
      const n = +el.dataset.n, said = el.querySelector('.ol').textContent, promptJ = JSON.stringify(st.prompt);
      const note = { key: 'n' + ++uid, at: topSeq(st), text: `You answered: ${said}` };
      S.ans = n; renderSession(false);
      const sent = queue(st, () => api(base + '/answer', { option: n }));
      setTimeout(() => { S.ans = 0; st.notes.push(note); optimistic(st, { state: 'working', promptJ, at: now() }); settle(st); if (S.view?.id === id) { S.stick = true; redraw(id); } }, 240);
      sent.catch((e) => { setTimeout(() => { st.notes = st.notes.filter((x) => x !== note); delete st.opt.state; delete st.opt.promptJ; redraw(id); toast(e.message); }, 260); });
      break;
    }
    case 'answer-text': {
      const t = field(), text = t.value.trim();
      if (!text) break;
      t.value = '';
      const p = { key: 'p' + ++uid, text, state: 'sending', top: topSeq(st), at: Date.now() };
      st.pend.push(p);
      optimistic(st, { state: 'working', promptJ: JSON.stringify(st.prompt), at: now() });
      S.stick = true;
      renderSession(false);
      queue(st, () => api(base + '/answer', { text })).then(() => { p.state = ''; redraw(id); }, (e) => { st.pend = st.pend.filter((x) => x !== p); delete st.opt.state; delete st.opt.promptJ; redraw(id); toast(e.message); });
      break;
    }
    case 'fit': S.term.fit = !S.term.fit; el.classList.toggle('on', S.term.fit); fitTerm(); break;
    case 'mod': S.term[el.dataset.m] = !S.term[el.dataset.m]; el.classList.toggle('on', S.term[el.dataset.m]); break;
    case 'tkey': termKey(el.dataset.keys); break;
    case 'tsend': { const t = field(); const text = t.value; if (!text) { termKey('enter'); break; } t.value = ''; api(base + '/key', { text, keys: ['enter'] }).then(() => setTimeout(loadScreen, 150)).catch((e) => toast(e.message)); break; }
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
  if (t.matches('.dock textarea')) { autosize(t); sendState(); }
  // In the terminal, a held ⌃ or ⌥ applies to the next typed character.
  if (t.dataset.role === 'tinput' && (S.term.ctrl || S.term.alt) && /^[a-z0-9]$/i.test(t.value)) { const c = t.value.toLowerCase(); t.value = ''; termKey(c); }
});
$app.addEventListener('keydown', (ev) => {
  if (ev.key !== 'Enter') return;
  const t = ev.target;
  if (t.dataset.role === 'tinput') { ev.preventDefault(); $app.querySelector('[data-act="tsend"]').click(); }
  else if (t.matches('.dock textarea') && (ev.metaKey || ev.ctrlKey)) { ev.preventDefault(); t.closest('.field').querySelector('.send').click(); }
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
// Start with the list the phone last saw; the Mac corrects it a moment later.
{ const c = LS.get('roi:agents'); if (c && Array.isArray(c.agents)) { S.agents = c.agents; S.host = c.host || ''; S.loaded = true; } }
show(parseRoute());
loadAgents();
