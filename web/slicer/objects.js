// Slicer page: the models on the plates (add, colour, move, rotate, copy, delete).
// See slicer.js for the list of files.
import * as THREE from 'three';
import { subdivide, pieceCentres, decodeTree, stateAt } from './project3mf.js';
import * as P from './paint.js';
import { S, ENFORCE, BLOCK, OVERHANG, BASE, $, F } from './core.js';
import { filColor } from './profiles.js';
import { objGroup, invalidate, bed, tcontrols } from './scene.js';
import { plateAt, plateX, renderPlateBar, platesObjects, buildBed } from './plates.js';
import { applyClip } from './pointer.js';
import { pushUndo } from './undo.js';
import { renderSide } from './toolbar.js';

// ================================================================== objects
let nextId = 1; // every model on the plates gets its own number
/**
 * Adds a model. positions: world mm (Float32Array, 9 per triangle); codes: colour paint per
 * triangle (or null); sup / seam: support and seam paint per triangle (or null).
 */
export function addObject({ name, positions, codes, sup, seam, filament = 1 }) {
  // centre the model on its own bounding box; the mesh position puts it back
  let minX = Infinity, minY = Infinity, minZ = Infinity, maxX = -Infinity, maxY = -Infinity, maxZ = -Infinity;
  for (let i = 0; i < positions.length; i += 3) {
    const x = positions[i], y = positions[i + 1], z = positions[i + 2];
    if (x < minX) minX = x; if (x > maxX) maxX = x; if (y < minY) minY = y; if (y > maxY) maxY = y; if (z < minZ) minZ = z; if (z > maxZ) maxZ = z;
  }
  const cx = (minX + maxX) / 2, cy = (minY + maxY) / 2, cz = (minZ + maxZ) / 2;
  const orig = new Float32Array(positions.length);
  for (let i = 0; i < positions.length; i += 3) { orig[i] = positions[i] - cx; orig[i + 1] = positions[i + 1] - cy; orig[i + 2] = positions[i + 2] - cz; }
  const sub = subdivide(orig, { budget: Math.max(150000, 900000 - totalTris()) });
  const decode = (list, max) => {
    if (!list) return null;
    const arr = new Uint8Array(sub.count);
    for (let t = 0; t < list.length; t++) {
      if (!list[t]) continue;
      let tree;
      try { tree = decodeTree(list[t]); } catch (_) { continue; }
      const lvl = sub.levels[t], st = sub.start[t];
      if (!tree.children) { arr.fill(Math.min(tree.state, max), st, st + 4 ** lvl); continue; }
      const cs = pieceCentres(lvl);
      for (let k = 0; k < cs.length; k++) arr[st + k] = Math.min(stateAt(tree, cs[k]), max);
    }
    return arr;
  };
  const g = new THREE.BufferGeometry();
  g.setAttribute('position', new THREE.BufferAttribute(sub.fine, 3));
  g.setAttribute('color', new THREE.BufferAttribute(new Float32Array(sub.fine.length), 3));
  g.computeVertexNormals();
  g.computeBoundsTree();
  const mat = new THREE.MeshStandardMaterial({ vertexColors: true, roughness: 0.62, metalness: 0.02, side: THREE.DoubleSide });
  const mesh = new THREE.Mesh(g, mat);
  mesh.position.set(cx, cy, cz);
  const o = {
    id: nextId++, name: name || 'Model', mesh, orig, levels: sub.levels, start: sub.start,
    states: decode(codes, 16) || new Uint8Array(sub.count), sup: decode(sup, 2), seam: decode(seam, 2),
    filament: Math.max(1, filament), outside: false, plate: S.plate,
  };
  mesh.userData.obj = o;
  objGroup.add(mesh);
  S.objects.push(o);
  applyClip(o);
  recolor(o);
  return o;
}
const totalTris = () => S.objects.reduce((n, o) => n + o.states.length, 0);

