// Slicer page: PrintHarbor projects. Saving the plates to a project, opening one, and sending
// plates to the queue. See slicer.js for the list of files.
import * as THREE from 'three';
import { export3mf, encodeTree } from './project3mf.js';
import { S, F, LIGHT_AT, $$ } from './core.js';
import { minLayerHeight } from './memory.js';
import { MAX_FILAMENTS, pickPreset, loadPrinter, loadValues, savePrefs } from './profiles.js';
import { invalidate, thumbnail } from './scene.js';
import { setPlate, buildBed, renderPlateBar, platesObjects, plateX, viewHome } from './plates.js';
import { freeObject, recolor, checkBounds, worldBox, select } from './objects.js';
import { addFiles } from './import.js';
import { renderSide, applyMode, renderTools } from './toolbar.js';
import { ovOf, useOv, markSaved, withPlateSettings, valueOf, fmtVal, headCount } from './panels.js';
import { usedPlates, busy, projectData, filamentList, plateUse, askMerge, plateKey, towerSpot } from './slice.js';
import { clearPreview, loadPreview } from './preview.js';

// ================================================================== projects (PrintHarbor's Projects page)
// A project holds the plates. Opening one here loads its plates; saving stores every plate exactly as
// it is set up here (placement, painting, filaments, printer and quality settings) on this PC, and the
// Projects page and the print queue then print those plates with those settings.
function pageSettings() {
  const used = usedPlates();
  return { process: S.process, bedType: S.bedType, filaments: S.filaments, overrides: ovOf(used[0] ?? S.plate), plateOv: used.map((i) => ({ ...ovOf(i) })), infill: S.infillFil || 0, light: S.light, color: S.previewColor };
}
function applySettings(st) {
  if (!st) return;
  if (S.info.processes.includes(st.process)) S.process = st.process;
  if (st.bedType !== undefined) S.bedType = st.bedType;
  if (Array.isArray(st.plateOv) && st.plateOv.length) { S.plateOv = st.plateOv.map((o) => ({ ...o })); useOv(S.plate); }
  else if (st.overrides && typeof st.overrides === 'object') { S.overrides = { ...st.overrides }; S.plateOv = []; S.plateOv[S.plate] = S.overrides; }
  if (Array.isArray(st.filaments) && st.filaments.length) S.filaments = st.filaments.slice(0, MAX_FILAMENTS).map((f) => ({ color: f.color, preset: S.info.filaments.includes(f.preset) ? f.preset : pickPreset(F.family(String(f.preset || '').replace(/^(Flashforge|Generic|FusRock)\s+/, ''))) }));
  S.infillFil = +st.infill || 0;
  if (['auto', 'on', 'off'].includes(st.light)) S.light = st.light;
  if (st.color) S.previewColor = st.color;
}
function clearWorkspace() {
  queue = []; clearTimeout(pollTimer); busy(null);
  S.plateOv = []; S.plateOv[0] = S.overrides;
  for (const o of S.objects) freeObject(o);
  S.objects = []; S.selected = null; S.undo = [];
  S.nPlates = 1; S.plate = 0; S.res = []; S.merge = {};
  clearPreview();
}
function bindProject(p) { S.project = p; if (F.projectChanged) F.projectChanged(p); }

