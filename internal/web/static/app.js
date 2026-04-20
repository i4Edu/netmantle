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
    window._me = me;
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
const ROUTES = ['dashboard', 'inventory', 'backups', 'compliance', 'automation', 'topology', 'approvals', 'audit', 'settings', 'zones', 'search', 'notifications', 'users'];

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

// Sidebar collapse toggle
document.addEventListener('DOMContentLoaded', () => {
  const sb = document.getElementById('sidebar');
  const toggle = document.getElementById('sidebar-toggle');
  if (toggle && sb) {
    const saved = localStorage.getItem('nm.sidebar.collapsed');
    if (saved === 'true') sb.dataset.collapsed = 'true';
    toggle.addEventListener('click', () => {
      const next = sb.dataset.collapsed !== 'true';
      sb.dataset.collapsed = String(next);
      localStorage.setItem('nm.sidebar.collapsed', String(next));
    });
  }

  // Slide-over close
  const overlay = document.getElementById('slideover-overlay');
  const slideoverEl = document.getElementById('device-slideover');
  const closeBtn = document.getElementById('slideover-close');
  function closeSlideOver() {
    if (slideoverEl) slideoverEl.hidden = true;
    if (overlay) overlay.hidden = true;
  }
  if (overlay) overlay.addEventListener('click', closeSlideOver);
  if (closeBtn) closeBtn.addEventListener('click', closeSlideOver);

  // Slide-over tab switching
  const tabsContainer = document.getElementById('slideover-tabs');
  if (tabsContainer) {
    tabsContainer.addEventListener('click', (e) => {
      const tab = e.target.closest('.slideover-tab');
      if (!tab) return;
      $$('.slideover-tab', tabsContainer).forEach(t => t.classList.remove('active'));
      tab.classList.add('active');
      if (window._currentSlideoverDev) {
        renderSlideoverTab(window._currentSlideoverDev, tab.dataset.tab);
      }
    });
  }

  // Topbar global search → navigate to Config Search
  const topbarSearch = document.getElementById('topbar-search');
  if (topbarSearch) {
    topbarSearch.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && topbarSearch.value.trim()) {
        window._globalSearchQuery = topbarSearch.value.trim();
        location.hash = '#/search';
        topbarSearch.blur();
      }
    });
  }
});

// ===========================================================
// Views
// ===========================================================
const views = {};

// ---------- Inventory (keeps original device CRUD + backup + runs) ----------
let currentDeviceId = null;

views.inventory = (root) => {
  const toolbar = el('div', { class: 'inv-toolbar' },
    el('input', { type: 'search', id: 'dev-search', placeholder: 'Filter devices…' }),
    el('select', { id: 'dev-group-filter' },
      el('option', { value: '' }, 'All drivers')),
    el('button', { class: 'btn', id: 'add-dev-toggle' }, '+ Add device'),
    el('button', { class: 'btn', id: 'add-cred-toggle' }, '+ Add credential'),
  );

  let allDevices = [], allFindings = [], ruleCount = 0, lastBackups = {};

  const addDevSection = el('div', { id: 'add-dev-section', hidden: true, class: 'card', style: 'margin-bottom:12px' },
    buildAddDeviceForm(() => loadAll()));
  const addCredSection = el('div', { id: 'add-cred-section', hidden: true, class: 'card', style: 'margin-bottom:12px' },
    buildAddCredentialForm());

  const tableWrap = el('div', { class: 'table-wrapper' });
  const statsBar = el('div', { style: 'font-size:var(--font-size-xs);color:var(--text-muted);padding:4px 0;', id: 'dev-stats' });

  root.append(el('h2', {}, 'Inventory'),
    toolbar, addDevSection, addCredSection, statsBar, tableWrap);

  toolbar.querySelector('#add-dev-toggle').onclick = () => {
    addDevSection.hidden = !addDevSection.hidden;
    loadAux();
  };
  toolbar.querySelector('#add-cred-toggle').onclick = () => {
    addCredSection.hidden = !addCredSection.hidden;
  };

  async function renderTable() {
    const q = ($('#dev-search') || { value: '' }).value.toLowerCase();
    const grp = ($('#dev-group-filter') || { value: '' }).value;
    let devs = allDevices.filter(d =>
      (!q || d.hostname.toLowerCase().includes(q) || d.driver.includes(q) || d.address.includes(q)) &&
      (!grp || d.driver === grp));

    const failByDev = {}, passByDev = {};
    for (const f of allFindings) {
      if (f.status === 'pass') passByDev[f.device_id] = (passByDev[f.device_id] || 0) + 1;
      if (f.status === 'fail') failByDev[f.device_id] = (failByDev[f.device_id] || 0) + 1;
    }

    const tbl = el('table', { class: 'device-table' });
    const thead = el('thead', {},
      el('tr', {},
        el('th', {}, 'Status'),
        el('th', {}, 'Hostname'),
        el('th', {}, 'Driver'),
        el('th', {}, 'Address'),
        el('th', {}, 'Last Backup'),
        el('th', {}, 'Compliance'),
        el('th', {}, 'Actions'),
      ));
    const tbody = el('tbody');

    for (const d of devs) {
      const fails = failByDev[d.id] || 0;
      const passes = passByDev[d.id] || 0;
      const evaluated = fails + passes;
      const score = evaluated > 0 ? Math.round((passes * 100) / evaluated) : null;
      const { cls, label: statusLabel } = complianceStatus(fails, ruleCount > 0);
      const lb = lastBackups[d.id] || '—';

      const quickActions = el('div', { class: 'quick-actions' },
        el('button', { class: 'qa-btn', title: 'Backup now' }, '⚡ Backup'),
        el('button', { class: 'qa-btn', title: 'View history' }, '📋 History'),
      );
      quickActions.children[0].onclick = async (e) => {
        e.stopPropagation();
        quickActions.children[0].textContent = '…';
        try { await api('POST', `/devices/${d.id}/backup`); await loadAll(); }
        catch (err) { alert('Backup failed: ' + err.message); }
        quickActions.children[0].textContent = '⚡ Backup';
      };
      quickActions.children[1].onclick = (e) => {
        e.stopPropagation();
        openSlideOver(d);
      };

      const tr = el('tr', { 'data-id': d.id },
        el('td', {}, el('span', { class: 'badge ' + cls }, statusLabel)),
        el('td', {}, el('strong', {}, escapeHTML(d.hostname))),
        el('td', {}, el('code', { style: 'font-size:0.75rem' }, escapeHTML(d.driver))),
        el('td', {}, `${escapeHTML(d.address)}:${d.port}`),
        el('td', {}, lb),
        el('td', {}, score == null ? '—' : score + '%'),
        el('td', {}, quickActions),
      );
      tr.onclick = () => openSlideOver(d);
      tbody.appendChild(tr);
    }
    tbl.append(thead, tbody);
    tableWrap.innerHTML = '';
    tableWrap.appendChild(tbl);

    const statsEl = $('#dev-stats');
    if (statsEl) statsEl.textContent = `${devs.length} device${devs.length === 1 ? '' : 's'}`;
  }

  async function loadAll() {
    [allDevices, allFindings, ruleCount] = await Promise.all([
      api('GET', '/devices').catch(() => []),
      api('GET', '/compliance/findings').catch(() => []),
      safeCount('/compliance/rules'),
    ]);
    const driverSel = $('#dev-group-filter');
    if (driverSel) {
      const drivers = [...new Set(allDevices.map(d => d.driver))].sort();
      driverSel.innerHTML = '<option value="">All drivers</option>';
      for (const dr of drivers) driverSel.appendChild(el('option', { value: dr }, dr));
    }
    await Promise.all(allDevices.map(async (d) => {
      try {
        const runs = await api('GET', `/devices/${d.id}/runs`);
        const ok = (runs || []).find(r => r.status === 'success');
        lastBackups[d.id] = ok ? relativeTime(new Date(ok.started_at)) : '—';
      } catch (_) { lastBackups[d.id] = '—'; }
    }));
    renderTable();
  }

  $('#dev-search')?.addEventListener('input', renderTable);
  $('#dev-group-filter')?.addEventListener('change', renderTable);

  loadAll();
};

function buildAddDeviceForm(onCreated) {
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
      if (onCreated) await onCreated(); else await loadDevices();
    } catch (err) { alert('Create failed: ' + err.message); }
  });
  return form;
}

