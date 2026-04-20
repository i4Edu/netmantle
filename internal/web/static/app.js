// NetMantle — Enterprise NMS SPA
// Vanilla JS, hash-router, no build step, no frameworks.
// Enterprise-grade: high-density tables, proper state, clean architecture.

'use strict';

// ===== Utilities =====
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const escHTML = s => String(s == null ? '' : s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

const api = async (method, path, body) => {
  const opts = { method, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const r = await fetch('/api/v1' + path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch (_) {}
    throw new Error(msg);
  }
  if (r.status === 204) return null;
  const ct = r.headers.get('content-type') || '';
  return ct.includes('json') ? r.json() : r.text();
};

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k === 'html') e.innerHTML = v;
    else if (k.startsWith('on') && typeof v === 'function') e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else e.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c == null) continue;
    e.appendChild(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return e;
}

function toast(msg, type = 'info') {
  const t = el('div', { class: `toast ${type}` }, msg);
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3500);
}

function timeAgo(dateStr) {
  if (!dateStr) return '—';
  const d = new Date(dateStr);
  const s = Math.floor((Date.now() - d) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

function fmtDate(dateStr) {
  if (!dateStr) return '—';
  const d = new Date(dateStr);
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) +
    ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

// ===== Theme =====
const THEME_KEY = 'netmantle.theme';
function applyTheme(theme) {
  if (theme === 'light' || theme === 'dark') document.documentElement.setAttribute('data-theme', theme);
  else document.documentElement.removeAttribute('data-theme');
}
function currentTheme() {
  const s = localStorage.getItem(THEME_KEY);
  if (s === 'light' || s === 'dark') return s;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}
function initTheme() {
  applyTheme(localStorage.getItem(THEME_KEY));
  const btn = $('#theme-toggle');
  const label = $('#theme-toggle-label');
  const refresh = () => { if (label) label.textContent = currentTheme() === 'dark' ? 'Light' : 'Dark'; };
  refresh();
  if (btn) btn.addEventListener('click', () => {
    const next = currentTheme() === 'dark' ? 'light' : 'dark';
    localStorage.setItem(THEME_KEY, next);
    applyTheme(next);
    refresh();
  });
}

// ===== Session =====
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

async function refreshApprovalsBadge() {
  try {
    const crs = await api('GET', '/change-requests');
    const pending = (crs || []).filter(c => c.status === 'submitted').length;
    const badge = $('#approvals-badge');
    if (badge) { badge.hidden = pending === 0; badge.textContent = pending; }
  } catch (_) {}
}

// ===== Router =====
const ROUTES = ['dashboard','inventory','zones','automation','compliance','search','users','approvals','audit','settings'];

function currentRoute() {
  const h = (location.hash || '').replace(/^#\/?/, '');
  const r = h.split('/')[0] || 'dashboard';
  return ROUTES.includes(r) ? r : 'dashboard';
}

function router() {
  const route = currentRoute();
  for (const a of $$('.sidebar a[data-route]')) a.classList.toggle('active', a.dataset.route === route);
  const view = $('#view');
  view.innerHTML = '';
  const fn = views[route];
  if (fn) fn(view);
}

window.addEventListener('hashchange', router);

// ===== Init =====
document.addEventListener('DOMContentLoaded', () => {
  initTheme();

  // Sidebar toggle
  const sb = $('#sidebar'), toggle = $('#sidebar-toggle');
  if (toggle && sb) {
    if (localStorage.getItem('nm.sidebar.collapsed') === 'true') sb.dataset.collapsed = 'true';
    toggle.addEventListener('click', () => {
      const next = sb.dataset.collapsed !== 'true';
      sb.dataset.collapsed = String(next);
      localStorage.setItem('nm.sidebar.collapsed', String(next));
    });
  }

  // Slideover
  const overlay = $('#slideover-overlay'), slideEl = $('#device-slideover'), closeBtn = $('#slideover-close');
  const closeSlideover = () => { if (slideEl) slideEl.hidden = true; if (overlay) overlay.hidden = true; };
  if (overlay) overlay.addEventListener('click', closeSlideover);
  if (closeBtn) closeBtn.addEventListener('click', closeSlideover);

  // Slideover tabs
  const tabsC = $('#slideover-tabs');
  if (tabsC) tabsC.addEventListener('click', e => {
    const tab = e.target.closest('.slideover-tab');
    if (!tab) return;
    $$('.slideover-tab', tabsC).forEach(t => t.classList.remove('active'));
    tab.classList.add('active');
    if (window._slideoverDevice) renderSlideoverTab(window._slideoverDevice, tab.dataset.tab);
  });

  // Global search
  const topSearch = $('#topbar-search');
  if (topSearch) topSearch.addEventListener('keydown', e => {
    if (e.key === 'Enter' && topSearch.value.trim()) {
      window._searchQuery = topSearch.value.trim();
      location.hash = '#/search';
      topSearch.blur();
    }
  });

  // Login form
  const loginForm = $('#login-form');
  if (loginForm) loginForm.addEventListener('submit', async e => {
    e.preventDefault();
    const fd = new FormData(loginForm);
    try {
      const res = await api('POST', '/auth/login', { username: fd.get('username'), password: fd.get('password') });
      if (res && res.mfa_required) {
        loginForm.hidden = true;
        $('#mfa-step').hidden = false;
        window._mfaToken = res.mfa_token;
      } else {
        await refreshSession();
      }
    } catch (err) { $('#login-error').textContent = err.message; }
  });

  // MFA form
  const mfaForm = $('#mfa-form');
  if (mfaForm) mfaForm.addEventListener('submit', async e => {
    e.preventDefault();
    const code = new FormData(mfaForm).get('code');
    try {
      await api('POST', '/auth/mfa-verify', { token: window._mfaToken, code });
      await refreshSession();
    } catch (err) { $('#mfa-error').textContent = err.message; }
  });

  // Logout
  const logoutBtn = $('#logout');
  if (logoutBtn) logoutBtn.addEventListener('click', async () => {
    try { await api('POST', '/auth/logout'); } catch (_) {}
    me = null;
    location.reload();
  });

  refreshSession();
});

// ==================================================================
// VIEWS
// ==================================================================
const views = {};

// ===== DASHBOARD =====
views.dashboard = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const summary = await api('GET', '/dashboard/summary');
    root.innerHTML = '';

    // Page header
    root.appendChild(el('div', { class: 'page-header' },
      el('div', null,
        el('h2', null, 'Dashboard'),
        el('p', { class: 'subtitle' }, 'Network configuration health overview')
      ),
      el('div', { class: 'page-actions' },
        el('button', { class: 'btn btn-primary', onclick: () => location.hash = '#/inventory' }, '+ Add Device')
      )
    ));

    // KPI cards
    const kpis = el('div', { class: 'kpi-grid' });
    const total = summary.device_count || 0;
    const lastOk = summary.last_backup_success || 0;
    const lastFail = summary.last_backup_failed || 0;
    const changes = summary.recent_changes || 0;
    const compliance = summary.compliance_pass_rate;

    kpis.appendChild(kpiCard('Total Devices', total, 'Managed inventory', 'info'));
    kpis.appendChild(kpiCard('Backup Success', lastOk, `${lastFail} failed`, lastFail > 0 ? 'warn' : 'ok'));
    kpis.appendChild(kpiCard('Recent Changes', changes, 'Last 24 hours', changes > 0 ? 'warn' : 'ok'));
    if (compliance != null) kpis.appendChild(kpiCard('Compliance', Math.round(compliance) + '%', 'Pass rate', compliance >= 90 ? 'ok' : 'bad'));
    root.appendChild(kpis);

    // Recent changes table
    root.appendChild(el('div', { class: 'section-title' }, 'RECENT CONFIGURATION CHANGES'));
    try {
      const changes_list = await api('GET', '/changes?limit=10');
      if (changes_list && changes_list.length > 0) {
        const wrap = el('div', { class: 'data-table-wrap' });
        const table = el('table', { class: 'data-table' });
        table.innerHTML = `<thead><tr><th>Device</th><th>Type</th><th>Time</th><th>Status</th></tr></thead>`;
        const tbody = el('tbody');
        for (const c of changes_list) {
          tbody.appendChild(el('tr', null,
            el('td', null, escHTML(c.device_name || c.device_id)),
            el('td', null, el('span', { class: 'badge badge-neutral' }, escHTML(c.change_type || 'config'))),
            el('td', { class: 'text-muted text-sm' }, timeAgo(c.detected_at || c.created_at)),
            el('td', null, el('span', { class: `badge ${c.reviewed ? 'badge-ok' : 'badge-warn'}` }, c.reviewed ? 'Reviewed' : 'Pending'))
          ));
        }
        table.appendChild(tbody);
        wrap.appendChild(table);
        root.appendChild(wrap);
      } else {
        root.appendChild(emptyState('No changes detected yet', 'Configuration diffs will appear here after backups run.'));
      }
    } catch (_) {
      root.appendChild(emptyState('No changes detected yet', 'Configuration diffs will appear here after backups run.'));
    }

  } catch (err) {
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'empty-state' },
      el('h3', null, 'Unable to load dashboard'),
      el('p', null, err.message)
    ));
  }
};

