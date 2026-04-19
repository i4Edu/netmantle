// NetMantle SPA — vanilla JS, hash-based router, no build step.
//
// Layout:
//   1. tiny api() helper
//   2. theme handling (light/dark + manual override in localStorage)
//   3. session refresh
//   4. router that maps #/<route> to a render function in `views`
//   5. per-view modules: Inventory, Backups (changes + diff), Compliance
//      (rules + findings), Topology (LLDP/CDP nodes & links), Approvals
//      (change-request queue), Audit (filtered log), Settings (tenants,
//      tokens, channels, rules, pollers).

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

const api = async (method, path, body) => {
  const opts = { method, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch('/api/v1' + path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch (_) { /* ignore */ }
    throw new Error(msg);
  }
  if (r.status === 204) return null;
  const ct = r.headers.get('content-type') || '';
  return ct.includes('application/json') ? r.json() : r.text();
};

// ---------- elements ----------
const escapeHTML = (s) => String(s == null ? '' : s).replace(/[&<>"']/g,
  (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const el = (tag, attrs = {}, ...children) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k === 'html') e.innerHTML = v;
    else if (k.startsWith('on') && typeof v === 'function') {
      e.addEventListener(k.slice(2), v);
    } else if (v === true) e.setAttribute(k, '');
    else e.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c == null) continue;
    e.appendChild(c.nodeType ? c : document.createTextNode(c));
  }
  return e;
};

// ---------- theme ----------
const THEME_KEY = 'netmantle.theme';

function applyTheme(theme) {
  if (theme === 'light' || theme === 'dark') {
    document.documentElement.setAttribute('data-theme', theme);
  } else {
    document.documentElement.removeAttribute('data-theme');
  }
}

function currentTheme() {
  const stored = localStorage.getItem(THEME_KEY);
  if (stored === 'light' || stored === 'dark') return stored;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function initTheme() {
  applyTheme(localStorage.getItem(THEME_KEY)); // honour explicit choice if present
  const btn = $('#theme-toggle');
  const label = $('#theme-toggle-label');
  const refresh = () => { label.textContent = currentTheme() === 'dark' ? 'Light' : 'Dark'; };
  refresh();
  btn.addEventListener('click', () => {
    const next = currentTheme() === 'dark' ? 'light' : 'dark';
    localStorage.setItem(THEME_KEY, next);
    applyTheme(next);
    refresh();
  });
}

// ---------- session ----------
let me = null;

async function refreshSession() {
  try {
    me = await api('GET', '/auth/me');
    $('#nav').hidden = false;
    $('#login-view').hidden = true;
    $('#app-view').hidden = false;
    $('#who').textContent = `${me.username} (${me.role})`;
    if (!location.hash) location.hash = '#/dashboard';
    else router();
    refreshApprovalsBadge();
  } catch (_) {
    me = null;
    $('#nav').hidden = true;
    $('#login-view').hidden = false;
    $('#app-view').hidden = true;
  }
}

// ---------- router ----------
const ROUTES = ['dashboard', 'inventory', 'backups', 'compliance', 'topology', 'approvals', 'audit', 'settings'];

function currentRoute() {
  const h = (location.hash || '').replace(/^#\/?/, '');
  const r = h.split('/')[0] || 'dashboard';
  return ROUTES.includes(r) ? r : 'dashboard';
}

function router() {
  const route = currentRoute();
  for (const a of $$('.sidebar a[data-route]')) {
    a.classList.toggle('active', a.dataset.route === route);
  }
  const view = $('#view');
  view.innerHTML = '';
  const fn = views[route] || views.inventory;
  fn(view);
}

window.addEventListener('hashchange', router);

// ===========================================================
// Views
// ===========================================================
const views = {};

// ---------- Inventory (keeps original device CRUD + backup + runs) ----------
let currentDeviceId = null;

views.inventory = (root) => {
  root.appendChild(el('h2', {}, 'Inventory'));
  root.appendChild(el('div', { class: 'card-grid', id: 'device-cards' }));
  const wrap = el('div', { class: 'inventory' });
  const aside = el('aside', { class: 'list-pane' },
    el('h2', {}, 'Devices'),
    el('ul', { id: 'devices' }),
    el('details', {},
      el('summary', {}, 'Add device'),
      buildAddDeviceForm()),
    el('details', {},
      el('summary', {}, 'Add credential'),
      buildAddCredentialForm()),
  );
  const article = el('article', { id: 'device-detail' },
    el('p', { class: 'muted' }, 'Select a device on the left.'));
  wrap.append(aside, article);
  root.appendChild(wrap);

  loadAux();
  loadDevices();
};

function buildAddDeviceForm() {
  const form = el('form', { id: 'add-device-form' },
    el('label', {}, 'Hostname ', el('input', { name: 'hostname', required: true })),
    el('label', {}, 'Address ', el('input', { name: 'address', required: true })),
    el('label', {}, 'Port ', el('input', { name: 'port', type: 'number', value: '22' })),
    el('label', {}, 'Driver ',
      el('select', { name: 'driver', id: 'driver-select', required: true })),
    el('label', {}, 'Credential ',
      el('select', { name: 'credential_id', id: 'cred-select' })),
    el('button', { type: 'submit' }, 'Create'),
  );
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    const body = {
      hostname: fd.get('hostname'),
      address: fd.get('address'),
      port: Number(fd.get('port') || 22),
      driver: fd.get('driver'),
    };
    const cid = fd.get('credential_id');
    if (cid) body.credential_id = Number(cid);
    try {
      await api('POST', '/devices', body);
      form.reset();
      await loadDevices();
    } catch (err) { alert('Create failed: ' + err.message); }
  });
  return form;
}

function buildAddCredentialForm() {
  const form = el('form', { id: 'add-cred-form' },
    el('label', {}, 'Name ', el('input', { name: 'name', required: true })),
    el('label', {}, 'Username ', el('input', { name: 'username', required: true })),
    el('label', {}, 'Password ', el('input', { name: 'secret', type: 'password', required: true })),
    el('button', { type: 'submit' }, 'Save'),
  );
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    try {
      await api('POST', '/credentials', {
        name: fd.get('name'),
        username: fd.get('username'),
        secret: fd.get('secret'),
      });
      form.reset();
      await loadAux();
    } catch (err) { alert('Save failed: ' + err.message); }
  });
  return form;
}

async function loadAux() {
  const drivers = await api('GET', '/drivers');
  const sel = $('#driver-select'); if (!sel) return;
  sel.innerHTML = '';
  for (const d of drivers) {
    sel.appendChild(el('option', { value: d }, d));
  }
  const creds = await api('GET', '/credentials');
  const csel = $('#cred-select'); if (!csel) return;
  csel.innerHTML = '<option value="">— none —</option>';
  for (const c of creds) {
    csel.appendChild(el('option', { value: c.id }, `${c.name} (${c.username})`));
  }
}

