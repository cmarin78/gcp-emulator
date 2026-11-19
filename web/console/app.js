// Frontend de la consola de administración tipo "Google Cloud Console"
// Habla directo con el emulador (sin build step, HTML5 + ES6 nativo).

function project() {
  return document.getElementById('projectInput').value.trim() || 'demo-project';
}

// --- Navegación ---
document.querySelectorAll('.nav-item').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.nav-item').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.view').forEach(v => v.classList.remove('active'));
    btn.classList.add('active');
    const target = document.getElementById('view-' + btn.dataset.view);
    if (target) target.classList.add('active');

    // Cargas lazy
    if (btn.dataset.view === 'serviceusage') loadServices();
    if (btn.dataset.view === 'pubsub') loadTopics();
    if (btn.dataset.view === 'secretmanager') loadSecrets();
    if (btn.dataset.view === 'alloydb') loadAlloyClusters();
    if (btn.dataset.view === 'system') loadSystemInfo();
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
  const data = await api('GET', '/storage/v1/b').catch(() => ({ items: [] }));
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
  const data = await api('GET', `/compute/v1/projects/${project()}/zones/${zone}/instances`).catch(() => ({ items: [] }));
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
  const data = await api('GET', `/v1/projects/${project()}/serviceAccounts`).catch(() => ({ accounts: [] }));
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

// --- Service Usage ---
async function loadServices() {
  const data = await api('GET', `/v1/projects/${project()}/services`).catch(() => ({ services: [] }));
  const rows = (data.services || []).map(s => `
    <tr>
      <td>${s.config?.name || s.name}</td>
      <td><strong>${s.state}</strong></td>
      <td>
        ${s.state === 'ENABLED' 
          ? `<button class="link" onclick="disableService('${s.config?.name || s.name}')">Deshabilitar</button>`
          : `<button class="link" onclick="enableSpecificService('${s.config?.name || s.name}')">Habilitar</button>`}
      </td>
    </tr>`).join('');
  document.getElementById('servicesTable').innerHTML = rows || '<tr><td colspan="3">Ningún servicio activado explícitamente.</td></tr>';
}
async function enableService() {
  const svc = document.getElementById('newServiceId').value.trim();
  if (!svc) return;
  await api('POST', `/v1/projects/${project()}/services/${svc}:enable`, {});
  document.getElementById('newServiceId').value = '';
  loadServices();
}
async function enableSpecificService(svc) {
  await api('POST', `/v1/projects/${project()}/services/${svc}:enable`, {});
  loadServices();
}
async function disableService(svc) {
  await api('POST', `/v1/projects/${project()}/services/${svc}:disable`, {});
  loadServices();
}

// --- Pub/Sub ---
async function loadTopics() {
  const data = await api('GET', `/v1/projects/${project()}/topics`).catch(() => ({ topics: [] }));
  const rows = (data.topics || []).map(t => {
    const id = t.name.split('/').pop();
    return `
      <tr>
        <td>${t.name}</td>
        <td><button class="link" onclick="deleteTopic('${id}')">Eliminar</button></td>
      </tr>`;
  }).join('');
  document.getElementById('topicsTable').innerHTML = rows || '<tr><td colspan="2">Sin topics.</td></tr>';
}
async function createTopic() {
  const id = document.getElementById('newTopicId').value.trim();
  if (!id) return;
  await api('PUT', `/v1/projects/${project()}/topics/${id}`, {});
  document.getElementById('newTopicId').value = '';
  loadTopics();
}
async function deleteTopic(id) {
  await api('DELETE', `/v1/projects/${project()}/topics/${id}`);
  loadTopics();
}

// --- Secret Manager ---
async function loadSecrets() {
  const data = await api('GET', `/v1/projects/${project()}/secrets`).catch(() => ({ secrets: [] }));
  const rows = (data.secrets || []).map(s => {
    const id = s.name.split('/').pop();
    return `
      <tr>
        <td>${s.name}</td>
        <td>${s.createTime ? new Date(s.createTime).toLocaleString() : ''}</td>
        <td><button class="link" onclick="deleteSecret('${id}')">Eliminar</button></td>
      </tr>`;
  }).join('');
  document.getElementById('secretsTable').innerHTML = rows || '<tr><td colspan="3">Sin secretos.</td></tr>';
}
async function createSecret() {
  const id = document.getElementById('newSecretId').value.trim();
  if (!id) return;
  await api('POST', `/v1/projects/${project()}/secrets?secretId=${id}`, {
    replication: { automatic: {} }
  });
  document.getElementById('newSecretId').value = '';
  loadSecrets();
}
async function deleteSecret(id) {
  await api('DELETE', `/v1/projects/${project()}/secrets/${id}`);
  loadSecrets();
}

// --- AlloyDB ---
async function loadAlloyClusters() {
  const loc = document.getElementById('newAlloydbLocation').value;
  const data = await api('GET', `/alloydb/v1/projects/${project()}/locations/${loc}/clusters`).catch(() => ({ clusters: [] }));
  const rows = (data.clusters || []).map(c => {
    const id = c.name.split('/').pop();
    return `
      <tr>
        <td>${c.name}</td>
        <td>${c.clusterType || 'PRIMARY'}</td>
        <td>${c.state}</td>
        <td><button class="link" onclick="deleteAlloyCluster('${loc}','${id}')">Eliminar</button></td>
      </tr>`;
  }).join('');
  document.getElementById('alloyTable').innerHTML = rows || '<tr><td colspan="4">Sin clusters de AlloyDB en esta ubicación.</td></tr>';
}
async function createAlloyCluster() {
  const id = document.getElementById('newAlloydbClusterId').value.trim();
  const loc = document.getElementById('newAlloydbLocation').value;
  if (!id) return;
  await api('POST', `/alloydb/v1/projects/${project()}/locations/${loc}/clusters?clusterId=${id}`, {
    clusterType: 'PRIMARY',
    networkConfig: { network: `projects/${project()}/global/networks/default` }
  });
  document.getElementById('newAlloydbClusterId').value = '';
  loadAlloyClusters();
}
async function deleteAlloyCluster(loc, id) {
  await api('DELETE', `/alloydb/v1/projects/${project()}/locations/${loc}/clusters/${id}`);
  loadAlloyClusters();
}

// --- Snapshots & Backup ---
function downloadBinarySnapshot() {
  window.open('/admin/snapshot?format=binary', '_blank');
}
async function exportJsonSnapshot() {
  const data = await api('GET', '/admin/snapshot');
  document.getElementById('snapshotJsonArea').value = JSON.stringify(data, null, 2);
}
async function importJsonSnapshot() {
  const text = document.getElementById('snapshotJsonArea').value.trim();
  if (!text) {
    alert('Pegá el JSON del snapshot primero en el área de texto.');
    return;
  }
  try {
    const parsed = JSON.parse(text);
    await api('POST', '/admin/snapshot', parsed);
    alert('Snapshot importado exitosamente.');
    loadBuckets();
  } catch (err) {
    alert('Error importando snapshot: ' + err.message);
  }
}

// --- Estado del Sistema ---
async function loadSystemInfo() {
  const health = await api('GET', '/healthz').catch(err => ({ status: 'error', detail: err.message }));
  const realGov = await api('GET', '/admin/real-backends').catch(err => ({ status: 'error', detail: err.message }));
  const info = {
    health,
    realBackendGovernor: realGov,
    clientTimestamp: new Date().toISOString(),
    userAgent: navigator.userAgent
  };
  document.getElementById('systemInfo').innerText = JSON.stringify(info, null, 2);
}

// Carga inicial
loadBuckets();
loadInstances();
loadServiceAccounts();