function kpiCard(label, value, sub, variant) {
  return el('div', { class: `kpi-card ${variant || ''}` },
    el('div', { class: 'kpi-label' }, label),
    el('div', { class: 'kpi-value' }, String(value)),
    el('div', { class: 'kpi-sub' }, sub)
  );
}

function emptyState(title, desc) {
  return el('div', { class: 'empty-state' },
    el('h3', null, title),
    el('p', null, desc)
  );
}

// ===== DEVICES / INVENTORY =====
views.inventory = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';

  let devices = [], credentials = [], groups = [];
  try {
    [devices, credentials, groups] = await Promise.all([
      api('GET', '/devices'),
      api('GET', '/credentials').catch(() => []),
      api('GET', '/device-groups').catch(() => [])
    ]);
    devices = devices || [];
  } catch (err) {
    root.innerHTML = '';
    root.appendChild(emptyState('Failed to load devices', err.message));
    return;
  }

  root.innerHTML = '';

  // State
  let search = '', statusFilter = 'all', vendorFilter = 'all', page = 0;
  const pageSize = 50;
  let selectedIds = new Set();

  // Header
  root.appendChild(el('div', { class: 'page-header' },
    el('div', null,
      el('h2', null, 'Devices'),
      el('p', { class: 'subtitle' }, `${devices.length} managed device${devices.length !== 1 ? 's' : ''}`)
    ),
    el('div', { class: 'page-actions' },
      el('button', { class: 'btn btn-secondary', onclick: () => openExportModal(devices) }, 'Export'),
      el('button', { class: 'btn btn-primary', onclick: showAddDevice }, '+ Add Device')
    )
  ));

  // Filter bar
  const filterBar = el('div', { class: 'filter-bar' });
  const searchInput = el('input', { type: 'search', placeholder: 'Search name, IP, vendor… (vendor:mikrotik status:fail)', autocomplete: 'off' });
  searchInput.addEventListener('input', () => { search = searchInput.value; page = 0; render(); });

  const statusSel = el('select', { class: '' });
  statusSel.innerHTML = '<option value="all">All Status</option><option value="ok">Success</option><option value="fail">Failed</option><option value="never">Never backed up</option>';
  statusSel.addEventListener('change', () => { statusFilter = statusSel.value; page = 0; render(); });

  const vendorSel = el('select');
  const vendors = [...new Set(devices.map(d => d.driver).filter(Boolean))].sort();
  vendorSel.innerHTML = '<option value="all">All Vendors</option>' + vendors.map(v => `<option value="${escHTML(v)}">${escHTML(v)}</option>`).join('');
  vendorSel.addEventListener('change', () => { vendorFilter = vendorSel.value; page = 0; render(); });

  const countEl = el('span', { class: 'filter-count' });
  filterBar.append(searchInput, statusSel, vendorSel, countEl);
  root.appendChild(filterBar);

  // Table
  const tableWrap = el('div', { class: 'data-table-wrap' });
  const table = el('table', { class: 'data-table' });
  const thead = el('thead');
  thead.innerHTML = `<tr>
    <th class="col-check"><input type="checkbox" id="select-all"></th>
    <th class="col-status"></th>
    <th>Name</th>
    <th>Address</th>
    <th>Driver</th>
    <th>Last Backup</th>
    <th>Status</th>
    <th class="col-actions">Actions</th>
  </tr>`;
  const selectAll = $('input', thead);
  selectAll.addEventListener('change', () => {
    const filtered = getFiltered();
    if (selectAll.checked) filtered.forEach(d => selectedIds.add(d.id));
    else selectedIds.clear();
    render();
  });
  table.appendChild(thead);
  const tbody = el('tbody');
  table.appendChild(tbody);
  tableWrap.appendChild(table);
  root.appendChild(tableWrap);

  // Pagination
  const pagBar = el('div', { class: 'pagination' });
  root.appendChild(pagBar);

  function getFiltered() {
    let list = devices;
    // Property-based search
    const terms = search.toLowerCase().split(/\s+/).filter(Boolean);
    if (terms.length) {
      list = list.filter(d => {
        return terms.every(term => {
          if (term.startsWith('vendor:') || term.startsWith('driver:')) {
            return (d.driver || '').toLowerCase().includes(term.split(':')[1]);
          }
          if (term.startsWith('status:')) {
            const sv = term.split(':')[1];
            if (sv === 'ok' || sv === 'success') return d.last_backup_status === 'success';
            if (sv === 'fail' || sv === 'failed') return d.last_backup_status === 'failed';
            return true;
          }
          if (term.startsWith('ip:')) return (d.address || '').includes(term.split(':')[1]);
          if (term.startsWith('tag:') || term.startsWith('group:')) {
            return (d.tags || []).some(t => t.toLowerCase().includes(term.split(':')[1]));
          }
          const hay = `${d.hostname || ''} ${d.address || ''} ${d.driver || ''} ${(d.tags || []).join(' ')}`.toLowerCase();
          return hay.includes(term);
        });
      });
    }
    if (statusFilter === 'ok') list = list.filter(d => d.last_backup_status === 'success');
    else if (statusFilter === 'fail') list = list.filter(d => d.last_backup_status === 'failed');
    else if (statusFilter === 'never') list = list.filter(d => !d.last_backup_at);
    if (vendorFilter !== 'all') list = list.filter(d => d.driver === vendorFilter);
    return list;
  }

  function render() {
    const filtered = getFiltered();
    countEl.textContent = `${filtered.length} of ${devices.length}`;
    const totalPages = Math.ceil(filtered.length / pageSize);
    const start = page * pageSize;
    const pageItems = filtered.slice(start, start + pageSize);

    tbody.innerHTML = '';
    if (pageItems.length === 0) {
      tbody.innerHTML = `<tr><td colspan="8" class="text-center text-muted" style="padding:32px">No devices match your filters.</td></tr>`;
    }
    for (const d of pageItems) {
      const status = d.last_backup_status === 'success' ? 'ok' : d.last_backup_status === 'failed' ? 'bad' : 'idle';
      const checked = selectedIds.has(d.id);
      const tr = el('tr', { onclick: (e) => { if (e.target.type !== 'checkbox' && !e.target.closest('.row-actions')) openSlideover(d); } },
        el('td', { class: 'col-check' }, el('input', { type: 'checkbox', checked: checked || undefined, onchange: (e) => { e.stopPropagation(); if (e.target.checked) selectedIds.add(d.id); else selectedIds.delete(d.id); } })),
        el('td', { class: 'col-status' }, el('span', { class: `status-dot ${status}${status === 'ok' ? '' : status === 'bad' ? '' : ''}` })),
        el('td', null, el('span', { class: 'font-mono', style: 'font-weight:500' }, escHTML(d.hostname || d.address))),
        el('td', { class: 'text-muted font-mono text-sm' }, escHTML(d.address)),
        el('td', null, el('span', { class: 'badge badge-neutral' }, escHTML(d.driver || '—'))),
        el('td', { class: 'text-muted text-sm' }, d.last_backup_at ? timeAgo(d.last_backup_at) : '—'),
        el('td', null, statusBadge(d.last_backup_status)),
        el('td', { class: 'col-actions' },
          el('div', { class: 'row-actions' },
            el('button', { title: 'Backup now', onclick: (e) => { e.stopPropagation(); triggerBackup(d); } }, '⟳'),
            el('button', { title: 'View config', onclick: (e) => { e.stopPropagation(); openSlideover(d, 'backups'); } }, '◎'),
            el('button', { title: 'Delete', onclick: (e) => { e.stopPropagation(); deleteDevice(d); } }, '✕')
          )
        )
      );
      tbody.appendChild(tr);
    }

    // Pagination
    pagBar.innerHTML = '';
    pagBar.appendChild(el('span', null, `Page ${page + 1} of ${totalPages || 1}`));
    const btns = el('div', { class: 'pagination-buttons' });
    btns.appendChild(el('button', { disabled: page === 0 || undefined, onclick: () => { page--; render(); } }, '← Prev'));
    btns.appendChild(el('button', { disabled: page >= totalPages - 1 || undefined, onclick: () => { page++; render(); } }, 'Next →'));
    pagBar.appendChild(btns);
  }

  render();

  function statusBadge(s) {
    if (s === 'success') return el('span', { class: 'badge badge-ok' }, 'OK');
    if (s === 'failed') return el('span', { class: 'badge badge-bad' }, 'FAIL');
    return el('span', { class: 'badge badge-neutral' }, 'N/A');
  }

  async function triggerBackup(d) {
    try {
      await api('POST', `/devices/${d.id}/backup`);
      toast('Backup triggered', 'ok');
    } catch (err) { toast(err.message, 'bad'); }
  }

  async function deleteDevice(d) {
    if (!confirm(`Delete device "${d.hostname || d.address}"?`)) return;
    try {
      await api('DELETE', `/devices/${d.id}`);
      devices = devices.filter(x => x.id !== d.id);
      render();
      toast('Device deleted', 'ok');
    } catch (err) { toast(err.message, 'bad'); }
  }

  function showAddDevice() {
    const overlay = el('div', { class: 'modal-overlay' });
    const modal = el('div', { class: 'modal' });
    modal.innerHTML = `
      <div class="modal-header"><h3>Add Device</h3><button class="slideover-close" id="modal-close">×</button></div>
      <div class="modal-body">
        <div class="form-group"><label class="form-label">Hostname</label><input class="form-input" name="hostname" placeholder="router-core-01" required></div>
        <div class="form-row">
          <div class="form-group"><label class="form-label">Address (IP/Host)</label><input class="form-input" name="address" placeholder="192.168.1.1" required></div>
          <div class="form-group"><label class="form-label">Port</label><input class="form-input" name="port" type="number" value="22"></div>
        </div>
        <div class="form-row">
          <div class="form-group"><label class="form-label">Driver</label><select class="form-select" name="driver"></select></div>
          <div class="form-group"><label class="form-label">Credential</label><select class="form-select" name="credential_id"></select></div>
        </div>
        <div class="form-group"><label class="form-label">Tags (comma-separated)</label><input class="form-input" name="tags" placeholder="core, dc-1"></div>
      </div>
      <div class="modal-footer"><button class="btn btn-secondary" id="modal-cancel">Cancel</button><button class="btn btn-primary" id="modal-save">Save Device</button></div>
    `;
    overlay.appendChild(modal);
    document.body.appendChild(overlay);

    // Populate selects
    const driverSel = $('select[name=driver]', modal);
    api('GET', '/drivers').then(drivers => {
      driverSel.innerHTML = (drivers || []).map(d => `<option value="${escHTML(d.id)}">${escHTML(d.name || d.id)}</option>`).join('');
    }).catch(() => { driverSel.innerHTML = '<option>generic_ssh</option>'; });

    const credSel = $('select[name=credential_id]', modal);
    credSel.innerHTML = '<option value="">— none —</option>' + (credentials || []).map(c => `<option value="${c.id}">${escHTML(c.label || c.username)}</option>`).join('');

    const close = () => overlay.remove();
    $('#modal-close', modal).onclick = close;
    $('#modal-cancel', modal).onclick = close;
    overlay.addEventListener('click', e => { if (e.target === overlay) close(); });

    $('#modal-save', modal).onclick = async () => {
      const hostname = $('input[name=hostname]', modal).value.trim();
      const address = $('input[name=address]', modal).value.trim();
      const port = parseInt($('input[name=port]', modal).value) || 22;
      const driver = driverSel.value;
      const credential_id = credSel.value ? parseInt(credSel.value) : undefined;
      const tags = $('input[name=tags]', modal).value.split(',').map(s => s.trim()).filter(Boolean);
      if (!hostname || !address) { toast('Hostname and address required', 'bad'); return; }
      try {
        const dev = await api('POST', '/devices', { hostname, address, port, driver, credential_id, tags });
        devices.push(dev);
        render();
        close();
        toast('Device created', 'ok');
      } catch (err) { toast(err.message, 'bad'); }
    };
  }
};