async function loadDevices() {
  const list = await api('GET', '/devices');
  const ul = $('#devices'); if (!ul) return;
  ul.innerHTML = '';
  if (!list.length) {
    ul.appendChild(el('li', { class: 'muted' }, 'No devices yet — add one below.'));
    renderDeviceCards(list);
    return;
  }
  for (const d of list) {
    const li = el('li', { 'data-id': d.id }, `${d.hostname} (${d.driver})`);
    if (d.id === currentDeviceId) li.classList.add('active');
    li.onclick = () => showDevice(d.id);
    ul.appendChild(li);
  }
  renderDeviceCards(list);
}

// renderDeviceCards renders the device-card grid above the split-pane list.
//
// Each card surfaces hostname, driver, last backup time, compliance score,
// and quick read-only actions (Open, Diff). Mutating actions (e.g. backup)
// remain on the device-detail pane to keep the proposal/audit invariants.
async function renderDeviceCards(devices) {
  const host = $('#device-cards');
  if (!host) return;
  host.innerHTML = '';
  if (!devices.length) {
    host.appendChild(el('p', { class: 'muted' }, 'No devices yet.'));
    return;
  }
  // Pull compliance findings + latest backup time for score/last-seen overlay.
  let findings = [];
  try { findings = await api('GET', '/compliance/findings'); } catch (_) {}
  const ruleCount = await safeCount('/compliance/rules');
  const failByDev = {}, passByDev = {};
  for (const f of findings) {
    if (f.status === 'pass') passByDev[f.device_id] = (passByDev[f.device_id] || 0) + 1;
    if (f.status === 'fail') failByDev[f.device_id] = (failByDev[f.device_id] || 0) + 1;
  }

  // Fan out the per-device "last successful backup" lookups in parallel so
  // card rendering does not block on N sequential network round-trips.
  const lastBackupByDev = {};
  await Promise.all(devices.map(async (d) => {
    try {
      const runs = await api('GET', `/devices/${d.id}/runs`);
      const ok = (runs || []).find((r) => r.status === 'success');
      if (ok) lastBackupByDev[d.id] = relativeTime(new Date(ok.started_at));
    } catch (_) { /* ignore */ }
  }));

  for (const d of devices) {
    const fails = failByDev[d.id] || 0;
    const passes = passByDev[d.id] || 0;
    const evaluated = fails + passes;
    const score = evaluated > 0 ? Math.round((passes * 100) / evaluated) : null;
    // Pass `ruleCount > 0` (not `evaluated > 0`) so a device that has not
    // yet been evaluated still classifies as "compliant" when rules exist
    // — matches the topology overlay's contract.
    const { cls, label: status } = complianceStatus(fails, ruleCount > 0);
    const lastBackup = lastBackupByDev[d.id] || '—';

    const card = el('div', { class: 'card device-card', 'data-id': d.id },
      el('div', { class: 'row' },
        el('span', { class: 'hostname' }, d.hostname),
        el('span', { style: 'flex:1' }),
        el('span', { class: 'badge ' + cls }, status)),
      el('div', { class: 'meta' }, `${d.address} · ${d.driver}`),
      el('div', { class: 'meta' }, `Last backup: ${lastBackup}`),
      el('div', {},
        el('div', { class: 'meta' }, score == null
          ? `Compliance: — (${ruleCount} rule${ruleCount === 1 ? '' : 's'} configured)`
          : `Compliance: ${score}%`),
        el('div', { class: 'compl-track' },
          el('div', { class: 'fill ' + cls, style: `width:${score == null ? 0 : score}%` }))),
      el('div', { class: 'footer' },
        el('span', {}, `cred ${d.credential_id ? '#' + d.credential_id : '—'}`),
        el('span', {}, '#' + d.id)),
    );
    card.onclick = () => showDevice(d.id);
    host.appendChild(card);
  }
}

async function safeCount(path) {
  try { const r = await api('GET', path); return Array.isArray(r) ? r.length : 0; }
  catch (_) { return 0; }
}

function relativeTime(date) {
  const sec = Math.max(0, (Date.now() - date.getTime()) / 1000);
  if (sec < 60) return Math.floor(sec) + 's ago';
  if (sec < 3600) return Math.floor(sec / 60) + 'm ago';
  if (sec < 86400) return Math.floor(sec / 3600) + 'h ago';
  return Math.floor(sec / 86400) + 'd ago';
}

async function showDevice(id) {
  currentDeviceId = id;
  for (const li of $$('#devices li')) {
    li.classList.toggle('active', Number(li.dataset.id) === id);
  }
  const dev = await api('GET', `/devices/${id}`);
  const detail = $('#device-detail');
  detail.innerHTML = '';
  detail.appendChild(el('h2', {}, dev.hostname));
  detail.appendChild(el('p', { class: 'muted' }, `${dev.driver} • ${dev.address}:${dev.port}`));

  const backupBtn = el('button', { class: 'btn' }, 'Backup now');
  backupBtn.onclick = async () => {
    backupBtn.disabled = true; backupBtn.textContent = 'Running…';
    try { await api('POST', `/devices/${id}/backup`); }
    catch (e) { alert('Backup failed: ' + e.message); }
    finally {
      backupBtn.disabled = false; backupBtn.textContent = 'Backup now';
      showDevice(id);
    }
  };
  const delBtn = el('button', { class: 'btn danger' }, 'Delete');
  delBtn.onclick = async () => {
    if (!confirm(`Delete ${dev.hostname}?`)) return;
    await api('DELETE', `/devices/${id}`);
    currentDeviceId = null;
    detail.innerHTML = '<p class="muted">Select a device on the left.</p>';
    loadDevices();
  };
  detail.appendChild(el('div', { class: 'actions' }, backupBtn, delBtn));

  detail.appendChild(el('h3', {}, 'Latest configuration'));
  try {
    const cfg = await api('GET', `/devices/${id}/config`);
    detail.appendChild(el('pre', { class: 'config' }, cfg));
  } catch (_) {
    detail.appendChild(el('p', { class: 'muted' }, 'No backup yet — click "Backup now".'));
  }

  detail.appendChild(el('h3', {}, 'Recent runs'));
  const runs = await api('GET', `/devices/${id}/runs`);
  if (!runs.length) {
    detail.appendChild(el('p', { class: 'muted' }, 'No runs yet.'));
    return;
  }
  const table = el('table', { class: 'data' });
  table.innerHTML = '<thead><tr><th>Started</th><th>Status</th><th>Commit</th><th>Error</th></tr></thead>';
  const tbody = el('tbody');
  for (const r of runs) {
    tbody.appendChild(el('tr', {},
      el('td', {}, new Date(r.started_at).toLocaleString()),
      el('td', { class: 'status-' + r.status }, r.status),
      el('td', {}, (r.commit_sha || '').slice(0, 8)),
      el('td', {}, r.error || ''),
    ));
  }
  table.appendChild(tbody);
  detail.appendChild(table);
}