function buildAddCredentialForm(onCreated) {
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
      onCreated?.();
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

// ---------- Contextual sidebar (inventory command center) ----------
async function showCtxSidebar(dev) {
  const sidebar = $('#ctx-sidebar');
  if (!sidebar) return;
  sidebar.hidden = false;
  const content = $('#ctx-content');
  content.innerHTML = '';

  const closeBtn = el('button', { class: 'close-btn', title: 'Close' }, '×');
  closeBtn.style.cssText = 'position:absolute;top:12px;right:12px;background:none;border:none;cursor:pointer;font-size:1.4rem;color:var(--text-muted)';
  closeBtn.onclick = () => { sidebar.hidden = true; };
  sidebar.style.position = 'relative';

  content.append(
    closeBtn,
    el('h3', {}, escapeHTML(dev.hostname)),
    el('p', { class: 'muted', style: 'font-size:var(--font-size-xs);margin:0 0 12px' },
      `${escapeHTML(dev.driver)} · ${escapeHTML(dev.address)}:${dev.port}`),
  );

  const bkBtn = el('button', { class: 'btn', style: 'width:100%;margin-bottom:12px' }, '⚡ Backup Now');
  bkBtn.onclick = async () => {
    bkBtn.disabled = true; bkBtn.textContent = 'Running…';
    try { await api('POST', `/devices/${dev.id}/backup`); bkBtn.textContent = '✓ Done'; }
    catch (e) { bkBtn.textContent = '✗ Failed'; alert(e.message); }
    setTimeout(() => { bkBtn.disabled = false; bkBtn.textContent = '⚡ Backup Now'; }, 2000);
    loadRunsSection();
  };
  content.appendChild(bkBtn);

  const runsSection = el('div');
  content.appendChild(runsSection);
  runsSection.appendChild(el('h3', {}, 'Recent Runs'));

  async function loadRunsSection() {
    let runRows = runsSection.querySelector('.runs-list');
    if (!runRows) {
      runRows = el('div', { class: 'runs-list' });
      runsSection.appendChild(runRows);
    }
    runRows.innerHTML = '<p style="font-size:var(--font-size-xs);color:var(--text-muted)">Loading…</p>';
    try {
      const runs = await api('GET', `/devices/${dev.id}/runs`);
      runRows.innerHTML = '';
      for (const r of (runs || []).slice(0, 10)) {
        const st = r.status === 'success' ? 'ok' : r.status === 'running' ? 'warn' : 'bad';
        const dt = r.started_at ? relativeTime(new Date(r.started_at)) : '—';
        runRows.appendChild(el('div', { class: 'run-row' },
          el('span', { class: 'badge badge-' + st }, r.status),
          el('span', { style: 'color:var(--text-muted)' }, dt),
          r.commit_sha ? el('code', { style: 'font-size:0.65rem' }, r.commit_sha.slice(0, 8)) : el('span', {}),
        ));
      }
      if (!runs || !runs.length) {
        runRows.innerHTML = '<p style="font-size:var(--font-size-xs);color:var(--text-muted)">No runs yet.</p>';
      }
    } catch (_) {
      runRows.innerHTML = '<p style="color:var(--status-bad);font-size:var(--font-size-xs)">Failed to load runs.</p>';
    }
  }
  loadRunsSection();

  const cfgHeader = el('h3', { style: 'margin-top:16px' }, 'Latest Config');
  const cfgPre = el('pre', { class: 'ctx-sidebar config-pre' }, 'Loading…');
  content.append(cfgHeader, cfgPre);
  try {
    const cfg = await api('GET', `/devices/${dev.id}/config`);
    cfgPre.textContent = typeof cfg === 'string'
      ? cfg.slice(0, 2000) + (cfg.length > 2000 ? '\n…' : '')
      : JSON.stringify(cfg, null, 2);
  } catch (_) { cfgPre.textContent = 'No config available.'; }
}

// ---------- Slide-over panel ----------
async function openSlideOver(dev) {
  window._currentSlideoverDev = dev;
  const slideoverEl = document.getElementById('device-slideover');
  const overlay = document.getElementById('slideover-overlay');
  if (!slideoverEl) { showCtxSidebar(dev); return; }

  document.getElementById('slideover-title').textContent = dev.hostname;
  document.getElementById('slideover-sub').textContent = `${dev.driver} · ${dev.address}:${dev.port}`;

  $$('.slideover-tab').forEach(t => t.classList.toggle('active', t.dataset.tab === 'info'));

  slideoverEl.hidden = false;
  overlay.hidden = false;

  renderSlideoverTab(dev, 'info');
}

async function renderSlideoverTab(dev, tab) {
  const body = document.getElementById('slideover-body');
  if (!body) return;
  body.innerHTML = '<p class="muted" style="padding:8px">Loading…</p>';

  if (tab === 'info') {
    body.innerHTML = '';
    const bkBtn = el('button', { class: 'btn', style: 'width:100%;margin-bottom:16px' }, '⚡ Backup Now');
    bkBtn.onclick = async () => {
      bkBtn.disabled = true; bkBtn.textContent = 'Running…';
      try { await api('POST', `/devices/${dev.id}/backup`); bkBtn.textContent = '✓ Queued'; }
      catch (e) { bkBtn.textContent = '✗ Failed'; alert(e.message); }
      setTimeout(() => { bkBtn.disabled = false; bkBtn.textContent = '⚡ Backup Now'; }, 2000);
    };
    body.appendChild(bkBtn);

    const rows = [
      ['Hostname', dev.hostname],
      ['Driver', dev.driver],
      ['Address', dev.address],
      ['Port', String(dev.port || 22)],
      ['Credential', dev.credential_id ? '#' + dev.credential_id : '—'],
      ['Device ID', '#' + dev.id],
    ];
    const dl = el('dl', { style: 'display:grid;grid-template-columns:140px 1fr;gap:6px 12px;font-size:var(--font-size-sm)' });
    for (const [k, v] of rows) {
      dl.append(el('dt', { style: 'font-weight:600;color:var(--text-muted)' }, k), el('dd', { style: 'margin:0' }, escapeHTML(String(v))));
    }
    body.appendChild(dl);

    const delBtn = el('button', { class: 'btn danger', style: 'width:100%;margin-top:20px' }, 'Delete device');
    delBtn.onclick = async () => {
      if (!confirm(`Delete ${dev.hostname}?`)) return;
      try {
        await api('DELETE', '/devices/' + dev.id);
        document.getElementById('device-slideover').hidden = true;
        document.getElementById('slideover-overlay').hidden = true;
        location.hash = '#/inventory';
        router();
      } catch (e) { alert(e.message); }
    };
    body.appendChild(delBtn);

  } else if (tab === 'backups') {
    body.innerHTML = '';
    try {
      const runs = await api('GET', `/devices/${dev.id}/runs`);
      if (!runs || !runs.length) { body.innerHTML = '<p class="muted">No backup runs yet.</p>'; return; }
      const tbl = el('table', { class: 'device-table' },
        el('thead', {}, el('tr', {}, el('th', {}, 'Started'), el('th', {}, 'Status'), el('th', {}, 'Commit'), el('th', {}, 'Error'))),
        el('tbody'));
      for (const r of runs) {
        const st = r.status === 'success' ? 'ok' : r.status === 'running' ? 'warn' : 'bad';
        const dt = r.started_at ? new Date(r.started_at).toLocaleString() : '—';
        tbl.querySelector('tbody').appendChild(el('tr', {},
          el('td', {}, dt),
          el('td', {}, el('span', { class: 'badge badge-' + st }, r.status)),
          el('td', {}, r.commit_sha ? el('code', { style: 'font-size:0.7rem' }, r.commit_sha.slice(0, 8)) : el('span', {}, '—')),
          el('td', { style: 'font-size:0.75rem;color:var(--text-muted);max-width:160px;overflow:hidden;text-overflow:ellipsis' }, escapeHTML(r.error || '')),
        ));
      }
      body.appendChild(tbl);

      // Export toolbar
      body.appendChild(exportToolbar(async () => {
        try { return await api('GET', `/devices/${dev.id}/config`); } catch (_) { return ''; }
      }));
    } catch (e) { body.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }

  } else if (tab === 'diffs') {
    body.innerHTML = '';
    try {
      const evts = await api('GET', `/changes?device_id=${dev.id}`);
      if (!evts || !evts.length) { body.innerHTML = '<p class="muted">No config changes recorded yet. Run a backup to detect changes.</p>'; return; }
      for (const c of evts.slice(0, 15)) {
        const dt = c.created_at ? relativeTime(new Date(c.created_at)) : '—';
        const addBadge = c.added > 0 ? el('span', { style: 'color:var(--status-ok);font-weight:600;font-size:0.7rem' }, `+${c.added}`) : null;
        const delBadge = c.removed > 0 ? el('span', { style: 'color:var(--status-bad);font-weight:600;font-size:0.7rem;margin-left:6px' }, `-${c.removed}`) : null;
        const card = el('div', { class: 'card', style: 'margin-bottom:10px;padding:10px 14px;cursor:pointer' });
        const hdr = el('div', { style: 'display:flex;justify-content:space-between;align-items:center;font-size:var(--font-size-xs)' },
          el('span', {}, el('strong', {}, dt), addBadge, delBadge),
          el('code', { style: 'font-size:0.65rem' }, c.new_sha ? c.new_sha.slice(0, 8) : '—'));
        const diffPre = el('div', { hidden: true, style: 'margin-top:8px;max-height:300px;overflow-y:auto' });
        hdr.onclick = async () => {
          if (!diffPre.hidden) { diffPre.hidden = true; return; }
          diffPre.innerHTML = '<span class="muted">Loading diff…</span>';
          diffPre.hidden = false;
          try {
            const diffText = await api('GET', `/changes/${c.id}/diff`);
            diffPre.innerHTML = '';
            if (!diffText || !diffText.trim()) {
              diffPre.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-xs)' }, 'No diff available.'));
              return;
            }
            for (const line of diffText.split('\n').slice(0, 200)) {
              const cls = line.startsWith('+') && !line.startsWith('+++') ? 'diff-add'
                : line.startsWith('-') && !line.startsWith('---') ? 'diff-del'
                : line.startsWith('@@') ? 'diff-hunk' : 'diff-ctx';
              diffPre.appendChild(el('div', { class: 'diff-line ' + cls }, escapeHTML(line)));
            }
          } catch (e) { diffPre.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
        };
        card.append(hdr, diffPre);
        body.appendChild(card);
      }
    } catch (e) { body.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }

  } else if (tab === 'log') {
    body.innerHTML = '';
    body.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' }, 'Raw CLI interaction log for the most recent backup run:'));
    try {
      const runs = await api('GET', `/devices/${dev.id}/runs`);
      const latest = (runs || [])[0];
      if (!latest) { body.innerHTML = '<p class="muted">No runs yet.</p>'; return; }
      const logEl = el('pre', { style: 'font-family:var(--font-config,var(--font-mono));font-size:0.72rem;background:#0d1117;color:#c9d1d9;padding:12px;border-radius:var(--radius-md);overflow:auto;max-height:400px;white-space:pre-wrap' });
      logEl.textContent = latest.log_output || latest.error || '(no log output)';
      body.appendChild(logEl);
    } catch (e) { body.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
  }
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
      el('span', { class: 'swatch add' }), 'added'),
    el('label', { style: 'display:inline-flex;gap:4px;align-items:center;cursor:pointer;margin-left:auto' },
      el('input', { type: 'checkbox', id: 'mute-ts' }),
      'Mute timestamps')));
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
        row(left, 'diff-del', ++lnA, dels[i]);
      } else {
        row(left, '', '', '');
      }
      if (i < adds.length) {
        row(right, 'diff-add', ++lnB, adds[i]);
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
      row(left,  'diff-hunk', '', ln);
      row(right, 'diff-hunk', '', ln);
      continue;
    }
    if (inHunk && ln.startsWith('+')) { adds.push(ln.slice(1)); continue; }
    if (inHunk && ln.startsWith('-')) { dels.push(ln.slice(1)); continue; }
    if (!inHunk) continue; // any other pre-hunk noise
    // context line (or empty) inside a hunk
    flush();
    const ctx = ln.startsWith(' ') ? ln.slice(1) : ln;
    row(left,  'diff-ctx', ++lnA, ctx);
    row(right, 'diff-ctx', ++lnB, ctx);
  }
  flush();

  // Wire mute-timestamps toggle (filters lines with clock/uptime noise).
  const muteInput = wrap.querySelector('#mute-ts');
  if (muteInput) {
    const TS_PAT = /\b(uptime|last.changed|clock|ntp|timestamp|\d{1,2}:\d{2}:\d{2}|\b(mon|tue|wed|thu|fri|sat|sun)\b)/i;
    muteInput.addEventListener('change', () => {
      for (const r of pane.querySelectorAll('.diff-row')) {
        const src = r.querySelector('.src');
        if (src && TS_PAT.test(src.textContent)) r.style.display = muteInput.checked ? 'none' : '';
      }
    });
  }

  wrap.appendChild(pane);
  return wrap;
}

// ---------- Compliance (rules + findings) ----------
views.compliance = async (root) => {
  root.appendChild(el('h2', {}, 'Compliance'));

  const pickerGroup = el('select', { id: 'rulepack-group-select' });
  const pickerPacks = el('div', { id: 'rulepack-pack-list', class: 'rulepack-picker-list' });
  const pickerStatus = el('p', { class: 'muted', id: 'rulepack-picker-status' }, 'Loading rule-pack assignments…');
  const pickerSave = el('button', { type: 'button', class: 'btn' }, 'Save group rule packs');
  const pickerCard = el('div', { class: 'card' },
    el('h3', {}, 'Rule-pack picker'),
    el('p', { class: 'muted' }, 'Select built-in compliance rule packs per device group.'),
    el('label', {}, 'Device group ', pickerGroup),
    pickerPacks,
    el('div', { class: 'actions' }, pickerSave),
    pickerStatus,
  );
  root.appendChild(pickerCard);

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

  const pickerState = {
    groups: [],
    packs: [],
    assignments: new Map(),
  };

  const renderPickerPackList = () => {
    pickerPacks.innerHTML = '';
    const gid = Number(pickerGroup.value || 0);
    if (!gid || !pickerState.packs.length) {
      pickerPacks.appendChild(el('p', { class: 'muted' }, 'No packs available.'));
      return;
    }
    const selected = new Set(pickerState.assignments.get(gid) || []);
    for (const p of pickerState.packs) {
      const id = `pack-${gid}-${p.name}`;
      const cb = el('input', { type: 'checkbox', id, value: p.name });
      cb.checked = selected.has(p.name);
      pickerPacks.appendChild(el('label', { class: 'rulepack-option', for: id },
        cb,
        el('span', {},
          `${p.name} `,
          el('span', { class: 'muted' }, `(v${p.version}, ${p.rule_count} rules)`)),
      ));
    }
  };

  const loadPicker = async () => {
    try {
      const [groups, packs, assignments] = await Promise.all([
        api('GET', '/device-groups'),
        api('GET', '/compliance/rulepacks'),
        api('GET', '/compliance/rulepack-assignments'),
      ]);
      pickerState.groups = groups || [];
      pickerState.packs = packs || [];
      pickerState.assignments = new Map((assignments || []).map((a) => [a.group_id, a.packs || []]));
      pickerGroup.innerHTML = '';
      if (!pickerState.groups.length) {
        pickerGroup.appendChild(el('option', { value: '' }, 'No groups defined'));
        pickerSave.disabled = true;
        pickerStatus.textContent = 'Create a device group to use group-scoped rule packs.';
        renderPickerPackList();
        return;
      }
      pickerSave.disabled = false;
      for (const g of pickerState.groups) {
        pickerGroup.appendChild(el('option', { value: g.id }, g.name));
      }
      pickerStatus.textContent = 'Select packs for a group and save.';
      renderPickerPackList();
    } catch (e) {
      pickerStatus.className = 'error';
      pickerStatus.textContent = 'Rule-pack picker unavailable: ' + e.message;
      pickerSave.disabled = true;
    }
  };

  pickerGroup.addEventListener('change', renderPickerPackList);
  pickerSave.addEventListener('click', async () => {
    const gid = Number(pickerGroup.value || 0);
    if (!gid) return;
    const selected = $$('#rulepack-pack-list input[type="checkbox"]:checked').map((x) => x.value);
    pickerSave.disabled = true;
    try {
      await api('PUT', `/compliance/rulepack-assignments/${gid}`, { packs: selected });
      pickerState.assignments.set(gid, selected);
      pickerStatus.className = 'muted';
      pickerStatus.textContent = `Saved ${selected.length} pack(s) for group #${gid}.`;
    } catch (e) {
      pickerStatus.className = 'error';
      pickerStatus.textContent = 'Failed to save: ' + e.message;
    } finally {
      pickerSave.disabled = false;
    }
  });

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

  await loadPicker();
  await renderRules();
  await renderFindings();
};

function topologyNodeCountFromLinks(links) {
  const set = new Set();
  for (const l of links) { set.add(l.a); set.add(l.b); }
  return set.size;
}