// ===== SLIDEOVER (Device Detail Panel) =====
function openSlideover(device, tab = 'info') {
  window._slideoverDevice = device;
  const slideEl = $('#device-slideover');
  const overlay = $('#slideover-overlay');
  if (!slideEl || !overlay) return;
  slideEl.hidden = false;
  overlay.hidden = false;
  $('#slideover-title').textContent = device.hostname || device.address;
  $('#slideover-sub').textContent = `${device.address} · ${device.driver || 'unknown'}`;
  $$('.slideover-tab').forEach(t => t.classList.toggle('active', t.dataset.tab === tab));
  renderSlideoverTab(device, tab);
}

async function renderSlideoverTab(device, tab) {
  const body = $('#slideover-body');
  if (!body) return;
  body.innerHTML = '<div class="loading"><div class="spinner"></div></div>';

  try {
    if (tab === 'info') {
      body.innerHTML = '';
      const info = [
        ['Hostname', device.hostname],
        ['Address', device.address],
        ['Port', device.port],
        ['Driver', device.driver],
        ['Tags', (device.tags || []).join(', ') || '—'],
        ['Last Backup', device.last_backup_at ? fmtDate(device.last_backup_at) : 'Never'],
        ['Status', device.last_backup_status || 'N/A'],
        ['Created', fmtDate(device.created_at)],
      ];
      const dl = el('div', { style: 'display:grid; grid-template-columns: 120px 1fr; gap:8px 12px; font-size:0.8rem;' });
      for (const [k, v] of info) {
        dl.appendChild(el('span', { class: 'text-muted', style: 'font-weight:600' }, k));
        dl.appendChild(el('span', { class: 'font-mono' }, String(v || '—')));
      }
      body.appendChild(dl);

    } else if (tab === 'backups') {
      const versions = await api('GET', `/devices/${device.id}/config/versions`).catch(() => null);
      body.innerHTML = '';
      if (!versions || versions.length === 0) {
        body.appendChild(emptyState('No backups yet', 'Trigger a backup to capture the first config snapshot.'));
        return;
      }
      const timeline = el('div', { class: 'timeline' });
      for (const v of versions) {
        const item = el('div', { class: `timeline-item ${v.changed ? 'changed' : ''}` },
          el('div', { class: 'tl-date' }, fmtDate(v.date || v.timestamp)),
          el('div', { class: 'tl-title' }, v.message || (v.changed ? 'Configuration changed' : 'No change')),
          el('div', { class: 'tl-actions' },
            el('button', { class: 'btn btn-xs btn-secondary', onclick: () => viewConfig(device, v.sha) }, 'View'),
            el('button', { class: 'btn btn-xs btn-ghost', onclick: () => downloadConfig(device, v.sha) }, 'Download'),
            el('button', { class: 'btn btn-xs btn-ghost', onclick: () => rollbackDevice(device, v.sha) }, 'Rollback')
          )
        );
        timeline.appendChild(item);
      }
      body.appendChild(timeline);

    } else if (tab === 'diffs') {
      const changes = await api('GET', `/changes?device_id=${device.id}&limit=20`).catch(() => []);
      body.innerHTML = '';
      if (!changes || changes.length === 0) {
        body.appendChild(emptyState('No diffs', 'Diffs appear when configuration changes are detected.'));
        return;
      }
      for (const c of changes) {
        const card = el('div', { style: 'margin-bottom:12px' },
          el('div', { class: 'flex justify-between items-center', style: 'margin-bottom:6px' },
            el('span', { class: 'text-sm font-mono' }, fmtDate(c.detected_at || c.created_at)),
            el('button', { class: 'btn btn-xs btn-secondary', onclick: () => loadDiff(c, body) }, 'View Diff')
          )
        );
        body.appendChild(card);
      }

    } else if (tab === 'log') {
      const runs = await api('GET', `/devices/${device.id}/runs?limit=20`).catch(() => []);
      body.innerHTML = '';
      if (!runs || runs.length === 0) {
        body.appendChild(emptyState('No logs', 'Backup run logs will appear here.'));
        return;
      }
      const terminal = el('div', { class: 'terminal-view' });
      for (const r of runs) {
        const cls = r.status === 'success' ? 't-success' : r.status === 'failed' ? 't-error' : 't-info';
        terminal.appendChild(el('div', { class: cls },
          `[${fmtDate(r.started_at)}] ${r.status} — ${r.duration_ms || 0}ms`
        ));
        if (r.error) terminal.appendChild(el('div', { class: 't-error' }, `  Error: ${r.error}`));
      }
      body.appendChild(terminal);
    }
  } catch (err) {
    body.innerHTML = '';
    body.appendChild(el('p', { class: 'error' }, err.message));
  }
}

async function viewConfig(device, sha) {
  const body = $('#slideover-body');
  body.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const config = await api('GET', `/devices/${device.id}/config${sha ? '?sha=' + sha : ''}`);
    body.innerHTML = '';
    const toolbar = el('div', { class: 'flex justify-between items-center', style: 'margin-bottom:10px' },
      el('span', { class: 'text-sm text-muted' }, `Config @ ${sha ? sha.slice(0, 8) : 'latest'}`),
      el('div', { class: 'flex gap-8' },
        el('button', { class: 'btn btn-xs btn-secondary', onclick: () => downloadBlob(config, `${device.hostname || 'config'}.cfg`) }, 'Download'),
        el('button', { class: 'btn btn-xs btn-ghost', onclick: () => { navigator.clipboard.writeText(config); toast('Copied', 'ok'); } }, 'Copy')
      )
    );
    body.appendChild(toolbar);
    const view = el('div', { class: 'config-view' });
    const lines = String(config).split('\n');
    for (let i = 0; i < lines.length; i++) {
      view.appendChild(el('div', { class: 'config-line' },
        el('span', { class: 'config-lineno' }, String(i + 1)),
        el('span', { class: 'config-text' }, lines[i])
      ));
    }
    body.appendChild(view);
  } catch (err) { body.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
}

async function downloadConfig(device, sha) {
  try {
    const config = await api('GET', `/devices/${device.id}/config${sha ? '?sha=' + sha : ''}`);
    downloadBlob(config, `${device.hostname || 'config'}-${(sha || 'latest').slice(0, 8)}.cfg`);
    toast('Downloaded', 'ok');
  } catch (err) { toast(err.message, 'bad'); }
}

