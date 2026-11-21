// PrintHarbor host for the FlashBridge slicer front end (slicer.js): gives it the helpers it
// expects from FlashBridge's app.js (window.FFB), the printer picker, and the
// Prepare / Paint / Preview routing. Slicing runs in PrintHarbor with OrcaSlicer.
import * as slicer from './slicer.js';

const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const store = {
  get(k, d) { try { return localStorage.getItem(k) ?? d; } catch (_) { return d; } },
  set(k, v) { try { localStorage.setItem(k, v); } catch (_) { /* private mode */ } },
};
const state = { printers: [], sel: store.get('ph-slicer-printer', '') };

async function api(path, { method = 'GET', body } = {}) {
  const res = await fetch('/api/' + path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
    cache: 'no-store',
  });
  let data = {};
  try { data = await res.json(); } catch (_) { /* empty */ }
  if (!res.ok && data.ok === undefined) { data.ok = false; data.message = data.message || data.error; }
  return data;
}
function toast(msg, err = false) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast' + (err ? ' err' : '');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => t.classList.add('hidden'), 4500);
}
function notice(el, msg, kind = '') { el.classList.remove('hidden'); el.className = 'notice' + (kind ? ' ' + kind : ''); el.textContent = msg; }
function confirmBox(title, text, yes = 'Yes') {
  const d = $('#confirm-dialog');
  $('#cf-title').textContent = title;
  $('#cf-text').textContent = text;
  $('#cf-yes').textContent = yes;
  d.returnValue = '';
  d.showModal();
  return new Promise((r) => d.addEventListener('close', () => r(d.returnValue === 'yes'), { once: true }));
}
document.addEventListener('click', (e) => { const c = e.target.closest('[data-close]'); if (c) c.closest('dialog').close(); });
const swatch = (hex, cls = '') => `<span class="swatch ${cls}" style="background:${esc(hex)}"></span>`;
function lab(hex) {
  const h = String(hex).replace('#', '');
  const v = (i) => { const c = parseInt(h.substr(i, 2), 16) / 255; return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4; };
  const r = v(0), g = v(2), b = v(4);
  const f = (t) => (t > 0.008856 ? Math.cbrt(t) : 7.787 * t + 16 / 116);
  const x = f((r * 0.4124 + g * 0.3576 + b * 0.1805) / 0.95047), y = f(r * 0.2126 + g * 0.7152 + b * 0.0722), z = f((r * 0.0193 + g * 0.1192 + b * 0.9505) / 1.08883);
  return [116 * y - 16, 500 * (x - y), 200 * (y - z)];
}
const colorDiff = (a, b) => { const p = lab(a), q = lab(b); return Math.hypot(p[0] - q[0], p[1] - q[1], p[2] - q[2]); };
const family = (m) => { const u = String(m).toUpperCase().replace(/[^A-Z0-9]/g, ''); return ['PETG', 'PLA', 'ABS', 'ASA', 'TPU', 'PA', 'PC', 'PVA', 'HIPS', 'PET'].find((f) => u.startsWith(f)) || u; };
const fmtTime = (s) => { s = Math.max(0, Math.round(s || 0)); const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60); return h ? `${h}h ${m}m` : `${m}m`; };

// PrintHarbor printers, in the shape the slicer knows from FlashBridge.
function shape(p) {
  if (!p) return null;
  const live = (p.status && p.status.slots) || [];
  const slots = (live.length ? live : p.slots || []).map((s) => {
    const t = String(s.type || '').trim();
    return { material: t, color: s.color || '#888888', hasFilament: !!t && !/^(none|empty)$/i.test(t) };
  });
  return { id: p.id, name: p.name, slots, state: p.status && p.status.state, ready: p.ready };
}
const printers = () => state.printers.map(shape);
const current = () => shape(state.printers.find((p) => p.id === state.sel) || state.printers[0]);

// After "Print" in the preview: the plate goes to the printer (FlashBridge opened its print dialog here).
async function printFile(token) {
  const p = current();
  const [plate, ...rest] = String(token).split('|');
  const name = rest.join('|');
  if (!(await confirmBox(`Print on ${p.name}?`, `${name} is sent to ${p.name} and starts printing. Make sure the plate is clear.`, 'Print'))) return;
  toast(`Sending to ${p.name}…`);
  const r = await api('slicer/print', { method: 'POST', body: { printer: p.id, plate: +plate } });
  if (!r.ok) { toast(r.message || 'Could not send the print.', true); return; }
  toast(`Printing on ${p.name}.`);
}