// ---------- Topology (interactive SVG canvas with hand-rolled force layout) ----------
views.topology = async (root) => {
  root.appendChild(el('div', { class: 'page-header' },
    el('div', { class: 'page-header-left' },
      el('div', { class: 'page-header-breadcrumb' }, 'Core Operations'),
      el('div', { class: 'page-header-title' }, 'Network Topology')),
    el('div', { class: 'page-header-actions' },
      el('button', { class: 'btn', id: 'topo-discover-btn' }, '🔍 Run LLDP/CDP Discovery'))));

  // Wire the discovery button.
  root.querySelector('#topo-discover-btn').onclick = async () => {
    const btn = root.querySelector('#topo-discover-btn');
    btn.disabled = true; btn.textContent = '⏳ Running probes…';
    try {
      const res = await api('POST', '/topology/discover');
      const ok = (res.results || []).filter(r => r.status === 'ok').length;
      const failed = (res.results || []).filter(r => r.status === 'failed').length;
      btn.textContent = `✓ Done (${ok} ok, ${failed} failed) — reloading…`;
      setTimeout(() => { router(); }, 1500);
    } catch (e) {
      btn.disabled = false; btn.textContent = '🔍 Run LLDP/CDP Discovery';
      alert('Discovery failed: ' + e.message);
    }
  };

  let data;
  try { data = await api('GET', '/topology'); }
  catch (e) { root.appendChild(errorState('Error: ' + e.message)); return; }
  const links = (data && data.links) || [];
  const nodeCount = Number.isFinite(data?.node_count) ? data.node_count : topologyNodeCountFromLinks(links);
  const linkCount = Number.isFinite(data?.edge_count) ? data.edge_count : links.length;
  if (!links.length) {
    const empty = el('div', { class: 'card', style: 'text-align:center;padding:48px 32px;margin-top:24px' },
      el('div', { style: 'font-size:3rem;margin-bottom:16px' }, '🗺'),
      el('h3', { style: 'margin:0 0 8px' }, 'No topology data yet'),
      el('p', { style: 'color:var(--text-muted);font-size:var(--font-size-sm);margin:0 0 20px;max-width:480px;margin-left:auto;margin-right:auto' },
        'Click "Run LLDP/CDP Discovery" above to poll all devices for neighbor tables. NetMantle will run ',
        el('code', {}, 'show lldp neighbors'), ' against each device and build the topology graph automatically.'),
      el('div', { style: 'display:flex;gap:12px;justify-content:center;flex-wrap:wrap' },
        el('div', { class: 'card', style: 'padding:12px 20px;min-width:160px' },
          el('div', { style: 'font-size:1.5rem' }, '📡'), el('div', { style: 'font-size:var(--font-size-sm);margin-top:4px' }, 'LLDP (IEEE 802.1ab)'),
          el('div', { class: 'muted', style: 'font-size:var(--font-size-xs)' }, 'Cisco, Juniper, Arista, Huawei')),
        el('div', { class: 'card', style: 'padding:12px 20px;min-width:160px' },
          el('div', { style: 'font-size:1.5rem' }, '🔗'), el('div', { style: 'font-size:var(--font-size-sm);margin-top:4px' }, 'CDP (Cisco)'),
          el('div', { class: 'muted', style: 'font-size:var(--font-size-xs)' }, 'Cisco IOS/NX-OS/IOS-XR')),
        el('div', { class: 'card', style: 'padding:12px 20px;min-width:160px' },
          el('div', { style: 'font-size:1.5rem' }, '🛰' ), el('div', { style: 'font-size:var(--font-size-sm);margin-top:4px' }, 'MikroTik neighbors'),
          el('div', { class: 'muted', style: 'font-size:var(--font-size-xs)' }, 'RouterOS /ip neighbor')),
      ));
    root.appendChild(empty);
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
  const stats = el('div', { class: 'legend topo-stats' },
    el('span', {}, `${nodeCount} devices`),
    el('span', {}, `${linkCount} links`),
  );
  wrap.appendChild(stats);
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

// ---------- Automation (Mass Config Push wizard — v2) ----------
// 5 steps: Create/Select Job → Map Variables → Preflight → Preview → Execute

// --- Config Editor component ---
function configEditor(initialValue, onChange) {
  const wrap = el('div', { class: 'config-editor-wrap' });
  const lineNums = el('div', { class: 'line-numbers' });
  const ta = el('textarea', { spellcheck: 'false', autocomplete: 'off', autocorrect: 'off' });
  ta.value = initialValue || '';
  wrap.append(lineNums, ta);

  function updateLineNumbers() {
    const count = (ta.value.match(/\n/g) || []).length + 1;
    lineNums.innerHTML = '';
    for (let i = 1; i <= count; i++) lineNums.appendChild(el('span', {}, String(i)));
  }

  ta.addEventListener('input', () => { updateLineNumbers(); onChange?.(ta.value); });
  ta.addEventListener('scroll', () => { lineNums.scrollTop = ta.scrollTop; });
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Tab') { e.preventDefault(); const s = ta.selectionStart; ta.value = ta.value.substring(0, s) + '  ' + ta.value.substring(ta.selectionEnd); ta.selectionStart = ta.selectionEnd = s + 2; }
  });
  updateLineNumbers();
  return { wrap, textarea: ta, getValue: () => ta.value };
}

// --- Variable Mapper component ---
function detectVariables(template) {
  const vars = new Set();
  const re = /\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}/g;
  let m;
  while ((m = re.exec(template)) !== null) vars.add(m[1]);
  return [...vars];
}

function variableMapper(template, devices) {
  const vars = detectVariables(template);
  if (!vars.length) return { el: el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' }, 'No {{variables}} detected in template.'), getMapping: () => ({}) };

  const deviceFields = ['hostname', 'address', 'port', 'driver', 'id'];
  const container = el('div', { class: 'var-mapper' });
  container.appendChild(el('div', { class: 'var-mapper-header' },
    el('span', {}, `${vars.length} variable${vars.length > 1 ? 's' : ''} detected`)));

  const mapping = {};
  for (const v of vars) {
    const source = el('select', {},
      el('option', { value: 'manual' }, 'Manual value'),
      ...deviceFields.map(f => el('option', { value: 'field:' + f }, 'Device: ' + f)));
    const manualInp = el('input', { type: 'text', placeholder: 'Enter value…' });
    source.addEventListener('change', () => {
      manualInp.hidden = source.value !== 'manual';
      mapping[v] = { source: source.value, manual: manualInp.value };
    });
    manualInp.addEventListener('input', () => {
      mapping[v] = { source: 'manual', manual: manualInp.value };
    });
    mapping[v] = { source: 'manual', manual: '' };
    container.appendChild(el('div', { class: 'var-mapper-row' },
      el('span', { class: 'var-name' }, `{{${v}}}`),
      manualInp, source));
  }

  return { el: container, getMapping: () => mapping };
}

// --- Export Utility toolbar ---
function exportToolbar(getText) {
  const bar = el('div', { class: 'export-toolbar' });
  const copyBtn = el('button', { type: 'button' }, '📋 Copy');
  copyBtn.onclick = () => {
    navigator.clipboard.writeText(getText()).then(() => { copyBtn.textContent = '✓ Copied'; setTimeout(() => { copyBtn.textContent = '📋 Copy'; }, 1500); });
  };
  const dlBtn = el('button', { type: 'button' }, '💾 Download .cfg');
  dlBtn.onclick = () => {
    const blob = new Blob([getText()], { type: 'text/plain' });
    const a = document.createElement('a'); a.href = URL.createObjectURL(blob);
    a.download = 'config.cfg'; a.click(); URL.revokeObjectURL(a.href);
  };
  bar.append(copyBtn, dlBtn);
  return bar;
}

views.automation = (root) => {
  root.appendChild(el('div', { class: 'page-header' },
    el('div', { class: 'page-header-left' },
      el('div', { class: 'page-header-breadcrumb' }, 'Automation & Intelligence'),
      el('div', { class: 'page-header-title' }, 'Mass Config Push')),
    el('div', { class: 'page-header-actions' },
      el('button', { class: 'btn ghost', id: 'push-export-btn' }, '📥 Export Configs'),
      el('button', { class: 'btn ghost', id: 'push-schedule-btn' }, '⏰ Schedules'))));

  let step = 0, selectedJob = null, previewResults = [], allDevices = [];
  let currentTemplate = '', varMapperState = null;

  const STEPS = ['Select Job', 'Variables', 'Pre-flight', 'Preview', 'Execute'];
  const stepsEl = el('div', { class: 'wizard-steps' });
  STEPS.forEach((s, i) => stepsEl.appendChild(
    el('div', { class: 'wizard-step' + (i === 0 ? ' active' : ''), 'data-step': String(i) }, `${i + 1}. ${s}`)));
  const stepContent = el('div', { class: 'card', style: 'padding:var(--space-5)' });
  root.append(stepsEl, stepContent);

  // Wire top-bar buttons
  root.querySelector('#push-export-btn').onclick = () => openExportModal();
  root.querySelector('#push-schedule-btn').onclick = () => { location.hash = '#/settings'; };

  function setStep(n) {
    step = n;
    $$('.wizard-step', stepsEl).forEach((s, i) => {
      s.classList.toggle('active', i === n);
      s.classList.toggle('done', i < n);
    });
    renderStep();
  }

  function renderStep() {
    stepContent.innerHTML = '';
    [renderJobSelect, renderVariables, renderPreflight, renderPreview, renderExecute][step]();
  }

  // STEP 0: Select or Create Job
  async function renderJobSelect() {
    stepContent.innerHTML = '<p class="muted">Loading…</p>';
    let jobs = [];
    try { jobs = await api('GET', '/push/jobs'); } catch (_) {}
    try { allDevices = await api('GET', '/devices'); } catch (_) { allDevices = []; }
    stepContent.innerHTML = '';

    const jobList = el('div', { style: 'display:grid;gap:10px;margin-bottom:16px' });
    for (const j of jobs) {
      const card = el('div', { class: 'card', style: 'cursor:pointer;border:2px solid transparent;padding:12px 16px' },
        el('div', { style: 'display:flex;justify-content:space-between;align-items:center' },
          el('strong', {}, escapeHTML(j.name)),
          el('div', { style: 'display:flex;gap:6px' },
            j.safe_mode ? el('span', { class: 'badge ok' }, '🛡 Safe') : null,
            j.verify_command ? el('span', { class: 'badge info' }, '✓ Verify') : null)),
        el('p', { class: 'muted', style: 'margin:4px 0 0;font-size:var(--font-size-xs)' },
          j.target_group_id ? `Group #${j.target_group_id}` : 'All devices'));
      card.onclick = () => {
        $$('.card', jobList).forEach(c => c.style.borderColor = 'transparent');
        card.style.borderColor = 'var(--accent)';
        selectedJob = j;
        currentTemplate = j.template || '';
      };
      jobList.appendChild(card);
    }
    if (!jobs.length) jobList.appendChild(el('p', { class: 'muted' }, 'No push jobs yet. Create one below.'));

    const nextBtn = el('button', { class: 'btn' }, 'Next: Variables →');
    nextBtn.onclick = () => { if (!selectedJob) { alert('Select a job first.'); return; } setStep(1); };
    stepContent.append(jobList, nextBtn);

    // Create new job form with config editor
    const createDet = el('details', { style: 'margin-top:20px' },
      el('summary', { style: 'cursor:pointer;font-weight:600;font-size:var(--font-size-sm)' }, '+ Create new push job'));
    const nameInp = el('input', { name: 'name', required: true, placeholder: 'Job name', style: 'width:100%;padding:7px 10px;border:1px solid var(--border-default);border-radius:var(--radius-sm);font-size:var(--font-size-sm);background:var(--surface-card);color:var(--text-default);margin-bottom:8px' });
    const editor = configEditor('/ip address add address={{customer_ip}} interface=ether1\n# Disable telnet\n/ip service set telnet disabled=yes', (v) => { currentTemplate = v; });

    const safeCheck = el('label', { style: 'display:flex;gap:8px;align-items:center;font-size:var(--font-size-sm);margin-top:8px' },
      el('input', { type: 'checkbox', name: 'safe_mode' }), ' Enable Safe Mode');
    const verifyInp = el('input', { type: 'text', placeholder: 'e.g. ping 8.8.8.8 count=1', style: 'width:100%;padding:7px 10px;border:1px solid var(--border-default);border-radius:var(--radius-sm);font-size:var(--font-size-sm);background:var(--surface-card);color:var(--text-default)' });
    const rollbackEditor = configEditor('# Rollback commands here\n', null);

    const createBtn = el('button', { class: 'btn', style: 'margin-top:12px' }, 'Create Job');
    createBtn.onclick = async () => {
      const name = nameInp.value.trim();
      if (!name) { alert('Name required'); return; }
      try {
        const job = await api('POST', '/push/jobs', {
          name,
          template: editor.getValue(),
          safe_mode: safeCheck.querySelector('input').checked,
          verify_command: verifyInp.value.trim(),
          rollback_template: rollbackEditor.getValue(),
        });
        selectedJob = job;
        currentTemplate = editor.getValue();
        createDet.open = false;
        renderStep();
      } catch (err) { alert('Create failed: ' + err.message); }
    };

    createDet.append(
      el('div', { style: 'margin-top:12px;display:grid;gap:8px' },
        el('label', { style: 'font-size:var(--font-size-sm);font-weight:600' }, 'Job Name'), nameInp,
        el('label', { style: 'font-size:var(--font-size-sm);font-weight:600;margin-top:4px' }, 'Template (Vendor CLI)'),
        el('p', { class: 'muted', style: 'font-size:var(--font-size-xs);margin:0 0 4px' }, 'Use {{variable_name}} for dynamic injection. Supports MikroTik, Cisco, Huawei CLI.'),
        editor.wrap,
        safeCheck,
        el('label', { style: 'font-size:var(--font-size-sm);font-weight:600;margin-top:8px' }, 'Verify Command (optional)'), verifyInp,
        el('span', { class: 'help' }, 'Runs after push to verify connectivity. Rollback triggers if this fails.'),
        el('label', { style: 'font-size:var(--font-size-sm);font-weight:600;margin-top:8px' }, 'Rollback Template (optional)'),
        rollbackEditor.wrap,
        createBtn));
    stepContent.appendChild(createDet);
  }

  // STEP 1: Variable Mapper
  function renderVariables() {
    stepContent.innerHTML = '';
    stepContent.appendChild(el('h3', {}, `Variables — ${escapeHTML(selectedJob.name)}`));
    const tpl = currentTemplate || selectedJob.template || '';
    varMapperState = variableMapper(tpl, allDevices);
    stepContent.appendChild(varMapperState.el);

    const nav = el('div', { style: 'display:flex;gap:8px;margin-top:16px' },
      el('button', { class: 'btn ghost' }, '← Back'),
      el('button', { class: 'btn' }, 'Next: Pre-flight →'));
    nav.children[0].onclick = () => setStep(0);
    nav.children[1].onclick = () => setStep(2);
    stepContent.appendChild(nav);
  }

  // STEP 2: Pre-flight connectivity check
  async function renderPreflight() {
    stepContent.innerHTML = '';
    stepContent.appendChild(el('h3', {}, 'Pre-flight Connectivity Check'));
    stepContent.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' }, 'Verifying SSH/Telnet connectivity to target devices…'));

    const grid = el('div', { class: 'preflight-grid' });
    const summary = el('div', { style: 'margin-top:12px;font-size:var(--font-size-sm)' });
    stepContent.append(grid, summary);

    let results = [];
    try {
      results = (await api('POST', `/push/jobs/${selectedJob.id}/preflight`)).results || [];
    } catch (e) {
      // Fallback: show all devices as unchecked
      for (const d of allDevices) results.push({ device_id: d.id, hostname: d.hostname, reachable: null, error: 'preflight not available' });
    }

    grid.innerHTML = '';
    let reachable = 0, unreachable = 0;
    for (const r of results) {
      if (r.reachable) reachable++; else unreachable++;
      grid.appendChild(el('div', { class: 'preflight-row' },
        el('span', { class: 'pf-dot ' + (r.reachable ? 'ok' : 'bad') }),
        el('span', {}, escapeHTML(r.hostname || 'Device #' + r.device_id)),
        el('span', { class: 'pf-latency' }, r.latency_ms ? r.latency_ms + 'ms' : '—'),
        el('span', { class: 'badge ' + (r.reachable ? 'ok' : 'bad') }, r.reachable ? 'OK' : 'Fail')));
    }
    summary.innerHTML = '';
    summary.append(
      el('span', { class: 'badge ok', style: 'margin-right:6px' }, `${reachable} reachable`),
      unreachable ? el('span', { class: 'badge bad' }, `${unreachable} unreachable`) : null);

    const nav = el('div', { style: 'display:flex;gap:8px;margin-top:16px' },
      el('button', { class: 'btn ghost' }, '← Back'),
      el('button', { class: 'btn' }, 'Next: Preview →'));
    nav.children[0].onclick = () => setStep(1);
    nav.children[1].onclick = () => setStep(3);
    stepContent.appendChild(nav);
  }

  // STEP 3: Preview rendered configs
  async function renderPreview() {
    stepContent.innerHTML = '<p class="muted">Generating preview…</p>';
    try {
      const res = await api('POST', `/push/jobs/${selectedJob.id}/preview`);
      previewResults = res.results || res || [];
    } catch (err) {
      stepContent.innerHTML = `<p class="error">Preview failed: ${escapeHTML(err.message)}</p>`;
      stepContent.appendChild(el('button', { class: 'btn ghost', onclick: () => setStep(2) }, '← Back'));
      return;
    }
    stepContent.innerHTML = '';
    stepContent.appendChild(el('h3', {}, `Preview — ${escapeHTML(selectedJob.name)}`));
    stepContent.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' },
      `${previewResults.length} device(s) targeted.`));

    for (const r of previewResults.slice(0, 30)) {
      const det = el('details', { style: 'margin-bottom:6px;border:1px solid var(--border-default);border-radius:var(--radius-md);overflow:hidden' },
        el('summary', { style: 'padding:8px 14px;cursor:pointer;font-size:var(--font-size-sm);background:var(--surface-sunken)' },
          el('strong', {}, escapeHTML(r.hostname || 'unknown'))),
        el('pre', { class: 'config-view', style: 'border-radius:0;border:none;margin:0' }, escapeHTML(r.rendered || '')));
      stepContent.appendChild(det);
    }

    const nav = el('div', { style: 'display:flex;gap:8px;margin-top:16px' },
      el('button', { class: 'btn ghost' }, '← Back'),
      el('button', { class: 'btn' }, '⚡ Execute Push'));
    nav.children[0].onclick = () => setStep(2);
    nav.children[1].onclick = () => setStep(4);
    stepContent.appendChild(nav);
  }

  // STEP 4: Execute with terminal queue
  async function renderExecute() {
    stepContent.innerHTML = '';
    stepContent.appendChild(el('h3', {}, `Executing — ${escapeHTML(selectedJob.name)}`));
    if (selectedJob.safe_mode) stepContent.appendChild(el('p', { style: 'font-size:var(--font-size-xs);color:var(--status-info)' }, '🛡 Safe Mode — devices will be rolled back if unreachable after push'));

    const total = previewResults.length || allDevices.length || 1;
    let done = 0, failures = 0;

    // Global progress
    const progressFill = el('div', { class: 'progress-bar-fill', style: 'width:0%' });
    const progressPct = el('span', { style: 'font-weight:600;font-size:var(--font-size-sm)' }, '0%');
    stepContent.appendChild(el('div', { class: 'device-progress', style: 'margin-bottom:12px' },
      el('div', { class: 'progress-bar-wrap' }, progressFill), progressPct));

    // Terminal queue: device list + console
    const tq = el('div', { class: 'terminal-queue' });
    const deviceList = el('div', { class: 'tq-device-list' });
    const consoleEl = el('div', { class: 'tq-console' });
    tq.append(deviceList, consoleEl);
    stepContent.appendChild(tq);

    // Populate device list
    const deviceItems = {};
    const deviceLogs = {};
    for (const r of previewResults) {
      const key = r.hostname || 'device-' + (r.device_id || Math.random());
      deviceLogs[key] = [];
      const item = el('div', { class: 'tq-device-item', 'data-key': key },
        el('span', { class: 'tq-status pending' }),
        el('span', {}, escapeHTML(r.hostname || 'Unknown')));
      item.onclick = () => showDeviceLogs(key);
      deviceItems[key] = item;
      deviceList.appendChild(item);
    }

    let activeKey = null;
    function showDeviceLogs(key) {
      activeKey = key;
      $$('.tq-device-item', deviceList).forEach(i => i.classList.toggle('active', i.dataset.key === key));
      consoleEl.innerHTML = '';
      for (const line of (deviceLogs[key] || [])) {
        consoleEl.appendChild(el('div', { class: 'tq-line ' + (line.cls || '') }, line.text));
      }
      consoleEl.scrollTop = consoleEl.scrollHeight;
    }

    function log(key, text, cls) {
      if (!deviceLogs[key]) deviceLogs[key] = [];
      deviceLogs[key].push({ text, cls });
      if (key === activeKey) {
        consoleEl.appendChild(el('div', { class: 'tq-line ' + (cls || '') }, text));
        consoleEl.scrollTop = consoleEl.scrollHeight;
      }
    }

    function setDeviceStatus(key, status) {
      const item = deviceItems[key];
      if (!item) return;
      const dot = item.querySelector('.tq-status');
      if (dot) { dot.className = 'tq-status ' + status; }
    }

    // Global console log
    function globalLog(text, cls) {
      consoleEl.appendChild(el('div', { class: 'tq-line ' + (cls || '') }, text));
      consoleEl.scrollTop = consoleEl.scrollHeight;
    }

    globalLog(`[info] Starting push job "${selectedJob.name}" · ${total} device(s)`, 'info');
    if (selectedJob.safe_mode) globalLog('[info] Safe Mode enabled', 'info');
    if (selectedJob.verify_command) globalLog(`[info] Verify command: ${selectedJob.verify_command}`, 'info');

    try {
      const res = await api('POST', `/push/jobs/${selectedJob.id}/run`, { concurrency: 4 });
      const results = res.results || res || [];
      for (const r of results) {
        done++;
        const pct = Math.round((done / total) * 100);
        progressFill.style.width = pct + '%';
        progressPct.textContent = pct + '%';
        const key = r.hostname || 'device-' + r.device_id;

        if (r.status === 'applied') {
          setDeviceStatus(key, 'ok');
          log(key, `[ok] Config applied successfully`, 'ok');
          globalLog(`[ok]   ${escapeHTML(r.hostname)} — applied`, 'ok');
        } else if (r.status === 'rolled_back') {
          failures++;
          setDeviceStatus(key, 'fail');
          log(key, `[warn] ROLLED BACK: ${r.error || 'unreachable'}`, 'warn');
          globalLog(`[warn] ${escapeHTML(r.hostname)} — ROLLED BACK`, 'warn');
        } else if (r.status === 'failed') {
          failures++;
          setDeviceStatus(key, 'fail');
          log(key, `[fail] ${r.error || 'unknown error'}`, 'err');
          globalLog(`[fail] ${escapeHTML(r.hostname)} — ${escapeHTML(r.error || 'error')}`, 'err');
        } else {
          log(key, `[----] ${r.status}`, '');
          globalLog(`[----] ${escapeHTML(r.hostname)} — ${r.status}`, '');
        }
      }

      if (failures > 0) {
        progressFill.classList.add('has-failures');
        globalLog(`\n[done] Completed with ${failures} failure(s) / ${total}`, 'warn');
      } else {
        globalLog(`\n[done] All ${total} device(s) pushed successfully`, 'ok');
      }
    } catch (err) {
      globalLog(`[fail] Execution error: ${escapeHTML(err.message)}`, 'err');
    }

    const doneBtn = el('button', { class: 'btn', style: 'margin-top:12px' }, '← Back to jobs');
    doneBtn.onclick = () => setStep(0);
    stepContent.appendChild(doneBtn);
  }

  renderStep();
};

