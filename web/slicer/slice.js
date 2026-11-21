// Slicer page: slicing, one plate at a time, with the progress box and the result per plate.
// See slicer.js for the list of files.
import * as THREE from 'three';
import { export3mf } from './project3mf.js';
import { S, F, esc, $ } from './core.js';
import { filColor } from './profiles.js';
import { bed } from './scene.js';
import { plateX, platesObjects } from './plates.js';
import { worldBox } from './objects.js';
import { valueOf, headCount, ovOf } from './panels.js';
import { platePositions, codesOf } from './project.js';

// ================================================================== slicing (one plate at a time)
function hashArr(a) { if (!a) return 0; let h = 0; for (let i = 0; i < a.length; i++) h = (h * 31 + a[i]) | 0; return h; }
/** The paint hashes of an object, computed once per change (recolor bumps o.rev); millions of pieces otherwise re-hash on every redraw. */
function paintHashes(o) {
  if (!o.hashes || o.hashes.rev !== o.rev) o.hashes = { rev: o.rev, h: [hashArr(o.states), hashArr(o.sup), hashArr(o.seam)] };
  return o.hashes.h;
}
/** Filament number -> how many paint pieces use it, for the models on plate i. */
/**
 * A free spot for the prime tower (purge block for colour changes) on plate i, from the models'
 * real outline on the bed (not their bounding box: round models leave the corners free).
 * Like OrcaSlicer, whose arrange packs the tower in beside the models: the free spot nearest the
 * models wins, right of them first, then left, then behind / in front. Plate coordinates of the
 * tower's front-left corner, inside its brim. free: false when nothing fits.
 */
export function towerSpot(i, tools = 2) {
  const b = bed(), px = plateX(i), G = 2;
  const nx = Math.ceil(b.w / G), ny = Math.ceil(b.d / G);
  const occ = new Uint8Array(nx * ny);
  const v = new THREE.Vector3();
  let mx0 = Infinity, my0 = Infinity, mx1 = -Infinity, my1 = -Infinity; // the models' footprint
  for (const o of platesObjects(i)) {
    const pos = o.mesh.geometry.attributes.position.array, m = o.mesh.matrixWorld;
    for (let t = 0; t < pos.length; t += 9) {
      let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      for (let k = 0; k < 9; k += 3) {
        v.set(pos[t + k], pos[t + k + 1], pos[t + k + 2]).applyMatrix4(m);
        const x = v.x - px - b.x0, y = v.y - b.y0;
        if (x < x0) x0 = x; if (x > x1) x1 = x; if (y < y0) y0 = y; if (y > y1) y1 = y;
      }
      const i0 = Math.max(0, Math.floor(x0 / G)), i1 = Math.min(nx - 1, Math.floor(x1 / G));
      const j0 = Math.max(0, Math.floor(y0 / G)), j1 = Math.min(ny - 1, Math.floor(y1 / G));
      for (let j = j0; j <= j1; j++) occ.fill(1, j * nx + i0, j * nx + i1 + 1);
      if (x0 < mx0) mx0 = x0; if (x1 > mx1) mx1 = x1; if (y0 < my0) my0 = y0; if (y1 > my1) my1 = y1;
    }
  }
  // How much room the tower takes (measured with the engine): its core is prime_tower_width wide and
  // grows ~10 mm deeper per extra colour; around it a 5 mm brim plus, depending on its walls, a cone
  // that widens with the print height (height x tan(apex / 2) on every side), ~10 mm ribs, or nothing.
  // Flashforge's profiles use the cone; when that does not fit, ribs, then plain walls are tried.
  let H = 0;
  for (const o of platesObjects(i)) H = Math.max(H, worldBox(o).max.z);
  const num = (k, d) => { const n = parseFloat(valueOf(k)); return Number.isFinite(n) ? n : d; };
  const width = num('prime_tower_width', 28), brim = num('prime_tower_brim_width', 5);
  const core = Math.min(45, 5 + 10 * Math.max(1, tools - 1));
  const current = String(valueOf('wipe_tower_wall_type') || 'cone');
  const walls = { cone: H * Math.tan((num('wipe_tower_cone_angle', 12) / 2) * Math.PI / 180), rib: num('wipe_tower_extra_rib_length', 10), rectangle: 0 };
  const order = [current, ...['cone', 'rib', 'rectangle'].filter((w) => w !== current)];
  const edge = 2, gap = 4;
  for (const wall of order) {
    const pad = brim + (walls[wall] || 0);
    const W = width + 2 * pad, D = core + 2 * pad;
    if (W > b.w - 2 * edge || D > b.d - 2 * edge) continue;
    const freeAt = (x, y) => {
      const i0 = Math.max(0, Math.floor((x - gap) / G)), i1 = Math.min(nx - 1, Math.floor((x + W + gap) / G));
      const j0 = Math.max(0, Math.floor((y - gap) / G)), j1 = Math.min(ny - 1, Math.floor((y + D + gap) / G));
      for (let j = j0; j <= j1; j++) for (let k = i0; k <= i1; k++) if (occ[j * nx + k]) return false;
      return true;
    };
    const spots = [];
    const mcy = (my0 + my1) / 2;
    for (let x = edge; x <= b.w - edge - W; x += G) for (let y = edge; y <= b.d - edge - D; y += G) {
      spots.push([towerScore(x, y, W, D, mx0, my0, mx1, my1, mcy), x, y]);
    }
    spots.sort((p, q) => p[0] - q[0]); // nearest the models, right side first
    for (const [, x, y] of spots) {
      if (freeAt(x, y)) return { x: +(b.x0 + x + pad).toFixed(1), y: +(b.y0 + y + pad).toFixed(1), free: true, wall: wall === current ? null : wall };
    }
  }
  return { x: 0, y: 0, free: false, wall: null };
}
/**
 * How good a tower spot is (lower = better): its distance from the models' footprint, plus a step
 * for the side (right of the models 0, left 25, behind / in front 50), plus a little for being
 * off the models' middle front-to-back. x, y, W, D: the tower with its brim; m*: the models.
 */