/** #slicer/prepare/project=<id>&plate=<n> opens a project once. */
export async function openProjectFromHash() {
  const m = /project=([0-9a-f]+)(?:&plate=(\d+))?/.exec(location.hash);
  if (!m) return;
  history.replaceState(null, '', '#slicer/' + (S.mode === 'paint' ? 'paint' : 'prepare'));
  await openProject(m[1], +(m[2] || 1));
}
export async function openProject(id, plate = 1) {
  if (S.project && S.project.id === id && S.objects.length) { // already open: just go to the plate
    setPlate(Math.min(Math.max(0, plate - 1), S.nPlates - 1));
    return;
  }
  const r = await F.api(`projects/${id}/slicer`);
  if (!r.ok) { F.toast(r.message || 'Could not open that project.', true); return; }
  if (!r.saved && !(S.info && S.info.known)) { F.toast('Pick a printer with a known profile first, then open the project again.', true); return; }
  if (S.objects.length && !(await F.confirmBox(`Open ${r.name}?`, `The ${S.objects.length} model(s) in the slicer now are taken off the plates${S.project ? ` (save them to ${S.project.name} first if you changed something)` : ' (save them as a project first to keep them)'}.`, 'Open project'))) return;
  if (S.res.some((x) => x && x.job && x.job.state === 'slicing')) await cancelSlice();
  clearWorkspace();
  const saved = r.saved;
  busy(`Opening ${r.name}…`, 'Reading the plates', null, false);
  try {
    if (saved) {
      const known = F.printers().some((p) => p.id === saved.printer);
      if (known && F.current().id !== saved.printer) F.usePrinter(saved.printer);
      if (known) await loadPrinter(saved.printer, '&machine=' + encodeURIComponent(saved.machine));
      else F.toast(`${r.name} was set up for ${saved.printer_name}, which is not in PrintHarbor any more. Showing it for ${F.current().name}: check it and save.`, true);
      if (!S.info || !S.info.known) throw new Error(S.err || 'the printer profiles could not be read');
      applySettings(JSON.parse(JSON.stringify(saved.settings || {})));
      await loadValues();
      buildBed();
    }
    const fetchFile = async (url) => {
      const f = await fetch(url, { cache: 'no-store' });
      if (!f.ok) throw new Error((await f.json().catch(() => ({}))).message || 'could not read the project file');
      return new File([await f.blob()], decodeURIComponent(f.headers.get('X-File-Name') || 'project.3mf'));
    };
    if (r.plates) { // STL / OBJ project never set up here: each plate's files onto that plate
      for (const e of r.plates) {
        if (e.plate > S.nPlates) S.nPlates = e.plate;
        S.plate = e.plate - 1;
        if (e.file) await addFiles([await fetchFile(e.file)]);
      }
    } else if (r.file) await addFiles([await fetchFile(r.file)]);
    if (saved) { // exactly as saved (the file's own filament list and settings are only a fallback)
      applySettings(JSON.parse(JSON.stringify(saved.settings || {})));
      savePrefs();
      for (const o of S.objects) recolor(o);
    }
    bindProject({ id: r.id, name: r.name, savedAt: saved ? saved.saved_at : '' });
    checkBounds(); buildBed(); renderPlateBar(); renderSide();
    setPlate(Math.min(Math.max(0, plate - 1), S.nPlates - 1));
    markSaved();
    if (S.pm && window.parent !== window) window.parent.postMessage({ ph: 'pm-opened', id: r.id }, location.origin);
    if (!S.pm) F.toast(saved ? `Opened ${r.name} as you saved it.` : `Opened ${r.name}. Set it up, then "Save to project" so the Projects page prints it this way.`);
  } catch (e) {
    F.toast(`Could not open ${r.name}: ${e.message}`, true);
  } finally { busy(null); invalidate(); }
}