// ---------- Export Modal (bulk config download) ----------
async function openExportModal() {
  let devices = [];
  try { devices = await api('GET', '/devices'); } catch (_) {}

  const backdrop = el('div', { class: 'modal-backdrop' });
  const box = el('div', { class: 'modal-box', style: 'max-width:600px' });

  box.appendChild(el('h3', { style: 'margin:0 0 var(--space-3)' }, '📥 Export Configurations'));

  const body = el('div', { class: 'export-modal-body' });

  // Device multi-select
  const selectAllCb = el('input', { type: 'checkbox' });
  const deviceListEl = el('div', { class: 'export-device-list' });
  const checkboxes = [];
  for (const d of devices) {
    const cb = el('input', { type: 'checkbox', value: String(d.id), 'data-hostname': d.hostname });
    checkboxes.push(cb);
    deviceListEl.appendChild(el('label', {}, cb, ` ${escapeHTML(d.hostname)} (${escapeHTML(d.driver)})`));
  }
  selectAllCb.onchange = () => checkboxes.forEach(cb => { cb.checked = selectAllCb.checked; });
  body.append(
    el('div', { style: 'display:flex;justify-content:space-between;align-items:center' },
      el('label', { style: 'font-size:var(--font-size-sm);font-weight:600' }, 'Select devices'),
      el('label', { style: 'font-size:var(--font-size-xs);display:flex;align-items:center;gap:4px;cursor:pointer' }, selectAllCb, ' Select all')),
    deviceListEl);

  // Format selector
  let selectedFormat = 'text';
  const formatGroup = el('div', { class: 'export-format-group' });
  const formats = [
    { id: 'text', label: '📄 Plain Text (.cfg)', desc: 'Standard readable config' },
    { id: 'json', label: '{ } JSON', desc: 'Structured for scripting' },
    { id: 'zip', label: '📦 ZIP Archive', desc: 'Bundle all as .zip' },
  ];
  for (const f of formats) {
    const btn = el('button', { type: 'button', class: 'export-format-btn' + (f.id === 'text' ? ' selected' : '') }, f.label);
    btn.onclick = () => {
      selectedFormat = f.id;
      formatGroup.querySelectorAll('.export-format-btn').forEach(b => b.classList.remove('selected'));
      btn.classList.add('selected');
    };
    formatGroup.appendChild(btn);
  }
  body.append(
    el('label', { style: 'font-size:var(--font-size-sm);font-weight:600' }, 'Export format'),
    formatGroup);

  // Actions
  const statusEl = el('p', { class: 'muted', style: 'font-size:var(--font-size-xs);min-height:1.2em' });
  const exportBtn = el('button', { class: 'btn' }, '💾 Download');
  const cancelBtn = el('button', { class: 'btn ghost' }, 'Cancel');
  cancelBtn.onclick = () => backdrop.remove();

  exportBtn.onclick = async () => {
    const ids = checkboxes.filter(cb => cb.checked).map(cb => Number(cb.value));
    if (!ids.length) { statusEl.textContent = 'Select at least one device.'; statusEl.className = 'error'; return; }
    statusEl.textContent = 'Exporting…'; statusEl.className = 'muted';
    exportBtn.disabled = true;

    if (selectedFormat === 'zip') {
      // Use bulk export endpoint
      try {
        const resp = await fetch('/api/v1/export/configs', {
          method: 'POST', credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ device_ids: ids, format: 'text' }),
        });
        if (!resp.ok) throw new Error(resp.statusText);
        const blob = await resp.blob();
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = 'netmantle-configs.zip';
        a.click();
        URL.revokeObjectURL(a.href);
        statusEl.textContent = '✓ Downloaded'; statusEl.className = 'muted';
      } catch (e) { statusEl.textContent = 'Export failed: ' + e.message; statusEl.className = 'error'; }
    } else {
      // Individual download per device
      try {
        const configs = [];
        for (const id of ids) {
          const cfg = await api('GET', `/devices/${id}/config`);
          const dev = devices.find(d => d.id === id);
          configs.push({ hostname: dev?.hostname || 'device-' + id, config: cfg });
        }
        if (selectedFormat === 'json') {
          const blob = new Blob([JSON.stringify(configs, null, 2)], { type: 'application/json' });
          const a = document.createElement('a'); a.href = URL.createObjectURL(blob);
          a.download = 'netmantle-configs.json'; a.click();
        } else {
          // Single text file concatenated
          const text = configs.map(c => `# === ${c.hostname} ===\n${c.config}\n`).join('\n');
          const blob = new Blob([text], { type: 'text/plain' });
          const a = document.createElement('a'); a.href = URL.createObjectURL(blob);
          a.download = 'netmantle-configs.cfg'; a.click();
        }
        statusEl.textContent = '✓ Downloaded'; statusEl.className = 'muted';
      } catch (e) { statusEl.textContent = 'Export failed: ' + e.message; statusEl.className = 'error'; }
    }
    exportBtn.disabled = false;
  };

  body.append(statusEl, el('div', { style: 'display:flex;gap:8px;justify-content:flex-end' }, cancelBtn, exportBtn));
  box.appendChild(body);
  backdrop.appendChild(box);
  backdrop.addEventListener('click', (e) => { if (e.target === backdrop) backdrop.remove(); });
  document.body.appendChild(backdrop);
}

