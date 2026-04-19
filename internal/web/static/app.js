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

// ---------- Backups (recent change events with diff viewer) ----------
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
  if (!c.reviewed) {
    const btn = el('button', { class: 'btn' }, 'Mark reviewed');
    btn.onclick = async () => {
      try { await api('POST', `/changes/${c.id}/review`); btn.disabled = true; btn.textContent = 'Reviewed'; }
      catch (e) { alert('Failed: ' + e.message); }
    };
    detail.appendChild(el('div', { class: 'actions' }, btn));
  } else {
    detail.appendChild(el('span', { class: 'badge ok' }, 'reviewed'));
  }
  detail.appendChild(el('h3', {}, 'Diff'));
  try {
    const d = await api('GET', `/changes/${c.id}/diff`);
    detail.appendChild(el('pre', { class: 'config' }, d || '(no diff)'));
  } catch (e) {
    detail.appendChild(el('p', { class: 'error' }, 'Could not load diff: ' + e.message));
  }
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

// ---------- Topology (LLDP/CDP node + link tables) ----------
views.topology = async (root) => {
  root.appendChild(el('h2', {}, 'Topology'));
  root.appendChild(el('p', { class: 'muted' },
    'Discovered links from LLDP/CDP neighbour reports. A graph canvas renderer is tracked as Phase 11+ work.'));
  let data;
  try { data = await api('GET', '/topology'); }
  catch (e) { root.appendChild(errorState('Error: ' + e.message)); return; }
  const links = (data && data.links) || [];
  if (!links.length) {
    root.appendChild(emptyState('No topology links recorded yet.'));
    return;
  }
  // Derive node set
  const nodes = new Set();
  for (const l of links) { nodes.add(l.a); nodes.add(l.b); }

  const nodesCard = el('div', { class: 'card' },
    el('h3', {}, `Nodes (${nodes.size})`));
  const nodeList = el('ul');
  for (const n of [...nodes].sort()) nodeList.appendChild(el('li', {}, n));
  nodesCard.appendChild(nodeList);

  const linksCard = el('div', { class: 'card' },
    el('h3', {}, `Links (${links.length})`));
  const t = el('table', { class: 'data' });
  t.innerHTML = '<thead><tr><th>A</th><th>A port</th><th>B</th><th>B port</th></tr></thead>';
  const tb = el('tbody');
  for (const l of links) {
    tb.appendChild(el('tr', {},
      el('td', {}, l.a), el('td', {}, el('code', {}, l.a_port)),
      el('td', {}, l.b), el('td', {}, el('code', {}, l.b_port)),
    ));
  }
  t.appendChild(tb);
  linksCard.appendChild(t);
  root.append(nodesCard, linksCard);
};

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