async function rollbackDevice(device, sha) {
  if (!confirm(`Rollback ${device.hostname || device.address} to version ${sha.slice(0, 8)}?`)) return;
  try {
    await api('POST', `/devices/${device.id}/rollback`, { target_sha: sha });
    toast('Rollback initiated', 'ok');
  } catch (err) { toast(err.message, 'bad'); }
}

async function loadDiff(change, container) {
  try {
    const diff = await api('GET', `/changes/${change.id}/diff`);
    container.innerHTML = '';
    container.appendChild(renderDiff(diff));
  } catch (err) { container.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
}

function renderDiff(diffText) {
  const wrap = el('div', { class: 'diff-container' });
  const header = el('div', { class: 'diff-header' },
    el('span', null, 'Unified Diff'),
    el('button', { class: 'btn btn-xs btn-ghost', style: 'color:#8b949e', onclick: () => { navigator.clipboard.writeText(diffText); toast('Copied', 'ok'); } }, 'Copy')
  );
  wrap.appendChild(header);
  const body = el('div', { class: 'diff-body' });
  const lines = String(diffText).split('\n');
  let lineNum = 0;
  for (const line of lines) {
    lineNum++;
    let cls = '';
    if (line.startsWith('+')) cls = 'add';
    else if (line.startsWith('-')) cls = 'del';
    else if (line.startsWith('@@')) cls = 'hunk';
    body.appendChild(el('div', { class: `diff-line ${cls}` },
      el('span', { class: 'diff-lineno' }, String(lineNum)),
      el('span', { class: 'diff-content' }, line)
    ));
  }
  wrap.appendChild(body);
  return wrap;
}

function downloadBlob(text, filename) {
  const blob = new Blob([text], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
  URL.revokeObjectURL(url);
}

// ===== EXPORT MODAL =====
function openExportModal(devices) {
  const overlay = el('div', { class: 'modal-overlay' });
  const modal = el('div', { class: 'modal' });
  modal.innerHTML = `
    <div class="modal-header"><h3>Export Configurations</h3><button class="slideover-close" id="exp-close">×</button></div>
    <div class="modal-body">
      <div class="form-group"><label class="form-label">Format</label>
        <select class="form-select" name="format">
          <option value="text">Plain Text (.cfg)</option>
          <option value="json">JSON</option>
          <option value="zip">ZIP Archive</option>
        </select>
      </div>
      <div class="form-row">
        <div class="form-group"><label class="form-label">From Date</label><input class="form-input" name="from" type="date"></div>
        <div class="form-group"><label class="form-label">To Date</label><input class="form-input" name="to" type="date"></div>
      </div>
      <p class="text-xs text-muted">${devices.length} device(s) selected. Latest config will be exported.</p>
    </div>
    <div class="modal-footer"><button class="btn btn-secondary" id="exp-cancel">Cancel</button><button class="btn btn-primary" id="exp-download">Download</button></div>
  `;
  overlay.appendChild(modal);
  document.body.appendChild(overlay);
  const close = () => overlay.remove();
  $('#exp-close', modal).onclick = close;
  $('#exp-cancel', modal).onclick = close;
  overlay.addEventListener('click', e => { if (e.target === overlay) close(); });

  $('#exp-download', modal).onclick = async () => {
    const format = $('select[name=format]', modal).value;
    const from = $('input[name=from]', modal).value;
    const to = $('input[name=to]', modal).value;
    const ids = devices.map(d => d.id);
    try {
      const body = { device_ids: ids, format };
      if (from) body.from_date = from;
      if (to) body.to_date = to;
      const resp = await fetch('/api/v1/export/configs', {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
      });
      if (!resp.ok) throw new Error('Export failed');
      const blob = await resp.blob();
      const ext = format === 'zip' ? 'zip' : format === 'json' ? 'json' : 'cfg';
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url; a.download = `netmantle-export.${ext}`;
      document.body.appendChild(a); a.click(); a.remove();
      URL.revokeObjectURL(url);
      close();
      toast('Export complete', 'ok');
    } catch (err) { toast(err.message, 'bad'); }
  };
}

// ===== ZONES =====
views.zones = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const pollers = await api('GET', '/pollers');
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'page-header' },
      el('div', null,
        el('h2', null, 'Zones'),
        el('p', { class: 'subtitle' }, 'Remote polling infrastructure')
      ),
      el('div', { class: 'page-actions' },
        el('button', { class: 'btn btn-primary', onclick: () => registerPoller() }, '+ Register Poller')
      )
    ));

    if (!pollers || pollers.length === 0) {
      root.appendChild(emptyState('No remote pollers', 'Register a remote polling core to manage devices behind NAT/firewalls.'));
      return;
    }

    const grid = el('div', { class: 'kpi-grid' });
    for (const p of pollers) {
      const alive = p.last_seen_at && (Date.now() - new Date(p.last_seen_at)) < 120000;
      grid.appendChild(el('div', { class: `kpi-card ${alive ? 'ok' : 'bad'}` },
        el('div', { class: 'flex items-center gap-8' },
          el('span', { class: `status-dot ${alive ? 'ok pulse' : 'bad'}` }),
          el('span', { class: 'kpi-label', style: 'margin:0' }, escHTML(p.name || p.id))
        ),
        el('div', { class: 'kpi-sub', style: 'margin-top:8px' }, `Last seen: ${p.last_seen_at ? timeAgo(p.last_seen_at) : 'never'}`),
        el('div', { class: 'kpi-sub' }, `Devices: ${p.device_count || 0}`)
      ));
    }
    root.appendChild(grid);
  } catch (err) {
    root.innerHTML = '';
    root.appendChild(emptyState('Failed to load zones', err.message));
  }

  function registerPoller() {
    const overlay = el('div', { class: 'modal-overlay' });
    const modal = el('div', { class: 'modal' });
    modal.innerHTML = `
      <div class="modal-header"><h3>Register Poller</h3><button class="slideover-close" id="rp-close">×</button></div>
      <div class="modal-body">
        <div class="form-group"><label class="form-label">Name</label><input class="form-input" name="name" placeholder="DC-East" required></div>
        <div class="form-group"><label class="form-label">Address</label><input class="form-input" name="address" placeholder="poller.east.internal:50051"></div>
      </div>
      <div class="modal-footer"><button class="btn btn-secondary" id="rp-cancel">Cancel</button><button class="btn btn-primary" id="rp-save">Register</button></div>
    `;
    overlay.appendChild(modal);
    document.body.appendChild(overlay);
    const close = () => overlay.remove();
    $('#rp-close', modal).onclick = close;
    $('#rp-cancel', modal).onclick = close;
    $('#rp-save', modal).onclick = async () => {
      const name = $('input[name=name]', modal).value.trim();
      const address = $('input[name=address]', modal).value.trim();
      if (!name) return;
      try {
        await api('POST', '/pollers', { name, address });
        close();
        toast('Poller registered', 'ok');
        views.zones(root);
      } catch (err) { toast(err.message, 'bad'); }
    };
  }
};

