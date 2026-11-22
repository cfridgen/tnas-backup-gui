'use strict';
// tnas-backup-gui – Weboberfläche (ohne Frameworks)

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
let S = { user: null, csrf: '' };
let refreshTimer = null;

// ---------- Hilfen ----------
function bytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']; let i = 0;
  while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
  return (n >= 100 || i === 0 ? n.toFixed(0) : n.toFixed(1)).replace('.', ',') + ' ' + u[i];
}
function dur(sec) {
  sec = Math.floor(sec); const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
  return d ? `${d} T ${h} Std` : h ? `${h} Std ${m} Min` : `${m} Min`;
}
function ago(t) {
  const s = (Date.now() - new Date(t)) / 1000;
  if (s < 3600) return `vor ${Math.max(1, Math.round(s / 60))} Min`;
  if (s < 86400 * 2) return `vor ${Math.round(s / 3600)} Std`;
  return `vor ${Math.round(s / 86400)} Tagen`;
}
const fmtTime = t => new Date(t).toLocaleString('de-DE', { dateStyle: 'medium', timeStyle: 'short' });
const num = (x, d = 1) => Number(x).toFixed(d).replace('.', ',');
const pill = (cls, txt) => `<span class="pill ${cls}">${esc(txt)}</span>`;
function bar(pct) { const c = pct >= 90 ? 'bad' : pct >= 75 ? 'warn' : ''; return `<div class="bar ${c}"><div data-pct="${Math.min(100, pct)}"></div></div>`; }
function applyBars(root) { $$('.bar > div[data-pct]', root).forEach(d => { d.style.width = d.dataset.pct + '%'; }); }

function toast(msg, bad = false) {
  const t = $('#toast'); t.textContent = msg; t.className = 'toast' + (bad ? ' bad' : '');
  clearTimeout(t._h); t._h = setTimeout(() => t.classList.add('hidden'), bad ? 7000 : 3500);
}

async function api(path, body, retried) {
  const opt = { headers: {} };
  if (body !== undefined) {
    opt.method = 'POST'; opt.body = JSON.stringify(body);
    opt.headers['Content-Type'] = 'application/json'; opt.headers['X-CSRF-Token'] = S.csrf;
  }
  const r = await fetch('/api/' + path, opt);
  let data = {}; try { data = await r.json(); } catch (_) { }
  if (r.status === 401 && path !== 'login' && path !== 'session') {
    // Sitzung abgelaufen: über das gemerkte Gerät still neu anmelden und Aufruf wiederholen
    if (!retried) {
      try { const s = await api('session'); if (s.user) { S = s; return api(path, body, true); } } catch (_) { }
    }
    showLogin(); throw new Error(data.error || 'Nicht angemeldet');
  }
  if (!r.ok) throw new Error(data.error || ('Fehler ' + r.status));
  return data;
}

// ---------- Dialog ----------
function modal(html, onOk, okLabel = 'Speichern', danger = false) {
  const box = $('#modalBox');
  box.classList.remove('wide');
  box.innerHTML = html + `<div class="err" id="mErr"></div><div class="modal-actions">
    <button id="mCancel">Abbrechen</button>${onOk ? `<button id="mOk" class="${danger ? 'danger solid' : 'primary'}">${esc(okLabel)}</button>` : ''}</div>`;
  $('#modal').classList.remove('hidden');
  const close = () => $('#modal').classList.add('hidden');
  $('#mCancel').onclick = close;
  if (onOk) $('#mOk').onclick = async () => {
    $('#mOk').disabled = true; $('#mErr').textContent = '';
    try { await onOk(box); close(); } catch (e) { $('#mErr').textContent = e.message; } finally { if ($('#mOk')) $('#mOk').disabled = false; }
  };
  const first = $('input,select', box); if (first) first.focus();
  return box;
}
function confirmBox(title, text, okLabel, fn) {
  modal(`<h2>${esc(title)}</h2><p>${text}</p>`, fn, okLabel, true);
}

// ---------- Anmeldung ----------
function showLogin() {
  clearInterval(refreshTimer);
  $('#app').classList.add('hidden'); $('#login').classList.remove('hidden');
  $('#loginUser').focus();
}
$('#loginForm').onsubmit = async e => {
  e.preventDefault(); $('#loginErr').textContent = '';
  try {
    const d = await api('login', { user: $('#loginUser').value.trim(), password: $('#loginPass').value, remember: $('#loginRemember').checked });
    S.user = d.user; S.csrf = d.csrf; $('#loginPass').value = ''; start();
  } catch (err) { $('#loginErr').textContent = err.message; }
};
$('#logoutBtn').onclick = async () => { try { await api('logout', {}); } catch (_) { } S = { user: null, csrf: '' }; showLogin(); };
$('#navToggle').onclick = () => $('#nav').classList.toggle('open');
document.addEventListener('keydown', e => {
  if (e.key === 'Escape' && !$('#modal').classList.contains('hidden')) { $('#modal').classList.add('hidden'); $('#modalBox').innerHTML = ''; }
});

function start() {
  $('#login').classList.add('hidden'); $('#app').classList.remove('hidden');
  $('#whoami').textContent = S.user;
  if (!location.hash) location.hash = '#/dashboard'; else route();
}

