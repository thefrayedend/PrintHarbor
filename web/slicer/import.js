// Slicer page: opening files (STL, OBJ, 3MF, ZIP). Reading a 3MF and simplifying a huge model
// run in a background worker (work.js) so the page doesn't freeze. See slicer.js for the list of files.
import * as THREE from 'three';
import { STLLoader } from './vendor/STLLoader.js';
import { simplifyModel, SIMPLIFY_AT } from './simplify.js';
import { OBJLoader } from './vendor/OBJLoader.js';
import { import3mf } from './project3mf.js';
import { unzipSync } from './vendor/fflate.js';
import { S, F } from './core.js';
import { MAX_FILAMENTS, pickPreset, defaultFilaments, savePrefs } from './profiles.js';
import { bed, camera, controls, invalidate } from './scene.js';
import { buildBed, platesObjects, plateX, setPlate, viewHome } from './plates.js';
import { addObject, checkBounds, dropToBed, worldBox, placeFree, select, recolor, arrange } from './objects.js';
import { renderSide } from './toolbar.js';
import { addFilament, fmtVal } from './panels.js';
import { busy } from './slice.js';

// ------------------------------------------------------------------ background worker
// Reading a 3MF and simplifying run in work.js so the page does not freeze; when a worker cannot
// be made (old browser, file:// page) the same functions run here.
let worker = null, workerBroken = false, workerProven = false, jobSeq = 0;
const jobs = new Map();
function inWorker(op, data, transfer, fallback) {
  if (workerBroken || typeof Worker === 'undefined') return fallback();
  try {
    if (!worker) {
      worker = new Worker(new URL('./work.js', import.meta.url), { type: 'module' });
      worker.onmessage = (e) => { workerProven = true; const j = jobs.get(e.data.id); if (!j) return; jobs.delete(e.data.id); e.data.ok ? j.res(e.data.result) : j.rej(new Error(e.data.error)); };
      worker.onerror = (e) => { // the worker itself failed to load: do the work here from now on
        workerBroken = true; worker = null;
        for (const j of jobs.values()) j.rej(new Error('worker: ' + (e.message || 'failed')));
        jobs.clear();
        console.warn('slicer worker unavailable, working on the main thread', e);
      };
    }
    const id = ++jobSeq;
    // big buffers are handed over (not copied) once the worker has answered once: a worker that
    // fails to load would otherwise take the only copy with it
    return new Promise((res, rej) => { jobs.set(id, { res, rej }); worker.postMessage({ id, op, data }, workerProven ? transfer : []); })
      .catch((e) => (workerBroken ? fallback() : Promise.reject(e)));
  } catch (e) {
    workerBroken = true;
    return fallback();
  }
}

// ================================================================== importing files
/** The models inside a ZIP (Printables' "download all" and the like): 3MF, STL and OBJ, any folder. */
async function unzipModels(file) {
  const all = unzipSync(new Uint8Array(await file.arrayBuffer()), { filter: (f) => /\.(3mf|stl|obj)$/i.test(f.name) && !/(^|\/)(__MACOSX|\.)/.test(f.name) });
  return Object.entries(all).map(([n, b]) => new File([b], n.split('/').pop()));
}
export async function addFiles(files) {
  if (!files.length) return;
  if (S.mode !== 'prepare' && S.mode !== 'paint') location.hash = '#slicer/prepare';
  // a ZIP brings its models in: they are opened like files picked one by one
  const zips = files.filter((f) => /\.zip$/i.test(f.name));
  if (zips.length) {
    files = files.filter((f) => !zips.includes(f));
    for (const z of zips) {
      try {
        const inner = await unzipModels(z);
        if (!inner.length) F.toast(z.name + ': no 3MF, STL or OBJ models inside.', true);
        files.push(...inner);
      } catch (e) { F.toast(z.name + ': could not open the ZIP (' + e.message + ').', true); }
    }
    if (!files.length) return;
  }
  for (const file of files) {
    const lower = file.name.toLowerCase();
    try {
      busy('Opening ' + file.name + '…', 'Reading the model…', null);
      await new Promise((r) => setTimeout(r, 30));
      const buf = await file.arrayBuffer();
      const base = file.name.replace(/\.[^.]+$/, '');
      if (lower.endsWith('.3mf')) await add3mf(buf, base);
      else if (lower.endsWith('.stl')) {
        const g = new STLLoader().parse(buf);
        placeNew(addObject({ name: base, positions: await lighter(base, nonIndexed(g)) }));
      } else if (lower.endsWith('.obj')) {
        const grp = new OBJLoader().parse(new TextDecoder().decode(buf));
        const parts = [];
        grp.updateMatrixWorld(true);
        grp.traverse((ch) => { if (ch.isMesh) { const g = ch.geometry.clone(); g.applyMatrix4(ch.matrixWorld); parts.push(nonIndexed(g)); } });
        if (!parts.length) throw new Error('No surfaces found in this OBJ.');
        const all = new Float32Array(parts.reduce((n, p) => n + p.length, 0));
        let k = 0; for (const p of parts) { all.set(p, k); k += p.length; }
        placeNew(addObject({ name: base, positions: await lighter(base, all) }));
      } else throw new Error('Only STL, OBJ and 3MF files can be added.');
    } catch (e) {
      F.toast(file.name + ': ' + e.message, true);
    } finally { busy(null); }
  }
  checkBounds(); buildBed(); renderSide();
  if (S.objects.length === files.length || platesObjects(S.plate).length <= 3) frameObjects();
}
/**
 * Models with millions of triangles (scans, sculpts) are simplified when opened: same shape
 * (within 0.05 mm), same painting, about a million triangles - far finer than the nozzle prints,
 * and light enough for the browser and PrintHarbor's slicer. Returns the (new) positions; the
 * paint object, if given, gets the matching codes.
 */
