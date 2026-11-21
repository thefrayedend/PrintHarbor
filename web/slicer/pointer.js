// Slicer page: the mouse and touch on the 3D view. Select, drag across the bed, lay on a
// face, and paint. See slicer.js for the list of files.
import * as THREE from 'three';
import { INTERSECTED, NOT_INTERSECTED, CONTAINED } from './vendor/three-mesh-bvh.js';
import * as P from './paint.js';
import { S, F } from './core.js';
import { invalidate, objGroup, renderer, raycaster, camera, bedGroup, tcontrols, bed, cursorSphere, cursorRing, cursorBand } from './scene.js';
import { platesObjects, stride, buildBed, setPlate, renderPlateBar } from './plates.js';
import { worldBox, select, checkBounds, layDown, recolor } from './objects.js';
import { pushUndo } from './undo.js';
import { renderSide } from './toolbar.js';
import { renderXf } from './panels.js';
import { busy } from './slice.js';

const clipPlane = new THREE.Plane(new THREE.Vector3(0, 0, -1), 1e6);

// ================================================================== pointer: select, lay on face, paint
let painting = null; // {obj, ch, value, changed: Map}
export function setPainting(p) { painting = p; }
let downAt = null;
const pointer = new THREE.Vector2();
const clipOn = () => S.mode === 'paint' && S.paint.clip < 100;
function clipZ() {
  if (!clipOn()) return Infinity;
  let top = 0;
  for (const o of platesObjects(S.plate)) top = Math.max(top, worldBox(o).max.z);
  return (top * S.paint.clip) / 100;
}
export function applyClip(o) {
  const on = clipOn();
  o.mesh.material.clippingPlanes = on ? [clipPlane] : null;
  o.mesh.material.needsUpdate = true;
}
export function updateClip() {
  clipPlane.constant = clipOn() ? clipZ() : 1e6;
  for (const o of S.objects) applyClip(o);
  invalidate();
}

