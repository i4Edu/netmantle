// NetMantle SPA — vanilla JS, hash-based router, no build step.
//
// Layout:
//   1. tiny api() helper
//   2. theme handling (light/dark + manual override in localStorage)
//   3. session refresh
//   4. router that maps #/<route> to a render function in `views`
//   5. per-view modules (inventory keeps the original behavior, audit is new,
//      everything else is a placeholder ready for follow-up PRs)

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
    if (!location.hash) location.hash = '#/inventory';
    else router();
  } catch (_) {
    me = null;
    $('#nav').hidden = true;
    $('#login-view').hidden = false;
    $('#app-view').hidden = true;
  }
}

// ---------- router ----------
const ROUTES = ['inventory', 'backups', 'compliance', 'topology', 'approvals', 'audit', 'settings'];

function currentRoute() {
  const h = (location.hash || '').replace(/^#\/?/, '');
  const r = h.split('/')[0] || 'inventory';
  return ROUTES.includes(r) ? r : 'inventory';
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
    return;
  }
  for (const d of list) {
    const li = el('li', { 'data-id': d.id }, `${d.hostname} (${d.driver})`);
    if (d.id === currentDeviceId) li.classList.add('active');
    li.onclick = () => showDevice(d.id);
    ul.appendChild(li);
  }
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

  filterBar.append(
    mkField('User ID', fUser),
    mkField('Action', fAction),
    mkField('Target', fTarget),
    mkField('Since',  fSince),
    mkField('Until',  fUntil),
    mkField('Limit',  fLimit),
    el('div', { class: 'field' }, el('label', {}, '\u00a0'),
      el('div', { class: 'actions' }, apply, clear)),
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

// ---------- placeholders for follow-up PRs ----------
const placeholder = (title, note) => (root) => {
  root.appendChild(el('h2', {}, title));
  root.appendChild(el('div', { class: 'placeholder' },
    el('h2', {}, title + ' is coming soon'),
    el('p', {}, note),
  ));
};

views.backups    = placeholder('Backups',
  'A Git-style diff viewer with one-click rollback ships in PR #4.');
views.compliance = placeholder('Compliance',
  'Rule packs with pass/fail counts and expandable findings ship in a follow-up PR.');
views.topology   = placeholder('Topology',
  'A graph canvas of LLDP/CDP links ships in PR #6. The /api/v1/topology endpoint already returns the data.');
views.approvals  = placeholder('Approvals',
  'The approval workflow + Approvals queue ship in PR #3.');
views.settings   = placeholder('Settings',
  'Tenant + integration token management ships in PR #7.');

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