async function lighter(name, positions, paint = null) {
  const n = positions.length / 9;
  if (n <= SIMPLIFY_AT) return positions;
  busy(`Simplifying ${name}…`, `${(n / 1e6).toFixed(1)} million triangles is far more detail than the nozzle can print`, null);
  await new Promise((r) => setTimeout(r, 30));
  const pos = positions;
  const r = await inWorker('simplify', { positions: pos, paint: paint || {} }, [pos.buffer], () => { if (!pos.byteLength) throw new Error('simplifying was interrupted (the browser may have run out of memory) - try again'); return simplifyModel(pos, paint || {}); });
  if (paint) { paint.color = r.color; paint.supports = r.supports; paint.seam = r.seam; }
  F.toast(`${name}: simplified from ${(r.before / 1e6).toFixed(1)} to ${(r.after / 1e6).toFixed(1)} million triangles (shape within ${Math.max(0.01, r.error).toFixed(2)} mm, painting kept) so it slices quickly.`);
  return r.positions;
}
function nonIndexed(g) {
  const ng = g.index ? g.toNonIndexed() : g;
  return Float32Array.from(ng.attributes.position.array);
}
/** A new model goes onto the current plate; when that is full, onto a new plate. */
function placeNew(o) {
  dropToBed(o);
  o.mesh.position.x += plateX(S.plate) - (worldBox(o).min.x + worldBox(o).max.x) / 2 + bed().x0 + bed().w / 2;
  o.mesh.updateMatrixWorld(true);
  o.plate = S.plate;
  if (!placeFree(o, S.plate)) {
    S.nPlates++;
    o.mesh.position.x += plateX(S.nPlates - 1) - plateX(S.plate);
    o.mesh.updateMatrixWorld(true);
    o.plate = S.nPlates - 1;
    placeFree(o, o.plate);
    setPlate(o.plate);
    F.toast(`Plate ${S.plate} was full, so it went onto a new plate ${S.plate + 1}.`);
  }
  checkBounds();
  select(o);
}

/**
 * Opens a project with all its plates. Bambu / Orca lay plates out in a grid (columns = √count,
 * gap = 1/5 of the bed of the printer it was made for, read from the project). Every plate keeps
 * its layout, centred on one of ours (new plates after the ones in use). Models that were not on
 * any plate in the project get their own plate. Its filaments join the project's filament list.
 */