function towerScore(x, y, W, D, mx0, my0, mx1, my1, mcy) {
  if (!Number.isFinite(mx0)) return 0;
  const dx = Math.max(mx0 - (x + W), x - mx1, 0), dy = Math.max(my0 - (y + D), y - my1, 0);
  const side = x >= mx1 ? 0 : x + W <= mx0 ? 25 : 50;
  return Math.hypot(dx, dy) + side + 0.2 * Math.abs(y + D / 2 - mcy);
}
/** Project filament number for each tool of plate i's slice (tool t -> fil[t]). */
export function toolFilaments(i) { return (S.res[i] && S.res[i].tools) || S.filaments.map((_, k) => k + 1); }
export function plateUse(i) {
  const n = new Float64Array(256);
  for (const o of platesObjects(i)) {
    const st = o.states;
    for (let t = 0; t < st.length; t++) n[st[t] || o.filament]++;
  }
  const m = new Map();
  n.forEach((c, f) => { if (c && f) m.set(f, c); });
  return m;
}
/** Plate i uses more filaments than the printer holds: which one prints each extra colour? */
export function askMerge(i, use) {
  const heads = headCount();
  const byUse = [...use.keys()].sort((a, b) => use.get(b) - use.get(a));
  const keep = byUse.slice(0, heads).sort((a, b) => a - b), extra = byUse.slice(heads).sort((a, b) => a - b);
  const nearest = (f) => keep.reduce((b, k) => (F.colorDiff(S.filaments[k - 1].color, S.filaments[f - 1].color) < F.colorDiff(S.filaments[b - 1].color, S.filaments[f - 1].color) ? k : b), keep[0]);
  const sw = (f) => `${F.swatch(filColor(f), 'sm')} <b>${f}</b>`;
  const dlg = document.createElement('dialog');
  dlg.className = 'dialog';
  dlg.innerHTML = `<form method="dialog" class="dialog-body"><h2>Plate ${i + 1} uses ${use.size} colours</h2>
    <p>${esc(S.info.name || 'This printer')} prints ${heads} colour${heads === 1 ? '' : 's'} at a time, so ${extra.length === 1 ? 'one colour has' : `${extra.length} colours have`} to print with another filament on this plate.
      The ${heads} most used colours keep their filament (${keep.map(sw).join(' ')}).</p>
    ${extra.map((f) => `<div class="row gap" style="align-items:center;margin:8px 0"><span>Colour ${sw(f)} prints with</span>
      <select data-merge="${f}">${keep.map((k) => `<option value="${k}" ${k === nearest(f) ? 'selected' : ''}>filament ${k} (${esc(S.filaments[k - 1].color)})</option>`).join('')}</select></div>`).join('')}
    <p class="small muted">Only this plate's print changes; your painting is kept. To print the colours differently, cancel and repaint or move parts to another plate.</p>
    <div class="row gap" style="justify-content:flex-end"><button class="btn" value="cancel">Cancel</button><button class="btn primary" value="ok">Slice</button></div></form>`;
  document.body.appendChild(dlg);
  return new Promise((res) => {
    dlg.addEventListener('close', () => {
      const map = {};
      if (dlg.returnValue === 'ok') dlg.querySelectorAll('[data-merge]').forEach((sel) => { map[+sel.dataset.merge] = +sel.value; });
      dlg.remove();
      res(dlg.returnValue === 'ok' ? map : null);
    });
    dlg.showModal();
  });
}
export function plateKey(i) {
  const px = plateX(i);
  return JSON.stringify([S.printerId, S.info && S.info.machine, S.process, S.bedType, S.filaments, ovOf(i), S.merge[i] || null, S.infillFil || 0, S.light,
    platesObjects(i).map((o) => { const e = o.mesh.matrix.elements.map((x) => Math.round(x * 1000)); e[12] -= Math.round(px * 1000); return [o.id, o.filament, e, ...paintHashes(o)]; })]);
}