// ---------- Audit ----------
views.audit = (root) => {
  root.appendChild(el('h2', {}, 'Audit log'));

  const filterBar = el('div', { class: 'filter-bar card' });
  const mkField = (label, input) =>
    el('div', { class: 'field' }, el('label', {}, label), input);

  const fUser   = el('input', { type: 'number', min: '1', placeholder: 'user id' });
  const fAction = el('input', { type: 'text',  placeholder: 'e.g. device.create' });
  const fTarget = el('input', { type: 'text',  placeholder: 'e.g. device:42' });
  const fSince  = el('input', { type: 'datetime-local' });
  const fUntil  = el('input', { type: 'datetime-local' });
  const fLimit  = el('input', { type: 'number', min: '1', max: '500', value: '100' });

  const apply = el('button', { class: 'btn' }, 'Apply');
  const clear = el('button', { class: 'btn ghost', type: 'button' }, 'Clear');
  const csv   = el('button', { class: 'btn ghost', type: 'button' }, 'Export CSV');

  filterBar.append(
    mkField('User ID', fUser),
    mkField('Action', fAction),
    mkField('Target', fTarget),
    mkField('Since',  fSince),
    mkField('Until',  fUntil),
    mkField('Limit',  fLimit),
    el('div', { class: 'field' }, el('label', {}, '\u00a0'),
      el('div', { class: 'actions' }, apply, clear, csv)),
  );
  root.appendChild(filterBar);

  const results = el('div', { class: 'card', id: 'audit-results' },
    el('p', { class: 'muted' }, 'Loading…'));
  root.appendChild(results);

  const localToRFC3339 = (v) => {
    if (!v) return '';
    // <input type="datetime-local"> gives "YYYY-MM-DDTHH:MM" in local time.
    const d = new Date(v);
    return isNaN(d.getTime()) ? '' : d.toISOString();
  };

  const load = async () => {
    const params = new URLSearchParams();
    if (fUser.value)   params.set('user', fUser.value);
    if (fAction.value) params.set('action', fAction.value.trim());
    if (fTarget.value) params.set('target', fTarget.value.trim());
    const s = localToRFC3339(fSince.value); if (s) params.set('since', s);
    const u = localToRFC3339(fUntil.value); if (u) params.set('until', u);
    if (fLimit.value)  params.set('limit', fLimit.value);
    results.innerHTML = '<p class="muted">Loading…</p>';
    try {
      const rows = await api('GET', '/audit?' + params.toString());
      renderAuditRows(results, rows);
    } catch (e) {
      results.innerHTML = '';
      results.appendChild(el('p', { class: 'error' }, 'Error: ' + e.message));
    }
  };

  apply.addEventListener('click', (e) => { e.preventDefault(); load(); });
  clear.addEventListener('click', () => {
    fUser.value = ''; fAction.value = ''; fTarget.value = '';
    fSince.value = ''; fUntil.value = ''; fLimit.value = '100';
    load();
  });
  csv.addEventListener('click', () => {
    // Build the same query string used by load(), then ask the browser to
    // download it. credentials:'same-origin' is the default for top-level
    // navigations so the session cookie travels along.
    const params = new URLSearchParams();
    if (fUser.value)   params.set('user', fUser.value);
    if (fAction.value) params.set('action', fAction.value.trim());
    if (fTarget.value) params.set('target', fTarget.value.trim());
    const s = localToRFC3339(fSince.value); if (s) params.set('since', s);
    const u = localToRFC3339(fUntil.value); if (u) params.set('until', u);
    if (fLimit.value)  params.set('limit', fLimit.value);
    params.set('format', 'csv');
    window.location.href = '/api/v1/audit?' + params.toString();
  });
  load();
};

function renderAuditRows(root, rows) {
  root.innerHTML = '';
  if (!rows || !rows.length) {
    root.appendChild(el('p', { class: 'muted' }, 'No matching audit entries.'));
    return;
  }
  const table = el('table', { class: 'data' });
  table.innerHTML = '<thead><tr>' +
    '<th>When</th><th>Actor</th><th>Source</th><th>Action</th>' +
    '<th>Target</th><th>Detail</th></tr></thead>';
  const tbody = el('tbody');
  for (const r of rows) {
    const when = new Date(r.created_at).toLocaleString();
    const actor = r.actor_user_id == null ? '—' : '#' + r.actor_user_id;
    const sourceBadge = r.source
      ? el('span', { class: 'badge' }, r.source)
      : el('span', { class: 'muted' }, '—');
    tbody.appendChild(el('tr', {},
      el('td', {}, when),
      el('td', {}, actor),
      el('td', {}, sourceBadge),
      el('td', {}, el('code', {}, r.action || '')),
      el('td', {}, r.target || ''),
      el('td', {}, r.detail || ''),
    ));
  }
  table.appendChild(tbody);
  root.appendChild(table);
}

// ---------- shared helpers ----------
function emptyState(message) {
  return el('div', { class: 'card' }, el('p', { class: 'muted' }, message));
}
function errorState(message) {
  return el('div', { class: 'card' }, el('p', { class: 'error' }, message));
}
function statusBadgeClass(status) {
  switch (status) {
    case 'pass':
    case 'success':
    case 'approved':
    case 'applied':
      return 'ok';
    case 'fail':
    case 'failed':
    case 'rejected':
    case 'cancelled':
      return 'bad';
    case 'running':
    case 'submitted':
    case 'pending':
      return 'warn';
    default:
      return 'info';
  }
}

// VIOLATION_THRESHOLD is the number of failing compliance findings at which
// a device is considered to be in "violation" rather than just "drift".
// Centralised here so the dashboard, inventory cards and topology canvas
// classify a device the same way.
const VIOLATION_THRESHOLD = 3;

// complianceStatus maps a failing-rule count to a (badge-class, label) pair.
// Used by the inventory cards and the topology side panel.
function complianceStatus(failingCount, hasRules) {
  if (!hasRules) return { cls: 'info', label: 'no rules' };
  if (failingCount === 0) return { cls: 'ok', label: 'compliant' };
  if (failingCount >= VIOLATION_THRESHOLD) return { cls: 'bad', label: 'violation' };
  return { cls: 'warn', label: 'drift' };
}