/** Every plate packed as for slicing, plus the workspace file, saved into the project (asNew: a new project). */
export async function saveToProject(asNew = false) {
  if (!S.objects.length) { F.toast('Put a model on a plate first.', true); return; }
  if (!S.info || !S.info.known) { F.toast('Pick a printer first.', true); return; }
  if (S.res.some((x) => x && x.job && x.job.state === 'slicing')) { F.toast('Wait for the slice to finish (or cancel it), then save.', true); return; }
  checkBounds();
  // A model bigger than THIS printer's bed does not stop the project being saved: every plate is
  // stored, and when you send a plate it goes to a printer it fits on (bigger beds are re-matched).
  // Only actually slicing a plate here checks it fits the printer you are on. So saving never blocks
  // because another plate is too big for the printer you are laid out for.
  const off = usedPlates().filter((i) => platesObjects(i).some((o) => o.outside));
  let id = !asNew && S.project ? S.project.id : '', name = S.project ? S.project.name : '';
  if (!id) {
    const first = S.objects[0].name || 'Project';
    name = await F.askText(asNew ? 'Save as a new project' : 'Save as a project', 'Project name', asNew && name ? name + ' (copy)' : first.replace(/\.(3mf|stl|obj)$/i, ''));
    if (!name) return;
  }
  const used = usedPlates();
  const fd = new FormData(), plates = [];
  const pageMode = S.mode;
  try {
    for (const [k, i] of used.entries()) {
      const pk = await packPlate(i, true); // forSave: store the plate even if it is too big for this printer's bed
      if (!pk) { busy(null); return; }
      const list = platesObjects(i);
      const box = new THREE.Box3();
      list.forEach((o) => box.union(worldBox(o)));
      const size = box.getSize(new THREE.Vector3());
      const parts = list.map((o) => { const s = worldBox(o).getSize(new THREE.Vector3()); return { name: o.name, size_x_mm: s.x, size_y_mm: s.y, size_z_mm: s.z }; });
      const base = pk.opts.name.replace(/ - plate \d+$/, '');
      const plateTitle = used.length > 1 ? `${base} - plate ${k + 1}` : base;
      const thumb = pk.opts.thumb;
      plates.push({ index: k + 1, name: plateTitle, opts: { ...pk.opts, name: plateTitle, thumb: undefined, thumbSmall: undefined, thumbGcode: undefined }, tools: pk.toolFils, parts,
        max_x_mm: size.x, max_y_mm: size.y, max_z_mm: size.z, footprint_mm2: parts.reduce((n, p) => n + p.size_x_mm * p.size_y_mm, 0),
        painted: list.some((o) => o.states.some((v) => v)) });
      fd.append(`plate-${k + 1}`, pk.blob, `plate-${k + 1}.3mf`);
      if (thumb) fd.append(`thumb-${k + 1}`, await (await fetch(thumb)).blob(), `plate-${k + 1}.png`);
    }
    busy('Saving the project…', 'Packing the whole workspace', null, false);
    await new Promise((r) => setTimeout(r, 30));
    fd.append('project', new Blob([projectData(name)]), 'project.3mf');
    fd.append('meta', JSON.stringify({ id, name, printer: S.printerId, printer_name: S.info.name, machine: S.info.machine, process: S.process, settings: pageSettings(), filaments: filamentList(), plates }));
    const r = await new Promise((res) => {
      const xhr = new XMLHttpRequest();
      xhr.open('POST', '/api/projects/slicer');
      xhr.upload.onprogress = (ev) => { if (ev.lengthComputable) busy('Saving the project…', `${(ev.loaded / 1e6).toFixed(1)} of ${(ev.total / 1e6).toFixed(1)} MB`, ev.loaded / ev.total * 100); };
      xhr.onload = () => { try { res(JSON.parse(xhr.responseText)); } catch (_) { res({ ok: false, message: 'PrintHarbor gave no answer.' }); } };
      xhr.onerror = () => res({ ok: false, message: 'Lost the connection to PrintHarbor.' });
      xhr.send(fd);
    });
    if (!r.ok) { F.toast(r.message || 'Could not save the project.', true); return; }
    bindProject({ id: r.project.id, name: r.project.name, savedAt: r.project.slicer.saved_at });
    markSaved();
    if (window.parent !== window) window.parent.postMessage({ ph: 'project-saved', id: r.project.id }, location.origin);
    F.toast(S.pm ? `Saved ${r.project.name}.` : `Saved to ${r.project.name}: ${plates.length} plate${plates.length === 1 ? '' : 's'}. The Projects page and the queue now print ${plates.length === 1 ? 'it' : 'them'} exactly like this.`);
    if (off.length) F.toast(`Saved. Plate${off.length === 1 ? '' : 's'} ${off.map((i) => usedPlates().indexOf(i) + 1).join(', ')} ${off.length === 1 ? 'has a model' : 'have models'} bigger than the ${S.info.name} bed — slice ${off.length === 1 ? 'it' : 'them'} on a bigger printer. The other plates slice here.`);
    return true;
  } catch (e) {
    F.toast('Could not save the project: ' + e.message, true);
  } finally {
    busy(null);
    if (S.mode !== pageMode) applyMode();
  }
}
export function platePositions(o, i) {
  o.mesh.updateMatrixWorld(true);
  const m = o.mesh.matrixWorld.elements, p = o.orig, out = new Float32Array(p.length), dx = plateX(i);
  for (let k = 0; k < p.length; k += 3) {
    const x = p[k], y = p[k + 1], z = p[k + 2];
    out[k] = m[0] * x + m[4] * y + m[8] * z + m[12] - dx;
    out[k + 1] = m[1] * x + m[5] * y + m[9] * z + m[13];
    out[k + 2] = m[2] * x + m[6] * y + m[10] * z + m[14];
  }
  return out;
}
export function codesOf(o, arr) {
  if (!arr) return null;
  const n = o.levels.length;
  let any = false;
  const codes = new Array(n);
  for (let t = 0; t < n; t++) { codes[t] = encodeTree(arr, o.start[t], o.levels[t]); if (codes[t]) any = true; }
  return any ? codes : null;
}