// ===== AUTOMATION (Mass Config Push) =====
views.automation = async (root) => {
  root.innerHTML = '';
  let step = 0; // 0=select, 1=script, 2=preflight, 3=execute

  root.appendChild(el('div', { class: 'page-header' },
    el('div', null,
      el('h2', null, 'Mass Config Push'),
      el('p', { class: 'subtitle' }, 'Deploy configuration scripts to multiple devices')
    )
  ));

  // Steps indicator
  const stepsEl = el('div', { class: 'wizard-steps' });
  const stepNames = ['Select Targets', 'Write Script', 'Preflight', 'Execute'];
  root.appendChild(stepsEl);

  const content = el('div');
  root.appendChild(content);

  // State
  let selectedDevices = [];
  let script = '';
  let verifyCmd = '';
  let rollbackScript = '';

  renderStep();

  function updateSteps() {
    stepsEl.innerHTML = '';
    for (let i = 0; i < stepNames.length; i++) {
      const cls = i < step ? 'done' : i === step ? 'active' : '';
      stepsEl.appendChild(el('div', { class: `wizard-step ${cls}` },
        el('span', { class: 'step-num' }, i < step ? '✓' : String(i + 1)),
        el('span', null, stepNames[i])
      ));
    }
  }

  function renderStep() {
    updateSteps();
    content.innerHTML = '';

    if (step === 0) renderTargetSelect();
    else if (step === 1) renderScriptEditor();
    else if (step === 2) renderPreflight();
    else if (step === 3) renderExecute();
  }

  function renderTargetSelect() {
    content.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    api('GET', '/devices').then(devices => {
      content.innerHTML = '';
      const card = el('div', { class: 'card' });
      card.appendChild(el('div', { class: 'card-header' }, 'Select target devices'));
      const body = el('div', { class: 'card-body' });

      const searchWrap = el('div', { style: 'margin-bottom:10px' });
      const searchIn = el('input', { type: 'search', class: 'form-input', placeholder: 'Filter devices…' });
      searchWrap.appendChild(searchIn);
      body.appendChild(searchWrap);

      const list = el('div', { style: 'max-height:300px; overflow-y:auto; border:1px solid var(--border-default); border-radius:var(--radius-sm)' });
      const selectedSet = new Set(selectedDevices.map(d => d.id));

      function renderList(filter = '') {
        list.innerHTML = '';
        const f = filter.toLowerCase();
        for (const d of (devices || [])) {
          if (f && !`${d.hostname} ${d.address} ${d.driver}`.toLowerCase().includes(f)) continue;
          const checked = selectedSet.has(d.id);
          const row = el('label', { style: 'display:flex; align-items:center; gap:8px; padding:6px 10px; border-bottom:1px solid var(--border-default); font-size:0.78rem; cursor:pointer' },
            el('input', { type: 'checkbox', checked: checked || undefined, onchange: (e) => {
              if (e.target.checked) selectedSet.add(d.id);
              else selectedSet.delete(d.id);
            }}),
            el('span', { class: 'font-mono' }, escHTML(d.hostname || d.address)),
            el('span', { class: 'text-muted' }, escHTML(d.address))
          );
          list.appendChild(row);
        }
      }
      renderList();
      searchIn.addEventListener('input', () => renderList(searchIn.value));
      body.appendChild(list);

      body.appendChild(el('div', { style: 'margin-top:12px; text-align:right' },
        el('button', { class: 'btn btn-primary', onclick: () => {
          selectedDevices = (devices || []).filter(d => selectedSet.has(d.id));
          if (selectedDevices.length === 0) { toast('Select at least one device', 'bad'); return; }
          step = 1; renderStep();
        }}, `Next → (${selectedSet.size} selected)`)
      ));

      card.appendChild(body);
      content.appendChild(card);
    }).catch(err => { content.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; });
  }

  function renderScriptEditor() {
    const card = el('div', { class: 'card' });
    card.appendChild(el('div', { class: 'card-header' }, 'Configuration Script'));
    const body = el('div', { class: 'card-body' });

    body.appendChild(el('div', { class: 'form-group' },
      el('label', { class: 'form-label' }, 'Commands (vendor CLI)'),
      el('textarea', { class: 'form-textarea', name: 'script', rows: '10', placeholder: '/ip address add address={{customer_ip}} interface=ether1\n# Use {{var}} for dynamic variables', style: 'font-family:var(--font-config)' })
    ));
    body.appendChild(el('div', { class: 'form-hint' }, 'Use {{variable}} syntax for dynamic values. Supported variables will be detected automatically.'));

    body.appendChild(el('div', { class: 'section-title' }, 'SAFE MODE (optional)'));
    body.appendChild(el('div', { class: 'form-group' },
      el('label', { class: 'form-label' }, 'Verify Command'),
      el('input', { class: 'form-input', name: 'verify', placeholder: 'ping 8.8.8.8 count=1' })
    ));
    body.appendChild(el('div', { class: 'form-group' },
      el('label', { class: 'form-label' }, 'Rollback Script (if verify fails)'),
      el('textarea', { class: 'form-textarea', name: 'rollback', rows: '4', style: 'font-family:var(--font-config)' })
    ));

    body.appendChild(el('div', { style: 'margin-top:16px; display:flex; justify-content:space-between' },
      el('button', { class: 'btn btn-secondary', onclick: () => { step = 0; renderStep(); } }, '← Back'),
      el('button', { class: 'btn btn-primary', onclick: () => {
        script = $('textarea[name=script]', body).value.trim();
        verifyCmd = $('input[name=verify]', body).value.trim();
        rollbackScript = $('textarea[name=rollback]', body).value.trim();
        if (!script) { toast('Script is required', 'bad'); return; }
        step = 2; renderStep();
      }}, 'Run Preflight →')
    ));

    card.appendChild(body);
    content.appendChild(card);
  }

  async function renderPreflight() {
    const card = el('div', { class: 'card' });
    card.appendChild(el('div', { class: 'card-header' }, 'Preflight Connectivity Check'));
    const body = el('div', { class: 'card-body' });
    body.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    card.appendChild(body);
    content.appendChild(card);

    try {
      const results = await api('POST', '/push/jobs/' + 0 + '/preflight', { device_ids: selectedDevices.map(d => d.id) }).catch(() => null);
      body.innerHTML = '';

      const table = el('table', { class: 'data-table' });
      table.innerHTML = '<thead><tr><th>Device</th><th>Address</th><th>Status</th></tr></thead>';
      const tb = el('tbody');
      let allOk = true;

      for (const d of selectedDevices) {
        const r = results ? results.find(x => x.device_id === d.id) : null;
        const ok = r ? r.reachable : true;
        if (!ok) allOk = false;
        tb.appendChild(el('tr', null,
          el('td', { class: 'font-mono' }, escHTML(d.hostname || d.address)),
          el('td', { class: 'text-muted' }, escHTML(d.address)),
          el('td', null, el('span', { class: `badge ${ok ? 'badge-ok' : 'badge-bad'}` }, ok ? 'Reachable' : 'Unreachable'))
        ));
      }
      table.appendChild(tb);
      body.appendChild(table);

      body.appendChild(el('div', { style: 'margin-top:16px; display:flex; justify-content:space-between' },
        el('button', { class: 'btn btn-secondary', onclick: () => { step = 1; renderStep(); } }, '← Back'),
        el('button', { class: 'btn btn-primary', onclick: () => { step = 3; renderStep(); } }, allOk ? 'Execute →' : 'Execute Anyway →')
      ));
    } catch (err) {
      body.innerHTML = `<p class="text-muted">Preflight skipped (${escHTML(err.message)}). Proceeding to execution.</p>`;
      body.appendChild(el('div', { style: 'margin-top:16px; display:flex; justify-content:space-between' },
        el('button', { class: 'btn btn-secondary', onclick: () => { step = 1; renderStep(); } }, '← Back'),
        el('button', { class: 'btn btn-primary', onclick: () => { step = 3; renderStep(); } }, 'Execute →')
      ));
    }
  }

  function renderExecute() {
    const card = el('div', { class: 'card' });
    card.appendChild(el('div', { class: 'card-header' },
      el('span', null, 'Execution Console'),
      el('span', { class: 'badge badge-info' }, `${selectedDevices.length} targets`)
    ));
    const body = el('div', { class: 'card-body' });

    // Progress
    const progressWrap = el('div', { style: 'margin-bottom:16px' });
    const progressLabel = el('div', { class: 'text-xs text-muted', style: 'margin-bottom:4px' }, 'Starting…');
    const progressBar = el('div', { class: 'progress-bar' }, el('div', { class: 'progress-fill', style: 'width:0%' }));
    progressWrap.append(progressLabel, progressBar);
    body.appendChild(progressWrap);

    // Terminal output
    const terminal = el('div', { class: 'terminal-view', style: 'max-height:350px' });
    body.appendChild(terminal);

    card.appendChild(body);
    content.appendChild(card);

    // Execute
    executePush(terminal, progressLabel, progressBar);
  }

  async function executePush(terminal, progressLabel, progressBar) {
    const total = selectedDevices.length;
    let done = 0, failed = 0;

    function log(msg, cls = '') {
      terminal.appendChild(el('div', { class: cls }, msg));
      terminal.scrollTop = terminal.scrollHeight;
    }

    try {
      const job = await api('POST', '/push/jobs', {
        name: 'Mass push ' + new Date().toISOString().slice(0, 16),
        template: script,
        device_ids: selectedDevices.map(d => d.id),
        verify_command: verifyCmd || undefined,
        rollback_template: rollbackScript || undefined
      });

      log(`[INFO] Job created: #${job.id}`, 't-info');
      log(`[INFO] Executing on ${total} device(s)…`, 't-info');

      // Try running the job
      try {
        await api('POST', `/push/jobs/${job.id}/run`);
        log('[SUCCESS] Job submitted for execution.', 't-success');
        progressLabel.textContent = 'Job submitted — check device logs for results.';
        progressBar.querySelector('.progress-fill').style.width = '100%';
        progressBar.querySelector('.progress-fill').classList.add('ok');
      } catch (err) {
        log(`[WARN] ${err.message}`, 't-warn');
        log('[INFO] Job may require approval. Check the Approvals page.', 't-info');
        progressLabel.textContent = 'Awaiting approval';
        progressBar.querySelector('.progress-fill').style.width = '100%';
      }
    } catch (err) {
      log(`[ERROR] ${err.message}`, 't-error');
      progressLabel.textContent = 'Failed';
    }
  }
};