/** Colours of one object: filaments, or (while painting supports / seams) enforcers and blockers. */
export function recolor(o, from = 0, to = o.states.length) {
  o.rev = (o.rev || 0) + 1; // paint changed: the cached paint hashes (plateKey) are stale
  const col = o.mesh.geometry.attributes.color;
  const arr = col.array;
  const cache = new Map();
  const rgb = (hex) => {
    if (!cache.has(hex)) { const c = new THREE.Color(hex).convertSRGBToLinear(); cache.set(hex, [c.r, c.g, c.b]); }
    return cache.get(hex);
  };
  const ch = S.mode === 'paint' ? S.paint.ch : 'color';
  const marks = ch === 'color' ? null : P.channel(o, ch);
  const oh = ch === 'support' && S.paint.overhang ? overhangSet(o) : null;
  for (let t = from; t < to; t++) {
    let hex;
    if (ch === 'color') hex = filColor(o.states[t] || o.filament);
    else {
      const m = marks ? marks[t] : 0;
      hex = m === 1 ? ENFORCE : m === 2 ? BLOCK : (oh && oh[t] ? OVERHANG : BASE);
    }
    const c = rgb(hex);
    const k = t * 9;
    arr[k] = arr[k + 3] = arr[k + 6] = c[0];
    arr[k + 1] = arr[k + 4] = arr[k + 7] = c[1];
    arr[k + 2] = arr[k + 5] = arr[k + 8] = c[2];
  }
  col.needsUpdate = true;
  invalidate();
}
export function overhangSet(o) {
  const set = new Uint8Array(o.states.length);
  for (const t of P.overhangs(o, S.paint.ohAngle)) set[t] = 1;
  return set;
}

/**
 * Exact world bounding box of an object. Walking every vertex of a million-piece mesh on each
 * mouse move is too slow, so the box is cached: when only the position changed since the last
 * call (dragging, arranging), the cached box is just shifted.
 */
export function worldBox(o) {
  o.mesh.updateMatrixWorld(true);
  const e = o.mesh.matrixWorld.elements, c = o.bbCache;
  if (c && c.geo === o.mesh.geometry) {
    let same = true;
    for (let k = 0; k < 12 && same; k++) if (c.e[k] !== e[k]) same = false;
    if (same) {
      const dx = e[12] - c.e[12], dy = e[13] - c.e[13], dz = e[14] - c.e[14];
      return new THREE.Box3(c.box.min.clone().add(new THREE.Vector3(dx, dy, dz)), c.box.max.clone().add(new THREE.Vector3(dx, dy, dz)));
    }
  }
  const box = new THREE.Box3().setFromObject(o.mesh, true);
  o.bbCache = { e: e.slice(), box: box.clone(), geo: o.mesh.geometry };
  return box;
}
export function dropToBed(o) {
  const b = worldBox(o);
  o.mesh.position.z -= b.min.z;
  o.mesh.updateMatrixWorld(true);
  invalidate();
}
export function fixScale(o) {
  const s = o.mesh.scale;
  s.set(Math.max(0.01, Math.abs(s.x)), Math.max(0.01, Math.abs(s.y)), Math.max(0.01, Math.abs(s.z)));
}
/** Works out every object's plate and whether it is (partly) off its plate. */
export function checkBounds(light = false) {
  const b = bed();
  for (const o of S.objects) {
    const w = worldBox(o);
    o.plate = plateAt((w.min.x + w.max.x) / 2);
    const x0 = b.x0 + plateX(o.plate);
    o.outside = w.min.x < x0 - 0.01 || w.min.y < b.y0 - 0.01 || w.max.x > x0 + b.w + 0.01 || w.max.y > b.y0 + b.d + 0.01 || w.max.z > b.h + 0.01;
    o.mesh.material.emissive.setHex(o.outside ? 0x5a0d0d : (o === S.selected && S.mode === 'prepare' ? 0x10294d : 0x000000));
  }
  $('#sl-empty').classList.toggle('hidden', !!S.objects.length || S.mode === 'preview');
  if (!light) renderPlateBar(); // (light: while dragging; the plate tabs hash every model)
  invalidate();
}

export function select(o) {
  S.selected = o;
  if (o && S.mode === 'prepare' && ['move', 'rotate', 'scale'].includes(S.tool)) {
    tcontrols.attach(o.mesh);
    tcontrols.setMode({ move: 'translate', rotate: 'rotate', scale: 'scale' }[S.tool]);
    tcontrols.showZ = S.tool !== 'move';
  } else tcontrols.detach();
  checkBounds();
  renderSide();
}

/** Takes a model out of the scene for good (its GPU buffers too). Not for undoable removes. */
export function freeObject(o) {
  objGroup.remove(o.mesh);
  o.mesh.geometry.dispose();
  o.mesh.material.dispose();
}
export function removeObject(o) {
  if (!o) return;
  pushUndo({ type: 'remove', obj: o });
  objGroup.remove(o.mesh);
  S.objects = S.objects.filter((x) => x !== o);
  if (S.selected === o) select(null);
  checkBounds(); renderSide();
}
/**
 * Mirrors a model left-right (in its own X). The mesh is rebuilt so it stays right-side out and
 * the painting stays on the same places: every triangle's corner order is swapped, which also
 * swaps the order of its pieces (children 1 and 2 at every level of the subdivision).
 */