let pollTimer = null, queue = [];
/**
 * Packs plate i for the engine: the plate's models as one 3MF (tools 1..n = the filaments this
 * plate uses) and the options the engine gets (printer, quality, filaments, overrides, prime tower,
 * pictures). Used for slicing here and for saving the plate into its project. null = it cannot
 * be packed (the reason was shown).
 */
// forSave: pack the plate's models just to store it in the project, even if a model is bigger than
// the printer the slicer is laid out for, or the plate would not slice on this machine's memory.
// Those are checked only when a plate is actually sliced or sent. Without forSave this packs a plate
// to slice it now, and refuses a plate that will not slice on this printer.
function packPlate(i, forSave = false) { return withPlateSettings(i, () => packPlateNow(i, forSave)); }
async function packPlateNow(i, forSave = false) {
  const list = platesObjects(i);
  if (!list.length) { F.toast(`Plate ${i + 1} is empty.`, true); return null; }
  checkBounds();
  if (!forSave && list.some((o) => o.outside)) { F.toast(`Some models on plate ${i + 1} are off the plate. Move them onto it first.`, true); setPlate(i); location.hash = '#slicer/prepare'; return null; }
  // Board memory: a layer height finer than this plate allows is raised before slicing (see plateMemory)
  if (!forSave) {
    const min = minLayerHeight(i), h = +valueOf('layer_height') || 0.2;
    if (min === null) {
      F.toast(`Plate ${i + 1} needs more memory than PrintHarbor has, even at the coarsest layer height. Paint supports only where needed, or move some models to another plate.`, true);
      setPlate(i); location.hash = '#slicer/prepare'; return null;
    }
    if (h < min - 1e-9) {
      if (String(fmtVal(S.values.layer_height)) === String(min)) delete S.overrides.layer_height; else S.overrides.layer_height = String(min);
      savePrefs();
      F.toast(`Layer height raised from ${h} to ${min.toFixed(2)} mm: finer would run PrintHarbor out of memory on plate ${i + 1}.`);
    }
  }
  const supOn = /^(1|true)$/.test(String(valueOf('enable_support')));
  if (!supOn && list.some((o) => o.sup && o.sup.includes(1))) F.toast('Painted supports are left out: Supports is switched off (switch it on under Paint → Supports).', true);
  // Only the filaments this plate uses are sent, as tools 1..n. More than the printer can hold
  // at once: ask which of the others prints each extra colour (or cancel and repaint).
  const use = plateUse(i);
  // Infill filament: the inside of every model in one filament (also for multi-colour prints).
  const infill = S.infillFil > 0 && S.infillFil <= S.filaments.length ? S.infillFil : 0;
  if (infill) use.set(infill, Infinity); // never merged away
  const U = [...use.keys()].sort((a, b) => a - b);
  let merge = {};
  if (U.length > headCount()) {
    const prev = S.merge[i];
    if (prev && prev.u === U.join()) merge = prev.map;
    else {
      merge = await askMerge(i, use);
      if (!merge) return null;
      S.merge[i] = { u: U.join(), map: merge };
    }
  } else delete S.merge[i];
  const toolFils = [...new Set(U.map((f) => merge[f] || f))].sort((a, b) => a - b);
  const toolOf = new Map(U.map((f) => [f, toolFils.indexOf(merge[f] || f) + 1]));
  const key = plateKey(i);
  busy(`Preparing plate ${i + 1}…`, 'Packing the models', 0, false);
  await new Promise((r) => setTimeout(r, 30));
  let blob;
  try {
    const data = export3mf(list.map((o) => {
      const st = new Uint8Array(o.states.length);
      // with an infill filament every surface is painted with its own colour: the engine then keeps
      // the colours as a skin and only the inside follows the infill filament (unpainted surfaces
      // would otherwise turn into the infill colour too).
      for (let t = 0; t < st.length; t++) st[t] = o.states[t] ? toolOf.get(o.states[t]) : (infill ? toolOf.get(o.filament) : 0);
      return { name: o.name, positions: platePositions(o, i), paint: { color: codesOf(o, st), supports: supOn ? codesOf(o, o.sup) : null, seam: codesOf(o, o.seam) }, filament: toolOf.get(o.filament) };
    }), plateName(i));
    blob = new Blob([data], { type: 'application/vnd.ms-package.3dmanufacturing-3dmodel+xml' });
  } catch (e) { busy(null); F.toast('Could not pack the plate: ' + e.message, true); return null; }
  const overrides = { ...S.overrides };
  if (S.bedType) overrides.curr_bed_type = S.bedType;
  if (infill) {
    overrides.sparse_infill_filament_id = String(toolOf.get(infill));
    if (overrides.mmu_segmented_region_max_width === undefined) overrides.mmu_segmented_region_max_width = '3';
  }
  // Lighter G-code (see the Quality card): the engine keeps every move of the file in memory while
  // it writes it, ~300 bytes each; a detailed scan makes millions. Measured on a 1M-triangle
  // painted helmet: 2.5 GB -> 1.7 GB, same print. The user's own values for these keys win.
  const tris = list.reduce((n, o) => n + o.orig.length / 9, 0);
  const light = S.light === 'on' || (S.light === 'auto' && tris >= LIGHT_AT);
  if (light) {
    if (overrides.resolution === undefined) overrides.resolution = '0.05';
    if (overrides.enable_arc_fitting === undefined) overrides.enable_arc_fitting = '0';
  }
  const towerOn = !/^(0|false)$/.test(String(valueOf('enable_prime_tower')));
  let tower = toolFils.length > 1 && towerOn ? towerSpot(i, toolFils.length) : null, noTower = false, towerWall = null;
  // No free spot for the prime tower (the models fill the plate): a printer with a nozzle per
  // colour (Creator 5) prints fine without one; a single nozzle needs it, so it is kept.
  if (tower && !tower.free && (S.info.extruders || 1) > 1 && overrides.enable_prime_tower === undefined) {
    overrides.enable_prime_tower = '0'; tower = null; noTower = true;
    F.toast(`No room for the prime tower on plate ${i + 1}: printing without it (each colour has its own nozzle).`);
  }
  if (tower && tower.free && tower.wall && overrides.wipe_tower_wall_type === undefined) { overrides.wipe_tower_wall_type = tower.wall; towerWall = tower.wall; }
  if (tower) tower = { x: tower.x, y: tower.y };
  // set only because of this printer (left out when the plate is sliced for another printer)
  const auto = [...(noTower ? ['enable_prime_tower'] : []), ...(towerWall ? ['wipe_tower_wall_type'] : [])];
  const opts = { printer: S.printerId, machine: S.info.machine, process: S.process, filaments: toolFils.map((f) => S.filaments[f - 1]), overrides, auto, name: plateName(i), thumb: thumbnail(list, 512, 512), thumbSmall: thumbnail(list, 128, 128), thumbGcode: thumbnail(list, 140, 110), tower };
  return { blob, opts, key, toolFils, noTower, towerWall, infill, light };
}
export async function slice(i = S.plate) {
  const pk = await packPlate(i);
  if (!pk) return false;
  if (S.mode !== 'preview') { history.replaceState(null, '', '#slicer/preview'); S.mode = 'preview'; $$('#mode-tabs a').forEach((a) => a.classList.toggle('active', a.dataset.mode === 'preview')); }
  if (S.plate !== i) { S.plate = i; buildBed(); }
  clearPreview();
  applyMode();
  const { blob, opts, key, toolFils, noTower, towerWall, infill, light } = pk;
  const fd = new FormData();
  fd.append('options', JSON.stringify(opts));
  fd.append('model', blob, 'plate.3mf');
  const r = await new Promise((res) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `/api/slicer/slice?plate=${i + 1}`);
    xhr.upload.onprogress = (ev) => { if (ev.lengthComputable) busy(`Sending plate ${i + 1} to the slicer…`, `${(ev.loaded / 1e6).toFixed(1)} of ${(ev.total / 1e6).toFixed(1)} MB`, ev.loaded / ev.total * 100); };
    xhr.onload = () => { try { res(JSON.parse(xhr.responseText)); } catch (_) { res({ ok: false, message: 'PrintHarbor gave no answer.' }); } };
    xhr.onerror = () => res({ ok: false, message: 'Lost the connection to PrintHarbor.' });
    xhr.send(fd);
  });
  if (!r.ok) { busy(null); F.toast(r.message || 'Slicing could not start.', true); renderSide(); return false; }
  S.res[i] = { job: r.job, key, g: null, tools: toolFils, noTower, towerWall, infill, light };
  renderSide(); renderPlateBar();
  poll(i);
  return true;
}
function plateName(i) {
  const names = [...new Set(platesObjects(i).map((o) => o.name))];
  const base = (names.length === 1 ? names[0] : names.length ? `${names[0]} + ${names.length - 1} more` : 'Plate').slice(0, 52);
  return S.nPlates > 1 ? `${base} - plate ${i + 1}` : base;
}
let pollFails = 0;
export async function poll(i) {
  clearTimeout(pollTimer);
  let r;
  try {
    r = await F.api(`slicer/job?plate=${i + 1}`);
    pollFails = 0;
  } catch (e) {
    // a lost request (Wi-Fi blip, board busy) must not end the polling: try again a few times
    if (++pollFails <= 20) { busy(`Slicing plate ${i + 1}…`, 'Waiting for PrintHarbor to answer…', null, true); pollTimer = setTimeout(() => poll(i), 2000); return; }
    busy(null); F.toast('Lost the connection to PrintHarbor while slicing. Reload the page to see the result.', true); return;
  }
  const j = r.job;
  if (!j) {
    // PrintHarbor has no such job any more: it restarted while slicing (or the job was cleaned up)
    busy(null);
    const was = S.res[i] && S.res[i].job && S.res[i].job.state === 'slicing';
    S.res[i] = null;
    queue = [];
    if (was) F.toast(`Plate ${i + 1}: PrintHarbor stopped while slicing (most likely it ran out of memory) and the slice was lost. Try again; if it happens again, put fewer or smaller models on the plate.`, true);
    renderPlateBar(); renderSide();
    return;
  }
  S.res[i] = S.res[i] || { key: '', g: null };
  S.res[i].job = j;
  if (j.state === 'slicing') {
    // memory line: what the engine uses (RAM + swap) and what PrintHarbor still has free
    const m = j.mem, gb = (mb) => (mb / 1024).toFixed(1);
    const mem = m ? ` · memory ${gb(m.rss + m.swap)} GB${m.swap > 50 ? ` (${gb(m.swap)} in swap)` : ''}, ${gb(m.avail)} GB free` : '';
    busy(`Slicing plate ${i + 1}…`, `${j.message || ''} (${j.seconds}s)${mem}${queue.length ? ` · ${queue.length} more plate(s) waiting` : ''}`, j.percent, true);
    pollTimer = setTimeout(() => poll(i), 700);
    return;
  }
  busy(null);
  renderPlateBar();
  if (j.state === 'done' && S.plate === i && S.mode === 'preview') await loadPreview(i);
  else if (j.state === 'error') F.toast(`Plate ${i + 1}: slicing failed: ${j.error || j.message}`, true);
  renderSide();
  while (queue.length) { if (await slice(queue.shift())) break; } // a plate that cannot start (e.g. cancelled colour question) is skipped
}
export async function sliceAll() {
  queue = Array.from({ length: S.nPlates }, (_, i) => i).filter((i) => platesObjects(i).length && !(S.res[i] && S.res[i].job && S.res[i].job.state === 'done' && S.res[i].key === plateKey(i)));
  if (!queue.length) { F.toast('Every plate is already sliced.'); return; }
  await slice(queue.shift());
}
/** Start over: every model, plate, slice and undo step goes; printer, filament and quality settings stay. */
export async function newProject() {
  if (S.objects.length && !(await F.confirmBox('Start a new project?', `All ${S.objects.length} model(s)${S.nPlates > 1 ? ` on ${S.nPlates} plates` : ''} and their slices are removed. Save the project first if you want to keep it. Printer, filament and quality settings stay.`, 'New project'))) return;
  if (S.res.some((r) => r && r.job && r.job.state === 'slicing')) await cancelSlice();
  clearWorkspace();
  bindProject(null);
  if (S.mode !== 'prepare') location.hash = '#slicer/prepare';
  else { select(null); checkBounds(); buildBed(); viewHome(); renderPlateBar(); renderTools(); renderSide(); }
  F.toast('New project: the plate is empty. Add a model to start.');
}
export async function cancelSlice() {
  queue = [];
  await F.api('slicer/job/cancel', { method: 'POST' });
  clearTimeout(pollTimer);
  busy(null);
  S.res.forEach((r, k) => { if (r && r.job && r.job.state === 'slicing') S.res[k] = null; }); // the one that was running, not necessarily the shown plate
  renderPlateBar();
  location.hash = '#slicer/prepare';
}

export async function sendToPrinter(i) {
  const r = await F.api('slicer/send', { method: 'POST', body: { printer: S.printerId, plate: i + 1 } });
  if (!r.ok) { F.toast(r.message, true); return; }
  if (F.current().id !== S.printerId) { F.toast('Saved to ' + S.info.name + "'s files."); return; }
  F.printFile(r.file);
}