// ===== COMPLIANCE =====
views.compliance = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const [rules, findings, rulepacks] = await Promise.all([
      api('GET', '/compliance/rules').catch(() => []),
      api('GET', '/compliance/findings').catch(() => []),
      api('GET', '/compliance/rulepacks').catch(() => [])
    ]);
    root.innerHTML = '';

    root.appendChild(el('div', { class: 'page-header' },
      el('div', null,
        el('h2', null, 'Compliance'),
        el('p', { class: 'subtitle' }, 'Configuration policy enforcement')
      ),
      el('div', { class: 'page-actions' },
        el('button', { class: 'btn btn-primary', onclick: () => addRule() }, '+ Add Rule')
      )
    ));

    // Summary KPIs
    const pass = (findings || []).filter(f => f.status === 'pass').length;
    const fail = (findings || []).filter(f => f.status === 'fail' || f.status === 'critical').length;
    const warn = (findings || []).filter(f => f.status === 'warning').length;
    const kpis = el('div', { class: 'kpi-grid' });
    kpis.appendChild(kpiCard('Total Rules', (rules || []).length, 'Active policies', 'info'));
    kpis.appendChild(kpiCard('Passing', pass, 'Compliant', 'ok'));
    kpis.appendChild(kpiCard('Warnings', warn, 'Non-critical', 'warn'));
    kpis.appendChild(kpiCard('Violations', fail, 'Requires action', 'bad'));
    root.appendChild(kpis);

    // Tabs
    const tabsEl = el('div', { class: 'tabs' });
    const rulesBtn = el('button', { class: 'tab-btn active', onclick: () => showTab('rules') }, 'Rules');
    const findingsBtn = el('button', { class: 'tab-btn', onclick: () => showTab('findings') }, 'Findings');
    const packsBtn = el('button', { class: 'tab-btn', onclick: () => showTab('packs') }, 'Rule Packs');
    tabsEl.append(rulesBtn, findingsBtn, packsBtn);
    root.appendChild(tabsEl);

    const tabContent = el('div');
    root.appendChild(tabContent);

    function showTab(t) {
      [rulesBtn, findingsBtn, packsBtn].forEach(b => b.classList.remove('active'));
      if (t === 'rules') { rulesBtn.classList.add('active'); renderRules(); }
      else if (t === 'findings') { findingsBtn.classList.add('active'); renderFindings(); }
      else { packsBtn.classList.add('active'); renderPacks(); }
    }

    function renderRules() {
      tabContent.innerHTML = '';
      if (!rules || rules.length === 0) { tabContent.appendChild(emptyState('No rules', 'Create compliance rules to audit your configs.')); return; }
      const wrap = el('div', { class: 'data-table-wrap' });
      const table = el('table', { class: 'data-table' });
      table.innerHTML = '<thead><tr><th>Name</th><th>Pattern</th><th>Severity</th><th>Actions</th></tr></thead>';
      const tbody = el('tbody');
      for (const r of rules) {
        tbody.appendChild(el('tr', null,
          el('td', { style: 'font-weight:500' }, escHTML(r.name)),
          el('td', { class: 'font-mono text-sm' }, escHTML(r.pattern || r.regex || '—')),
          el('td', null, el('span', { class: `badge badge-${r.severity === 'critical' ? 'bad' : r.severity === 'warning' ? 'warn' : 'info'}` }, escHTML(r.severity || 'info'))),
          el('td', null, el('button', { class: 'btn btn-xs btn-danger', onclick: () => deleteRule(r.id) }, 'Delete'))
        ));
      }
      table.appendChild(tbody);
      wrap.appendChild(table);
      tabContent.appendChild(wrap);
    }

    function renderFindings() {
      tabContent.innerHTML = '';
      if (!findings || findings.length === 0) { tabContent.appendChild(emptyState('No findings', 'Run compliance checks to see results.')); return; }
      for (const f of findings.slice(0, 50)) {
        const cls = f.status === 'fail' || f.status === 'critical' ? 'critical' : f.status === 'warning' ? 'warning' : 'pass';
        tabContent.appendChild(el('div', { class: `finding-card ${cls}` },
          el('div', { class: 'flex justify-between items-center' },
            el('strong', { class: 'text-sm' }, escHTML(f.rule_name || f.rule_id)),
            el('span', { class: `badge badge-${cls === 'critical' ? 'bad' : cls === 'warning' ? 'warn' : 'ok'}` }, escHTML(f.status))
          ),
          el('div', { class: 'text-xs text-muted', style: 'margin-top:4px' }, escHTML(f.device_name || `Device #${f.device_id}`)),
          f.detail ? el('div', { class: 'text-xs font-mono', style: 'margin-top:4px; color:var(--text-muted)' }, escHTML(f.detail)) : null
        ));
      }
    }

    function renderPacks() {
      tabContent.innerHTML = '';
      if (!rulepacks || rulepacks.length === 0) { tabContent.appendChild(emptyState('No rule packs', 'Rule packs provide pre-built compliance checks.')); return; }
      const grid = el('div', { class: 'grid-3' });
      for (const p of rulepacks) {
        grid.appendChild(el('div', { class: 'card' },
          el('div', { class: 'card-body' },
            el('div', { style: 'font-weight:600; margin-bottom:4px' }, escHTML(p.name)),
            el('div', { class: 'text-xs text-muted' }, escHTML(p.description || `${p.rule_count || 0} rules`)),
            el('button', { class: 'btn btn-xs btn-secondary', style: 'margin-top:8px', onclick: () => applyPack(p.name) }, 'Apply')
          )
        ));
      }
      tabContent.appendChild(grid);
    }

    showTab('rules');

    async function addRule() {
      const name = prompt('Rule name:');
      if (!name) return;
      const pattern = prompt('Regex pattern to check:');
      if (!pattern) return;
      try {
        await api('POST', '/compliance/rules', { name, pattern, severity: 'warning', must_match: true });
        toast('Rule created', 'ok');
        views.compliance(root);
      } catch (err) { toast(err.message, 'bad'); }
    }

    async function deleteRule(id) {
      if (!confirm('Delete this rule?')) return;
      try { await api('DELETE', `/compliance/rules/${id}`); toast('Deleted', 'ok'); views.compliance(root); } catch (err) { toast(err.message, 'bad'); }
    }

    async function applyPack(name) {
      try { await api('POST', `/compliance/rulepacks/${name}/apply`); toast('Rule pack applied', 'ok'); } catch (err) { toast(err.message, 'bad'); }
    }

  } catch (err) {
    root.innerHTML = '';
    root.appendChild(emptyState('Failed to load compliance', err.message));
  }
};

// ===== CONFIG SEARCH =====
views.search = async (root) => {
  root.innerHTML = '';
  root.appendChild(el('div', { class: 'page-header' },
    el('h2', null, 'Config Search'),
    el('p', { class: 'subtitle' }, 'Search across all device configurations')
  ));

  const searchBar = el('div', { class: 'filter-bar' });
  const input = el('input', { type: 'search', placeholder: 'Search configs (e.g. "VLAN 100", "ip route 0.0.0.0")…', value: window._searchQuery || '' });
  const btn = el('button', { class: 'btn btn-primary' }, 'Search');
  searchBar.append(input, btn);
  root.appendChild(searchBar);
  window._searchQuery = '';

  const results = el('div');
  root.appendChild(results);

  async function doSearch() {
    const q = input.value.trim();
    if (!q) return;
    results.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    try {
      const data = await api('GET', '/search?q=' + encodeURIComponent(q));
      results.innerHTML = '';
      if (!data || data.length === 0) {
        results.appendChild(emptyState('No results', `No configurations contain "${q}".`));
        return;
      }
      results.appendChild(el('p', { class: 'text-xs text-muted', style: 'margin-bottom:12px' }, `${data.length} result(s) found`));
      for (const r of data) {
        const card = el('div', { class: 'search-result' });
        const header = el('div', { class: 'search-result-header' },
          el('div', { class: 'flex items-center gap-8' },
            el('span', { class: 'badge badge-neutral' }, escHTML(r.driver || '')),
            el('strong', null, escHTML(r.device_name || r.hostname || `Device #${r.device_id}`)),
            el('span', { class: 'text-muted' }, escHTML(r.address || ''))
          ),
          el('button', { class: 'btn btn-xs btn-secondary', onclick: (e) => {
            e.stopPropagation();
            api('GET', '/devices/' + r.device_id).then(d => openSlideover(d)).catch(() => {});
          }}, 'View Device')
        );
        const body = el('div', { class: 'search-result-body' });
        const snippet = el('div', { class: 'search-snippet' });
        const lines = (r.snippet || r.context || '').split('\n');
        for (let i = 0; i < lines.length; i++) {
          const lineNo = (r.line_number || 1) + i;
          const highlighted = lines[i].replace(new RegExp(escRegex(q), 'gi'), m => `<mark>${escHTML(m)}</mark>`);
          snippet.appendChild(el('div', { class: 'search-line' },
            el('span', { class: 'search-lineno' }, String(lineNo)),
            el('span', { class: 'search-content', html: highlighted })
          ));
        }
        body.appendChild(snippet);
        header.addEventListener('click', () => body.hidden = !body.hidden);
        card.append(header, body);
        results.appendChild(card);
      }
    } catch (err) { results.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
  }

  btn.addEventListener('click', doSearch);
  input.addEventListener('keydown', e => { if (e.key === 'Enter') doSearch(); });
  if (input.value) doSearch();
};

function escRegex(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }

// ===== APPROVALS =====
views.approvals = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const crs = await api('GET', '/change-requests');
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'page-header' },
      el('h2', null, 'Approvals'),
      el('p', { class: 'subtitle' }, 'Change request review queue')
    ));

    if (!crs || crs.length === 0) {
      root.appendChild(emptyState('No change requests', 'Push jobs requiring approval will appear here.'));
      return;
    }

    const wrap = el('div', { class: 'data-table-wrap' });
    const table = el('table', { class: 'data-table' });
    table.innerHTML = '<thead><tr><th>#</th><th>Title</th><th>Author</th><th>Status</th><th>Created</th><th>Actions</th></tr></thead>';
    const tbody = el('tbody');
    for (const cr of crs) {
      const statusCls = cr.status === 'approved' ? 'badge-ok' : cr.status === 'rejected' ? 'badge-bad' : cr.status === 'submitted' ? 'badge-warn' : 'badge-neutral';
      tbody.appendChild(el('tr', null,
        el('td', null, `#${cr.id}`),
        el('td', { style: 'font-weight:500' }, escHTML(cr.title || cr.description || 'Untitled')),
        el('td', { class: 'text-muted' }, escHTML(cr.author || '—')),
        el('td', null, el('span', { class: `badge ${statusCls}` }, escHTML(cr.status))),
        el('td', { class: 'text-muted text-sm' }, timeAgo(cr.created_at)),
        el('td', null, cr.status === 'submitted' ? el('div', { class: 'flex gap-8' },
          el('button', { class: 'btn btn-xs btn-primary', onclick: () => approveReject(cr.id, 'approve') }, 'Approve'),
          el('button', { class: 'btn btn-xs btn-danger', onclick: () => approveReject(cr.id, 'reject') }, 'Reject')
        ) : el('span', { class: 'text-muted text-xs' }, '—'))
      ));
    }
    table.appendChild(tbody);
    wrap.appendChild(table);
    root.appendChild(wrap);
  } catch (err) {
    root.innerHTML = '';
    root.appendChild(emptyState('Failed to load approvals', err.message));
  }

  async function approveReject(id, action) {
    try {
      await api('POST', `/change-requests/${id}/${action}`);
      toast(`Change request ${action}d`, 'ok');
      refreshApprovalsBadge();
      views.approvals(root);
    } catch (err) { toast(err.message, 'bad'); }
  }
};

