// Fetch wrapper for the /api/manage/ endpoints.
// Every helper rejects with Error(server message) on non-2xx; views catch and toast.
const API = (() => {
  const BASE = '/api/manage';

  async function request(path, opts) {
    const resp = await fetch(BASE + path, opts);
    if (!resp.ok) {
      const text = (await resp.text()).trim();
      throw new Error(text || ('HTTP ' + resp.status));
    }
    return resp.json();
  }

  function qs(params) {
    const sp = new URLSearchParams();
    for (const k of Object.keys(params || {})) {
      const v = params[k];
      if (v !== undefined && v !== null && v !== '') sp.set(k, v);
    }
    const s = sp.toString();
    return s ? '?' + s : '';
  }

  function jsonOpts(method, body) {
    return {
      method: method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    };
  }

  function uploadOpts(file) {
    const fd = new FormData();
    fd.append('file', file);
    return { method: 'POST', body: fd };
  }

  return {
    dashboard: () => request('/dashboard'),

    // p: {search, group, page, size, include_offline}
    points: (p) => request('/points' + qs(p)),
    putMeta: (body) => request('/points/meta', jsonOpts('PUT', body)),
    // body: {matchers, drop_meta}
    deletePoints: (body) => request('/points', jsonOpts('DELETE', body)),
    pointsLast: (keys) => request('/points/last' + qs({ keys: keys.join(',') })),

    // p: {keys, start, end, step, agg}  (times in ms)
    history: (p) => request('/history' + qs(p)),

    exportUrl: (format) => BASE + '/export' + qs({ format: format }),
    importFile: (file) => request('/import', uploadOpts(file)),

    backupCreate: (name) => request('/backup', jsonOpts('POST', { name: name })),
    backups: () => request('/backups'),
    backupDownloadUrl: (name) => BASE + '/backups/' + encodeURIComponent(name) + '/download',
    backupDelete: (name) => request('/backups/' + encodeURIComponent(name), { method: 'DELETE' }),
    restoreByName: (name) => request('/restore', jsonOpts('POST', { name: name })),
    restoreUpload: (file) => request('/restore', uploadOpts(file)),
  };
})();
