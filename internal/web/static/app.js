// NetMantle minimal SPA — vanilla JS to keep the binary node-toolchain free.
const $ = (sel) => document.querySelector(sel);
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

let currentDeviceId = null;

async function refreshSession() {
  try {
    const me = await api('GET', '/auth/me');
    $('#nav').hidden = false;
    $('#login-view').hidden = true;
    $('#app-view').hidden = false;
    $('#who').textContent = `${me.username} (${me.role})`;
    await loadAux();
    await loadDevices();
  } catch (_) {
    $('#nav').hidden = true;
    $('#login-view').hidden = false;
    $('#app-view').hidden = true;
  }
}

async function loadAux() {
  const drivers = await api('GET', '/drivers');
  const sel = $('#driver-select');
  sel.innerHTML = '';
  for (const d of drivers) {
    const o = document.createElement('option');
    o.value = d; o.textContent = d;
    sel.appendChild(o);
  }
  const creds = await api('GET', '/credentials');
  const csel = $('#cred-select');
  csel.innerHTML = '<option value="">— none —</option>';
  for (const c of creds) {
    const o = document.createElement('option');
    o.value = c.id; o.textContent = `${c.name} (${c.username})`;
    csel.appendChild(o);
  }
}

async function loadDevices() {
  const list = await api('GET', '/devices');
  const ul = $('#devices');
  ul.innerHTML = '';
  if (!list.length) {
    const li = document.createElement('li');
    li.className = 'muted';
    li.textContent = 'No devices yet — add one below.';
    ul.appendChild(li);
    return;
  }
  for (const d of list) {
    const li = document.createElement('li');
    li.textContent = `${d.hostname} (${d.driver})`;
    li.dataset.id = d.id;
    if (d.id === currentDeviceId) li.classList.add('active');
    li.onclick = () => showDevice(d.id);
    ul.appendChild(li);
  }
}

async function showDevice(id) {
  currentDeviceId = id;
  for (const li of document.querySelectorAll('#devices li')) {
    li.classList.toggle('active', Number(li.dataset.id) === id);
  }
  const dev = await api('GET', `/devices/${id}`);
  const detail = $('#device-detail');
  detail.innerHTML = '';
  const h = document.createElement('h2');
  h.textContent = dev.hostname;
  detail.appendChild(h);
  const meta = document.createElement('p');
  meta.className = 'muted';
  meta.textContent = `${dev.driver} • ${dev.address}:${dev.port}`;
  detail.appendChild(meta);

  const actions = document.createElement('div');
  actions.className = 'actions';
  const backupBtn = document.createElement('button');
  backupBtn.textContent = 'Backup now';
  backupBtn.onclick = async () => {
    backupBtn.disabled = true; backupBtn.textContent = 'Running…';
    try {
      await api('POST', `/devices/${id}/backup`);
    } catch (e) {
      alert('Backup failed: ' + e.message);
    } finally {
      backupBtn.disabled = false; backupBtn.textContent = 'Backup now';
      showDevice(id);
    }
  };
  const delBtn = document.createElement('button');
  delBtn.textContent = 'Delete';
  delBtn.style.background = '#b00020';
  delBtn.onclick = async () => {
    if (!confirm(`Delete ${dev.hostname}?`)) return;
    await api('DELETE', `/devices/${id}`);
    currentDeviceId = null;
    detail.innerHTML = '<p class="muted">Select a device on the left.</p>';
    loadDevices();
  };
  actions.append(backupBtn, delBtn);
  detail.appendChild(actions);

  // Latest config.
  const cfgHeader = document.createElement('h3');
  cfgHeader.textContent = 'Latest configuration';
  detail.appendChild(cfgHeader);
  try {
    const cfg = await api('GET', `/devices/${id}/config`);
    const pre = document.createElement('pre');
    pre.className = 'config';
    pre.textContent = cfg;
    detail.appendChild(pre);
  } catch (_) {
    const p = document.createElement('p'); p.className = 'muted';
    p.textContent = 'No backup yet — click "Backup now".';
    detail.appendChild(p);
  }

  // Recent runs.
  const runsHeader = document.createElement('h3');
  runsHeader.textContent = 'Recent runs';
  detail.appendChild(runsHeader);
  const runs = await api('GET', `/devices/${id}/runs`);
  if (!runs.length) {
    const p = document.createElement('p'); p.className = 'muted';
    p.textContent = 'No runs yet.';
    detail.appendChild(p);
  } else {
    const table = document.createElement('table');
    table.className = 'runs';
    table.innerHTML = '<thead><tr><th>Started</th><th>Status</th><th>Commit</th><th>Error</th></tr></thead>';
    const tbody = document.createElement('tbody');
    for (const r of runs) {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td>${new Date(r.started_at).toLocaleString()}</td>
        <td class="status-${r.status}">${r.status}</td>
        <td>${(r.commit_sha || '').slice(0, 8)}</td>
        <td>${r.error || ''}</td>`;
      tbody.appendChild(tr);
    }
    table.appendChild(tbody);
    detail.appendChild(table);
  }
}

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

$('#add-device-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
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
    e.target.reset();
    await loadDevices();
  } catch (err) {
    alert('Create failed: ' + err.message);
  }
});

$('#add-cred-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const fd = new FormData(e.target);
  try {
    await api('POST', '/credentials', {
      name: fd.get('name'),
      username: fd.get('username'),
      secret: fd.get('secret'),
    });
    e.target.reset();
    await loadAux();
  } catch (err) {
    alert('Save failed: ' + err.message);
  }
});

refreshSession();