export function mirrorObject(o) {
  if (!o) return;
  const g = o.mesh.geometry, fine = g.attributes.position.array, n = o.states.length;
  const perm = new Uint32Array(n); // new piece index -> old piece index
  for (let t = 0; t < o.levels.length; t++) {
    const lvl = o.levels[t], st = o.start[t], cnt = 4 ** lvl;
    for (let k = 0; k < cnt; k++) {
      let m = 0, kk = k; // swap base-4 digits 1 <-> 2
      for (let d = 0, w = 1; d < lvl; d++, w *= 4) { const dig = kk % 4; kk = (kk - dig) / 4; m += (dig === 1 ? 2 : dig === 2 ? 1 : dig) * w; }
      perm[st + k] = st + m;
    }
  }
  const nf = new Float32Array(fine.length), nc = new Float32Array(fine.length);
  const remap = (arr) => { if (!arr) return arr; const out = new Uint8Array(n); for (let k = 0; k < n; k++) out[k] = arr[perm[k]]; return out; };
  for (let k = 0; k < n; k++) {
    const src = perm[k] * 9, dst = k * 9;
    // corners a, c, b (winding swapped) with x negated
    const order = [0, 6, 3];
    for (let j = 0; j < 3; j++) { nf[dst + j * 3] = -fine[src + order[j]]; nf[dst + j * 3 + 1] = fine[src + order[j] + 1]; nf[dst + j * 3 + 2] = fine[src + order[j] + 2]; }
  }
  for (let i = 0; i < o.orig.length; i += 9) {
    const a = i, b = i + 3, c = i + 6, out = [a, c, b];
    for (let j = 0; j < 3; j++) { nc[i + j * 3] = -o.orig[out[j]]; nc[i + j * 3 + 1] = o.orig[out[j] + 1]; nc[i + j * 3 + 2] = o.orig[out[j] + 2]; }
  }
  pushUndo({ type: 'mirror', obj: o });
  o.orig = nc;
  o.states = remap(o.states); o.sup = remap(o.sup); o.seam = remap(o.seam);
  g.attributes.position.array.set(nf); g.attributes.position.needsUpdate = true;
  g.disposeBoundsTree(); g.computeVertexNormals(); g.computeBoundsTree();
  o.geo = o.adj = o.wz = o.wnz = null; o.bbCache = null; // the paint helpers' caches
  recolor(o); dropToBed(o); checkBounds(); renderSide();
}
export function duplicate(o) {
  if (!o) return;
  const g = o.mesh.geometry.clone();
  g.computeBoundsTree();
  const mesh = new THREE.Mesh(g, o.mesh.material.clone());
  mesh.position.copy(o.mesh.position); mesh.quaternion.copy(o.mesh.quaternion); mesh.scale.copy(o.mesh.scale);
  const c = { ...o, id: nextId++, mesh, states: o.states.slice(), sup: o.sup && o.sup.slice(), seam: o.seam && o.seam.slice(), name: o.name + ' (copy)', adj: o.adj, geo: o.geo, wz: null, wnz: null };
  mesh.userData.obj = c;
  objGroup.add(mesh);
  S.objects.push(c);
  applyClip(c);
  if (!placeFree(c, o.plate)) arrange(o.plate);
  dropToBed(c); select(c);
}

/** Moves an object to a free spot on plate i (false when there is none). keepIfFree: stay put when not overlapping. */
export function placeFree(o, i = S.plate, keepIfFree = false) {
  const b = bed(), x0 = b.x0 + plateX(i);
  const w = worldBox(o);
  const sx = w.max.x - w.min.x, sy = w.max.y - w.min.y;
  const others = S.objects.filter((x) => x !== o && x.plate === i).map(worldBox);
  const free = (x, y) => others.every((q) => x + sx + 3 < q.min.x || x - 3 > q.max.x || y + sy + 3 < q.min.y || y - 3 > q.max.y);
  if (keepIfFree && w.min.x >= x0 && w.max.x <= x0 + b.w && w.min.y >= b.y0 && w.max.y <= b.y0 + b.d && free(w.min.x, w.min.y)) return true;
  const cx = x0 + b.w / 2 - sx / 2, cy = b.y0 + b.d / 2 - sy / 2;
  const cand = [];
  for (let y = b.y0 + 2; y + sy <= b.y0 + b.d - 2; y += 5) for (let x = x0 + 2; x + sx <= x0 + b.w - 2; x += 5) cand.push([x, y]);
  cand.sort((p, q) => Math.hypot(p[0] - cx, p[1] - cy) - Math.hypot(q[0] - cx, q[1] - cy));
  const spot = free(cx, cy) && cx >= x0 ? [cx, cy] : cand.find(([x, y]) => free(x, y));
  if (!spot) return false;
  o.mesh.position.x += spot[0] - w.min.x;
  o.mesh.position.y += spot[1] - w.min.y;
  o.mesh.updateMatrixWorld(true);
  return true;
}