// ---------- Seiten ----------
const pages = {};
function route() {
  clearInterval(refreshTimer);
  const p = (location.hash.match(/^#\/(\w+)/) || [])[1] || 'dashboard';
  $$('#nav a').forEach(a => a.classList.toggle('active', a.dataset.page === p));
  $('#nav').classList.remove('open');
  const fn = pages[p] || pages.dashboard;
  $('#main').innerHTML = '<p class="muted">Lade …</p>';
  fn().catch(e => { $('#main').innerHTML = `<div class="banner bad">${esc(e.message)}</div>`; });
}
window.addEventListener('hashchange', () => { if (S.user) route(); });

// Übersicht
pages.dashboard = async () => {
  const draw = async () => {
    const d = await api('dashboard');
    const memUsed = d.mem_total - d.mem_avail, memPct = memUsed * 100 / d.mem_total;
    const b = d.backup; let banner;
    if (!b.last) banner = `<div class="banner warn">Noch kein Backup von ${esc(b.source)} vorhanden.</div>`;
    else if (b.ok) banner = `<div class="banner ok">✔ Letztes Backup von ${esc(b.source)}: <b>${fmtTime(b.last.time)}</b> (${ago(b.last.time)}), ${b.last.shares.length} Freigaben, ${b.count} Versionen gespeichert.</div>`;
    else banner = `<div class="banner bad">⚠ Letztes Backup von ${esc(b.source)} ist älter als ${b.max_age_h} Stunden: ${fmtTime(b.last.time)} (${ago(b.last.time)}).</div>`;
    const disks = d.disks.filter(x => x.role === 'Daten').map(x => `<tr><td>${esc(x.dev)}</td><td>${esc(x.model)}</td><td>${bytes(x.size)}</td>
      <td>${x.sleeping ? pill('off', 'schläft') : x.health === 'PASSED' && !x.realloc && !x.pending ? pill('ok', 'OK') : x.health ? pill('bad', x.health === 'PASSED' ? 'Warnung' : 'FEHLER') : pill('off', '–')}</td>
      <td>${x.temp_c ? x.temp_c + ' °C' : '–'}</td></tr>`).join('');
    const fs = d.fs.map(f => `<div><div class="row"><b>${esc(f.label)}</b><span class="muted">${esc(f.mount)}</span><span class="spacer"></span>${bytes(f.used)} von ${bytes(f.used + f.free)} (${num(f.pct, 0)} %)</div>${bar(f.pct)}</div>`).join('');
    const sv = d.services.map(s => `<div class="row">${s.running ? pill('ok', 'läuft') : pill('off', 'aus')} ${esc(s.label)}</div>`).join('');
    $('#main').innerHTML = `<h1>Übersicht</h1>${banner}
    <div class="grid">
      <div class="card"><h2>System</h2><dl class="kv">
        <dt>Name</dt><dd>${esc(d.hostname)}</dd><dt>Gerät</dt><dd>${esc(d.model || '–')}</dd>
        <dt>Kernel</dt><dd>${esc(d.kernel)} (${esc(d.arch)})</dd><dt>Laufzeit</dt><dd>${dur(d.uptime)}</dd>
        <dt>Uhrzeit</dt><dd>${fmtTime(d.time)}</dd><dt>Oberfläche</dt><dd>tnas-backup-gui ${esc(d.version)}</dd></dl></div>
      <div class="card"><h2>Auslastung</h2>
        <div class="row"><b>CPU</b><span class="muted">${d.cpus} Kerne, Last ${esc(d.load.slice(0, 3).join(' / '))}</span><span class="spacer"></span>${num(d.cpu_pct, 0)} %</div>${bar(d.cpu_pct)}
        <div class="row"><b>RAM</b><span class="spacer"></span>${bytes(memUsed)} von ${bytes(d.mem_total)}</div>${bar(memPct)}
        <div class="row"><b>CPU-Temperatur</b><span class="spacer"></span>${d.cpu_temp ? num(d.cpu_temp, 0) + ' °C' : '–'}</div></div>
      <div class="card"><h2>Speicher</h2>${fs}</div>
      <div class="card"><h2>Festplatten</h2><table><tbody>${disks || '<tr><td class="muted">keine</td></tr>'}</tbody></table></div>
      <div class="card"><h2>Netzwerk</h2><dl class="kv"><dt>Schnittstelle</dt><dd>${esc(d.net.iface)} ${esc(d.net.speed || '')}</dd>
        <dt>IP</dt><dd>${esc((d.net.ips || []).join(', ') || '–')}</dd><dt>MAC</dt><dd>${esc(d.net.mac)}</dd>
        <dt>Wake-on-LAN</dt><dd>${d.net.wol ? pill('ok', 'aktiv') : pill('bad', 'aus')}</dd></dl></div>
      <div class="card"><h2>Dienste</h2>${sv}</div>
    </div>`;
    applyBars($('#main'));
  };
  await draw();
  refreshTimer = setInterval(() => draw().catch(() => { }), 15000);
};

// Datenträger
pages.disks = async () => {
  const d = await api('disks');
  const fs = d.fs.map(f => `<tr><td>${esc(f.label)}</td><td><code>${esc(f.mount)}</code></td><td>${bytes(f.used + f.free)}</td><td>${bytes(f.used)}</td><td>${bytes(f.free)}</td><td>${bar(f.pct)}${num(f.pct, 0)} %</td></tr>`).join('');
  const disks = d.disks.map(x => {
    let st = x.role !== 'Daten' ? pill('off', 'nicht überwacht') : x.sleeping ? pill('off', 'schläft – keine Werte') :
      x.health === 'FAILED' ? pill('bad', 'SMART FEHLER') : (x.realloc || x.pending || x.uncorrectable) ? pill('warn', 'defekte Sektoren') : x.health ? pill('ok', 'gesund') : pill('off', '–');
    const attrs = (x.attributes || []).map(a => `<tr><td>${a.id}</td><td>${esc(a.name)}</td><td>${esc(a.value)}</td><td>${esc(a.worst)}</td><td>${esc(a.thresh)}</td><td>${esc(a.raw)}</td></tr>`).join('');
    return `<div class="card"><div class="row"><h2>${esc(x.dev)} – ${esc(x.model || 'unbekannt')}</h2><span class="spacer"></span>${st}</div>
      <dl class="kv"><dt>Rolle</dt><dd>${esc(x.role)}</dd><dt>Größe</dt><dd>${bytes(x.size)}</dd>
      ${x.serial ? `<dt>Seriennummer</dt><dd>${esc(x.serial)}</dd>` : ''}
      ${x.temp_c ? `<dt>Temperatur</dt><dd>${x.temp_c} °C</dd>` : ''}
      ${x.power_on_h ? `<dt>Betriebsstunden</dt><dd>${x.power_on_h.toLocaleString('de-DE')} h (${num(x.power_on_h / 8760, 1)} Jahre)</dd>` : ''}
      ${x.role === 'Daten' && !x.sleeping ? `<dt>Umgelagerte / ausstehende / nicht korrigierbare Sektoren</dt><dd>${x.realloc} / ${x.pending} / ${x.uncorrectable}</dd>` : ''}</dl>
      ${attrs ? `<details><summary class="muted">Alle SMART-Werte</summary><div class="table-wrap"><table><thead><tr><th>ID</th><th>Attribut</th><th>Wert</th><th>Schlechtester</th><th>Grenze</th><th>Rohwert</th></tr></thead><tbody>${attrs}</tbody></table></div></details>` : ''}
    </div>`;
  }).join('');
  $('#main').innerHTML = `<h1>Datenträger</h1><div class="card"><h2>Dateisysteme</h2><div class="table-wrap"><table>
    <thead><tr><th>Name</th><th>Einhängepunkt</th><th>Größe</th><th>Belegt</th><th>Frei</th><th>Auslastung</th></tr></thead><tbody>${fs}</tbody></table></div></div>${disks}`;
  applyBars($('#main'));
};

// Freigaben
pages.shares = async () => {
  const d = await api('shares');
  const rows = (d.shares || []).map(s => {
    const users = Object.entries(s.users || {}).map(([u, a]) => `${esc(u)} <span class="muted">(${a === 'rw' ? 'schreiben' : 'lesen'})</span>`).join(', ');
    const access = s.guest ? pill('warn', 'Gäste' + (s.read_only ? ', nur lesen' : '')) : users || pill('bad', 'niemand');
    return `<tr><td><b>${esc(s.name)}</b>${s.system ? ' ' + pill('off', 'automatisch') : ''}${s.comment ? `<div class="muted">${esc(s.comment)}</div>` : ''}</td>
      <td><code>${esc(s.path)}</code>${s.exists ? '' : ' ' + pill('bad', 'Ordner fehlt')}</td><td>${access}</td>
      <td>${s.browsable ? 'sichtbar' : pill('off', 'versteckt')}${s.read_only ? ' ' + pill('off', 'nur lesen') : ''}${s.recycle ? ' ' + pill('off', 'Papierkorb') : ''}</td>
      <td class="actions">${s.system ? '' : `<button data-edit="${esc(s.name)}">Bearbeiten</button> <button class="danger" data-del="${esc(s.name)}">Entfernen</button>`}</td></tr>`;
  }).join('');
  $('#main').innerHTML = `<h1>Freigaben (SMB)</h1>
    <div class="toolbar"><button class="primary" id="addShare">+ Freigabe hinzufügen</button>
    <span class="muted">Zugriff im Finder: <code>smb://${esc(location.hostname)}/&lt;Name&gt;</code></span></div>
    <div class="card table-wrap"><table><thead><tr><th>Name</th><th>Ordner</th><th>Zugriff</th><th>Optionen</th><th></th></tr></thead>
    <tbody>${rows || '<tr><td colspan="5" class="muted">Keine Freigaben</td></tr>'}</tbody></table></div>`;
  $('#addShare').onclick = () => shareDialog(null, d);
  $$('[data-edit]').forEach(b => b.onclick = () => shareDialog(d.shares.find(s => s.name === b.dataset.edit), d));
  $$('[data-del]').forEach(b => b.onclick = () => confirmBox('Freigabe entfernen',
    `Die Freigabe <b>${esc(b.dataset.del)}</b> wird entfernt. <b>Die Daten im Ordner bleiben erhalten.</b>`, 'Entfernen',
    async () => { await api('shares/delete', { name: b.dataset.del }); toast('Freigabe entfernt'); route(); }));
};

function shareDialog(s, d) {
  const isNew = !s;
  s = s || { name: '', path: d.data_dir + '/', comment: '', browsable: true, guest: false, read_only: false, recycle: false, users: {} };
  const perm = d.users.map(u => `<tr><td>${esc(u)}</td>${['rw', 'ro', ''].map(a => `<td><input type="radio" name="perm_${esc(u)}" value="${a}" ${(s.users[u] || '') === a ? 'checked' : ''}></td>`).join('')}</tr>`).join('');
  modal(`<h2>${isNew ? 'Freigabe hinzufügen' : 'Freigabe bearbeiten'}</h2><div class="form">
    <div class="two"><label>Name<input id="fName" value="${esc(s.name)}" maxlength="64"></label>
    <label>Beschreibung<input id="fComment" value="${esc(s.comment)}"></label></div>
    <label>Ordner<input id="fPath" value="${esc(s.path)}"></label>
    <label class="check"><input type="checkbox" id="fCreate" ${isNew ? 'checked' : ''}> Ordner anlegen, falls er nicht existiert</label>
    <div><b>Berechtigungen</b><table class="perm-table"><thead><tr><th>Benutzer</th><th>Lesen + Schreiben</th><th>Nur lesen</th><th>Kein Zugriff</th></tr></thead><tbody>${perm}</tbody></table></div>
    <label class="check"><input type="checkbox" id="fGuest" ${s.guest ? 'checked' : ''}> Gastzugang ohne Passwort (für alle im Netz!)</label>
    <label class="check"><input type="checkbox" id="fRO" ${s.read_only ? 'checked' : ''}> Für alle nur lesen (schützt das Backup vor versehentlichem Ändern)</label>
    <label class="check"><input type="checkbox" id="fBrowse" ${s.browsable ? 'checked' : ''}> In der Netzwerkumgebung sichtbar</label>
    <label class="check"><input type="checkbox" id="fRecycle" ${s.recycle ? 'checked' : ''}> Papierkorb (gelöschte Dateien landen in #recycle)</label>
  </div>`, async box => {
    const users = {};
    d.users.forEach(u => { const v = $(`input[name="perm_${CSS.escape(u)}"]:checked`, box); if (v && v.value) users[u] = v.value; });
    await api('shares/save', {
      old_name: isNew ? '' : s.name, create_dir: $('#fCreate', box).checked,
      share: { name: $('#fName', box).value, path: $('#fPath', box).value, comment: $('#fComment', box).value,
        browsable: $('#fBrowse', box).checked, guest: $('#fGuest', box).checked, read_only: $('#fRO', box).checked,
        recycle: $('#fRecycle', box).checked, users }
    });
    toast('Freigabe gespeichert'); route();
  });
}

// Dateien
const F = { root: '', path: '', roots: null };
const qs = o => Object.entries(o).map(([k, v]) => k + '=' + encodeURIComponent(v)).join('&');
const joinPath = (a, b) => (a ? a.replace(/\/$/, '') + '/' : '') + b;

function rootLabel(root) {
  if (!F.roots) return root;
  if (root.startsWith('share:')) return root.slice(6);
  const [snap, sub] = root.slice(5).split('/');
  const s = F.roots.snapshots.find(x => x.name === snap);
  return `${sub} – Version ${s ? fmtTime(s.time) : snap}`;
}

pages.files = async () => {
  F.roots = await api('files/roots');
  if (!F.root) {
    const pref = F.roots.shares.find(s => s.name === 'OMV-Backup') || F.roots.shares[0];
    F.root = pref ? 'share:' + pref.name : '';
  }
  const opts = `<optgroup label="Freigaben (aktueller Stand)">${F.roots.shares.map(s => `<option value="share:${esc(s.name)}">${esc(s.name)}</option>`).join('')}</optgroup>` +
    (F.roots.snapshots || []).map(sn => `<optgroup label="Backup-Version ${esc(fmtTime(sn.time))}">${sn.shares.map(sub =>
      `<option value="snap:${esc(sn.name)}/${esc(sub)}">${esc(sub)} – ${esc(fmtTime(sn.time))}</option>`).join('')}</optgroup>`).join('');
  $('#main').innerHTML = `<h1>Dateien</h1>
    <div class="toolbar"><select class="root-select" id="fRoot">${opts}</select>
      <form id="fSearch" class="row"><input id="fQ" placeholder="Dateiname suchen (ab hier)" size="28"><button>Suchen</button></form></div>
    <div id="fMode"></div><div class="crumbs" id="fCrumbs"></div><div class="card table-wrap" id="fList"><p class="muted">Lade …</p></div>`;
  $('#fRoot').value = F.root;
  $('#fRoot').onchange = () => { F.root = $('#fRoot').value; F.path = ''; listDir(); };
  $('#fSearch').onsubmit = e => { e.preventDefault(); searchFiles($('#fQ').value); };
  await listDir();
};

function drawHead() {
  $('#fMode').innerHTML = F.root.startsWith('snap:') ? `<div class="mode-banner">🕘 Du siehst eine <b>Backup-Version</b>: ${esc(rootLabel(F.root))} (nur lesen)</div>` : '';
  const parts = F.path ? F.path.split('/') : [];
  let acc = '';
  $('#fCrumbs').innerHTML = `<a data-p="">📁 ${esc(F.root.startsWith('share:') ? F.root.slice(6) : rootLabel(F.root))}</a>` +
    parts.map(p => { acc = joinPath(acc, p); return `<span>›</span><a data-p="${esc(acc)}">${esc(p)}</a>`; }).join('');
  $$('#fCrumbs a').forEach(a => a.onclick = () => { F.path = a.dataset.p; listDir(); });
}

function fileRows(entries, withPath) {
  return entries.map(e => `<tr><td class="name" data-name="${esc(withPath ? e.path : e.name)}" data-dir="${e.dir ? 1 : 0}">${e.dir ? '📁' : '📄'} ${esc(withPath ? e.path : e.name)}</td>
    <td class="num">${e.dir ? '' : bytes(e.size)}</td><td class="num">${fmtTime(e.mtime)}</td></tr>`).join('');
}

function bindRows(base) {
  $$('#fList td.name').forEach(td => td.onclick = () => {
    const p = joinPath(base, td.dataset.name);
    if (td.dataset.dir === '1') { F.path = p; listDir(); } else viewFile(F.root, p);
  });
}

async function listDir() {
  drawHead();
  $('#fList').innerHTML = '<p class="muted">Lade …</p>';
  try {
    const d = await api('files/list?' + qs({ root: F.root, path: F.path }));
    const up = F.path ? `<tr><td class="name" id="fUp">⬆️ ..</td><td></td><td></td></tr>` : '';
    $('#fList').innerHTML = `<table class="files"><thead><tr><th>Name</th><th class="num">Größe</th><th class="num">Geändert</th></tr></thead>
      <tbody>${up}${fileRows(d.entries, false) || '<tr><td colspan="3" class="muted">Ordner ist leer</td></tr>'}</tbody></table>
      ${d.truncated ? '<p class="muted">Nur die ersten 5000 Einträge – bitte die Suche benutzen.</p>' : ''}`;
    bindRows(F.path);
    if ($('#fUp')) $('#fUp').onclick = () => { F.path = F.path.split('/').slice(0, -1).join('/'); listDir(); };
  } catch (e) { $('#fList').innerHTML = `<div class="banner bad">${esc(e.message)}</div>`; }
}

async function searchFiles(q) {
  $('#fList').innerHTML = '<p class="muted">Suche … (bei großen Ordnern bis zu 15 Sekunden)</p>';
  try {
    const d = await api('files/search?' + qs({ root: F.root, path: F.path, q }));
    $('#fList').innerHTML = `<p class="muted">${d.results.length} Treffer für „${esc(q)}“${F.path ? ' in ' + esc(F.path) : ''}${d.truncated ? ' (nur die ersten 500)' : ''}${d.timeout ? ' – Suche nach 15 s abgebrochen, bitte genauer suchen oder in einen Unterordner wechseln' : ''}
      <button id="fBack">Zurück zum Ordner</button></p>
      <table class="files"><thead><tr><th>Pfad</th><th class="num">Größe</th><th class="num">Geändert</th></tr></thead><tbody>${fileRows(d.results, true)}</tbody></table>`;
    bindRows('');
    $('#fBack').onclick = listDir;
  } catch (e) { $('#fList').innerHTML = `<div class="banner bad">${esc(e.message)}</div>`; }
}

const isMac = /Mac OS X|Macintosh/.test(navigator.userAgent);
const isLinux = /Linux/.test(navigator.userAgent) && !/Android/.test(navigator.userAgent);

function noPlay(d) {
  const ext = (d.name.match(/\.([^.]+)$/) || [, ''])[1].toUpperCase();
  return `<div class="banner warn"><p>▶ Dieses ${d.kind === 'audio' ? 'Audio' : 'Video'}format${ext ? ` (${esc(ext)})` : ''} kann der Browser nicht abspielen
    (Browser können nur MP4/H.264, WebM u. Ä. – nicht AVI/DivX/XviD, MPEG, VOB, TS, WMV oder MP4 mit H.265/AC3).
    Browser-Plugins dafür gibt es seit Jahren nicht mehr – stattdessen streamt ein Videoplayer auf deinem Rechner die Datei direkt vom NAS:</p>
    <div class="row">
      <a class="btn-link" id="pM3u"><button class="primary">▶ Im Videoplayer öffnen</button></a>
      ${isMac ? '<button id="pIina">In IINA öffnen</button>' : ''}
      ${isLinux ? '<button id="pVlc" title="Benötigt einen vlc://-Link-Vermittler (siehe README)">In VLC öffnen</button>' : ''}
      <button id="pLink">Stream-Link anzeigen</button>
    </div>
    <div id="pLinkBox" class="hidden"><input id="pLinkIn" readonly class="w100">
      <p class="muted">Im Player „Netzwerk-Stream öffnen“ wählen und den Link einfügen. Gültig 12 Stunden, nur von diesem Rechner.</p></div>
    <p class="muted">„Im Videoplayer öffnen“ lädt eine kleine Abspielliste (.m3u) herunter – einmal anklicken, dann öffnet sie der Player.
    Noch keiner installiert? <a href="https://www.videolan.org/vlc/" target="_blank" rel="noopener">VLC herunterladen</a> (Mac, Windows, Linux)${isMac ? ' oder <a href="https://iina.io/" target="_blank" rel="noopener">IINA</a> (nur Mac, moderner)' : ''}.</p></div>`;
}

// Knöpfe im Hinweis mit Datei verknüpfen (auch wenn der Hinweis erst nach einem Abspielfehler erscheint)
function bindNoPlay(root, path) {
  const m3u = $('#pM3u');
  if (!m3u) return;
  m3u.href = '/api/files/m3u?' + qs({ root, path });
  const link = async () => (await api('files/streamlink', { root, path })).url;
  if ($('#pIina')) $('#pIina').onclick = async () => {
    try { location.href = 'iina://weblink?url=' + encodeURIComponent(await link()); } catch (e) { toast(e.message, true); }
  };
  if ($('#pVlc')) $('#pVlc').onclick = async () => {
    try { location.href = 'vlc://open?url=' + encodeURIComponent(await link()); } catch (e) { toast(e.message, true); }
  };
  $('#pLink').onclick = async () => {
    try { $('#pLinkIn').value = await link(); $('#pLinkBox').classList.remove('hidden'); $('#pLinkIn').select(); } catch (e) { toast(e.message, true); }
  };
  $('#pLinkIn').onfocus = e => e.target.select();
}

async function viewFile(root, path) {
  const box = $('#modalBox');
  box.classList.add('wide');
  box.innerHTML = '<p class="muted">Lade …</p>';
  $('#modal').classList.remove('hidden');
  const close = () => { $('#modal').classList.add('hidden'); box.classList.remove('wide'); box.innerHTML = ''; };
  const raw = (dl) => '/api/files/raw?' + qs(Object.assign({ root, path }, dl ? { download: 1 } : {}));
  let d;
  try { d = await api('files/view?' + qs({ root, path })); } catch (e) { box.innerHTML = `<div class="banner bad">${esc(e.message)}</div><div class="modal-actions"><button id="vClose">Schließen</button></div>`; $('#vClose').onclick = close; return; }
  let content;
  switch (d.kind) {
    case 'text': content = `<pre class="viewer-text">${esc(d.text)}</pre>${d.truncated ? '<p class="muted">Nur die ersten 512 KB werden angezeigt.</p>' : ''}${d.encoding !== 'UTF-8' ? `<p class="muted">Zeichensatz: ${esc(d.encoding)}</p>` : ''}`; break;
    case 'image': content = `<img class="viewer-media" src="${raw()}" alt="">`; break;
    case 'audio':
    case 'video': {
      const probe = document.createElement(d.kind);
      content = probe.canPlayType(d.mime) === '' ? noPlay(d)
        : `<div id="vMedia"><${d.kind} class="viewer-media" controls preload="metadata" src="${raw()}"></${d.kind}></div>`;
      break;
    }
    case 'pdf': content = `<iframe class="viewer-pdf" src="${raw()}"></iframe>`; break;
    default: content = `<p class="muted">Binärdatei (${esc(d.mime)}) – Vorschau der ersten Bytes:</p><pre class="viewer-text">${esc(d.hex)}</pre>`;
  }
  box.innerHTML = `<div class="v-head"><div class="row"><button class="x-close" id="vX" title="Schließen (Esc)" aria-label="Schließen">✕</button><h2>${esc(d.name)}</h2><span class="spacer"></span><span class="muted">${bytes(d.size)} · geändert ${fmtTime(d.mtime)}</span></div>
    ${root.startsWith('snap:') ? `<div class="mode-banner">🕘 Backup-Version: ${esc(rootLabel(root))}</div>` : ''}
    <div class="muted v-path"><code>${esc(path)}</code></div></div>
    <div class="v-body">${content}
    <h2 class="mt">Versionen dieser Datei</h2><div id="vHist" class="muted v-hist">Lade …</div></div>
    <div class="modal-actions v-foot"><a href="${raw(true)}"><button>⬇ Herunterladen</button></a><button id="vClose" class="primary">Schließen</button></div>`;
  $('#vClose').onclick = close;
  $('#vX').onclick = close;
  // Format wird gemeldet, aber der Inhalt (z. B. H.265, AC3) lässt sich doch nicht abspielen
  const media = $('#vMedia video, #vMedia audio');
  if (media) media.addEventListener('error', () => { $('#vMedia').innerHTML = noPlay(d); bindNoPlay(root, path); });
  bindNoPlay(root, path);
  try {
    const h = await api('files/history?' + qs({ root, path }));
    if (!h.versions.length) { $('#vHist').textContent = h.note || 'Keine Versionen.'; return; }
    let prev = null;
    const rows = h.versions.map(v => {
      const same = prev && v.exists && prev.exists && prev.size === v.size && prev.mtime === v.mtime;
      if (v.exists) prev = v;
      const sel = v.root === root && v.path === path;
      return `<tr class="${v.exists ? '' : 'missing'} ${sel ? 'sel' : ''}"><td>${v.current ? '<b>Aktuell</b>' : fmtTime(v.time)}</td>
        <td>${v.exists ? bytes(v.size) : '–'}</td><td>${v.exists ? fmtTime(v.mtime) : 'nicht vorhanden'}</td><td>${same ? 'unverändert' : v.exists ? '' : ''}</td>
        <td class="actions">${v.exists && !sel ? `<button data-vr="${esc(v.root)}" data-vp="${esc(v.path)}">Anzeigen</button>` : ''}
        ${v.exists ? `<a href="/api/files/raw?${qs({ root: v.root, path: v.path, download: 1 })}"><button>⬇</button></a>` : ''}</td></tr>`;
    }).join('');
    $('#vHist').innerHTML = `<div class="table-wrap"><table class="vers"><thead><tr><th>Stand</th><th>Größe</th><th>Datei geändert</th><th></th><th></th></tr></thead><tbody>${rows}</tbody></table></div>`;
    $$('#vHist [data-vr]').forEach(b => b.onclick = () => viewFile(b.dataset.vr, b.dataset.vp));
  } catch (e) { $('#vHist').textContent = e.message; }
}

// Backup-Versionen
pages.snapshots = async () => {
  const d = await api('snapshots');
  const rows = (d.snapshots || []).map((s, i) => `<tr><td><b>${fmtTime(s.time)}</b> <span class="muted">${ago(s.time)}</span>${i === 0 ? ' ' + pill('ok', 'neuester') : ''}</td>
    <td>${s.shares.length} <span class="muted">(${esc(s.shares.join(', '))})</span></td><td><code>${esc(s.name)}</code></td>
    <td class="actions"><button data-browse="${esc(s.name)}/${esc(s.shares[0] || '')}">Durchsuchen</button> <button class="danger" data-del="${esc(s.name)}">Löschen</button></td></tr>`).join('');
  $('#main').innerHTML = `<h1>Backup-Versionen</h1>
    <div class="card"><p>Nach jedem nächtlichen Backup von <b>${esc(d.source)}</b> wird ein schreibgeschützter btrfs-Schnappschuss jeder Freigabe angelegt.
    Aufbewahrung (steuert das Backup-Skript auf ${esc(d.source)}): 7 Tage, 4 Wochen, 6 Monate. Schnappschüsse belegen nur Platz für geänderte Daten.</p>
    <label class="check"><input type="checkbox" id="verShare" ${d.versions_share ? 'checked' : ''}> Versionen als Freigabe <b>${esc(d.share_name)}</b> bereitstellen (nur lesen, für Administratoren)
    – so lassen sich ältere Stände direkt im Finder öffnen: <code>smb://${esc(location.hostname)}/${esc(d.share_name)}</code></label></div>
    <div class="card table-wrap"><table><thead><tr><th>Zeitpunkt</th><th>Freigaben</th><th>Name</th><th></th></tr></thead>
    <tbody>${rows || '<tr><td colspan="4" class="muted">Noch keine Versionen</td></tr>'}</tbody></table></div>`;
  $('#verShare').onchange = async e => {
    try { await api('snapshots/share', { on: e.target.checked }); toast(e.target.checked ? 'Freigabe eingerichtet' : 'Freigabe entfernt'); }
    catch (err) { toast(err.message, true); e.target.checked = !e.target.checked; }
  };
  $$('[data-browse]').forEach(b => b.onclick = () => { F.root = 'snap:' + b.dataset.browse; F.path = ''; location.hash = '#/files'; });
  $$('[data-del]').forEach(b => b.onclick = () => confirmBox('Version löschen',
    `Die Backup-Version <b>${esc(b.dataset.del)}</b> wird endgültig gelöscht. Die aktuellen Daten sind davon nicht betroffen.`, 'Löschen',
    async () => { await api('snapshots/delete', { name: b.dataset.del }); toast('Version gelöscht'); route(); }));
};

// Benutzer
pages.users = async () => {
  const d = await api('users');
  const rows = d.users.map(u => `<tr><td><b>${esc(u.name)}</b>${u.me ? ' <span class="muted">(du)</span>' : ''}</td>
    <td><label class="check"><input type="checkbox" data-admin="${esc(u.name)}" ${u.admin ? 'checked' : ''} ${u.me ? 'disabled' : ''}> Administrator</label></td>
    <td class="actions"><button data-pw="${esc(u.name)}">Passwort ändern</button> ${u.me ? '' : `<button class="danger" data-del="${esc(u.name)}">Löschen</button>`}</td></tr>`).join('');
  $('#main').innerHTML = `<h1>Benutzer</h1><div class="toolbar"><button class="primary" id="addUser">+ Benutzer anlegen</button>
    <span class="muted">Administratoren dürfen sich an dieser Oberfläche anmelden. Alle Benutzer können – je nach Freigabe – per SMB zugreifen.</span></div>
    <div class="card table-wrap"><table><thead><tr><th>Name</th><th>Rolle</th><th></th></tr></thead><tbody>${rows}</tbody></table></div>
    <h2>Gemerkte Geräte (automatische Anmeldung)</h2>
    <p class="muted">Browser, die sich mit „Auf diesem Gerät angemeldet bleiben“ angemeldet haben. Sie gelten nur von derselben IP-Adresse aus;
    „Abmelden“ oder eine Passwortänderung entfernt sie ebenfalls.</p>
    <div class="card table-wrap" id="devList"><p class="muted">Lade …</p></div>`;
  loadDevices();
  const pwFields = `<label>Neues Passwort (mind. 8 Zeichen)<input id="p1" type="password" autocomplete="new-password"></label>
    <label>Wiederholen<input id="p2" type="password" autocomplete="new-password"></label>`;
  const pwCheck = box => { if ($('#p1', box).value !== $('#p2', box).value) throw new Error('Passwörter stimmen nicht überein'); return $('#p1', box).value; };
  $('#addUser').onclick = () => modal(`<h2>Benutzer anlegen</h2><div class="form"><label>Benutzername<input id="uName" autocomplete="off"></label>${pwFields}
    <label class="check"><input type="checkbox" id="uAdmin"> Administrator (darf sich hier anmelden)</label></div>`, async box => {
      await api('users/create', { user: $('#uName', box).value.trim(), password: pwCheck(box), admin: $('#uAdmin', box).checked });
      toast('Benutzer angelegt'); route();
    }, 'Anlegen');
  $$('[data-pw]').forEach(b => b.onclick = () => modal(`<h2>Passwort für ${esc(b.dataset.pw)}</h2><div class="form">${pwFields}
    <p class="muted">Gilt für die Anmeldung hier, für SSH und für die SMB-Freigaben. Gespeicherte Passwörter (z. B. im Mac-Schlüsselbund) müssen danach angepasst werden.</p></div>`,
    async box => { await api('users/password', { user: b.dataset.pw, password: pwCheck(box) }); toast('Passwort geändert'); }));
  $$('[data-del]').forEach(b => b.onclick = () => confirmBox('Benutzer löschen',
    `Benutzer <b>${esc(b.dataset.del)}</b> wird gelöscht und aus allen Freigaben entfernt.`, 'Löschen',
    async () => { await api('users/delete', { user: b.dataset.del }); toast('Benutzer gelöscht'); route(); }));
  $$('[data-admin]').forEach(c => c.onchange = async () => {
    try { await api('users/admin', { user: c.dataset.admin, admin: c.checked }); toast('Gespeichert'); }
    catch (e) { toast(e.message, true); c.checked = !c.checked; }
  });
};

function agentName(ua) {
  const b = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : 'Browser';
  const o = /Mac OS X/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Linux/.test(ua) ? 'Linux' : '';
  return b + (o ? ' auf ' + o : '');
}

async function loadDevices() {
  try {
    const d = await api('devices');
    $('#devList').innerHTML = d.devices.length ? `<table><thead><tr><th>Benutzer</th><th>Gerät</th><th>IP</th><th>Gemerkt seit</th><th>Zuletzt</th><th></th></tr></thead><tbody>
      ${d.devices.map(x => `<tr><td>${esc(x.user)}</td><td title="${esc(x.agent)}">${esc(agentName(x.agent))}${x.current ? ' ' + pill('ok', 'dieser Browser') : ''}</td>
      <td>${esc(x.ip)}</td><td>${fmtTime(x.created)}</td><td>${ago(x.last_used)}</td>
      <td class="actions"><button class="danger" data-dev="${esc(x.id)}">Entfernen</button></td></tr>`).join('')}</tbody></table>`
      : '<p class="muted">Keine gemerkten Geräte.</p>';
    $$('[data-dev]').forEach(b => b.onclick = async () => {
      try { await api('devices/revoke', { id: b.dataset.dev }); toast('Gerät entfernt – dort ist wieder eine Anmeldung nötig'); loadDevices(); }
      catch (e) { toast(e.message, true); }
    });
  } catch (e) { $('#devList').innerHTML = `<div class="banner bad">${esc(e.message)}</div>`; }
}

// Dienste
pages.services = async () => {
  const d = await api('dashboard');
  $('#main').innerHTML = `<h1>Dienste</h1><div class="card table-wrap"><table><thead><tr><th>Dienst</th><th>Status</th><th></th></tr></thead><tbody>
    ${d.services.map(s => `<tr><td>${esc(s.label)}</td><td>${s.running ? pill('ok', 'läuft') : pill('off', 'aus')}</td>
    <td class="actions">${s.name === 'smbd' ? '<button id="smbRestart">Neu starten</button>' : ''}</td></tr>`).join('')}</tbody></table></div>
    <div class="card"><h2>Hinweise</h2><p>Die Samba-Konfiguration wird von dieser Oberfläche erzeugt (<code>/etc/samba/smb.conf</code>);
    Freigaben bitte nur hier ändern. Wake-on-LAN: ${d.net.wol ? pill('ok', 'aktiv') : pill('bad', 'aus')} – MAC <code>${esc(d.net.mac)}</code>.</p></div>`;
  const b = $('#smbRestart');
  if (b) b.onclick = async () => { b.disabled = true; try { await api('services/restart', { name: 'samba' }); toast('Samba neu gestartet'); } catch (e) { toast(e.message, true); } route(); };
};

// Protokolle
pages.logs = async () => {
  const names = { gui: 'Diese Oberfläche', messages: 'System', samba: 'Samba' };
  const cur = (location.hash.split('?name=')[1]) || 'gui';
  const d = await api('logs?name=' + encodeURIComponent(cur));
  $('#main').innerHTML = `<h1>Protokolle</h1><div class="toolbar">${Object.entries(names).map(([k, v]) =>
    `<a href="#/logs?name=${k}"><button class="${k === cur ? 'primary' : ''}">${esc(v)}</button></a>`).join('')}
    <span class="muted"><code>${esc(d.path)}</code> – letzte 300 Zeilen</span></div><pre class="log">${esc(d.lines.join('\n'))}</pre>`;
  const pre = $('pre.log'); pre.scrollTop = pre.scrollHeight;
};

// Energie
pages.power = async () => {
  $('#main').innerHTML = `<h1>Energie</h1><div class="card"><p>Der TNAS ist normalerweise aus. ${esc(S.source || 'Die Backup-Quelle')} weckt ihn nachts per Wake-on-LAN,
    sichert und schaltet ihn danach wieder aus. Wenn du ihn manuell eingeschaltet hast, geht er nach dem nächsten nächtlichen Backup automatisch aus –
    oder du schaltest ihn hier aus.</p>
    <div class="row"><button id="pwOff" class="danger solid">Ausschalten</button><button id="pwReboot">Neu starten</button></div></div>`;
  $('#pwOff').onclick = () => confirmBox('Ausschalten', 'Der TNAS wird heruntergefahren. Einschalten wieder per Wake-on-LAN oder Netzschalter.', 'Ausschalten',
    async () => { await api('power', { action: 'poweroff' }); $('#main').innerHTML = '<div class="banner warn">Der TNAS fährt herunter …</div>'; });
  $('#pwReboot').onclick = () => confirmBox('Neu starten', 'Der TNAS startet neu (ca. 2 Minuten).', 'Neu starten',
    async () => { await api('power', { action: 'reboot' }); $('#main').innerHTML = '<div class="banner warn">Neustart … Seite in ca. 2 Minuten neu laden.</div>'; });
};

// ---------- Start ----------
(async () => {
  try {
    const d = await api('session');
    $('#hostName').textContent = $('#loginHost').textContent = d.hostname;
    document.title = d.hostname + ' – Backup';
    $('#navFoot').textContent = 'tnas-backup-gui ' + d.version;
    if (d.user) { S = d; start(); } else showLogin();
  } catch (e) { showLogin(); }
})();