// ---------- Settings ----------
views.settings = async (root) => {
  const CATS = [
    { id: 'general',       label: 'General',         icon: '⚙' },
    { id: 'connectivity',  label: 'Connectivity',     icon: '🔌' },
    { id: 'credentials',   label: 'Credentials',      icon: '🔑' },
    { id: 'connectors',    label: 'Connectors',       icon: '🛰' },
    { id: 'security',      label: 'Security',         icon: '🛡' },
    { id: 'notifications', label: 'Notifications',    icon: '🔔' },
    { id: 'api',           label: 'API & Tokens',     icon: '🗝' },
    { id: 'auditlogs',     label: 'Audit Logs',       icon: '📋' },
  ];

  // Save bar (appended to body, removed on navigation)
  const saveBar = el('div', { class: 'settings-save-bar', id: 'settings-save-bar' },
    el('span', { class: 'msg' }, 'You have unsaved changes.'),
    el('button', { class: 'btn ghost', id: 'settings-discard' }, 'Discard'),
    el('button', { class: 'btn', id: 'settings-save' }, 'Save changes'),
  );
  document.body.appendChild(saveBar);

  let dirty = false;
  function markDirty() { dirty = true; saveBar.classList.add('visible'); }
  function markClean() { dirty = false; saveBar.classList.remove('visible'); }

  // Two-column shell
  const shell = el('div', { class: 'settings-shell' });
  const nav = el('nav', { class: 'settings-nav' });
  for (const c of CATS) {
    const a = el('a', { 'data-cat': c.id }, c.icon + ' ' + c.label);
    a.onclick = (e) => { e.preventDefault(); showSection(c.id); };
    nav.appendChild(a);
  }

  const contentWrap = el('div', { class: 'settings-content' });
  const breadcrumb = el('div', { class: 'settings-breadcrumb' });
  const contentArea = el('div', { id: 'settings-section-content' });
  contentWrap.append(breadcrumb, contentArea);
  shell.append(nav, contentWrap);
  root.appendChild(shell);

  // Parse active section from hash, e.g. #/settings/connectivity
  const hashPart = (location.hash || '').replace(/^#\/?settings\/?/, '');
  const initCat = CATS.find(c => c.id === hashPart)?.id || 'general';

  function showSection(id) {
    nav.querySelectorAll('[data-cat]').forEach(a =>
      a.classList.toggle('active', a.dataset.cat === id));
    const cat = CATS.find(c => c.id === id);
    breadcrumb.innerHTML = '';
    breadcrumb.append('Settings › ', el('span', {}, cat?.label || id));
    contentArea.innerHTML = '';
    markClean();
    ({
      general:       renderGeneral,
      connectivity:  renderConnectivity,
      credentials:   renderCredentials,
      connectors:    renderConnectors,
      security:      renderSecurity,
      notifications: renderNotifications,
      api:           renderApiTokens,
      auditlogs:     renderAuditLogs,
    }[id] || renderGeneral)(contentArea);
  }

  // ---- shared helpers ----
  function sectionDivider(title) {
    return el('div', { class: 'section-divider' }, title);
  }

  function fieldRow(labelText, input, helpText) {
    const row = el('div', { class: 'field-row' }, el('label', {}, labelText));
    row.appendChild(input);
    if (helpText) row.appendChild(el('span', { class: 'help' }, helpText));
    return row;
  }

  function toggleRow(labelText, checked, helpText, onChange) {
    const inp = el('input', { type: 'checkbox' });
    inp.checked = checked;
    inp.addEventListener('change', () => { onChange && onChange(inp.checked); markDirty(); });
    const sw = el('label', { class: 'toggle-switch' }, inp, el('span', { class: 'toggle-slider' }));
    const row = el('div', { class: 'toggle-row field-row' }, sw, el('label', {}, labelText));
    if (helpText) row.appendChild(el('span', { class: 'help' }, helpText));
    return row;
  }

  function rangeRow(labelText, min, max, val, unit, helpText) {
    const valLabel = el('span', { class: 'range-val' }, String(val) + (unit || ''));
    const inp = el('input', { type: 'range', min: String(min), max: String(max), value: String(val) });
    inp.addEventListener('input', () => { valLabel.textContent = inp.value + (unit || ''); markDirty(); });
    const rangeWrap = el('div', { class: 'range-row' }, inp, valLabel);
    const row = el('div', { class: 'field-row' }, el('label', {}, labelText), rangeWrap);
    if (helpText) row.appendChild(el('span', { class: 'help' }, helpText));
    return row;
  }

  function numInput(val) {
    const inp = el('input', { type: 'number', value: String(val), style: 'max-width:120px' });
    inp.addEventListener('input', markDirty);
    return inp;
  }

  function textInput(val, placeholder) {
    const inp = el('input', { type: 'text', value: val || '', placeholder: placeholder || '' });
    inp.addEventListener('input', markDirty);
    return inp;
  }

  // ---- General ----
  function renderGeneral(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'General'));

    area.appendChild(sectionDivider('Backup Scheduling'));
    area.appendChild(el('div', { class: 'field-group' },
      toggleRow('Enable scheduled backups',
        localStorage.getItem('nm.sched.enabled') !== 'false',
        'When enabled, all devices are polled for config changes on the interval below.',
        (v) => localStorage.setItem('nm.sched.enabled', String(v))),
      fieldRow('Backup interval (hours)',
        Object.assign(numInput(localStorage.getItem('nm.sched.interval') || '4'),
          { onchange: function() { localStorage.setItem('nm.sched.interval', this.value); } }),
        'How often devices are automatically polled for configuration changes.'),
    ));

    area.appendChild(sectionDivider('Concurrency'));
    area.appendChild(el('div', { class: 'field-group' },
      rangeRow('Max concurrent backups', 1, 50,
        Number(localStorage.getItem('nm.concurrency') || 5), '',
        'Limits simultaneous SSH sessions. Lower values reduce CPU spike risk on large networks.'),
    ));

    area.appendChild(sectionDivider('Retention'));
    area.appendChild(el('div', { class: 'field-group' },
      fieldRow('Keep last N versions per device',
        Object.assign(numInput(localStorage.getItem('nm.retention') || '50'),
          { onchange: function() { localStorage.setItem('nm.retention', this.value); } }),
        'Older git commits are pruned per device. Set to 0 for unlimited.'),
    ));

    // -- Automation Scheduler --
    area.appendChild(sectionDivider('Automation Scheduler'));
    const schedWrap = el('div', { class: 'field-group' });
    schedWrap.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-xs);margin:0 0 12px' },
      'Schedule recurring backup or push jobs. Cron expressions run in server timezone.'));

    const schedGrid = el('div', { class: 'schedule-grid', id: 'schedule-grid' });
    schedWrap.appendChild(schedGrid);

    // Load existing schedules
    async function loadSchedules() {
      schedGrid.innerHTML = '<p class="muted" style="font-size:var(--font-size-sm)">Loading schedules…</p>';
      let schedules = [];
      try { schedules = await api('GET', '/schedules'); } catch (_) {}
      schedGrid.innerHTML = '';
      if (!schedules.length) {
        schedGrid.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' }, 'No schedules configured.'));
      }
      for (const s of schedules) {
        const toggle = el('input', { type: 'checkbox' });
        toggle.checked = s.enabled;
        const sw = el('label', { class: 'toggle-switch' }, toggle, el('span', { class: 'toggle-slider' }));
        toggle.onchange = async () => {
          try { await api('PUT', `/schedules/${s.id}`, { ...s, enabled: toggle.checked }); } catch (_) {}
        };
        const delBtn = el('button', { class: 'btn ghost', style: 'font-size:0.7rem;padding:3px 8px' }, '🗑');
        delBtn.onclick = async () => {
          if (!confirm('Delete this schedule?')) return;
          try { await api('DELETE', `/schedules/${s.id}`); loadSchedules(); } catch (_) {}
        };
        schedGrid.appendChild(el('div', { class: 'schedule-card' },
          el('div', { class: 'sc-info' },
            el('span', { class: 'sc-name' }, escapeHTML(s.kind || 'backup') + ': ' + escapeHTML(s.target || 'all')),
            el('span', { class: 'sc-cron' }, escapeHTML(s.cron_expr || ''))),
          sw, delBtn));
      }
    }
    loadSchedules();

    // Add new schedule form
    const addSchedDet = el('details', { style: 'margin-top:12px' },
      el('summary', { style: 'cursor:pointer;font-weight:600;font-size:var(--font-size-sm)' }, '+ Add schedule'));

    const cronPresets = [
      { label: 'Every hour', cron: '0 * * * *' },
      { label: 'Every 4 hours', cron: '0 */4 * * *' },
      { label: 'Daily 02:00', cron: '0 2 * * *' },
      { label: 'Daily 06:00', cron: '0 6 * * *' },
      { label: 'Twice daily', cron: '0 2,14 * * *' },
      { label: 'Weekly Mon', cron: '0 2 * * 1' },
    ];

    const cronInput = el('input', { type: 'text', placeholder: '0 */4 * * *', style: 'width:100%;padding:7px 10px;border:1px solid var(--border-default);border-radius:var(--radius-sm);font-family:var(--font-mono);font-size:0.8rem;background:var(--surface-card);color:var(--text-default)' });
    const presetGrid = el('div', { class: 'cron-preset-grid' });
    for (const p of cronPresets) {
      const b = el('div', { class: 'cron-preset' }, p.label);
      b.onclick = () => {
        cronInput.value = p.cron;
        presetGrid.querySelectorAll('.cron-preset').forEach(x => x.classList.remove('selected'));
        b.classList.add('selected');
      };
      presetGrid.appendChild(b);
    }

    const kindSel = el('select', { style: 'padding:6px 10px;border:1px solid var(--border-default);border-radius:var(--radius-sm);font-size:var(--font-size-sm);background:var(--surface-card);color:var(--text-default)' },
      el('option', { value: 'backup' }, 'Backup all devices'),
      el('option', { value: 'push' }, 'Run push job'));
    const targetInp = el('input', { type: 'text', placeholder: 'all (or group ID)', style: 'padding:6px 10px;border:1px solid var(--border-default);border-radius:var(--radius-sm);font-size:var(--font-size-sm);background:var(--surface-card);color:var(--text-default)' });
    const addBtn = el('button', { class: 'btn', style: 'margin-top:8px' }, 'Add Schedule');
    addBtn.onclick = async () => {
      const cron = cronInput.value.trim();
      if (!cron) { alert('Enter a cron expression.'); return; }
      try {
        await api('POST', '/schedules', {
          kind: kindSel.value,
          target: targetInp.value.trim() || 'all',
          cron_expr: cron,
          enabled: true,
        });
        addSchedDet.open = false;
        loadSchedules();
      } catch (err) { alert('Failed: ' + err.message); }
    };

    addSchedDet.append(el('div', { style: 'margin-top:12px;display:grid;gap:8px' },
      el('label', { style: 'font-size:var(--font-size-sm);font-weight:600' }, 'Frequency'),
      presetGrid,
      el('label', { style: 'font-size:var(--font-size-sm)' }, 'Or enter custom cron expression:'),
      cronInput,
      el('span', { class: 'help' }, 'Standard 5-field cron: minute hour day month weekday'),
      el('div', { style: 'display:grid;grid-template-columns:1fr 1fr;gap:8px' },
        el('div', {},
          el('label', { style: 'font-size:var(--font-size-sm);font-weight:600' }, 'Type'),
          kindSel),
        el('div', {},
          el('label', { style: 'font-size:var(--font-size-sm);font-weight:600' }, 'Target'),
          targetInp)),
      addBtn));
    schedWrap.appendChild(addSchedDet);
    area.appendChild(schedWrap);

    saveBar.querySelector('#settings-save').onclick = () => {
      alert('Settings saved locally. Server-side application requires restart or API support.');
      markClean();
    };
    saveBar.querySelector('#settings-discard').onclick = () => { showSection('general'); };
  }

  // ---- Connectivity ----
  function renderConnectivity(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'Connectivity'));

    area.appendChild(sectionDivider('SSH'));
    area.appendChild(el('div', { class: 'field-group' },
      fieldRow('Connection timeout (seconds)', numInput(30), 'Max time waiting for TCP connection to establish.'),
      fieldRow('Command timeout (seconds)', numInput(60), 'Max time waiting for a CLI prompt response after sending a command.'),
      fieldRow('Retries on failure', numInput(2), 'Number of retry attempts before marking a backup run as failed.'),
      fieldRow('Command delay (ms)', numInput(0), 'Millisecond pause between CLI commands. Increase for slow or legacy hardware that drops characters under fast input.'),
    ));

    area.appendChild(sectionDivider('Telnet'));
    area.appendChild(el('div', { class: 'field-group' },
      fieldRow('Port', numInput(23), ''),
      fieldRow('Timeout (seconds)', numInput(30), ''),
    ));

    area.appendChild(sectionDivider('HTTP / REST'));
    area.appendChild(el('div', { class: 'field-group' },
      fieldRow('HTTP timeout (seconds)', numInput(30), ''),
      toggleRow('Verify TLS certificates', true, 'Disable only for internal test environments with self-signed certs.'),
    ));

    saveBar.querySelector('#settings-save').onclick = () => { alert('Connectivity settings noted. Full server-side API integration is on the roadmap.'); markClean(); };
    saveBar.querySelector('#settings-discard').onclick = () => { showSection('connectivity'); };
  }

  // ---- Credentials ----
  async function renderCredentials(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'Credentials'));

    const tblWrap = el('div', { class: 'table-wrapper', style: 'margin-bottom:var(--space-4)' });
    area.appendChild(tblWrap);

    async function loadCredsTable() {
      tblWrap.innerHTML = '<p class="muted">Loading…</p>';
      try {
        const creds = await api('GET', '/credentials');
        if (!creds || !creds.length) { tblWrap.innerHTML = '<p class="muted">No credentials yet.</p>'; return; }
        const tbl = el('table', { class: 'device-table' },
          el('thead', {}, el('tr', {},
            el('th', {}, 'Name'), el('th', {}, 'Username'), el('th', {}, 'Created'), el('th', {}, ''))),
          el('tbody'));
        for (const c of creds) {
          const tr = el('tr', {},
            el('td', {}, escapeHTML(c.name)),
            el('td', {}, escapeHTML(c.username || '—')),
            el('td', {}, c.created_at ? relativeTime(new Date(c.created_at)) : '—'),
            el('td', {}, (() => {
              const d = el('button', { class: 'qa-btn' }, 'Delete');
              d.onclick = async () => {
                if (!confirm('Delete credential "' + c.name + '"?')) return;
                try { await api('DELETE', '/credentials/' + c.id); loadCredsTable(); }
                catch (err) { alert(err.message); }
              };
              return d;
            })()),
          );
          tbl.querySelector('tbody').appendChild(tr);
        }
        tblWrap.innerHTML = '';
        tblWrap.appendChild(tbl);
      } catch (e) { tblWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
    }
    loadCredsTable();

    const addDetails = el('details');
    addDetails.appendChild(el('summary', { style: 'cursor:pointer;font-weight:600;font-size:var(--font-size-sm);margin-bottom:8px' }, '+ Add credential'));
    const addForm = buildAddCredentialForm(() => { addDetails.open = false; loadCredsTable(); });
    addDetails.appendChild(addForm);
    area.appendChild(addDetails);

    area.appendChild(sectionDivider('Credential Binding'));
    area.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' },
      'Assign credentials to devices from the Inventory page. Select a device row and the contextual sidebar lets you choose a credential. Bulk assignment is planned via device group tags.'));
  }

  // ---- Connectors ----
  async function renderConnectors(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'Remote Cores (Pollers)'));

    const grid = el('div', { class: 'poller-grid', style: 'margin-bottom:var(--space-4)' });
    area.appendChild(grid);
    try {
      const pollers = await api('GET', '/pollers');
      if (!pollers || !pollers.length) {
        grid.appendChild(el('p', { class: 'muted' }, 'No remote pollers registered. This server handles all polling directly.'));
      } else {
        for (const p of pollers) {
          const lastSeen = p.last_seen ? new Date(p.last_seen) : null;
          const ageMs = lastSeen ? Date.now() - lastSeen.getTime() : Infinity;
          const statusCls = ageMs < 120000 ? 'ok' : ageMs < 600000 ? 'warn' : 'bad';
          const statusLabel = ageMs < 120000 ? 'Healthy' : ageMs < 600000 ? 'Stale' : 'Offline';
          grid.appendChild(el('div', { class: 'poller-card' },
            el('div', { class: 'p-name' }, escapeHTML(p.name || 'Unnamed')),
            el('div', { class: 'p-zone' }, 'Zone: ' + escapeHTML(p.zone || '—')),
            el('div', { class: 'p-status' },
              el('span', { class: 'badge badge-' + statusCls }, statusLabel),
              el('span', { class: 'muted', style: 'font-size:var(--font-size-xs);margin-left:8px' },
                lastSeen ? relativeTime(lastSeen) : 'never seen')),
          ));
        }
      }
    } catch (e) {
      grid.appendChild(el('p', { class: 'error' }, 'Error: ' + escapeHTML(e.message)));
    }

    area.appendChild(sectionDivider('Register a Remote Core'));
    area.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' },
      'Deploy the netmantle binary on a remote site with the same config.yaml pointing to this server\'s DB. Remote pollers self-register on startup.'));
  }

  // ---- Security ----
  function renderSecurity(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'Security & Identity'));

    // IP Whitelisting
    area.appendChild(sectionDivider('IP Whitelisting'));
    const ipTa = el('textarea', { rows: '5', placeholder: '0.0.0.0/0\n10.0.0.0/8', style: 'width:100%;font-family:var(--font-mono);font-size:0.8rem' });
    ipTa.value = localStorage.getItem('nm.ip_whitelist') || '';
    ipTa.addEventListener('input', markDirty);
    area.appendChild(el('div', { class: 'field-group' },
      el('div', { class: 'field-row' },
        el('label', {}, 'Allowed IP ranges (CIDR, one per line)'),
        ipTa,
        el('span', { class: 'help' }, 'Restricts web UI and API access. Leave blank to allow all. Enforced on next server restart.'),
      )));

    // Authentication Sources
    area.appendChild(sectionDivider('Authentication Sources'));
    const authTabs = ['Local', 'LDAP', 'RADIUS'];
    let activeAuthTab = 0;
    const tabStrip = el('div', { class: 'tab-strip' });
    const authContent = el('div');

    function renderAuthTab(i) {
      activeAuthTab = i;
      tabStrip.querySelectorAll('.tab').forEach((t, ti) => t.classList.toggle('active', ti === i));
      authContent.innerHTML = '';
      if (i === 0) {
        authContent.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm);padding:12px 0' },
          'Local authentication is always enabled. Manage users via the Tenants section or the API.'));
      } else if (i === 1) {
        const flds = el('div', { class: 'field-group' },
          el('div', { class: 'field-row' }, el('label', {}, 'LDAP Server URL'), textInput('', 'ldap://ldap.example.com:389'), el('span', { class: 'help' }, 'e.g. ldaps://ad.corp.local:636')),
          el('div', { class: 'field-row' }, el('label', {}, 'Base DN'), textInput('', 'DC=corp,DC=local')),
          el('div', { class: 'field-row' }, el('label', {}, 'Bind DN'), textInput('', 'CN=svcNetMantle,CN=Users,DC=corp,DC=local')),
          el('div', { class: 'field-row' }, el('label', {}, 'Bind password'), (() => { const inp = el('input', { type: 'password', placeholder: '••••••••' }); inp.addEventListener('input', markDirty); return inp; })()),
          el('div', { class: 'field-row' }, el('label', {}, 'Username attribute'), textInput('sAMAccountName')),
        );
        const testBtn = el('button', { class: 'btn ghost', style: 'margin-top:8px' }, 'Test LDAP connection');
        testBtn.onclick = () => alert('LDAP integration is on the roadmap. Connection test not yet available.');
        authContent.append(flds, testBtn, el('p', { class: 'help', style: 'margin-top:6px' }, 'LDAP/Active Directory sync is planned. See docs/roadmap.md for status.'));
      } else {
        const flds = el('div', { class: 'field-group' },
          el('div', { class: 'field-row' }, el('label', {}, 'RADIUS Server'), textInput('', '10.0.0.1')),
          el('div', { class: 'field-row' }, el('label', {}, 'Port'), numInput(1812)),
          el('div', { class: 'field-row' }, el('label', {}, 'Shared Secret'), (() => { const inp = el('input', { type: 'password' }); inp.addEventListener('input', markDirty); return inp; })()),
        );
        const testBtn = el('button', { class: 'btn ghost', style: 'margin-top:8px' }, 'Test RADIUS connection');
        testBtn.onclick = () => alert('RADIUS integration is on the roadmap.');
        authContent.append(flds, testBtn, el('p', { class: 'help', style: 'margin-top:6px' }, 'RADIUS is planned for ISP/enterprise SSO scenarios. See docs/roadmap.md.'));
      }
    }

    for (let i = 0; i < authTabs.length; i++) {
      const tab = el('div', { class: 'tab' + (i === 0 ? ' active' : '') }, authTabs[i]);
      const idx = i;
      tab.onclick = () => renderAuthTab(idx);
      tabStrip.appendChild(tab);
    }
    renderAuthTab(0);
    area.append(tabStrip, authContent);

    // MFA
    area.appendChild(sectionDivider('Multi-Factor Authentication'));
    const mfaContainer = el('div', { class: 'field-group' });
    area.appendChild(mfaContainer);

    async function loadMFA() {
      mfaContainer.innerHTML = '';
      const meObj = window._me || {};
      const enrolled = sessionStorage.getItem('mfa_enrolled_' + (meObj.id || ''));
      if (enrolled === 'true') {
        mfaContainer.appendChild(el('p', { style: 'color:var(--status-ok);font-weight:600' }, '✓ MFA enabled for your account'));
        const disBtn = el('button', { class: 'btn danger' }, 'Disable MFA');
        disBtn.onclick = async () => {
          if (!confirm('Disable MFA?')) return;
          try { await api('DELETE', '/auth/mfa'); sessionStorage.removeItem('mfa_enrolled_' + (meObj.id || '')); loadMFA(); }
          catch (err) { alert(err.message); }
        };
        mfaContainer.appendChild(disBtn);
      } else {
        mfaContainer.appendChild(el('p', { class: 'muted', style: 'font-size:var(--font-size-sm)' }, 'MFA is not enabled. Add a TOTP authenticator app for extra security.'));
        const enrollBtn = el('button', { class: 'btn' }, 'Set up TOTP MFA');
        enrollBtn.onclick = async () => {
          try {
            const { secret, otpauth_url } = await api('POST', '/auth/mfa/enroll');
            mfaContainer.innerHTML = '';
            mfaContainer.append(
              el('p', { style: 'font-size:var(--font-size-sm)' }, 'Scan this secret in your authenticator, then confirm:'),
              el('div', { class: 'mfa-secret-box' }, escapeHTML(secret)),
              el('p', { style: 'font-size:0.7rem;color:var(--text-muted);word-break:break-all' }, escapeHTML(otpauth_url)),
            );
            const cf = el('form', { style: 'display:flex;gap:8px;margin-top:12px' },
              el('input', { name: 'code', placeholder: '000000', maxlength: '6', pattern: '[0-9]{6}', inputmode: 'numeric', style: 'width:90px;text-align:center;letter-spacing:0.2em;font-size:1.1rem' }),
              el('button', { type: 'submit', class: 'btn' }, 'Activate'));
            cf.onsubmit = async (ev) => {
              ev.preventDefault();
              const code = new FormData(cf).get('code');
              try {
                await api('POST', '/auth/mfa/confirm', { code });
                sessionStorage.setItem('mfa_enrolled_' + (meObj.id || ''), 'true');
                loadMFA();
              } catch (_) { alert('Invalid code.'); }
            };
            mfaContainer.appendChild(cf);
          } catch (err) { alert(err.message); }
        };
        mfaContainer.appendChild(enrollBtn);
      }
    }
    loadMFA();

    saveBar.querySelector('#settings-save').onclick = () => {
      localStorage.setItem('nm.ip_whitelist', ipTa.value);
      alert('Security settings saved locally.');
      markClean();
    };
    saveBar.querySelector('#settings-discard').onclick = () => showSection('security');
  }

  // ---- Notifications ----
  async function renderNotifications(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'Notifications'));

    area.appendChild(sectionDivider('Notification Channels'));
    const chanTblWrap = el('div', { class: 'table-wrapper', style: 'margin-bottom:var(--space-3)' });
    area.appendChild(chanTblWrap);

    async function loadChannels() {
      chanTblWrap.innerHTML = '<p class="muted">Loading…</p>';
      try {
        const channels = await api('GET', '/notifications/channels');
        if (!channels || !channels.length) { chanTblWrap.innerHTML = '<p class="muted">No channels configured.</p>'; return; }
        const tbl = el('table', { class: 'device-table' },
          el('thead', {}, el('tr', {}, el('th', {}, 'Name'), el('th', {}, 'Kind'), el('th', {}, 'Created'))),
          el('tbody'));
        for (const c of channels) {
          tbl.querySelector('tbody').appendChild(el('tr', {},
            el('td', {}, escapeHTML(c.name)),
            el('td', {}, el('code', {}, escapeHTML(c.kind))),
            el('td', {}, c.created_at ? relativeTime(new Date(c.created_at)) : '—'),
          ));
        }
        chanTblWrap.innerHTML = '';
        chanTblWrap.appendChild(tbl);
      } catch (e) { chanTblWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
    }
    loadChannels();

    // Create channel
    const chanKindSel = el('select', { name: 'kind' },
      el('option', { value: 'webhook' }, 'Webhook'),
      el('option', { value: 'slack' }, 'Slack'),
      el('option', { value: 'email' }, 'Email'),
      el('option', { value: 'pushover' }, 'Pushover'),
    );
    const chanKindFields = el('div', { class: 'field-group' });
    function renderChanFields() {
      chanKindFields.innerHTML = '';
      const kind = chanKindSel.value;
      if (kind === 'webhook' || kind === 'slack') {
        chanKindFields.appendChild(el('div', { class: 'field-row' },
          el('label', {}, 'URL'), el('input', { name: 'config_url', type: 'url', required: true })));
      } else if (kind === 'email') {
        chanKindFields.append(
          el('div', { class: 'field-row' }, el('label', {}, 'SMTP host'), el('input', { name: 'config_host', required: true })),
          el('div', { class: 'field-row' }, el('label', {}, 'To address'), el('input', { name: 'config_to', type: 'email', required: true })),
        );
      } else if (kind === 'pushover') {
        chanKindFields.append(
          el('div', { class: 'field-row' }, el('label', {}, 'API token'), el('input', { name: 'config_token', required: true })),
          el('div', { class: 'field-row' }, el('label', {}, 'User key'), el('input', { name: 'config_user_key', required: true })),
        );
      }
    }
    chanKindSel.addEventListener('change', renderChanFields);
    renderChanFields();

    const chanCreateDet = el('details', { style: 'margin-bottom:var(--space-4)' });
    chanCreateDet.appendChild(el('summary', { style: 'cursor:pointer;font-weight:600;font-size:var(--font-size-sm)' }, '+ Create channel'));
    const chanForm = el('form', { style: 'margin-top:10px;display:grid;gap:10px' },
      el('div', { class: 'field-row' }, el('label', {}, 'Name'), el('input', { name: 'name', required: true })),
      el('div', { class: 'field-row' }, el('label', {}, 'Kind'), chanKindSel),
      chanKindFields,
      el('button', { type: 'submit', class: 'btn' }, 'Create channel'),
    );
    chanForm.onsubmit = async (ev) => {
      ev.preventDefault();
      const fd = new FormData(chanForm);
      const kind = fd.get('kind');
      let config = {};
      if (kind === 'webhook' || kind === 'slack') config = { url: fd.get('config_url') };
      else if (kind === 'email') config = { host: fd.get('config_host'), to: fd.get('config_to') };
      else if (kind === 'pushover') config = { token: fd.get('config_token'), user_key: fd.get('config_user_key') };
      try {
        await api('POST', '/notifications/channels', { name: fd.get('name'), kind, config });
        chanForm.reset(); renderChanFields(); chanCreateDet.open = false; loadChannels();
      } catch (err) { alert('Create failed: ' + err.message); }
    };
    chanCreateDet.appendChild(chanForm);
    area.appendChild(chanCreateDet);

    // Rules
    area.appendChild(sectionDivider('Notification Rules'));
    const rulesTblWrap = el('div', { class: 'table-wrapper' });
    area.appendChild(rulesTblWrap);
    try {
      const rules = await api('GET', '/notifications/rules');
      if (!rules || !rules.length) { rulesTblWrap.innerHTML = '<p class="muted" style="padding:8px">No rules configured.</p>'; }
      else {
        const tbl = el('table', { class: 'device-table' },
          el('thead', {}, el('tr', {}, el('th', {}, 'Name'), el('th', {}, 'Event'), el('th', {}, 'Channel'))),
          el('tbody'));
        for (const r of rules) {
          tbl.querySelector('tbody').appendChild(el('tr', {},
            el('td', {}, escapeHTML(r.name || '—')),
            el('td', {}, el('code', {}, escapeHTML(r.event_type || '—'))),
            el('td', {}, '#' + (r.channel_id || '?')),
          ));
        }
        rulesTblWrap.appendChild(tbl);
      }
    } catch (e) { rulesTblWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
  }

  // ---- API & Tokens ----
  async function renderApiTokens(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'API & Tokens'));

    area.appendChild(sectionDivider('API Tokens'));
    const tokenTblWrap = el('div', { class: 'table-wrapper', style: 'margin-bottom:var(--space-3)' });
    area.appendChild(tokenTblWrap);

    async function loadTokens() {
      tokenTblWrap.innerHTML = '<p class="muted">Loading…</p>';
      try {
        const tokens = await api('GET', '/api-tokens');
        if (!tokens || !tokens.length) { tokenTblWrap.innerHTML = '<p class="muted">No tokens yet.</p>'; return; }
        const tbl = el('table', { class: 'device-table' },
          el('thead', {}, el('tr', {},
            el('th', {}, 'Name'), el('th', {}, 'Prefix'), el('th', {}, 'Scopes'), el('th', {}, 'Expires'), el('th', {}, ''))),
          el('tbody'));
        for (const t of tokens) {
          const expiry = t.expires_at ? (() => { const d = new Date(t.expires_at); return isNaN(d) ? '—' : d.toLocaleDateString(); })() : 'Never';
          const tr = el('tr', {},
            el('td', {}, escapeHTML(t.name || '—')),
            el('td', {}, el('code', {}, escapeHTML(t.prefix || '—'))),
            el('td', {}, Array.isArray(t.scopes) ? t.scopes.join(', ') : escapeHTML(String(t.scopes || ''))),
            el('td', {}, expiry),
            el('td', {}, (() => {
              const d = el('button', { class: 'qa-btn' }, 'Revoke');
              d.onclick = async () => {
                if (!confirm('Revoke token "' + (t.name || t.id) + '"?')) return;
                try { await api('DELETE', '/api-tokens/' + t.id); loadTokens(); }
                catch (err) { alert(err.message); }
              };
              return d;
            })()),
          );
          tbl.querySelector('tbody').appendChild(tr);
        }
        tokenTblWrap.innerHTML = '';
        tokenTblWrap.appendChild(tbl);
      } catch (e) { tokenTblWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
    }
    loadTokens();

    // Create token form
    const createDet = el('details', { style: 'margin-bottom:var(--space-4)' });
    createDet.appendChild(el('summary', { style: 'cursor:pointer;font-weight:600;font-size:var(--font-size-sm)' }, '+ Generate new token'));
    const tokenReveal = el('div', { hidden: true });
    const scopeChecks = ['read', 'write', 'admin'].map(s => {
      const cb = el('input', { type: 'checkbox', name: 'scope_' + s, id: 'scope_' + s });
      if (s === 'read') cb.checked = true;
      return el('label', { style: 'display:flex;align-items:center;gap:6px;font-size:var(--font-size-sm)' },
        cb, s.charAt(0).toUpperCase() + s.slice(1));
    });
    const createForm = el('form', { style: 'margin-top:10px;display:grid;gap:10px' },
      el('div', { class: 'field-row' }, el('label', {}, 'Name'), el('input', { name: 'name', required: true })),
      el('div', { class: 'field-row' },
        el('label', {}, 'Scopes'),
        el('div', { style: 'display:flex;gap:16px;flex-wrap:wrap;margin-top:4px' }, ...scopeChecks)),
      el('div', { class: 'field-row' }, el('label', {}, 'Expires'), el('input', { name: 'expires_at', type: 'date' }),
        el('span', { class: 'help' }, 'Leave blank for non-expiring token.')),
      el('button', { type: 'submit', class: 'btn' }, 'Generate token'),
    );
    createForm.onsubmit = async (ev) => {
      ev.preventDefault();
      const fd = new FormData(createForm);
      const scopes = ['read', 'write', 'admin'].filter(s => fd.get('scope_' + s));
      const body = { name: fd.get('name'), scopes };
      if (fd.get('expires_at')) body.expires_at = new Date(fd.get('expires_at')).toISOString();
      try {
        const result = await api('POST', '/api-tokens', body);
        const tok = result.token || result.raw_token || '(check API response)';
        tokenReveal.hidden = false;
        tokenReveal.innerHTML = '';
        tokenReveal.append(
          el('p', { style: 'font-weight:600;color:var(--status-ok);font-size:var(--font-size-sm)' }, '✓ Token created — copy it now, it will not be shown again.'),
          el('div', { class: 'token-reveal' },
            el('code', {}, escapeHTML(tok)),
            (() => {
              const btn = el('button', { class: 'copy-btn' }, 'Copy');
              btn.onclick = () => { navigator.clipboard.writeText(tok); btn.textContent = 'Copied!'; setTimeout(() => { btn.textContent = 'Copy'; }, 2000); };
              return btn;
            })()),
        );
        createForm.reset();
        createDet.open = false;
        loadTokens();
      } catch (err) { alert('Error: ' + err.message); }
    };
    createDet.append(createForm, tokenReveal);
    area.appendChild(createDet);

    // OpenAPI docs link
    area.appendChild(sectionDivider('API Documentation'));
    area.append(
      el('p', { style: 'font-size:var(--font-size-sm);margin-bottom:12px' },
        'NetMantle provides a full OpenAPI 3 REST API. Use the interactive explorer to test endpoints, generate sample requests, and build integrations.'),
      el('a', { href: '/api/docs', target: '_blank', rel: 'noopener', class: 'btn' }, '🔍 Open API Explorer →'),
      el('p', { class: 'help', style: 'margin-top:8px' }, 'Authenticate via session cookie or Authorization: Bearer <token> header.'),
    );

    // Tenants
    area.appendChild(sectionDivider('Tenants'));
    const tenantWrap = el('div', { class: 'table-wrapper' });
    area.appendChild(tenantWrap);
    try {
      const tenants = await api('GET', '/tenants');
      if (!tenants || !tenants.length) { tenantWrap.innerHTML = '<p class="muted" style="padding:8px">No tenants.</p>'; }
      else {
        const tbl = el('table', { class: 'device-table' },
          el('thead', {}, el('tr', {}, el('th', {}, 'ID'), el('th', {}, 'Name'), el('th', {}, 'Max Devices'), el('th', {}, 'Created'))),
          el('tbody'));
        for (const t of tenants) {
          tbl.querySelector('tbody').appendChild(el('tr', {},
            el('td', {}, '#' + t.id),
            el('td', {}, escapeHTML(t.name || '—')),
            el('td', {}, t.max_devices != null ? String(t.max_devices) : '—'),
            el('td', {}, t.created_at ? relativeTime(new Date(t.created_at)) : '—'),
          ));
        }
        tenantWrap.appendChild(tbl);
      }
    } catch (e) { tenantWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
  }

  // ---- Audit Logs ----
  async function renderAuditLogs(area) {
    area.appendChild(el('h2', { style: 'margin-bottom:var(--space-3)' }, 'Audit Logs'));

    let autoRefresh = false;
    let refreshTimer = null;

    const filterInp = el('input', { type: 'search', id: 'audit-filter', placeholder: 'Filter by user or action…', style: 'flex:1;min-width:180px;padding:5px 10px;border:1px solid var(--border-default);border-radius:var(--radius-sm);font-size:var(--font-size-sm);background:var(--surface-card);color:var(--text-default)' });
    const arInp = el('input', { type: 'checkbox' });
    const arSw = el('label', { class: 'toggle-switch' }, arInp, el('span', { class: 'toggle-slider' }));
    const arLabel = el('label', { class: 'toggle-row', style: 'gap:6px;font-size:var(--font-size-sm)' }, arSw, 'Auto-refresh (30s)');
    arInp.addEventListener('change', () => {
      autoRefresh = arInp.checked;
      if (autoRefresh) refreshTimer = setInterval(loadAuditLogs, 30000);
      else { clearInterval(refreshTimer); refreshTimer = null; }
    });
    const refreshBtn = el('button', { class: 'btn ghost' }, '↻ Refresh');
    refreshBtn.onclick = loadAuditLogs;

    area.appendChild(el('div', { style: 'display:flex;gap:10px;align-items:center;margin-bottom:10px;flex-wrap:wrap' },
      filterInp, arLabel, refreshBtn));

    const terminal = el('div', { class: 'audit-terminal' });
    terminal.appendChild(el('div', { class: 'alog-row', style: 'font-weight:700;color:#8b949e;border-bottom:1px solid #30363d;padding-bottom:4px;margin-bottom:4px' },
      el('span', {}, 'TIMESTAMP'), el('span', {}, 'USER'), el('span', {}, 'ACTION'), el('span', {}, 'TARGET')));
    area.appendChild(terminal);

    let allLogs = [];

    async function loadAuditLogs() {
      try {
        const logs = await api('GET', '/audit/events?limit=200');
        allLogs = logs || [];
        renderLogs();
      } catch (e) {
        terminal.innerHTML = '<div style="color:#f87171;padding:8px">Error: ' + escapeHTML(e.message) + '</div>';
      }
    }

    function renderLogs() {
      const filter = filterInp.value.toLowerCase();
      const filtered = filter
        ? allLogs.filter(l => (l.actor || '').toLowerCase().includes(filter) || (l.action || '').toLowerCase().includes(filter))
        : allLogs;
      while (terminal.children.length > 1) terminal.removeChild(terminal.lastChild);
      for (const log of filtered) {
        const ts = log.created_at ? new Date(log.created_at).toLocaleString() : '—';
        terminal.appendChild(el('div', { class: 'alog-row' },
          el('span', { class: 'alog-ts' }, ts),
          el('span', { class: 'alog-user' }, escapeHTML(log.actor || log.username || '—')),
          el('span', { class: 'alog-action' }, escapeHTML(log.action || '—')),
          el('span', { class: 'alog-target' }, escapeHTML(log.target || log.detail || '')),
        ));
      }
      if (!filtered.length) terminal.appendChild(el('div', { style: 'color:#6e7681;padding:8px' }, 'No log entries found.'));
      terminal.scrollTop = terminal.scrollHeight;
    }

    filterInp.addEventListener('input', renderLogs);
    loadAuditLogs();

    window.addEventListener('hashchange', () => { clearInterval(refreshTimer); }, { once: true });
  }

  // Remove save bar when navigating away
  window.addEventListener('hashchange', () => { saveBar.remove(); }, { once: true });

  showSection(initCat);
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
    const res = await api('POST', '/auth/login', {
      username: fd.get('username'),
      password: fd.get('password'),
    });
    if (res && res.mfa_required) {
      e.target.closest('section').dataset.mfaChallenge = res.mfa_challenge;
      $('#login-form').hidden = true;
      $('#mfa-step').hidden = false;
      return;
    }
    e.target.reset();
    await refreshSession();
  } catch (err) {
    $('#login-error').textContent = err.message;
  }
});

