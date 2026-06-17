// Frontend mínimo tipo "Google Cloud Console" que habla directo con el
// emulador (mismo origen, sin build step: HTML + JS plano).

function project() {
  return document.getElementById('projectInput').value.trim() || 'demo-project';
}

// --- Navegación ---
document.querySelectorAll('.nav-item').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById('view-' + btn.dataset.view).classList.add('active');
  });
});

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok && res.status !== 204) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error?.message || res.statusText);
  }
  if (res.status === 204) return null;
  return res.json();
}

// --- Cloud Storage ---
async function loadBuckets() {
  const data = await api('GET', '/storage/v1/b');
  const rows = (data.items || []).map(b => `
    <tr>
      <td>${b.name}</td><td>${b.location}</td><td>${b.storageClass}</td>
      <td>${new Date(b.timeCreated).toLocaleString()}</td>
      <td><button class="link" onclick="deleteBucket('${b.name}')">Eliminar</button></td>
    </tr>`).join('');
  document.getElementById('bucketsTable').innerHTML = rows || '<tr><td colspan="5">Sin buckets.</td></tr>';
}
async function createBucket() {
  const name = document.getElementById('newBucketName').value.trim();
  if (!name) return;
  await api('POST', '/storage/v1/b', { name, location: 'US', storageClass: 'STANDARD' });
  document.getElementById('newBucketName').value = '';
  loadBuckets();
}
async function deleteBucket(name) {
  await api('DELETE', `/storage/v1/b/${name}`);
  loadBuckets();
}

// --- Compute Engine ---
async function loadInstances() {
  const zone = document.getElementById('newInstanceZone').value;
  const data = await api('GET', `/compute/v1/projects/${project()}/zones/${zone}/instances`);
  const rows = (data.items || []).map(i => `
    <tr>
      <td>${i.name}</td><td>${i.zone}</td><td>${i.machineType}</td><td>${i.status}</td>
      <td><button class="link" onclick="deleteInstance('${i.zone}','${i.name}')">Eliminar</button></td>
    </tr>`).join('');
  document.getElementById('instancesTable').innerHTML = rows || '<tr><td colspan="5">Sin instancias.</td></tr>';
}
async function createInstance() {
  const name = document.getElementById('newInstanceName').value.trim();
  const zone = document.getElementById('newInstanceZone').value;
  if (!name) return;
  await api('POST', `/compute/v1/projects/${project()}/zones/${zone}/instances`, { name, machineType: 'e2-medium' });
  document.getElementById('newInstanceName').value = '';
  loadInstances();
}
async function deleteInstance(zone, name) {
  await api('DELETE', `/compute/v1/projects/${project()}/zones/${zone}/instances/${name}`);
  loadInstances();
}

// --- IAM ---
async function loadServiceAccounts() {
  const data = await api('GET', `/v1/projects/${project()}/serviceAccounts`);
  const rows = (data.accounts || []).map(a => `
    <tr>
      <td>${a.email}</td><td>${a.displayName || ''}</td>
      <td><button class="link" onclick="deleteServiceAccount('${a.email}')">Eliminar</button></td>
    </tr>`).join('');
  document.getElementById('saTable').innerHTML = rows || '<tr><td colspan="3">Sin service accounts.</td></tr>';
}
async function createServiceAccount() {
  const accountId = document.getElementById('newSaId').value.trim();
  if (!accountId) return;
  await api('POST', `/v1/projects/${project()}/serviceAccounts`, { accountId, serviceAccount: { displayName: accountId } });
  document.getElementById('newSaId').value = '';
  loadServiceAccounts();
}
async function deleteServiceAccount(email) {
  await api('DELETE', `/v1/projects/${project()}/serviceAccounts/${email}`);
  loadServiceAccounts();
}

// Carga inicial
loadBuckets();
loadInstances();
loadServiceAccounts();