// ---------- Backups (recent change events with two-pane Git-style diff) ----------
views.backups = async (root) => {
  root.appendChild(el('h2', {}, 'Backups & changes'));
  const layout = el('div', { class: 'inventory' });
  const left = el('aside', { class: 'list-pane' },
    el('h2', {}, 'Recent changes'),
    el('ul', { id: 'changes-list' }));
  const right = el('article', { id: 'change-detail' },
    el('p', { class: 'muted' }, 'Select a change on the left to view its diff.'));
  layout.append(left, right);
  root.appendChild(layout);

  let rows;
  try { rows = await api('GET', '/changes'); }
  catch (e) { left.appendChild(errorState('Error: ' + e.message)); return; }

  const ul = $('#changes-list');
  ul.innerHTML = '';
  if (!rows.length) {
    ul.appendChild(el('li', { class: 'muted' },
      'No changes recorded yet — run a backup that produces a different config.'));
    return;
  }
  for (const c of rows) {
    const summary = `device ${c.device_id} • ${c.artifact} • +${c.added_lines}/-${c.removed_lines}`;
    const li = el('li', { 'data-id': c.id }, summary);
    li.onclick = () => showChange(c);
    ul.appendChild(li);
  }
};

async function showChange(c) {
  const detail = $('#change-detail');
  if (!detail) return;
  for (const li of $$('#changes-list li')) {
    li.classList.toggle('active', Number(li.dataset.id) === c.id);
  }
  detail.innerHTML = '';
  detail.appendChild(el('h2', {}, `Change #${c.id}`));
  detail.appendChild(el('p', { class: 'muted' },
    `${c.artifact} • device ${c.device_id} • ` +
    `${new Date(c.created_at).toLocaleString()} • ` +
    `+${c.added_lines}/-${c.removed_lines}`));
  const actionsRow = el('div', { class: 'actions' });
  if (!c.reviewed) {
    const btn = el('button', { class: 'btn' }, 'Mark reviewed');
    btn.onclick = async () => {
      try { await api('POST', `/changes/${c.id}/review`); btn.disabled = true; btn.textContent = 'Reviewed'; }
      catch (e) { alert('Failed: ' + e.message); }
    };
    actionsRow.appendChild(btn);
  } else {
    actionsRow.appendChild(el('span', { class: 'badge ok' }, 'reviewed'));
  }
  // Rollback always proposes — never applies directly.
  if (c.device_id && c.old_sha) {
    const rb = el('button', { class: 'btn ghost' }, 'Propose rollback to previous');
    rb.onclick = async () => {
      if (!confirm(`Open a rollback proposal for device #${c.device_id} to commit ${c.old_sha.slice(0,8)}?`)) return;
      try {
        await api('POST', `/devices/${c.device_id}/rollback`, {
          artifact: c.artifact || 'running-config', target_sha: c.old_sha,
        });
        alert('Rollback proposal created. See Approvals.');
        location.hash = '#/approvals';
      } catch (e) { alert('Failed: ' + e.message); }
    };
    actionsRow.appendChild(rb);
  }
  detail.appendChild(actionsRow);

  detail.appendChild(el('h3', {}, 'Diff'));
  try {
    const d = await api('GET', `/changes/${c.id}/diff`);
    detail.appendChild(renderTwoPaneDiff(d || ''));
  } catch (e) {
    detail.appendChild(el('p', { class: 'error' }, 'Could not load diff: ' + e.message));
  }
}

