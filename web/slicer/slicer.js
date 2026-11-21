// Flash Bridge slicer: Prepare (place models), Paint (colours, supports, seams), Preview (layer by layer).
// The 3D work happens in the browser; PrintHarbor runs the OrcaSlicer engine with the open
// printer's own Flashforge profiles (src/slicer.dart). Loaded by app.js on first use.
//
// Plates work like OrcaSlicer's: several beds side by side. An object belongs to the plate it
// stands on; every plate is sliced, previewed and printed on its own.
//
// This file is the way in: the page (host.js) calls init() once and show() when the slicer is
// opened. The rest is split by what it does, one file each:
//
//   core.js          the shared state (S), small helpers, colours
//   profiles.js      the printer and its Orca profiles, filaments
//   scene.js         the 3D scene, camera and render loop; plate thumbnails
//   plates.js        the beds side by side, arranging, the plate bar
//   objects.js       models on the plates: add, colour, move, copy, delete
//   import.js        opening STL / OBJ / 3MF / ZIP files (heavy work in work.js)
//   pointer.js       mouse and touch: select, drag, lay on face, paint
//   fullspectrum.js  mixing filament colours to approximate any colour
//   undo.js          undo
//   toolbar.js       Prepare / Paint / Preview, the tool bar, the side panel frame
//   panels.js        what the side panel shows (plates, paint, settings)
//   events.js        clicks and changes in the panels; the All settings dialog
//   memory.js        how much memory a slice will need (layer height guard)
//   slice.js         slicing one plate at a time
//   project.js       saving to and opening from PrintHarbor's Projects page
//   preview.js       the layer-by-layer G-code preview
//
// They import from each other freely (functions only run after everything has loaded), so to
// find something, search for "function name" across this folder.
import { setHelpers, $, F, S, esc } from './core.js';
import { loadPrinter } from './profiles.js';
import { setup3d, resize } from './scene.js';
import { platesObjects } from './plates.js';
import { addFiles } from './import.js';
import { onKey } from './undo.js';
import { applyMode } from './toolbar.js';
import { onClick, onChange, onInput, renderAll } from './events.js';
import { cancelSlice, openProjectFromHash, poll } from './project.js';
import { setLayer, previewPlate } from './preview.js';

// ================================================================== entry points (app.js)
export function init(ffb) {
  setHelpers(ffb);
  $('#sl-file').addEventListener('change', (e) => { addFiles([...e.target.files]); e.target.value = ''; });
  $('#view-slicer').addEventListener('click', onClick);
  $('#view-slicer').addEventListener('change', onChange);
  $('#view-slicer').addEventListener('input', onInput);
  $('#sl-busy-cancel').addEventListener('click', cancelSlice);
  $('#sl-layer').addEventListener('input', () => setLayer(+$('#sl-layer').value));
  $('#sl-all-search').addEventListener('input', renderAll);
  $('#sl-all-changed').addEventListener('change', renderAll);
  $('#sl-all-list').addEventListener('change', onChange);
  $('#sl-all-list').addEventListener('click', onClick);
  window.addEventListener('keydown', onKey);
  const stage = $('#sl-stage');
  ['dragenter', 'dragover'].forEach((ev) => stage.addEventListener(ev, (e) => { if (e.dataTransfer && [...e.dataTransfer.types].includes('Files')) { e.preventDefault(); $('#sl-drop').classList.remove('hidden'); } }));
  ['dragleave', 'drop'].forEach((ev) => stage.addEventListener(ev, (e) => { e.preventDefault(); if (ev === 'drop' || e.target === $('#sl-drop')) $('#sl-drop').classList.add('hidden'); }));
  stage.addEventListener('drop', (e) => { if (e.dataTransfer.files.length) addFiles([...e.dataTransfer.files]); });
}

export async function show(mode) {
  const st = await F.api('slicer');
  if (st.memTotalMB) S.memMB = st.memTotalMB;
  if (!st.installed) { showSetup(st); return; }
  $('#sl-setup').classList.add('hidden');
  $('#sl-wrap').classList.remove('hidden');
  if (!S.ready) setup3d();
  S.mode = mode;
  const p = F.current();
  if (p && p.id !== S.printerId) await loadPrinter(p.id);
  if (mode === 'paint' && (!S.selected || S.selected.plate !== S.plate)) S.selected = platesObjects(S.plate)[0] || S.selected || S.objects[0] || null;
  applyMode();
  resize();
  await openInboxFromHash();
  await openProjectFromHash();
  const running = Object.values(st.jobs || {}).find((j) => j.state === 'slicing');
  if (running) { poll(running.plate - 1); return; }
  // a plate this page still thinks is slicing, but PrintHarbor does not: find out (it shows a message)
  const stale = S.res.findIndex((r, k) => r && r.job && r.job.state === 'slicing' && !(st.jobs || {})[k + 1]);
  if (stale >= 0) { await poll(stale); return; }
  if (mode === 'preview' && S.info && S.info.known) previewPlate(S.plate);
}

/** A model sent from another device (the phone app): #slicer/prepare/inbox=<id> opens it once. */
async function openInboxFromHash() {
  const m = /inbox=([0-9a-f]{16})/.exec(location.hash);
  if (!m) return;
  history.replaceState(null, '', '#slicer/prepare');
  await openInbox(m[1]);
}
async function openInbox(id) {
  try {
    const r = await fetch(`/api/slicer/inbox/${id}`, { cache: 'no-store' });
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).message || 'not found');
    const name = decodeURIComponent(r.headers.get('X-File-Name') || 'model.3mf');
    await addFiles([new File([await r.blob()], name)]);
  } catch (e) { F.toast('Could not open the file sent to the slicer: ' + e.message, true); }
}
window.ffbOpenInbox = (id) => { location.hash = '#slicer/prepare/inbox=' + id; };

export function hide() { /* rendering stops by itself when nothing changes */ }

export async function printerChanged() {
  const p = F.current();
  if (p && p.id !== S.printerId) { await loadPrinter(p.id); applyMode(); }
}

function showSetup(st) {
  $('#sl-wrap').classList.add('hidden');
  const box = $('#sl-setup');
  box.classList.remove('hidden');
  box.innerHTML = `<div class="card"><h2>OrcaSlicer not found</h2>
    <p class="muted">${esc(st.message || 'PrintHarbor slices with OrcaSlicer on this computer, but could not find it.')}</p>
    <p class="muted">Install OrcaSlicer, or set its path in PrintHarbor's Settings → OrcaSlicer, then come back.</p></div>`;
}

// The rest of what the page (host.js) calls lives in these files.
export { setTab, setStatus, setProjectMode, markSaved, isDirty } from './panels.js';
export { openProject, saveToProject } from './project.js';