/** A one-line question (project name …): resolves to the text, or '' when cancelled. */
function askText(title, label, value = '') {
  const d = document.createElement('dialog');
  d.className = 'dialog';
  d.innerHTML = `<form method="dialog" class="dialog-body"><h2>${esc(title)}</h2>
    <label class="field"><span class="muted small">${esc(label)}</span><input name="v" maxlength="120" style="width:100%"></label>
    <div class="row gap" style="justify-content:flex-end;margin-top:14px"><button class="btn" value="">Cancel</button><button class="btn primary" value="ok">Save</button></div></form>`;
  document.body.appendChild(d);
  const inp = d.querySelector('input');
  inp.value = value;
  d.showModal();
  inp.select();
  return new Promise((r) => d.addEventListener('close', () => { const v = d.returnValue === 'ok' ? inp.value.trim() : ''; d.remove(); r(v); }, { once: true }));
}
/** Switches the printer picker (the slicer then loads that printer). */
function usePrinter(id) {
  if (!state.printers.some((p) => p.id === id)) return false;
  state.sel = id;
  store.set('ph-slicer-printer', id);
  $('#ph-printer').value = id;
  return true;
}
// The project open in the slicer: shown in the bar with Save / Back to project.
function projectChanged(p) {
  state.project = p;
  const box = $('#ph-project');
  if (!p) {
    box.innerHTML = `<button class="btn small primary" type="button" data-ph="save-new" title="Keep these plates as a PrintHarbor project: the Projects page and the queue print them exactly as set up here">Save as project</button>`;
    return;
  }
  const when = p.savedAt ? new Date(p.savedAt) : null;
  box.innerHTML = `<span class="ph-proj-name" title="${esc(p.name)}"><span class="muted small">Project</span> ${esc(p.name)}</span>
    ${when ? `<span class="muted small" title="Last saved ${esc(when.toLocaleString())}">saved ${esc(when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))}</span>` : `<span class="muted small">not saved from the slicer yet</span>`}
    <button class="btn small primary" type="button" data-ph="save" title="Save every plate exactly as set up here into ${esc(p.name)}: placement, painting, filaments, printer and settings">Save to project</button>
    <button class="btn small" type="button" data-ph="save-new" title="Save these plates as a separate, new project">Save as new</button>
    ${window.parent !== window ? `<button class="btn small" type="button" data-ph="back" title="Open ${esc(p.name)} on the Projects page (send plates to printers, copies, progress)">Back to project</button>` : ''}`;
}
document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-ph]');
  if (!b) return;
  if (b.dataset.ph === 'save') slicer.saveToProject(false);
  if (b.dataset.ph === 'save-new') slicer.saveToProject(true);
  if (b.dataset.ph === 'back' && state.project) window.parent.postMessage({ ph: 'open-project', id: state.project.id }, location.origin);
});

window.FFB = { api, toast, notice, confirmBox, esc, swatch, current, printers, printFile, colorDiff, family, fmtTime, state, refreshStatus, askText, usePrinter, projectChanged };

async function refreshStatus() {
  try {
    const r = await fetch('/api/printers', { cache: 'no-store' });
    if (r.ok) state.printers = await r.json();
  } catch (_) { /* PrintHarbor restarting */ }
  if (!state.printers.some((p) => p.id === state.sel)) state.sel = (state.printers[0] || {}).id || '';
  const sel = $('#ph-printer');
  const html = state.printers.map((p) => `<option value="${esc(p.id)}" ${p.id === state.sel ? 'selected' : ''}>${esc(p.name)}</option>`).join('') || '<option value="">No printers yet</option>';
  if (sel.dataset.html !== html) { sel.innerHTML = html; sel.dataset.html = html; }
}

// Routing: #slicer/prepare | #slicer/paint | #slicer/preview (plus /inbox=<id> to open a file).
function mode() { const m = /^#slicer\/(prepare|paint|preview)/.exec(location.hash); return m ? m[1] : 'prepare'; }
function route() {
  if (!/^#slicer\//.test(location.hash)) history.replaceState(null, '', '#slicer/prepare');
  const m = mode();
  document.querySelectorAll('#mode-tabs a').forEach((a) => a.classList.toggle('active', a.dataset.mode === m));
  slicer.show(m);
}
window.addEventListener('hashchange', route);

$('#ph-printer').addEventListener('change', (e) => {
  state.sel = e.target.value;
  store.set('ph-slicer-printer', state.sel);
  slicer.printerChanged();
});
$('#ph-add').addEventListener('click', () => $('#sl-file').click());

// PrintHarbor (the page around this frame) opens files here: { ph: 'slicer-open', id, printer },
// and projects: { ph: 'slicer-project', id, plate }.
window.addEventListener('message', (e) => {
  if (e.origin !== location.origin || !e.data) return;
  if (e.data.ph === 'pm-tab') {
    if (e.data.tab === 'paint') location.hash = '#slicer/paint';
    else { slicer.setTab(e.data.tab); if (mode() !== 'prepare') location.hash = '#slicer/prepare'; }
    return;
  }
  if (e.data.ph === 'pm-save') {
    slicer.saveToProject(false).then((ok) => window.parent.postMessage({ ph: 'pm-save-done', ok: ok === true, then: e.data.then || null }, location.origin));
    return;
  }
  if (e.data.ph === 'pm-status') { slicer.setStatus(e.data.status); return; }
  if (e.data.ph === 'slicer-project') { location.hash = `#slicer/prepare/project=${e.data.id}&plate=${+e.data.plate || 1}`; return; }
  if (e.data.ph === 'slicer-printer') {
    if (e.data.id && e.data.id !== state.sel && usePrinter(e.data.id)) slicer.printerChanged();
    if (mode() !== 'prepare') location.hash = '#slicer/prepare';
    return;
  }
  if (e.data.ph !== 'slicer-open') return;
  if (e.data.printer && e.data.printer !== state.sel) { state.sel = e.data.printer; store.set('ph-slicer-printer', state.sel); $('#ph-printer').value = state.sel; slicer.printerChanged(); }
  location.hash = '#slicer/prepare/inbox=' + e.data.id;
});

(async () => {
  const q = new URLSearchParams(location.search);
  // Inside a project on the Projects page: Plates / Paint / Settings, the page around it has the bar.
  if (q.get('project')) { document.body.classList.add('pm'); slicer.setProjectMode(true); }
  if (q.get('printer')) { state.sel = q.get('printer'); store.set('ph-slicer-printer', state.sel); }
  await refreshStatus();
  projectChanged(null);
  slicer.init(window.FFB);
  if (q.get('project')) {
    slicer.setTab(q.get('tab') || 'plates');
    history.replaceState(null, '', `#slicer/${q.get('tab') === 'paint' ? 'paint' : 'prepare'}/project=${q.get('project')}&plate=${+q.get('plate') || 1}`);
  }
  route();
  setInterval(() => { if (!document.hidden) refreshStatus(); }, 4000);
})();