// renderTwoPaneDiff converts a unified diff into a side-by-side Git-style view.
//
// The input is the raw unified-diff text returned by /changes/{id}/diff.
// We parse hunks, then for each hunk fan lines into a left (before) and right
// (after) column so add/del rows align. Context lines appear in both columns.
//
// This is an intentionally minimal renderer: we do not compute intra-line
// highlights. For the common NCM case (line-level edits) it is dramatically
// clearer than a single-pane unified diff.
function renderTwoPaneDiff(text) {
  const wrap = el('div');
  wrap.appendChild(el('div', { class: 'diff-toolbar' },
    el('span', {}, 'Side-by-side diff'),
    el('span', { class: 'legend' },
      el('span', { class: 'swatch del' }), 'removed'),
    el('span', { class: 'legend' },
      el('span', { class: 'swatch add' }), 'added')));
  const pane = el('div', { class: 'diff-twopane' });
  const left  = el('div', {}, el('div', { class: 'pane-head' }, 'before'));
  const right = el('div', { class: 'right' }, el('div', { class: 'pane-head' }, 'after'));
  pane.append(left, right);
  if (!text) {
    left.appendChild(el('div', { class: 'diff-row' },
      el('span', { class: 'ln' }, ''), el('span', { class: 'src muted' }, '(no diff)')));
    right.appendChild(el('div', { class: 'diff-row' },
      el('span', { class: 'ln' }, ''), el('span', { class: 'src muted' }, '(no diff)')));
    wrap.appendChild(pane);
    return wrap;
  }

  const row = (col, cls, lnLabel, src) => {
    col.appendChild(el('div', { class: 'diff-row ' + cls },
      el('span', { class: 'ln' }, lnLabel == null ? '' : String(lnLabel)),
      el('span', { class: 'src' }, src)));
  };

  let lnA = 0, lnB = 0;
  const lines = text.split(/\r?\n/);
  // Buffers of pending del/add to align them within a hunk.
  let dels = [], adds = [];
  const flush = () => {
    const n = Math.max(dels.length, adds.length);
    for (let i = 0; i < n; i++) {
      if (i < dels.length) {
        row(left, 'del', ++lnA, dels[i]);
      } else {
        row(left, '', '', '');
      }
      if (i < adds.length) {
        row(right, 'add', ++lnB, adds[i]);
      } else {
        row(right, '', '', '');
      }
    }
    dels = []; adds = [];
  };

  // Only treat lines as file/diff headers before we've seen the first hunk.
  // Once inside a hunk, a `-` or `+` prefix is a real diff line and a `---`
  // (or `+++`) at column 0 within a config could be legitimate content
  // (e.g. ASCII separator banners on Cisco IOS) — skipping it would silently
  // drop config text from the rendered view.
  let inHunk = false;
  for (const ln of lines) {
    if (!inHunk && (
        /^--- (a\/|\/dev\/null|"a\/)/.test(ln) ||
        /^\+\+\+ (b\/|\/dev\/null|"b\/)/.test(ln) ||
        ln.startsWith('diff --git ') ||
        ln.startsWith('diff -') ||
        ln.startsWith('index ') ||
        ln.startsWith('Binary files ') ||
        ln.startsWith('similarity index ') ||
        ln.startsWith('rename ') ||
        ln.startsWith('new file mode ') ||
        ln.startsWith('deleted file mode '))) {
      // pre-hunk file/diff headers — skip in the rendered view
      continue;
    }
    if (ln.startsWith('@@')) {
      inHunk = true;
      flush();
      // Parse "@@ -a,b +c,d @@" to reset line counters.
      const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(ln);
      if (m) { lnA = Number(m[1]) - 1; lnB = Number(m[2]) - 1; }
      row(left,  'hunk', '', ln);
      row(right, 'hunk', '', ln);
      continue;
    }
    if (inHunk && ln.startsWith('+')) { adds.push(ln.slice(1)); continue; }
    if (inHunk && ln.startsWith('-')) { dels.push(ln.slice(1)); continue; }
    if (!inHunk) continue; // any other pre-hunk noise
    // context line (or empty) inside a hunk
    flush();
    const ctx = ln.startsWith(' ') ? ln.slice(1) : ln;
    row(left,  '', ++lnA, ctx);
    row(right, '', ++lnB, ctx);
  }
  flush();
  wrap.appendChild(pane);
  return wrap;
}

// ---------- Compliance (rules + findings) ----------
views.compliance = async (root) => {
  root.appendChild(el('h2', {}, 'Compliance'));

  // Add-rule form
  const form = el('form', { id: 'add-rule-form' },
    el('label', {}, 'Name ', el('input', { name: 'name', required: true })),
    el('label', {}, 'Kind ',
      el('select', { name: 'kind' },
        el('option', { value: 'must_include' }, 'must_include'),
        el('option', { value: 'must_exclude' }, 'must_exclude'),
        el('option', { value: 'regex' }, 'regex'),
        el('option', { value: 'ordered_block' }, 'ordered_block'))),
    el('label', {}, 'Pattern ', el('input', { name: 'pattern', required: true })),
    el('label', {}, 'Severity ',
      el('select', { name: 'severity' },
        el('option', { value: 'low' }, 'low'),
        el('option', { value: 'medium', selected: true }, 'medium'),
        el('option', { value: 'high' }, 'high'),
        el('option', { value: 'critical' }, 'critical'))),
    el('button', { type: 'submit' }, 'Add rule'),
  );
  const rulesCard = el('div', { class: 'card' },
    el('h3', {}, 'Rules'),
    el('details', {}, el('summary', {}, 'Add rule'), form),
    el('div', { id: 'rules-table' }, el('p', { class: 'muted' }, 'Loading…')));
  const findingsCard = el('div', { class: 'card' },
    el('h3', {}, 'Findings'),
    el('div', { id: 'findings-table' }, el('p', { class: 'muted' }, 'Loading…')));
  root.append(rulesCard, findingsCard);

  const renderRules = async () => {
    const dst = $('#rules-table');
    if (!dst) return;
    let rules;
    try { rules = await api('GET', '/compliance/rules'); }
    catch (e) { dst.innerHTML = ''; dst.appendChild(el('p', { class: 'error' }, 'Error: ' + e.message)); return; }
    dst.innerHTML = '';
    if (!rules.length) { dst.appendChild(el('p', { class: 'muted' }, 'No rules defined yet.')); return; }
    const t = el('table', { class: 'data' });
    t.innerHTML = '<thead><tr><th>ID</th><th>Name</th><th>Kind</th><th>Pattern</th><th>Severity</th><th></th></tr></thead>';
    const tb = el('tbody');
    for (const r of rules) {
      const del = el('button', { class: 'btn ghost' }, 'Delete');
      del.onclick = async () => {
        if (!confirm('Delete rule ' + r.name + '?')) return;
        try { await api('DELETE', `/compliance/rules/${r.id}`); renderRules(); }
        catch (e) { alert('Failed: ' + e.message); }
      };
      tb.appendChild(el('tr', {},
        el('td', {}, String(r.id)),
        el('td', {}, r.name),
        el('td', {}, el('code', {}, r.kind)),
        el('td', {}, el('code', {}, r.pattern)),
        el('td', {}, el('span', { class: 'badge ' + (r.severity === 'high' || r.severity === 'critical' ? 'bad' : 'info') }, r.severity)),
        el('td', {}, del),
      ));
    }
    t.appendChild(tb);
    dst.appendChild(t);
  };

  const renderFindings = async () => {
    const dst = $('#findings-table');
    if (!dst) return;
    let findings, rules;
    try {
      [findings, rules] = await Promise.all([
        api('GET', '/compliance/findings'),
        api('GET', '/compliance/rules'),
      ]);
    } catch (e) { dst.innerHTML = ''; dst.appendChild(el('p', { class: 'error' }, 'Error: ' + e.message)); return; }
    dst.innerHTML = '';
    if (!findings.length) { dst.appendChild(el('p', { class: 'muted' }, 'No findings yet — back up a device with rules defined.')); return; }
    const ruleByID = new Map((rules || []).map((r) => [r.id, r]));
    const sevOrder = { critical: 0, high: 1, medium: 2, low: 3 };
    const groups = new Map();
    for (const f of findings) {
      const rule = ruleByID.get(f.rule_id);
      const sev = (rule && rule.severity) || 'unknown';
      if (!groups.has(sev)) groups.set(sev, []);
      groups.get(sev).push({ ...f, _rule: rule });
    }
    const sevs = Array.from(groups.keys()).sort(
      (a, b) => (sevOrder[a] ?? 99) - (sevOrder[b] ?? 99));
    for (const sev of sevs) {
      const group = groups.get(sev);
      dst.appendChild(el('h4', { class: 'finding-group' },
        el('span', { class: 'badge ' + statusBadgeClass(sev === 'critical' || sev === 'high' ? 'fail' : (sev === 'low' ? 'pass' : 'pending')) }, sev),
        ' ', String(group.length) + (group.length === 1 ? ' finding' : ' findings')));
      const t = el('table', { class: 'data' });
      t.innerHTML = '<thead><tr><th>Device</th><th>Rule</th><th>Status</th><th>Detail</th></tr></thead>';
      const tb = el('tbody');
      for (const f of group) {
        const ruleLabel = f._rule ? `${f._rule.name} (#${f.rule_id})` : '#' + f.rule_id;
        tb.appendChild(el('tr', {},
          el('td', {}, '#' + f.device_id),
          el('td', {}, ruleLabel),
          el('td', {}, el('span', { class: 'badge ' + statusBadgeClass(f.status) }, f.status)),
          el('td', {}, f.detail || ''),
        ));
      }
      t.appendChild(tb);
      dst.appendChild(t);
    }
  };

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const fd = new FormData(form);
    try {
      await api('POST', '/compliance/rules', {
        name: fd.get('name'), kind: fd.get('kind'),
        pattern: fd.get('pattern'), severity: fd.get('severity'),
      });
      form.reset();
      renderRules();
    } catch (err) { alert('Failed: ' + err.message); }
  });

  await renderRules();
  await renderFindings();
};