async function add3mf(buf, base) {
  const r = await inWorker('import', { buffer: buf }, [buf], () => { if (!buf.byteLength) throw new Error('opening was interrupted (the browser may have run out of memory) - try again'); return import3mf(buf); });
  const byId = new Map(r.objects.map((o) => [o.id, o]));
  const groups = r.plates.map((p) => ({ id: p.id, list: p.objectIds.map((id) => byId.get(id)).filter(Boolean) }));
  const assigned = new Set(groups.flatMap((g) => g.list));
  const loose = r.objects.filter((o) => !assigned.has(o));
  if (!groups.length) groups.push({ id: 0, list: loose.splice(0) }); // plain 3MF: one plate
  const used = groups.filter((g) => g.list.length);
  // first plate to fill: the current one if it is empty, else a new one after the last used plate
  let first = S.objects.length ? (platesObjects(S.plate).length ? lastPlate() + 1 : S.plate) : 0;
  const fresh = !S.objects.length;
  const fmap = takeFilaments(r, fresh);
  const kept = fresh ? takeSettings(r.settings || {}) : [];
  const b = bed();
  const boxOf = (list) => {
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const o of list) for (let i = 0; i < o.positions.length; i += 3) {
      const x = o.positions[i], y = o.positions[i + 1];
      if (x < x0) x0 = x; if (x > x1) x1 = x; if (y < y0) y0 = y; if (y > y1) y1 = y;
    }
    return { x0, y0, x1, y1 };
  };
  // Where a plate's area was in the project: from the plate number when the project names its
  // printer's bed, otherwise the bed size that puts the models inside one grid cell.
  const inCell = (bx, c) => bx.x0 >= c.x0 - 2 && bx.x1 <= c.x0 + c.w + 2 && bx.y0 >= c.y0 - 2 && bx.y1 <= c.y0 + c.d + 2;
  const cellById = (id, sb) => {
    const cols = Math.ceil(Math.sqrt(Math.max(1, r.plateCount))), n = id - 1;
    return { x0: (n % cols) * sb.w * 1.2 + sb.x0, y0: -Math.floor(n / cols) * sb.d * 1.2 + sb.y0, w: sb.w, d: sb.d };
  };
  const cellByPos = (bx, w, d) => {
    const sx = w * 1.2, sy = d * 1.2;
    const col = Math.round(((bx.x0 + bx.x1) / 2 - w / 2) / sx), row = Math.round(-((bx.y0 + bx.y1) / 2 - d / 2) / sy);
    const c = { x0: col * sx, y0: -row * sy, w, d };
    return col >= 0 && row >= 0 && inCell(bx, c) ? c : null;
  };
  const beds = [[256, 256], [180, 180], [330, 320], [350, 320], [b.w, b.d], [220, 220], [235, 235], [250, 210], [300, 300], [320, 320], [400, 400]];
  const boxes = used.map((g) => boxOf(g.list));
  let guess = null, best = 0;
  if (!r.bed) for (const [w, d] of beds) {
    const n = boxes.filter((bx) => cellByPos(bx, w, d)).length;
    if (n > best) { best = n; guess = [w, d]; }
  }
  const added = [];
  const place = async (list, bx, cell, target) => {
    if (target >= S.nPlates) S.nPlates = target + 1;
    const mx = b.x0 + b.w / 2, my = b.y0 + b.d / 2;
    // keep the layout of the source plate (centred), or centre the models when unknown
    let dx = cell ? mx - (cell.x0 + cell.w / 2) : mx - (bx.x0 + bx.x1) / 2;
    let dy = cell ? my - (cell.y0 + cell.d / 2) : my - (bx.y0 + bx.y1) / 2;
    // the source plate was bigger and the models would hang over the edge: centre them
    if (bx.x1 + dx > b.x0 + b.w + 1 || bx.x0 + dx < b.x0 - 1 || bx.y1 + dy > b.y0 + b.d + 1 || bx.y0 + dy < b.y0 - 1) {
      dx = mx - (bx.x0 + bx.x1) / 2; dy = my - (bx.y0 + bx.y1) / 2;
    }
    dx += plateX(target);
    for (const o of list) {
      let pos = o.positions;
      for (let i = 0; i < pos.length; i += 3) { pos[i] += dx; pos[i + 1] += dy; }
      const paint = { color: o.paint, supports: o.sup, seam: o.seam };
      pos = await lighter(o.name || base, pos, paint);
      const obj = addObject({ name: o.name || base, positions: pos, codes: paint.color, sup: paint.supports, seam: paint.seam, filament: fmap(o.filament) });
      const st = obj.states;
      for (let i = 0; i < st.length; i++) if (st[i]) st[i] = fmap(st[i]);
      recolor(obj);
      obj.plate = target;
      dropToBed(obj);
      added.push(obj);
    }
  };
  let target = first;
  for (const [k, g] of used.entries()) {
    const bx = boxes[k];
    let cell = r.bed && g.id ? cellById(g.id, r.bed) : null;
    if (cell && !inCell(bx, cell)) cell = null;
    if (!cell && guess) cell = cellByPos(bx, guess[0], guess[1]);
    await place(g.list, bx, cell, target++);
  }
  // models that sat beside the plates in the project: a plate of their own, packed (more plates if needed)
  if (loose.length) {
    const t = target;
    for (const o of loose) await place([o], boxOf([o]), null, t);
    arrange(t, false);
  }
  // a plain 3MF that names more filaments than the list has: add filaments for them
  let maxFil = 0;
  for (const o of added) {
    if (o.filament > maxFil) maxFil = o.filament;
    const st = o.states;
    for (let i = 0; i < st.length; i++) if (st[i] > maxFil) maxFil = st[i];
  }
  if (maxFil > S.filaments.length) { while (S.filaments.length < maxFil) addFilament(false); added.forEach(recolor); }
  const plates = new Set(added.map((o) => o.plate)).size;
  if (kept.length) F.toast(`Kept the project's print settings: ${kept.join(', ')}. Change them under Quality → All settings.`);
  if (r.skipped) F.toast(`${r.skipped} modifier / helper part${r.skipped === 1 ? ' was' : 's were'} left out (only printable parts are opened).`);
  F.toast(`Opened ${base}: ${added.length} object${added.length === 1 ? '' : 's'} on ${plates} plate${plates === 1 ? '' : 's'}${loose.length ? ` (${loose.length} that were beside the plates got a plate of their own)` : ''}. Your printer's own profiles are used, not the ones saved in the project.`);
  checkBounds();
  renderSide();
  setPlate(first);
  if (added.length) select(added.find((o) => o.plate === first) || added[0]);
}
const lastPlate = () => S.objects.reduce((m, o) => Math.max(m, o.plate), 0);