$('#mfa-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  const challenge = $('#login-view').dataset.mfaChallenge;
  try {
    await api('POST', '/auth/mfa-verify', { mfa_challenge: challenge, code: fd.get('code') });
    $('#mfa-step').hidden = true;
    $('#login-form').hidden = false;
    await refreshSession();
  } catch (err) {
    $('#mfa-error').textContent = 'Invalid code. Try again.';
  }
});

$('#logout').addEventListener('click', async () => {
  try { await api('POST', '/auth/logout'); } catch (_) {}
  refreshSession();
});

// ---------- Zones ----------
views.zones = async (root) => {
  root.appendChild(el('div', { class: 'page-header' },
    el('div', { class: 'page-header-left' },
      el('div', { class: 'page-header-breadcrumb' }, 'Core Operations'),
      el('div', { class: 'page-header-title' }, 'Zones & Remote Cores')),
    el('div', { class: 'page-header-actions' },
      el('a', { href: '#/settings', class: 'btn ghost' }, 'Settings →'))));

  const grid = el('div', { class: 'zones-grid' });
  root.appendChild(grid);

  try {
    const pollers = await api('GET', '/pollers');
    if (!pollers || !pollers.length) {
      grid.appendChild(el('div', { class: 'zone-card' },
        el('div', { class: 'z-name' }, 'Local (this server)'),
        el('div', { class: 'z-zone' }, 'Zone: default'),
        el('div', { class: 'z-meta' },
          el('span', { class: 'badge badge-ok' }, 'Healthy'),
          el('span', { class: 'z-seen' }, 'Primary'))));
    } else {
      grid.appendChild(el('div', { class: 'zone-card' },
        el('div', { class: 'z-name' }, 'Local Server'),
        el('div', { class: 'z-zone' }, 'Zone: primary'),
        el('div', { class: 'z-meta' }, el('span', { class: 'badge badge-ok' }, 'Healthy'), el('span', { class: 'z-seen' }, 'This server'))));

      for (const p of pollers) {
        const lastSeen = p.last_seen ? new Date(p.last_seen) : null;
        const ageMs = lastSeen ? Date.now() - lastSeen.getTime() : Infinity;
        const stCls = ageMs < 120000 ? 'ok' : ageMs < 600000 ? 'warn' : 'bad';
        const stLabel = ageMs < 120000 ? 'Healthy' : ageMs < 600000 ? 'Stale' : 'Offline';
        grid.appendChild(el('div', { class: 'zone-card' },
          el('div', { class: 'z-name' }, escapeHTML(p.name || 'Remote Core')),
          el('div', { class: 'z-zone' }, 'Zone: ' + escapeHTML(p.zone || '—')),
          el('div', { class: 'z-meta' },
            el('span', { class: 'badge badge-' + stCls }, stLabel),
            el('span', { class: 'z-seen' }, lastSeen ? 'seen ' + relativeTime(lastSeen) : 'never'))));
      }
    }
  } catch (e) {
    grid.appendChild(el('p', { class: 'error' }, 'Error: ' + escapeHTML(e.message)));
  }

  root.appendChild(el('div', { class: 'card', style: 'margin-top:20px;padding:16px 20px' },
    el('h3', { style: 'margin:0 0 8px' }, 'Deploying a Remote Core'),
    el('p', { style: 'font-size:var(--font-size-sm);color:var(--text-muted);margin:0' },
      'Run the netmantle binary on a remote site with the same DB config. Pollers self-register on startup and appear here automatically.')));
};