// ---------- Topology (interactive SVG canvas with hand-rolled force layout) ----------
views.topology = async (root) => {
  root.appendChild(el('h2', {}, 'Topology'));
  let data;
  try { data = await api('GET', '/topology'); }
  catch (e) { root.appendChild(errorState('Error: ' + e.message)); return; }
  const links = (data && data.links) || [];
  if (!links.length) {
    root.appendChild(emptyState('No topology links recorded yet — install LLDP/CDP probes to populate this view.'));
    return;
  }
  // Optional: also load devices so we can colour nodes by compliance.
  let devices = [], findings = [];
  try { devices = await api('GET', '/devices'); } catch (_) {}
  try { findings = await api('GET', '/compliance/findings'); } catch (_) {}
  // Fetch rule count once so the helper agrees with the Inventory cards.
  const ruleCount = await safeCount('/compliance/rules');
  const hasRules = ruleCount > 0;
  const failsByHost = {};
  const devByHost = {};
  const devByID = {};
  for (const d of devices) {
    devByHost[d.hostname] = d;
    devByID[d.id] = d;
  }
  for (const f of (findings || [])) {
    if (f.status !== 'fail') continue;
    const dev = devByID[f.device_id];
    if (dev) failsByHost[dev.hostname] = (failsByHost[dev.hostname] || 0) + 1;
  }

  const wrap = el('div', { class: 'topo-wrap' });
  const legend = el('div', { class: 'legend' },
    el('span', {}, el('span', { class: 'dot', style: 'background:var(--status-ok)' }), 'ok'),
    el('span', {}, el('span', { class: 'dot', style: 'background:var(--status-warn)' }), 'drift'),
    el('span', {}, el('span', { class: 'dot', style: 'background:var(--status-bad)' }), 'violation'),
    el('span', {}, el('span', { class: 'dot', style: 'background:var(--text-muted)' }), 'unknown'));
  wrap.appendChild(legend);
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 1000 540');
  wrap.appendChild(svg);
  const sidePanel = el('aside', { class: 'side-panel', hidden: true, id: 'topo-side' });
  wrap.appendChild(sidePanel);
  root.appendChild(wrap);

  // Build node + edge sets.
  const nodeNames = new Set();
  for (const l of links) { nodeNames.add(l.a); nodeNames.add(l.b); }
  const nodes = [...nodeNames].map((name, i) => {
    // initial layout: ring (deterministic, no jitter on reload)
    const angle = (i / nodeNames.size) * Math.PI * 2;
    return {
      name,
      x: 500 + Math.cos(angle) * 200,
      y: 270 + Math.sin(angle) * 180,
      vx: 0, vy: 0,
    };
  });
  const idx = Object.fromEntries(nodes.map((n, i) => [n.name, i]));
  const edges = links.map((l) => ({ a: idx[l.a], b: idx[l.b], aPort: l.a_port, bPort: l.b_port }));

  // Hand-rolled spring layout: ~120 iterations is enough for graphs ≤ 100 nodes.
  // Repulsion (Coulomb-like) between every pair, attraction (Hooke) along edges.
  const W = 1000, H = 540, ITER = 200;
  for (let it = 0; it < ITER; it++) {
    // Repulsion
    for (let i = 0; i < nodes.length; i++) {
      let fx = 0, fy = 0;
      for (let j = 0; j < nodes.length; j++) {
        if (i === j) continue;
        const dx = nodes[i].x - nodes[j].x;
        const dy = nodes[i].y - nodes[j].y;
        const dist2 = dx * dx + dy * dy + 0.01;
        const f = 9000 / dist2;
        fx += dx * f / Math.sqrt(dist2);
        fy += dy * f / Math.sqrt(dist2);
      }
      nodes[i].vx = (nodes[i].vx + fx) * 0.5;
      nodes[i].vy = (nodes[i].vy + fy) * 0.5;
    }
    // Attraction along edges (target length 130)
    for (const e of edges) {
      const A = nodes[e.a], B = nodes[e.b];
      const dx = B.x - A.x, dy = B.y - A.y;
      const dist = Math.sqrt(dx * dx + dy * dy) || 0.01;
      const f = (dist - 130) * 0.05;
      const fx = dx / dist * f, fy = dy / dist * f;
      A.vx += fx; A.vy += fy;
      B.vx -= fx; B.vy -= fy;
    }
    // Centering & bound
    for (const n of nodes) {
      n.vx += (W / 2 - n.x) * 0.001;
      n.vy += (H / 2 - n.y) * 0.001;
      n.x += Math.max(-15, Math.min(15, n.vx));
      n.y += Math.max(-15, Math.min(15, n.vy));
      n.x = Math.max(40, Math.min(W - 40, n.x));
      n.y = Math.max(40, Math.min(H - 40, n.y));
    }
  }

  // Render edges first so nodes draw on top.
  for (const e of edges) {
    const A = nodes[e.a], B = nodes[e.b];
    const line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
    line.setAttribute('x1', A.x); line.setAttribute('y1', A.y);
    line.setAttribute('x2', B.x); line.setAttribute('y2', B.y);
    line.setAttribute('class', 'topo-edge');
    svg.appendChild(line);
  }
  for (const n of nodes) {
    const g = document.createElementNS('http://www.w3.org/2000/svg', 'g');
    g.setAttribute('class', 'topo-node');
    g.setAttribute('transform', `translate(${n.x},${n.y})`);
    const circle = document.createElementNS('http://www.w3.org/2000/svg', 'circle');
    circle.setAttribute('r', 10);
    const f = failsByHost[n.name] || 0;
    let cls = 'unknown';
    if (devByHost[n.name]) cls = complianceStatus(f, hasRules).cls;
    circle.setAttribute('class', cls);
    const t = document.createElementNS('http://www.w3.org/2000/svg', 'text');
    t.setAttribute('y', 24);
    t.textContent = n.name;
    g.append(circle, t);
    g.addEventListener('click', () => showTopologyNode(n.name, devByHost[n.name], failsByHost[n.name] || 0, hasRules));
    svg.appendChild(g);
  }
};