/**
 * The project's filaments join ours: same colour and material = same filament, others are added
 * (up to 16). Into an empty workspace the project's filaments replace ours, in its order.
 * Returns project filament number -> our filament number.
 */
function takeFilaments(r, fresh) {
  const cols = r.colours || [];
  if (!cols.length) return (f) => Math.min(Math.max(1, f || 1), MAX_FILAMENTS); // list grows to fit after placing
  if (fresh) S.filaments = [];
  const map = cols.map((c, k) => {
    const preset = S.info.filaments.includes(r.presets && r.presets[k]) ? r.presets[k] : pickPreset(r.types[k] || 'PLA');
    let n = S.filaments.findIndex((f) => f.color.toUpperCase() === c && F.family(f.preset) === F.family(preset));
    if (n < 0 && S.filaments.length < MAX_FILAMENTS) { S.filaments.push({ color: c, preset }); n = S.filaments.length - 1; }
    if (n < 0) n = nearestFilament(c);
    return n + 1;
  });
  if (!S.filaments.length) S.filaments = defaultFilaments(S.info, null);
  savePrefs();
  return (f) => map[(f || 1) - 1] || 1;
}
/** Takes the project's support / infill / wall / brim settings (only valid values of known settings). */
function takeSettings(set) {
  const byKey = new Map((S.schema || []).map((s) => [s.key, s]));
  const kept = [];
  for (const [k, v] of Object.entries(set)) {
    const sc = byKey.get(k);
    if (!sc || v === '' || v === 'nil') continue;
    if (sc.type === 'enum' && !sc.enum.some((e) => e.value === v)) continue;
    if (sc.type === 'bool' && !/^(0|1|true|false)$/.test(v)) continue;
    if ((sc.type === 'int' || sc.type === 'float') && !Number.isFinite(+v)) continue;
    if (String(fmtVal(S.values[k])) === v) { delete S.overrides[k]; continue; }
    S.overrides[k] = v;
    const label = sc.label || k;
    const shown = sc.type === 'bool' ? (/^(1|true)$/.test(v) ? 'on' : 'off') : sc.type === 'enum' ? (sc.enum.find((e) => e.value === v) || {}).label || v : v;
    kept.push(`${label} ${shown}`);
  }
  if (kept.length) savePrefs();
  return kept;
}
function nearestFilament(color) {
  let best = 0, bd = Infinity;
  S.filaments.forEach((f, i) => { const d = F.colorDiff(f.color, color); if (d < bd) { bd = d; best = i; } });
  return best;
}

export function frameObjects() {
  const list = platesObjects(S.plate);
  if (!list.length) { viewHome(); return; }
  const box = new THREE.Box3();
  list.forEach((o) => box.union(worldBox(o)));
  const c = box.getCenter(new THREE.Vector3());
  const r = Math.max(60, box.getSize(new THREE.Vector3()).length());
  const dir = camera.position.clone().sub(controls.target).normalize();
  controls.target.copy(c);
  camera.position.copy(c).addScaledVector(dir, r * 1.6);
  controls.update(); invalidate();
}