/**
 * Shelf packing of plate i's objects, biggest first. What does not fit goes onto the next plate
 * (a new one if needed), like OrcaSlicer's arrange.
 */
export function arrange(i = S.plate, undoable = true) {
  let items = platesObjects(i);
  if (!items.length) return;
  if (undoable) pushUndo({ type: 'many', list: S.objects.map((o) => ({ obj: o, m: o.mesh.matrix.clone() })) });
  const b = bed(), gap = 6;
  let plate = i, moved = 0;
  while (items.length) {
    const x0 = b.x0 + plateX(plate);
    const sized = items.map((o) => ({ o, w: worldBox(o) })).sort((p, q) => (q.w.max.y - q.w.min.y) - (p.w.max.y - p.w.min.y));
    let x = 0, y = 0, rowH = 0;
    const rows = [[]], rest = [];
    for (const it of sized) {
      const sx = it.w.max.x - it.w.min.x, sy = it.w.max.y - it.w.min.y;
      if (x > 0 && x + sx > b.w) { y += rowH + gap; x = 0; rowH = 0; rows.push([]); }
      if (y + sy > b.d && (x > 0 || y > 0)) { rest.push(it.o); continue; }
      it.x = x; it.y = y; x += sx + gap; rowH = Math.max(rowH, sy);
      rows[rows.length - 1].push(it);
    }
    const totalH = Math.max(0, ...rows.map((r) => (r.length ? Math.max(...r.map((it) => it.y + it.w.max.y - it.w.min.y)) : 0)));
    const offY = b.y0 + Math.max(0, (b.d - totalH) / 2);
    for (const row of rows) {
      if (!row.length) continue;
      const last = row[row.length - 1];
      const rowW = last.x + (last.w.max.x - last.w.min.x);
      const offX = x0 + Math.max(0, (b.w - rowW) / 2);
      for (const it of row) {
        it.o.mesh.position.x += offX + it.x - it.w.min.x;
        it.o.mesh.position.y += offY + it.y - it.w.min.y;
        it.o.mesh.updateMatrixWorld(true);
        it.o.plate = plate;
      }
    }
    if (!rest.length) break;
    // the rest goes onto the next plate
    plate++;
    if (plate >= S.nPlates) S.nPlates = plate + 1;
    for (const o of rest) { o.mesh.position.x += plateX(plate) - plateX(o.plate); o.plate = plate; o.mesh.updateMatrixWorld(true); }
    moved += rest.length;
    items = [...rest, ...S.objects.filter((o) => o.plate === plate && !rest.includes(o))];
  }
  checkBounds(); buildBed(); renderSide();
  if (moved) F.toast(`Not everything fitted on plate ${i + 1}: ${moved} object(s) went onto the next plate.`);
  if (S.objects.some((o) => o.outside)) F.toast('Something is still bigger than the plate. Shrink or rotate it.', true);
}

/** Rotates so the given world-space direction points down onto the plate. */
export function layDown(o, worldNormal) {
  pushUndo({ type: 'xf', obj: o, m: o.mesh.matrix.clone() });
  const q = new THREE.Quaternion().setFromUnitVectors(worldNormal.clone().normalize(), new THREE.Vector3(0, 0, -1));
  o.mesh.quaternion.premultiply(q);
  o.mesh.updateMatrixWorld(true);
  dropToBed(o); checkBounds(); renderSide();
}

/** Auto-orient: the biggest flat side goes down. */
export function autoOrient(o) {
  if (!o) return;
  o.mesh.updateMatrixWorld(true);
  const p = o.orig, m = o.mesh.matrixWorld;
  const areas = new Map();
  const a = new THREE.Vector3(), b = new THREE.Vector3(), c = new THREE.Vector3(), n = new THREE.Vector3();
  for (let i = 0; i < p.length; i += 9) {
    a.set(p[i], p[i + 1], p[i + 2]).applyMatrix4(m); b.set(p[i + 3], p[i + 4], p[i + 5]).applyMatrix4(m); c.set(p[i + 6], p[i + 7], p[i + 8]).applyMatrix4(m);
    n.subVectors(b, a).cross(c.clone().sub(a));
    const area = n.length() / 2;
    if (area < 1e-6) continue;
    n.normalize();
    const key = `${Math.round(n.x * 40)},${Math.round(n.y * 40)},${Math.round(n.z * 40)}`;
    const e = areas.get(key) || { area: 0, n: new THREE.Vector3() };
    e.area += area; e.n.addScaledVector(n, area);
    areas.set(key, e);
  }
  let best = null;
  for (const e of areas.values()) if (!best || e.area > best.area) best = e;
  if (best) layDown(o, best.n.normalize());
}