// ===== AUDIT LOG =====
views.audit = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const logs = await api('GET', '/audit');
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'page-header' },
      el('h2', null, 'Audit Log'),
      el('p', { class: 'subtitle' }, 'System-wide mutation log')
    ));

    if (!logs || logs.length === 0) {
      root.appendChild(emptyState('No audit entries', 'Actions will be recorded here.'));
      return;
    }

    const terminal = el('div', { class: 'terminal-view', style: 'max-height:calc(100vh - 180px)' });
    for (const entry of logs) {
      const cls = entry.action && entry.action.includes('delete') ? 't-error' : entry.action && entry.action.includes('create') ? 't-success' : 't-info';
      terminal.appendChild(el('div', { class: cls },
        `[${fmtDate(entry.timestamp || entry.created_at)}] ${entry.actor || entry.username || '?'} → ${entry.action || '?'}${entry.resource ? ' on ' + entry.resource : ''}`
      ));
    }
    root.appendChild(terminal);
  } catch (err) {
    root.innerHTML = '';
    root.appendChild(emptyState('Failed to load audit log', err.message));
  }
};

// ===== USERS =====
views.users = async (root) => {
  root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
  try {
    const users = await api('GET', '/auth/me').then(u => u.role === 'admin' ? api('GET', '/tenants').catch(() => null) : null);
    // Fallback — show current user
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'page-header' },
      el('h2', null, 'Users & Access'),
      el('p', { class: 'subtitle' }, 'User management and RBAC')
    ));

    // Show current user info
    const card = el('div', { class: 'card' });
    card.appendChild(el('div', { class: 'card-header' }, 'Current Session'));
    card.appendChild(el('div', { class: 'card-body' },
      el('div', { style: 'display:grid; grid-template-columns:100px 1fr; gap:6px 12px; font-size:0.82rem' },
        el('span', { class: 'text-muted' }, 'Username'), el('span', { class: 'font-mono' }, me ? me.username : '—'),
        el('span', { class: 'text-muted' }, 'Role'), el('span', null, el('span', { class: `badge ${me && me.role === 'admin' ? 'badge-bad' : 'badge-info'}` }, me ? me.role : '—')),
        el('span', { class: 'text-muted' }, 'Tenant'), el('span', null, me ? String(me.tenant_id) : '—'),
        el('span', { class: 'text-muted' }, 'MFA'), el('span', null, me && me.mfa_enabled ? 'Enabled ✓' : 'Disabled')
      )
    ));
    root.appendChild(card);

    // MFA enrollment
    if (me && !me.mfa_enabled) {
      root.appendChild(el('div', { style: 'margin-top:16px' },
        el('button', { class: 'btn btn-secondary', onclick: enrollMFA }, 'Enable MFA (TOTP)')
      ));
    }
  } catch (err) {
    root.innerHTML = '';
    root.appendChild(emptyState('Failed to load users', err.message));
  }

  async function enrollMFA() {
    try {
      const res = await api('POST', '/auth/mfa/enroll');
      toast('Scan the QR code in your authenticator app. Secret: ' + (res.secret || ''), 'info');
    } catch (err) { toast(err.message, 'bad'); }
  }
};