// ---------- Config Search ----------
views.search = (root) => {
  const header = el('div', { class: 'page-header' },
    el('div', { class: 'page-header-left' },
      el('div', { class: 'page-header-breadcrumb' }, 'Automation & Intelligence'),
      el('div', { class: 'page-header-title' }, 'Config Search')),
  );
  root.appendChild(header);

  const searchWrap = el('div', { style: 'display:flex;gap:8px;margin-bottom:20px;align-items:center' },
    el('input', { type: 'search', id: 'cfg-search-input', placeholder: 'Search all device configs (VLAN ID, IP, hostname, password…)', style: 'flex:1;padding:9px 14px;border:1px solid var(--border-default);border-radius:var(--radius-pill);font-size:var(--font-size-sm);background:var(--surface-card);color:var(--text-default)' }),
    el('button', { class: 'btn', id: 'cfg-search-btn' }, '🔍 Search'),
  );
  root.appendChild(searchWrap);

  const results = el('div', { id: 'cfg-search-results' });
  root.appendChild(results);

  if (window._globalSearchQuery) {
    $('#cfg-search-input').value = window._globalSearchQuery;
    window._globalSearchQuery = null;
    runSearch();
  }

  async function runSearch() {
    const q = ($('#cfg-search-input') || { value: '' }).value.trim();
    if (!q) return;
    results.innerHTML = '<p class="muted">Searching…</p>';
    try {
      const hits = await api('GET', '/search?q=' + encodeURIComponent(q));
      results.innerHTML = '';
      if (!hits || !hits.length) { results.innerHTML = '<p class="muted">No results found.</p>'; return; }
      results.appendChild(el('p', { style: 'font-size:var(--font-size-xs);color:var(--text-muted);margin-bottom:12px' }, `${hits.length} result(s) for "${escapeHTML(q)}"`));
      for (const h of hits) {
        const card = el('div', { class: 'search-result' });
        const hdr = el('div', { class: 'search-result-header' },
          el('div', {},
            el('strong', {}, escapeHTML(h.hostname || h.device_hostname || 'Device #' + h.device_id)),
            el('span', { class: 'muted', style: 'margin-left:8px;font-size:var(--font-size-xs)' }, escapeHTML(h.driver || ''))),
          el('span', { style: 'font-size:0.7rem;color:var(--text-muted)' }, h.commit_sha ? h.commit_sha.slice(0, 8) : ''));
        card.appendChild(hdr);
        const body = el('div', { class: 'search-result-body', hidden: true });
        if (h.snippet || h.context) {
          const raw = h.snippet || h.context || '';
          const snippet = el('div', { class: 'search-snippet' });
          snippet.innerHTML = escapeHTML(raw).replace(
            new RegExp('(' + q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + ')', 'gi'),
            '<mark>$1</mark>');
          body.appendChild(snippet);
        }
        hdr.onclick = () => { body.hidden = !body.hidden; };
        card.appendChild(body);
        results.appendChild(card);
      }
    } catch (e) { results.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
  }

  $('#cfg-search-btn').addEventListener('click', runSearch);
  $('#cfg-search-input').addEventListener('keydown', (e) => { if (e.key === 'Enter') runSearch(); });
};

// ---------- Notifications ----------
views.notifications = async (root) => {
  root.appendChild(el('div', { class: 'page-header' },
    el('div', { class: 'page-header-left' },
      el('div', { class: 'page-header-breadcrumb' }, 'System'),
      el('div', { class: 'page-header-title' }, 'Notifications'))));

  root.appendChild(el('div', { class: 'section-divider' }, 'Channels'));
  const chanTblWrap = el('div', { class: 'table-wrapper', style: 'margin-bottom:var(--space-4)' });
  root.appendChild(chanTblWrap);
  async function loadChannels() {
    chanTblWrap.innerHTML = '<p class="muted" style="padding:8px">Loading…</p>';
    try {
      const channels = await api('GET', '/notifications/channels');
      if (!channels || !channels.length) { chanTblWrap.innerHTML = '<p class="muted" style="padding:8px">No channels yet.</p>'; return; }
      const tbl = el('table', { class: 'device-table' },
        el('thead', {}, el('tr', {}, el('th', {}, 'Name'), el('th', {}, 'Kind'), el('th', {}, 'Created'))),
        el('tbody'));
      for (const c of channels) {
        tbl.querySelector('tbody').appendChild(el('tr', {},
          el('td', {}, escapeHTML(c.name)), el('td', {}, el('code', {}, escapeHTML(c.kind))),
          el('td', {}, c.created_at ? relativeTime(new Date(c.created_at)) : '—')));
      }
      chanTblWrap.innerHTML = ''; chanTblWrap.appendChild(tbl);
    } catch (e) { chanTblWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
  }
  loadChannels();

  const addDet = el('details');
  addDet.appendChild(el('summary', { style: 'cursor:pointer;font-weight:600;font-size:var(--font-size-sm)' }, '+ Add channel'));
  const kindSel = el('select', { name: 'kind' }, el('option', { value: 'webhook' }, 'Webhook'), el('option', { value: 'slack' }, 'Slack'), el('option', { value: 'email' }, 'Email'), el('option', { value: 'pushover' }, 'Pushover'));
  const kindFields = el('div');
  function renderKF() {
    kindFields.innerHTML = '';
    if (kindSel.value === 'webhook' || kindSel.value === 'slack') kindFields.appendChild(el('label', {}, 'URL ', el('input', { name: 'config_url', type: 'url', required: true })));
    else if (kindSel.value === 'email') kindFields.append(el('label', {}, 'SMTP host ', el('input', { name: 'config_host', required: true })), el('label', {}, 'To ', el('input', { name: 'config_to', type: 'email', required: true })));
    else kindFields.append(el('label', {}, 'API token ', el('input', { name: 'config_token', required: true })), el('label', {}, 'User key ', el('input', { name: 'config_user_key', required: true })));
  }
  kindSel.addEventListener('change', renderKF); renderKF();
  const cf = el('form', { style: 'margin-top:10px;display:grid;gap:8px' },
    el('label', {}, 'Name ', el('input', { name: 'name', required: true })),
    el('label', {}, 'Kind ', kindSel), kindFields,
    el('button', { type: 'submit', class: 'btn' }, 'Create'));
  cf.onsubmit = async (ev) => {
    ev.preventDefault();
    const fd = new FormData(cf);
    const kind = fd.get('kind');
    const config = kind === 'webhook' || kind === 'slack' ? { url: fd.get('config_url') } :
      kind === 'email' ? { host: fd.get('config_host'), to: fd.get('config_to') } :
      { token: fd.get('config_token'), user_key: fd.get('config_user_key') };
    try { await api('POST', '/notifications/channels', { name: fd.get('name'), kind, config }); cf.reset(); renderKF(); addDet.open = false; loadChannels(); }
    catch (err) { alert(err.message); }
  };
  addDet.appendChild(cf); root.appendChild(addDet);

  root.appendChild(el('div', { class: 'section-divider', style: 'margin-top:20px' }, 'Rules'));
  try {
    const rules = await api('GET', '/notifications/rules');
    if (!rules || !rules.length) { root.appendChild(el('p', { class: 'muted' }, 'No rules configured.')); }
    else {
      const tbl = el('table', { class: 'device-table' },
        el('thead', {}, el('tr', {}, el('th', {}, 'Name'), el('th', {}, 'Event'), el('th', {}, 'Channel'))),
        el('tbody'));
      for (const r of rules) {
        tbl.querySelector('tbody').appendChild(el('tr', {},
          el('td', {}, escapeHTML(r.name || '—')), el('td', {}, el('code', {}, escapeHTML(r.event_type || '—'))), el('td', {}, '#' + (r.channel_id || '?'))));
      }
      const wrap = el('div', { class: 'table-wrapper' }); wrap.appendChild(tbl); root.appendChild(wrap);
    }
  } catch (e) { root.appendChild(el('p', { class: 'error' }, escapeHTML(e.message))); }
};

// ---------- Users ----------
views.users = async (root) => {
  root.appendChild(el('div', { class: 'page-header' },
    el('div', { class: 'page-header-left' },
      el('div', { class: 'page-header-breadcrumb' }, 'System'),
      el('div', { class: 'page-header-title' }, 'Users & Access Control')),
    el('div', { class: 'page-header-actions' },
      el('button', { class: 'btn', id: 'add-user-btn' }, '+ Add user'))));

  const tblWrap = el('div', { class: 'table-wrapper' });
  root.appendChild(tblWrap);

  async function loadUsers() {
    tblWrap.innerHTML = '<p class="muted" style="padding:8px">Loading…</p>';
    try {
      const users = await api('GET', '/users');
      if (!users || !users.length) { tblWrap.innerHTML = '<p class="muted" style="padding:8px">No users found.</p>'; return; }
      const tbl = el('table', { class: 'device-table' },
        el('thead', {}, el('tr', {}, el('th', {}, 'Username'), el('th', {}, 'Role'), el('th', {}, 'Tenant'), el('th', {}, 'MFA'), el('th', {}, 'Created'), el('th', {}, ''))),
        el('tbody'));
      for (const u of users) {
        const roleCls = u.role === 'admin' ? 'admin' : u.role === 'operator' ? 'operator' : 'viewer';
        tbl.querySelector('tbody').appendChild(el('tr', {},
          el('td', {}, el('strong', {}, escapeHTML(u.username))),
          el('td', {}, el('span', { class: 'role-badge ' + roleCls }, u.role || '—')),
          el('td', {}, u.tenant_id ? '#' + u.tenant_id : '—'),
          el('td', {}, u.totp_enabled ? el('span', { class: 'badge badge-ok' }, '✓ MFA') : el('span', { class: 'badge' }, 'off')),
          el('td', {}, u.created_at ? relativeTime(new Date(u.created_at)) : '—'),
          el('td', {}, (() => {
            const d = el('button', { class: 'qa-btn' }, 'Delete');
            d.onclick = async () => {
              if (!confirm('Delete user "' + u.username + '"? This cannot be undone.')) return;
              try { await api('DELETE', '/users/' + u.id); loadUsers(); }
              catch (err) { alert(err.message); }
            };
            return d;
          })()),
        ));
      }
      tblWrap.innerHTML = '';
      tblWrap.appendChild(tbl);
    } catch (e) { tblWrap.innerHTML = '<p class="error">' + escapeHTML(e.message) + '</p>'; }
  }
  loadUsers();

  const addSection = el('div', { id: 'add-user-section', hidden: true, class: 'card', style: 'margin-top:16px;padding:16px 20px' });
  addSection.appendChild(el('h3', { style: 'margin:0 0 12px' }, 'Create user'));
  const addForm = el('form', { style: 'display:grid;gap:10px;max-width:400px' },
    el('label', {}, 'Username ', el('input', { name: 'username', required: true })),
    el('label', {}, 'Password ', el('input', { name: 'password', type: 'password', required: true })),
    el('label', {}, 'Role ',
      el('select', { name: 'role' },
        el('option', { value: 'viewer' }, 'Viewer'),
        el('option', { value: 'operator' }, 'Operator'),
        el('option', { value: 'admin' }, 'Admin'))),
    el('button', { type: 'submit', class: 'btn' }, 'Create user'),
  );
  addForm.onsubmit = async (ev) => {
    ev.preventDefault();
    const fd = new FormData(addForm);
    try {
      await api('POST', '/users', { username: fd.get('username'), password: fd.get('password'), role: fd.get('role') });
      addForm.reset(); addSection.hidden = true; loadUsers();
    } catch (err) { alert('Error: ' + err.message); }
  };
  addSection.appendChild(addForm);
  root.appendChild(addSection);

  root.querySelector('#add-user-btn').onclick = () => { addSection.hidden = !addSection.hidden; };

  root.appendChild(el('div', { class: 'card', style: 'margin-top:16px;padding:16px 20px' },
    el('h3', { style: 'margin:0 0 8px' }, 'Roles'),
    el('dl', { style: 'display:grid;grid-template-columns:100px 1fr;gap:6px 12px;font-size:var(--font-size-sm)' },
      el('dt', {}, el('span', { class: 'role-badge admin' }, 'admin')), el('dd', { style: 'margin:0' }, 'Full access to all resources and settings.'),
      el('dt', {}, el('span', { class: 'role-badge operator' }, 'operator')), el('dd', { style: 'margin:0' }, 'Can backup, push configs, and view compliance. Cannot manage users or tenants.'),
      el('dt', {}, el('span', { class: 'role-badge viewer' }, 'viewer')), el('dd', { style: 'margin:0' }, 'Read-only access to device configs and compliance reports.'),
    )));
};

initTheme();
refreshSession();