export function busy(title, msg, pct, cancel = false) {
  const b = $('#sl-busy');
  if (!title) { b.classList.add('hidden'); return; }
  b.classList.remove('hidden');
  $('#sl-busy-title').textContent = title;
  $('#sl-busy-msg').textContent = msg || '';
  $('#sl-busy-bar').parentElement.classList.toggle('hidden', pct === null);
  $('#sl-busy-bar').style.width = (pct || 0) + '%';
  $('#sl-busy-cancel').classList.toggle('hidden', !cancel);
}

/** World positions on the plate (plate i's own coordinates). */
/**
 * Downloads the whole workspace as one 3MF: every plate (laid out in the grid Bambu / Orca use,
 * so it opens here and in those programs with the plates intact), positions, filament colours,
 * painting (colours, supports, seams). Reloading the page empties the workspace; this keeps it.
 */
/** The non-empty plates, in order (saved projects number them 1..n without gaps). */
export const usedPlates = () => Array.from({ length: S.nPlates }, (_, i) => i).filter((i) => platesObjects(i).length);
/** The whole workspace as one 3MF (see saveProject). */
export function projectData(title = 'PrintHarbor project') {
  const b = bed();
  const used = usedPlates();
  const cols = Math.ceil(Math.sqrt(Math.max(1, used.length)));
  const objs = [], plates = used.map(() => []);
  used.forEach((i, k) => {
    const ox = (k % cols) * b.w * 1.2, oy = -Math.floor(k / cols) * b.d * 1.2;
    for (const o of platesObjects(i)) {
      const pos = platePositions(o, i);
      if (ox || oy) for (let q = 0; q < pos.length; q += 3) { pos[q] += ox; pos[q + 1] += oy; }
      objs.push({ name: o.name, positions: pos, paint: { color: codesOf(o, o.states), supports: codesOf(o, o.sup), seam: codesOf(o, o.seam) }, filament: o.filament });
      plates[k].push(objs.length);
    }
  });
  return export3mf(objs, title, { plates, filaments: filamentList(), bed: b });
}
export const filamentList = () => S.filaments.map((f) => ({ color: f.color, preset: f.preset, type: F.family(f.preset.replace(/^(Flashforge|Generic|FusRock)\s+/, '')) || 'PLA' }));
/**
 * Downloads the whole workspace as one 3MF: every plate (laid out in the grid Bambu / Orca use,
 * so it opens here and in those programs with the plates intact), positions, filament colours,
 * painting (colours, supports, seams).
 */
export async function saveProject() {
  if (!S.objects.length) return;
  busy('Saving the project…', 'Packing the models', null, false);
  await new Promise((r) => setTimeout(r, 30));
  try {
    const data = projectData();
    const name = `${(S.project && S.project.name) || (S.info && S.info.name) || 'printharbor'}-${new Date().toISOString().slice(0, 10)}.3mf`.replace(/[^\w.-]+/g, '_');
    const url = URL.createObjectURL(new Blob([data], { type: 'application/vnd.ms-package.3dmanufacturing-3dmodel+xml' }));
    const a = Object.assign(document.createElement('a'), { href: url, download: name });
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    F.toast(`Downloaded ${name} (${(data.length / 1e6).toFixed(1)} MB).`);
  } catch (e) {
    F.toast('Could not save the project: ' + e.message, true);
  } finally { busy(null); }
}