// ===== SETTINGS =====
views.settings = async (root) => {
  root.innerHTML = '';
  root.appendChild(el('div', { class: 'page-header' },
    el('h2', null, 'Settings'),
    el('p', { class: 'subtitle' }, 'System configuration')
  ));

  const layout = el('div', { class: 'vtab-layout card' });
  const nav = el('div', { class: 'vtab-nav' });
  const content = el('div', { class: 'vtab-content' });
  layout.append(nav, content);
  root.appendChild(layout);

  const tabs = [
    { id: 'general', label: 'General' },
    { id: 'credentials', label: 'Credentials' },
    { id: 'notifications', label: 'Notifications' },
    { id: 'schedules', label: 'Scheduling' },
    { id: 'api-tokens', label: 'API Tokens' },
    { id: 'security', label: 'Security' },
  ];

  let active = 'general';

  function renderNav() {
    nav.innerHTML = '';
    for (const t of tabs) {
      nav.appendChild(el('button', {
        class: `vtab-item ${t.id === active ? 'active' : ''}`,
        onclick: () => { active = t.id; renderNav(); renderContent(); }
      }, t.label));
    }
  }

  function renderContent() {
    content.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    if (active === 'general') renderGeneral();
    else if (active === 'credentials') renderCredentials();
    else if (active === 'notifications') renderNotifications();
    else if (active === 'schedules') renderSchedules();
    else if (active === 'api-tokens') renderTokens();
    else if (active === 'security') renderSecurity();
  }

  renderNav();
  renderContent();

  // --- General ---
  function renderGeneral() {
    content.innerHTML = '';
    content.appendChild(el('div', { class: 'section-title' }, 'BACKUP SETTINGS'));
    content.appendChild(el('div', { class: 'form-group' },
      el('label', { class: 'form-label' }, 'Worker Concurrency'),
      el('input', { class: 'form-input', type: 'number', value: '4', style: 'max-width:120px' }),
      el('p', { class: 'form-hint' }, 'Number of simultaneous backup workers.')
    ));
    content.appendChild(el('div', { class: 'form-group' },
      el('label', { class: 'form-label' }, 'Timeout (seconds)'),
      el('input', { class: 'form-input', type: 'number', value: '60', style: 'max-width:120px' }),
      el('p', { class: 'form-hint' }, 'Per-device backup timeout.')
    ));
    content.appendChild(el('div', { class: 'section-title' }, 'SYSTEM'));
    content.appendChild(el('p', { class: 'text-xs text-muted' }, 'Server version and config are managed via config.yaml and environment variables. Restart required for changes.'));
  }

  // --- Credentials ---
  async function renderCredentials() {
    content.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    try {
      const creds = await api('GET', '/credentials');
      content.innerHTML = '';
      content.appendChild(el('div', { class: 'flex justify-between items-center', style: 'margin-bottom:12px' },
        el('div', { class: 'section-title', style: 'margin:0; border:0; padding:0' }, 'CREDENTIAL VAULT'),
        el('button', { class: 'btn btn-sm btn-primary', onclick: addCred }, '+ Add')
      ));

      if (!creds || creds.length === 0) {
        content.appendChild(emptyState('No credentials', 'Add SSH/Telnet credentials for device access.'));
        return;
      }
      const wrap = el('div', { class: 'data-table-wrap' });
      const table = el('table', { class: 'data-table' });
      table.innerHTML = '<thead><tr><th>Label</th><th>Username</th><th>Type</th><th>Actions</th></tr></thead>';
      const tbody = el('tbody');
      for (const c of creds) {
        tbody.appendChild(el('tr', null,
          el('td', { style: 'font-weight:500' }, escHTML(c.label || '—')),
          el('td', { class: 'font-mono' }, escHTML(c.username)),
          el('td', null, el('span', { class: 'badge badge-neutral' }, escHTML(c.auth_type || 'password'))),
          el('td', null, el('button', { class: 'btn btn-xs btn-danger', onclick: () => delCred(c.id) }, 'Delete'))
        ));
      }
      table.appendChild(tbody);
      wrap.appendChild(table);
      content.appendChild(wrap);
    } catch (err) { content.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
  }

  function addCred() {
    const label = prompt('Label (e.g. "Core routers"):');
    if (!label) return;
    const username = prompt('Username:');
    if (!username) return;
    const password = prompt('Password:');
    api('POST', '/credentials', { label, username, password }).then(() => {
      toast('Credential created', 'ok');
      renderCredentials();
    }).catch(err => toast(err.message, 'bad'));
  }

  async function delCred(id) {
    if (!confirm('Delete this credential?')) return;
    try { await api('DELETE', `/credentials/${id}`); toast('Deleted', 'ok'); renderCredentials(); } catch (err) { toast(err.message, 'bad'); }
  }

  // --- Notifications ---
  async function renderNotifications() {
    content.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    try {
      const [channels, rules] = await Promise.all([
        api('GET', '/notifications/channels').catch(() => []),
        api('GET', '/notifications/rules').catch(() => [])
      ]);
      content.innerHTML = '';
      content.appendChild(el('div', { class: 'flex justify-between items-center', style: 'margin-bottom:12px' },
        el('div', { class: 'section-title', style: 'margin:0; border:0; padding:0' }, 'NOTIFICATION CHANNELS'),
        el('button', { class: 'btn btn-sm btn-primary', onclick: addChannel }, '+ Add Channel')
      ));

      if (!channels || channels.length === 0) {
        content.appendChild(emptyState('No channels', 'Add Webhook, Slack or email channels.'));
      } else {
        const wrap = el('div', { class: 'data-table-wrap' });
        const table = el('table', { class: 'data-table' });
        table.innerHTML = '<thead><tr><th>Name</th><th>Type</th><th>Actions</th></tr></thead>';
        const tbody = el('tbody');
        for (const ch of channels) {
          tbody.appendChild(el('tr', null,
            el('td', { style: 'font-weight:500' }, escHTML(ch.name || '—')),
            el('td', null, el('span', { class: 'badge badge-neutral' }, escHTML(ch.type))),
            el('td', null, el('button', { class: 'btn btn-xs btn-danger', onclick: () => delChannel(ch.id) }, 'Delete'))
          ));
        }
        table.appendChild(tbody);
        wrap.appendChild(table);
        content.appendChild(wrap);
      }

      content.appendChild(el('div', { class: 'section-title', style: 'margin-top:24px' }, 'NOTIFICATION RULES'));
      if (!rules || rules.length === 0) {
        content.appendChild(el('p', { class: 'text-xs text-muted' }, 'No rules configured.'));
      } else {
        for (const r of rules) {
          content.appendChild(el('div', { class: 'card', style: 'margin-bottom:8px; padding:10px 14px; font-size:0.8rem' },
            el('span', { style: 'font-weight:500' }, escHTML(r.event_type || 'all')),
            el('span', { class: 'text-muted' }, ` → Channel #${r.channel_id}`)
          ));
        }
      }
    } catch (err) { content.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
  }

  function addChannel() {
    const name = prompt('Channel name:');
    if (!name) return;
    const type = prompt('Type (webhook / slack / email):') || 'webhook';
    const url = prompt('URL / endpoint:');
    if (!url) return;
    api('POST', '/notifications/channels', { name, type, url }).then(() => {
      toast('Channel created', 'ok');
      renderNotifications();
    }).catch(err => toast(err.message, 'bad'));
  }

  async function delChannel(id) {
    if (!confirm('Delete this channel?')) return;
    try { await api('DELETE', `/notifications/channels/${id}`); toast('Deleted', 'ok'); renderNotifications(); } catch (err) { toast(err.message, 'bad'); }
  }

  // --- Schedules ---
  async function renderSchedules() {
    content.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    try {
      const schedules = await api('GET', '/schedules').catch(() => []);
      content.innerHTML = '';
      content.appendChild(el('div', { class: 'flex justify-between items-center', style: 'margin-bottom:12px' },
        el('div', { class: 'section-title', style: 'margin:0; border:0; padding:0' }, 'BACKUP SCHEDULES'),
        el('button', { class: 'btn btn-sm btn-primary', onclick: addSchedule }, '+ Add Schedule')
      ));

      if (!schedules || schedules.length === 0) {
        content.appendChild(emptyState('No schedules', 'Create automated backup schedules.'));
        return;
      }
      const wrap = el('div', { class: 'data-table-wrap' });
      const table = el('table', { class: 'data-table' });
      table.innerHTML = '<thead><tr><th>Name</th><th>Cron</th><th>Status</th><th>Actions</th></tr></thead>';
      const tbody = el('tbody');
      for (const s of schedules) {
        tbody.appendChild(el('tr', null,
          el('td', { style: 'font-weight:500' }, escHTML(s.name || '—')),
          el('td', { class: 'font-mono text-sm' }, escHTML(s.cron || s.schedule || '—')),
          el('td', null, el('span', { class: `badge ${s.enabled !== false ? 'badge-ok' : 'badge-neutral'}` }, s.enabled !== false ? 'Active' : 'Disabled')),
          el('td', null, el('button', { class: 'btn btn-xs btn-danger', onclick: () => delSchedule(s.id) }, 'Delete'))
        ));
      }
      table.appendChild(tbody);
      wrap.appendChild(table);
      content.appendChild(wrap);
    } catch (err) { content.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
  }

  function addSchedule() {
    const name = prompt('Schedule name:');
    if (!name) return;
    const cron = prompt('Cron expression (e.g. "0 2 * * *" for daily at 2AM):');
    if (!cron) return;
    api('POST', '/schedules', { name, cron, enabled: true }).then(() => {
      toast('Schedule created', 'ok');
      renderSchedules();
    }).catch(err => toast(err.message, 'bad'));
  }

  async function delSchedule(id) {
    if (!confirm('Delete?')) return;
    try { await api('DELETE', `/schedules/${id}`); toast('Deleted', 'ok'); renderSchedules(); } catch (err) { toast(err.message, 'bad'); }
  }

  // --- API Tokens ---
  async function renderTokens() {
    content.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    try {
      const tokens = await api('GET', '/api-tokens').catch(() => []);
      content.innerHTML = '';
      content.appendChild(el('div', { class: 'flex justify-between items-center', style: 'margin-bottom:12px' },
        el('div', { class: 'section-title', style: 'margin:0; border:0; padding:0' }, 'API TOKENS'),
        el('button', { class: 'btn btn-sm btn-primary', onclick: createToken }, '+ Generate Token')
      ));

      if (!tokens || tokens.length === 0) {
        content.appendChild(emptyState('No API tokens', 'Generate tokens for machine-to-machine access.'));
        return;
      }
      const wrap = el('div', { class: 'data-table-wrap' });
      const table = el('table', { class: 'data-table' });
      table.innerHTML = '<thead><tr><th>Name</th><th>Scope</th><th>Created</th><th>Actions</th></tr></thead>';
      const tbody = el('tbody');
      for (const t of tokens) {
        tbody.appendChild(el('tr', null,
          el('td', { style: 'font-weight:500' }, escHTML(t.name || t.label || '—')),
          el('td', { class: 'font-mono text-sm' }, escHTML(t.scope || 'read')),
          el('td', { class: 'text-muted text-sm' }, timeAgo(t.created_at)),
          el('td', null, el('button', { class: 'btn btn-xs btn-danger', onclick: () => revokeToken(t.id) }, 'Revoke'))
        ));
      }
      table.appendChild(tbody);
      wrap.appendChild(table);
      content.appendChild(wrap);
    } catch (err) { content.innerHTML = `<p class="error">${escHTML(err.message)}</p>`; }
  }

  async function createToken() {
    const name = prompt('Token name:');
    if (!name) return;
    try {
      const res = await api('POST', '/api-tokens', { name, scope: 'read' });
      if (res && res.token) {
        toast('Token created. Copy it now — it won\'t be shown again.', 'info');
        alert('Your token:\n\n' + res.token);
      } else {
        toast('Token created', 'ok');
      }
      renderTokens();
    } catch (err) { toast(err.message, 'bad'); }
  }

  async function revokeToken(id) {
    if (!confirm('Revoke this token?')) return;
    try { await api('DELETE', `/api-tokens/${id}`); toast('Revoked', 'ok'); renderTokens(); } catch (err) { toast(err.message, 'bad'); }
  }

  // --- Security ---
  function renderSecurity() {
    content.innerHTML = '';
    content.appendChild(el('div', { class: 'section-title' }, 'AUTHENTICATION'));
    content.appendChild(el('div', { class: 'card', style: 'padding:14px' },
      el('div', { class: 'flex justify-between items-center' },
        el('div', null,
          el('div', { style: 'font-weight:500; font-size:0.85rem' }, 'Multi-Factor Authentication (TOTP)'),
          el('p', { class: 'form-hint', style: 'margin-top:4px' }, 'Enforce TOTP for all users via individual enrollment on the Users page.')
        ),
        el('span', { class: `badge ${me && me.mfa_enabled ? 'badge-ok' : 'badge-neutral'}` }, me && me.mfa_enabled ? 'Enabled' : 'Not enforced')
      )
    ));

    content.appendChild(el('div', { class: 'section-title', style: 'margin-top:24px' }, 'SESSION'));
    content.appendChild(el('p', { class: 'text-xs text-muted' }, 'Session TTL and cookie settings are configured via server config.yaml (security.session_ttl). Current session is valid.'));

    content.appendChild(el('div', { class: 'section-title', style: 'margin-top:24px' }, 'ENCRYPTION'));
    content.appendChild(el('p', { class: 'text-xs text-muted' }, 'Credentials are envelope-encrypted with AES-256-GCM. The master passphrase is derived from the server config and never stored in the database.'));
  }
};