function showTopologyNode(name, device, fails, hasRules) {
  const side = $('#topo-side');
  if (!side) return;
  side.hidden = false;
  side.innerHTML = '';
  side.appendChild(el('h3', {}, name));
  if (!device) {
    side.appendChild(el('p', { class: 'muted' },
      'Discovered via neighbour reports but not registered as a managed device.'));
    return;
  }
  side.appendChild(el('p', { class: 'muted' },
    `${device.driver} • ${device.address}:${device.port}`));
  const { cls, label: status } = complianceStatus(fails, hasRules);
  side.appendChild(el('p', {}, el('span', { class: 'badge ' + cls }, status),
    ' ', String(fails) + ' failing rule' + (fails === 1 ? '' : 's')));
  const open = el('button', { class: 'btn' }, 'Open device →');
  open.onclick = () => { location.hash = '#/inventory'; setTimeout(() => showDevice(device.id), 50); };
  const close = el('button', { class: 'btn ghost' }, 'Close');
  close.onclick = () => { side.hidden = true; };
  side.appendChild(el('div', { class: 'actions' }, open, close));
}

// ---------- Approvals (change-request queue) ----------
views.approvals = async (root) => {
  root.appendChild(el('h2', {}, 'Approvals'));
  let rows;
  try { rows = await api('GET', '/change-requests'); }
  catch (e) { root.appendChild(errorState('Error: ' + e.message)); return; }
  if (!rows.length) {
    root.appendChild(emptyState('No change requests yet.'));
    return;
  }
  const card = el('div', { class: 'card' });
  const t = el('table', { class: 'data' });
  t.innerHTML = '<thead><tr><th>ID</th><th>Title</th><th>Kind</th><th>Status</th><th>Created</th><th>Actions</th></tr></thead>';
  const tb = el('tbody');
  const reload = () => router();
  for (const r of rows) {
    const actions = el('span', { class: 'actions' });
    const mkBtn = (label, cls, op, prompt) => {
      const b = el('button', { class: 'btn ' + cls }, label);
      b.onclick = async () => {
        const body = {};
        if (prompt) {
          const reason = window.prompt(prompt + ' (optional)');
          if (reason === null) return;          // user aborted the prompt
          if (reason !== '') body.reason = reason;
        }
        try { await api('POST', `/change-requests/${r.id}/${op}`, prompt ? body : undefined); reload(); }
        catch (e) { alert('Failed: ' + e.message); }
      };
      return b;
    };
    if (r.status === 'draft')      actions.append(mkBtn('Submit', '', 'submit'));
    if (r.status === 'submitted')  actions.append(mkBtn('Approve', '', 'approve', 'Approval note'),
                                                  mkBtn('Reject', 'danger', 'reject', 'Rejection reason'));
    if (r.status === 'approved')   actions.append(mkBtn('Apply', '', 'apply'));
    if (r.status === 'draft' || r.status === 'submitted')
      actions.append(mkBtn('Cancel', 'ghost', 'cancel', 'Cancel reason'));

    tb.appendChild(el('tr', {},
      el('td', {}, '#' + r.id),
      el('td', {}, r.title),
      el('td', {}, el('code', {}, r.kind)),
      el('td', {}, el('span', { class: 'badge ' + statusBadgeClass(r.status) }, r.status)),
      el('td', {}, new Date(r.created_at).toLocaleString()),
      el('td', {}, actions),
    ));
  }
  t.appendChild(tb);
  card.appendChild(t);
  root.appendChild(card);
};

// ---------- Settings (tenants, API tokens, channels, pollers) ----------
views.settings = async (root) => {
  root.appendChild(el('h2', {}, 'Settings'));

  const sections = [
    { id: 'tenants',  title: 'Tenants',                path: '/tenants',
      cols: ['id', 'name', 'max_devices', 'created_at'] },
    { id: 'tokens',   title: 'API tokens',             path: '/api-tokens',
      cols: ['id', 'name', 'prefix', 'scopes', 'created_at', 'expires_at'] },
    { id: 'channels', title: 'Notification channels',  path: '/notifications/channels',
      cols: ['id', 'name', 'kind', 'created_at'] },
    { id: 'rules',    title: 'Notification rules',     path: '/notifications/rules',
      cols: ['id', 'name', 'event_type', 'channel_id', 'created_at'] },
    { id: 'pollers',  title: 'Pollers',                path: '/pollers',
      cols: ['id', 'zone', 'name', 'last_seen', 'created_at'] },
  ];

  for (const sec of sections) {
    const card = el('div', { class: 'card' }, el('h3', {}, sec.title));
    const dst = el('div', {}, el('p', { class: 'muted' }, 'Loading…'));
    card.appendChild(dst);
    root.appendChild(card);

    let rows;
    try { rows = await api('GET', sec.path); }
    catch (e) {
      dst.innerHTML = '';
      dst.appendChild(el('p', { class: 'error' }, 'Error: ' + e.message));
      continue;
    }
    dst.innerHTML = '';
    if (!rows || !rows.length) {
      dst.appendChild(el('p', { class: 'muted' }, 'None configured.'));
      continue;
    }
    const t = el('table', { class: 'data' });
    const head = '<thead><tr>' + sec.cols.map((c) => `<th>${escapeHTML(c)}</th>`).join('') + '</tr></thead>';
    t.innerHTML = head;
    const tb = el('tbody');
    for (const r of rows) {
      const tr = el('tr');
      for (const c of sec.cols) {
        let v = r[c];
        if (v == null) v = '';
        else if (Array.isArray(v)) v = v.join(', ');
        else if (typeof v === 'object') v = JSON.stringify(v);
        else if ((c.endsWith('_at') || c === 'last_seen') && v) {
          const d = new Date(v);
          if (!isNaN(d.getTime()) && d.getUTCFullYear() > 1) v = d.toLocaleString();
          else v = '—';
        }
        tr.appendChild(el('td', {}, String(v)));
      }
      tb.appendChild(tr);
    }
    t.appendChild(tb);
    dst.appendChild(t);
  }
};