function hitAt(e, objects = objGroup.children) {
  const r = renderer.domElement.getBoundingClientRect();
  pointer.set(((e.clientX - r.left) / r.width) * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
  raycaster.setFromCamera(pointer, camera);
  const cz = clipZ();
  raycaster.firstHitOnly = cz === Infinity;
  const hits = raycaster.intersectObjects(objects, false);
  return hits.find((h) => h.point.z <= cz + 1e-3) || null;
}
function plateHitAt(e) {
  const r = renderer.domElement.getBoundingClientRect();
  pointer.set(((e.clientX - r.left) / r.width) * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
  raycaster.setFromCamera(pointer, camera);
  raycaster.firstHitOnly = false;
  const h = raycaster.intersectObjects(bedGroup.children.filter((c) => c.isMesh), false)[0];
  return h ? h.object.userData.plate : null;
}

const DRAG_TOOLS = new Set(['circle', 'sphere', 'triangle', 'height']);

// ---- drag a model across the bed (and onto other plates) with the mouse or a finger
let dragging = null; // {obj, plane, off: Vector3, start: Matrix4, moved}
const floorHit = (e, plane) => {
  const r = renderer.domElement.getBoundingClientRect();
  pointer.set(((e.clientX - r.left) / r.width) * 2 - 1, -((e.clientY - r.top) / r.height) * 2 + 1);
  raycaster.setFromCamera(pointer, camera);
  return raycaster.ray.intersectPlane(plane, new THREE.Vector3());
};
function startDrag(e) {
  if (S.tool === 'face' || tcontrols.axis) return; // lay-on-face clicks / the arrows handle themselves
  const h = hitAt(e);
  if (!h) return; // empty space: turn the view
  e.stopPropagation(); e.preventDefault();
  const o = h.object.userData.obj;
  const plane = new THREE.Plane(new THREE.Vector3(0, 0, 1), -h.point.z);
  const p = floorHit(e, plane);
  if (!p) return;
  dragging = { obj: o, plane, off: o.mesh.position.clone().sub(p), start: o.mesh.matrix.clone(), moved: false, x: e.clientX, y: e.clientY };
  renderer.domElement.setPointerCapture(e.pointerId);
  renderer.domElement.style.cursor = 'grabbing';
}
function moveDrag(e) {
  const d = dragging;
  if (!d.moved && Math.hypot(e.clientX - d.x, e.clientY - d.y) < 4) return;
  const p = floorHit(e, d.plane);
  if (!p) return;
  if (!d.moved) { d.moved = true; if (S.selected !== d.obj) select(d.obj); }
  d.obj.mesh.position.x = p.x + d.off.x;
  d.obj.mesh.position.y = p.y + d.off.y;
  d.obj.mesh.updateMatrixWorld(true);
  // dragged past the last plate: a new plate appears there
  const b = bed(), st = stride(), w = worldBox(d.obj);
  const raw = Math.floor(((w.min.x + w.max.x) / 2 - b.x0 + (st - b.w) / 2) / st);
  if (raw === S.nPlates && S.nPlates < 99) { S.nPlates++; buildBed(); }
  checkBounds(true); // plate follows the model, red when it hangs off
  renderXf();
}
function endDrag() {
  const d = dragging;
  dragging = null;
  renderer.domElement.style.cursor = '';
  if (!d.moved) return false;
  pushUndo({ type: 'xf', obj: d.obj, m: d.start });
  checkBounds();
  if (d.obj.plate !== S.plate) setPlate(d.obj.plate); // the view follows the model
  else { renderPlateBar(); renderSide(); }
  return true;
}
export async function onPointerDown(e) {
  if (e.target !== renderer.domElement || e.button !== 0) return;
  downAt = [e.clientX, e.clientY];
  if (S.mode === 'prepare') { startDrag(e); return; }
  if (S.mode !== 'paint') return;
  const h = hitAt(e);
  if (!h) return; // no model under the cursor: turn the view
  e.stopPropagation(); e.preventDefault();
  const o = h.object.userData.obj;
  if (S.selected !== o) { S.selected = o; renderSide(); }
  const ch = S.paint.ch;
  const value = e.shiftKey ? 0 : (ch === 'color' ? S.paint.filament : S.paint.mark);
  painting = { obj: o, ch, value, changed: new Map() };
  renderer.domElement.setPointerCapture(e.pointerId);
  if (DRAG_TOOLS.has(S.paint.tool)) applyTool(h);
  else { painting.once = true; await applyOnce(h); finishStroke(); }
}
export function onPointerMove(e) {
  if (dragging) { moveDrag(e); return; }
  if (S.mode === 'prepare' && !tcontrols.dragging) {
    renderer.domElement.style.cursor = S.tool !== 'face' && e.buttons === 0 && hitAt(e) ? 'grab' : '';
    return;
  }
  if (S.mode !== 'paint') return;
  const h = hitAt(e);
  if (h) {
    showCursor(h, e.shiftKey);
    if (painting && DRAG_TOOLS.has(S.paint.tool) && h.object.userData.obj === painting.obj) applyTool(h);
  } else hideCursor();
  invalidate();
}
export function onPointerUp(e) {
  if (painting) { if (!painting.once) finishStroke(); return; }
  if (dragging) { const moved = endDrag(); if (moved) { downAt = null; return; } }
  if (!downAt || Math.hypot(e.clientX - downAt[0], e.clientY - downAt[1]) > 4 || tcontrols.dragging) { downAt = null; return; }
  downAt = null;
  if (S.mode === 'preview') { const pl = plateHitAt(e); if (pl != null && pl !== S.plate) setPlate(pl, true, true); return; }
  if (S.mode === 'paint') return;
  const h = hitAt(e);
  if (S.tool === 'face' && h) {
    const n = h.face.normal.clone().transformDirection(h.object.matrixWorld);
    layDown(h.object.userData.obj, n);
    return;
  }
  if (h) { const o = h.object.userData.obj; if (o.plate !== S.plate) setPlate(o.plate, true, true); select(o); return; }
  const pl = plateHitAt(e);
  if (pl != null && pl !== S.plate) setPlate(pl, true, true);
  select(null);
}
export function finishStroke() {
  if (painting.changed.size) pushUndo({ type: 'paint', obj: painting.obj, ch: painting.ch, changed: painting.changed });
  painting = null;
  renderSide();
}
/** Alt + wheel: brush size (or band height); Ctrl + wheel: section view. */
export function onWheel(e) {
  if (S.mode !== 'paint' || e.target !== renderer.domElement || !(e.altKey || e.ctrlKey)) return;
  e.preventDefault(); e.stopPropagation();
  const up = e.deltaY < 0;
  if (e.ctrlKey) { S.paint.clip = Math.max(0, Math.min(100, S.paint.clip + (up ? 2 : -2))); updateClip(); }
  else if (S.paint.tool === 'height') S.paint.height = Math.max(0.1, Math.min(50, +(S.paint.height * (up ? 1.15 : 0.87)).toFixed(2)));
  else S.paint.size = Math.max(0.3, Math.min(40, +(S.paint.size * (up ? 1.15 : 0.87)).toFixed(2)));
  renderSide();
}

export function hideCursor() { cursorSphere.visible = cursorRing.visible = cursorBand.visible = false; }
function showCursor(h, erase) {
  hideCursor();
  const tool = S.paint.tool, col = erase ? 0xff6b6b : 0xffffff;
  for (const c of [cursorSphere, cursorRing, cursorBand]) c.material.color.setHex(col);
  if (tool === 'sphere') { cursorSphere.visible = true; cursorSphere.position.copy(h.point); cursorSphere.scale.setScalar(S.paint.size); }
  else if (tool === 'height') {
    const w = worldBox(h.object.userData.obj);
    cursorBand.visible = true;
    cursorBand.position.set((w.min.x + w.max.x) / 2, (w.min.y + w.max.y) / 2, h.point.z);
    cursorBand.scale.set(w.max.x - w.min.x + 6, w.max.y - w.min.y + 6, S.paint.height);
  } else {
    cursorRing.visible = true;
    cursorRing.position.copy(h.point);
    cursorRing.lookAt(camera.position);
    cursorRing.scale.setScalar(tool === 'circle' ? S.paint.size : 1.2);
  }
}

const fineTri = (geom, triIndex) => { const a = geom.index ? geom.index.getX(triIndex * 3) : triIndex * 3; return Math.floor(a / 3); };
/** Sets the stroke's value on the given fine triangles (below the section cut only). */
function setPaint(ids, valueOf) {
  const { obj: o, ch, changed } = painting;
  const arr = P.channel(o, ch, true);
  const cz = clipZ(), wz = cz === Infinity ? null : P.worldZ(o);
  let lo = Infinity, hi = -1;
  for (let i = 0; i < ids.length; i++) {
    const t = ids[i];
    if (wz && wz[t] > cz) continue;
    const v = valueOf ? valueOf(i) : painting.value;
    if (arr[t] === v) continue;
    if (!changed.has(t)) changed.set(t, arr[t]);
    arr[t] = v;
    if (t < lo) lo = t; if (t > hi) hi = t;
  }
  if (hi >= 0) { recolor(o, lo, hi + 1); markChanged(); }
}
function applyTool(h) {
  const o = painting.obj, tool = S.paint.tool;
  let ids;
  if (tool === 'sphere') ids = P.sphereTool(o, THREE, h.point, S.paint.size, INTERSECTED, NOT_INTERSECTED, CONTAINED, fineTri);
  else if (tool === 'circle') ids = P.circleTool(o, THREE, h.point, h.point.clone().sub(camera.position).normalize(), S.paint.size, INTERSECTED, NOT_INTERSECTED, fineTri);
  else if (tool === 'triangle') ids = P.triangleTool(o, fineTri(o.mesh.geometry, h.faceIndex));
  else if (tool === 'height') ids = P.heightTool(o, h.point.z, S.paint.height);
  if (ids) setPaint(ids);
}
export async function applyOnce(h) {
  const o = painting.obj;
  if (!o.adj) { busy('Preparing the model…', 'Finding which surfaces touch (only the first time)', null); await new Promise((r) => setTimeout(r, 30)); P.adjacency(o); busy(null); }
  const arr = P.channel(o, painting.ch, true);
  if (S.paint.tool === 'fill') setPaint(P.fillTool(o, arr, fineTri(o.mesh.geometry, h.faceIndex), S.paint.smart ? S.paint.angle : null));
  else if (S.paint.tool === 'gap') {
    const pairs = P.gapFill(o, arr, S.paint.gap);
    setPaint(pairs.map((p) => p[0]), (i) => pairs[i][1]);
    F.toast(pairs.length ? 'Gaps filled.' : `No unpainted gaps smaller than ${S.paint.gap} mm² on this model.`);
  }
}
function markChanged() { /* the plate's slice is out of date (checked by plateKey) */ }

/** Whole-object paint actions (with undo). */
export function paintWhole(o, ch, fn, filament) {
  if (!o) return;
  const arr = P.channel(o, ch, true);
  const changed = new Map();
  for (let t = 0; t < arr.length; t++) { const v = fn(arr[t], t); if (v !== arr[t]) { changed.set(t, arr[t]); arr[t] = v; } }
  pushUndo({ type: 'paint', obj: o, ch, changed, filament: o.filament });
  if (filament) o.filament = filament;
  recolor(o); renderSide();
}
