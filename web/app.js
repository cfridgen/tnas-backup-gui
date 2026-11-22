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

async function api(path, body) {
  const opt = { headers: {} };
  if (body !== undefined) {
    opt.method = 'POST'; opt.body = JSON.stringify(body);
    opt.headers['Content-Type'] = 'application/json'; opt.headers['X-CSRF-Token'] = S.csrf;
  }
  const r = await fetch('/api/' + path, opt);
  let data = {}; try { data = await r.json(); } catch (_) { }
  if (r.status === 401 && path !== 'login') { showLogin(); throw new Error(data.error || 'Nicht angemeldet'); }
  if (!r.ok) throw new Error(data.error || ('Fehler ' + r.status));
  return data;
}

// ---------- Dialog ----------
function modal(html, onOk, okLabel = 'Speichern', danger = false) {
  const box = $('#modalBox');
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
    const d = await api('login', { user: $('#loginUser').value.trim(), password: $('#loginPass').value });
    S.user = d.user; S.csrf = d.csrf; $('#loginPass').value = ''; start();
  } catch (err) { $('#loginErr').textContent = err.message; }
};
$('#logoutBtn').onclick = async () => { try { await api('logout', {}); } catch (_) { } S = { user: null, csrf: '' }; showLogin(); };
$('#navToggle').onclick = () => $('#nav').classList.toggle('open');

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

// Backup-Versionen
pages.snapshots = async () => {
  const d = await api('snapshots');
  const rows = (d.snapshots || []).map((s, i) => `<tr><td><b>${fmtTime(s.time)}</b> <span class="muted">${ago(s.time)}</span>${i === 0 ? ' ' + pill('ok', 'neuester') : ''}</td>
    <td>${s.shares.length} <span class="muted">(${esc(s.shares.join(', '))})</span></td><td><code>${esc(s.name)}</code></td>
    <td class="actions"><button class="danger" data-del="${esc(s.name)}">Löschen</button></td></tr>`).join('');
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
    <div class="card table-wrap"><table><thead><tr><th>Name</th><th>Rolle</th><th></th></tr></thead><tbody>${rows}</tbody></table></div>`;
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