// ===========================================================
// Dashboard
// ===========================================================
//
// One server round-trip (`/dashboard/summary`) populates: stat cards with
// inline SVG sparklines, the per-driver compliance bars, drift hotspots,
// the recent-events timeline, and the cluster health card. No external
// chart library — see app.css for the .dash-* / .driver-bar / .timeline
// styles.
views.dashboard = async (root) => {
  root.appendChild(el('h2', {}, 'Dashboard'));
  let s;
  try { s = await api('GET', '/dashboard/summary'); }
  catch (e) { root.appendChild(errorState('Error: ' + e.message)); return; }

  const fmtPct = (v) => (v == null ? '—' : `${v.toFixed(1)}%`);
  const fmtInt = (v) => (v == null ? '—' : v.toLocaleString());

  // --- top stat row ---
  const stats = el('div', { class: 'dash-grid' });
  stats.append(
    statCard('Devices', fmtInt(s.devices.total),
      s.devices.added_recent ? `+${s.devices.added_recent} this week` : 'no new devices',
      null),
    statCard('Compliance', fmtPct(s.compliance.percent),
      `${s.compliance.pass_count} pass · ${s.compliance.fail_count} fail`,
      s.compliance.sparkline_14d),
    statCard('Backups (24h)', fmtPct(s.backups.success_rate_24h),
      `${s.backups.total_24h} run${s.backups.total_24h === 1 ? '' : 's'}`,
      s.backups.sparkline_14d),
    statCard('Approvals', String(s.approvals.pending),
      s.approvals.oldest_age ? `oldest ${s.approvals.oldest_age}` : 'queue empty',
      null),
  );
  root.appendChild(stats);

  // --- two-column body ---
  const body = el('div', { class: 'dash-cols' });

  // Left column: status by driver, drift hotspots
  const leftCol = el('div');
  const driverCard = el('div', { class: 'card' },
    el('h3', {}, 'Status by driver'));
  if (!s.status_by_driver.length) {
    driverCard.appendChild(el('p', { class: 'muted' }, 'No devices yet.'));
  } else {
    for (const d of s.status_by_driver) {
      const fillCls = d.percent >= 90 ? 'ok' : (d.percent >= 70 ? 'warn' : 'bad');
      driverCard.appendChild(el('div', { class: 'driver-bar' },
        el('span', {}, `${d.driver} (${d.compliant}/${d.total})`),
        el('span', { class: 'track' },
          el('span', { class: 'fill ' + fillCls, style: `width:${d.percent}%` })),
        el('span', { class: 'pct' }, `${d.percent}%`)));
    }
  }
  leftCol.appendChild(driverCard);

  const driftCard = el('div', { class: 'card' },
    el('h3', {}, 'Drift hotspots'));
  if (!s.drift_hotspots.length) {
    driftCard.appendChild(el('p', { class: 'muted' }, 'No failing rules — nice.'));
  } else {
    for (const h of s.drift_hotspots) {
      const link = el('a', { href: '#/inventory', class: 'hotspot' },
        el('span', {},
          el('span', { class: 'status-dot bad' }), ' ',
          el('strong', {}, h.hostname || `device ${h.device_id}`),
          ' ',
          el('span', { class: 'detail' }, `${h.failing} failing`)),
        el('span', { class: 'detail' }, h.top_detail || ''));
      link.onclick = () => { setTimeout(() => showDevice(h.device_id), 50); };
      driftCard.appendChild(link);
    }
  }
  leftCol.appendChild(driftCard);
  body.appendChild(leftCol);

  // Right column: recent events, health
  const rightCol = el('div');
  const eventsCard = el('div', { class: 'card' },
    el('h3', {}, 'Recent events'));
  if (!s.recent_events.length) {
    eventsCard.appendChild(el('p', { class: 'muted' }, 'No audit entries yet.'));
  } else {
    const list = el('ul', { class: 'timeline' });
    for (const e of s.recent_events.slice(0, 10)) {
      const t = new Date(e.created_at);
      const hh = String(t.getHours()).padStart(2, '0');
      const mm = String(t.getMinutes()).padStart(2, '0');
      list.appendChild(el('li', {},
        el('span', { class: 'when' }, `${hh}:${mm}`),
        el('code', {}, e.action || ''),
        el('span', { class: 'what' }, e.target || e.detail || '')));
    }
    eventsCard.appendChild(list);
    eventsCard.appendChild(el('p', { class: 'muted' },
      el('a', { href: '#/audit' }, 'See all in Audit →')));
  }
  rightCol.appendChild(eventsCard);

  const healthCard = el('div', { class: 'card health' },
    el('h3', {}, 'Health'),
    el('dl', {},
      el('dt', {}, 'Pollers'),
      el('dd', {}, `${s.health.pollers_healthy}/${s.health.pollers_total} healthy`),
      el('dt', {}, 'Git mirror'),
      el('dd', {}, s.health.git_mirror.replace('_', ' '))));
  rightCol.appendChild(healthCard);
  body.appendChild(rightCol);

  root.appendChild(body);

  // Refresh approvals badge with the count we just fetched.
  applyApprovalsBadge(s.approvals.pending);
};

// statCard returns a single .dash-stat tile. `series` may be null (no spark).
function statCard(label, value, sub, series) {
  const card = el('div', { class: 'dash-stat' },
    el('div', { class: 'label' }, label),
    el('div', { class: 'value' }, value));
  if (series && series.length) card.appendChild(sparkline(series));
  card.appendChild(el('div', { class: 'sub' }, sub));
  return card;
}

// sparkline renders a 14-bucket SVG polyline with a soft fill underneath.
// Hand-rolled to avoid pulling in a chart library — the design tokens
// (--accent, --accent-soft) drive the colour so it themes for free.
function sparkline(values) {
  const W = 140, H = 32, PAD = 2;
  const max = 100; // values are percentages 0..100
  const n = values.length;
  if (!n) return el('span');
  const step = (W - PAD * 2) / (n - 1 || 1);
  const pts = values.map((v, i) => {
    const x = PAD + i * step;
    const y = H - PAD - (Math.max(0, Math.min(max, v)) / max) * (H - PAD * 2);
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('class', 'spark');
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  // Filled area first, line on top.
  const poly = document.createElementNS('http://www.w3.org/2000/svg', 'polygon');
  poly.setAttribute('points', `${PAD},${H - PAD} ${pts.join(' ')} ${W - PAD},${H - PAD}`);
  const line = document.createElementNS('http://www.w3.org/2000/svg', 'polyline');
  line.setAttribute('points', pts.join(' '));
  svg.append(poly, line);
  return svg;
}

// refreshApprovalsBadge fetches the pending-approvals count for the
// sidebar badge. Called on session refresh; the dashboard view also
// updates the badge from its summary payload to avoid an extra request.
async function refreshApprovalsBadge() {
  try {
    const rows = await api('GET', '/change-requests?status=submitted');
    applyApprovalsBadge((rows || []).length);
  } catch (_) { /* ignore — badge stays hidden */ }
}

function applyApprovalsBadge(n) {
  const b = $('#approvals-badge');
  if (!b) return;
  if (!n || n <= 0) { b.hidden = true; return; }
  b.hidden = false;
  b.textContent = String(n > 99 ? '99+' : n);
}

// ===========================================================
// Login form + logout wiring
// ===========================================================
$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('#login-error').textContent = '';
  const fd = new FormData(e.target);
  try {
    await api('POST', '/auth/login', {
      username: fd.get('username'), password: fd.get('password'),
    });
    e.target.reset();
    await refreshSession();
  } catch (err) {
    $('#login-error').textContent = err.message;
  }
});

$('#logout').addEventListener('click', async () => {
  try { await api('POST', '/auth/logout'); } catch (_) {}
  refreshSession();
});

initTheme();
refreshSession();
