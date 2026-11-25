// Slicer page: plates. Several beds side by side, as in OrcaSlicer; an object belongs to the
// plate it stands on. See slicer.js for the list of files.
import * as THREE from 'three';
import { S, $, F } from './core.js';
import { bed, bedGroup, invalidate, controls, camera } from './scene.js';
import { select, freeObject, checkBounds, placeFree, worldBox } from './objects.js';
import { pushUndo } from './undo.js';
import { renderSide } from './toolbar.js';
import { ovOf, useOv } from './panels.js';
import { plateKey, usedPlates } from './slice.js';
import { previewPlate } from './preview.js';

// ================================================================== plates
/** Distance from one plate to the next (beds stand in a row with a gap, like OrcaSlicer). */
export const stride = () => { const b = bed(); return b.w + Math.max(30, Math.round(b.w * 0.2)); };
export const plateX = (i) => i * stride();
/** Which plate a world x position is on (between plates: the nearer one). */
export function plateAt(x) {
  const b = bed(), st = stride();
  return Math.max(0, Math.min(S.nPlates - 1, Math.floor((x - b.x0 + (st - b.w) / 2) / st)));
}
export const platesObjects = (i) => S.objects.filter((o) => o.plate === i);

function labelTexture(text, active) {
  const c = document.createElement('canvas');
  c.width = 256; c.height = 128;
  const g = c.getContext('2d');
  g.fillStyle = active ? '#4c9aff' : '#9aa3ad';
  g.font = 'bold 84px system-ui, sans-serif';
  g.textAlign = 'center'; g.textBaseline = 'middle';
  g.fillText(text, 128, 68);
  const t = new THREE.CanvasTexture(c);
  t.colorSpace = THREE.SRGBColorSpace;
  return t;
}

/** Plate i is printed (every copy it needs is done, printed or marked by hand). */
export function plateDone(i) {
  if (!S.pm || !S.pmStatus) return false;
  const k = usedPlates().indexOf(i) + 1;
  const pr = k ? (S.pmStatus.progress || []).find((x) => x.plate === k) : null;
  return !!(pr && pr.needed && pr.accepted >= pr.needed);
}

export function buildBed() {
  if (!bedGroup) return;
  bedGroup.clear();
  const b = bed();
  const line = (arr, color, opacity) => {
    const g = new THREE.BufferGeometry(); g.setAttribute('position', new THREE.Float32BufferAttribute(arr, 3));
    return new THREE.LineSegments(g, new THREE.LineBasicMaterial({ color, transparent: true, opacity }));
  };
  for (let i = 0; i < S.nPlates; i++) {
    const ox = b.x0 + plateX(i), oy = b.y0, cur = i === S.plate;
    // A printed plate is red, so it stands out from the ones still to print.
    const done = plateDone(i);
    const plate = new THREE.Mesh(new THREE.PlaneGeometry(b.w, b.d), new THREE.MeshStandardMaterial({ color: done ? (cur ? 0x7a2228 : 0x5c1a1f) : (cur ? 0x2e333a : 0x25292f), roughness: 0.9 }));
    plate.position.set(ox + b.w / 2, oy + b.d / 2, -0.05);
    plate.userData.plate = i;
    bedGroup.add(plate);
    const pts = [], major = [];
    for (let x = 0; x <= b.w + 1e-6; x += 10) (Math.round(x) % 50 === 0 ? major : pts).push(ox + x, oy, 0, ox + x, oy + b.d, 0);
    for (let y = 0; y <= b.d + 1e-6; y += 10) (Math.round(y) % 50 === 0 ? major : pts).push(ox, oy + y, 0, ox + b.w, oy + y, 0);
    bedGroup.add(line(pts, 0x8a919b, cur ? 0.25 : 0.12), line(major, 0x9aa3ad, cur ? 0.55 : 0.25));
    const edge = [ox, oy, 0, ox + b.w, oy, 0, ox + b.w, oy, 0, ox + b.w, oy + b.d, 0, ox + b.w, oy + b.d, 0, ox, oy + b.d, 0, ox, oy + b.d, 0, ox, oy, 0];
    bedGroup.add(line(edge, cur ? 0x4c9aff : 0x6b737d, cur ? 0.95 : 0.6));
    if (cur) {
      const box = new THREE.LineSegments(new THREE.EdgesGeometry(new THREE.BoxGeometry(b.w, b.d, b.h)), new THREE.LineBasicMaterial({ color: 0x9aa3ad, transparent: true, opacity: 0.12 }));
      box.position.set(ox + b.w / 2, oy + b.d / 2, b.h / 2);
      bedGroup.add(box);
    }
    if (S.nPlates > 1) {
      const lab = new THREE.Mesh(new THREE.PlaneGeometry(36, 18), new THREE.MeshBasicMaterial({ map: labelTexture(String(i + 1), cur), transparent: true, depthWrite: false }));
      lab.position.set(ox + 20, oy - 14, 0.1);
      lab.userData.plate = i;
      bedGroup.add(lab);
    }
  }
  renderPlateBar();
  invalidate();
}

export function renderPlateBar() {
  const bar = $('#sl-plates');
  if (!bar) return;
  const dot = (i) => {
    const r = S.res[i];
    if (!platesObjects(i).length) return '';
    if (r && r.job && r.job.state === 'slicing') return '<i class="pd busy"></i>';
    if (r && r.job && r.job.state === 'done' && r.key === plateKey(i)) return '<i class="pd ok"></i>';
    return '';
  };
  const used = usedPlates();
  const prog = (i) => {
    const k = used.indexOf(i) + 1, pr = S.pm && S.pmStatus && k ? (S.pmStatus.progress || []).find((x) => x.plate === k) : null;
    if (!pr || !pr.needed) return '';
    return `<em class="pp ${pr.accepted >= pr.needed ? 'done' : pr.printing || pr.review ? 'busy' : ''}" title="${pr.accepted} of ${pr.needed} printed${pr.printing ? `, ${pr.printing} printing` : ''}${pr.review ? `, ${pr.review} finished and not checked yet` : ''}">${pr.accepted >= pr.needed ? '✓ ' : ''}${pr.accepted}/${pr.needed}</em>`;
  };
  bar.innerHTML = Array.from({ length: S.nPlates }, (_, i) => `<button data-plate="${i}" class="${i === S.plate ? 'active' : ''}${plateDone(i) ? ' printed' : ''}" title="Plate ${i + 1}: ${platesObjects(i).length} object(s)${plateDone(i) ? ', printed' : ''}">${dot(i)}Plate ${i + 1}<span>${platesObjects(i).length}</span>${prog(i)}</button>`).join('')
    + (S.mode === 'preview' ? '' : '<button data-sl="addplate" class="add" title="Add an empty plate (or drag a model past the last plate)">+ Plate</button>');
}

/** Makes plate i the current one; the camera follows. */
export function setPlate(i, moveCamera = true, frame = false) {
  i = Math.max(0, Math.min(S.nPlates - 1, i));
  const dx = plateX(i) - plateX(S.plate);
  ovOf(S.plate);
  useOv(i);
  S.plate = i;
  if (frame) framePlate(i);
  else if (moveCamera && dx) { controls.target.x += dx; camera.position.x += dx; controls.update(); }
  if (S.selected && S.selected.plate !== i && S.mode !== 'paint') select(null);
  if (S.mode === 'paint' && (!S.selected || S.selected.plate !== i)) S.selected = platesObjects(i)[0] || null;
  buildBed();
  if (S.mode === 'preview') previewPlate(i);
  renderSide();
}

export function addPlate() {
  S.nPlates++;
  setPlate(S.nPlates - 1);
  F.toast(`Plate ${S.nPlates} added. Drag models onto it, or use "Plate" in the Objects list.`);
}

export async function deletePlate(i) {
  const objs = platesObjects(i);
  if (S.nPlates === 1) { F.toast('There is always at least one plate.', true); return; }
  if (objs.length && !(await F.confirmBox(`Delete plate ${i + 1}?`, `Its ${objs.length} object(s) are removed too.`, 'Delete plate'))) return;
  for (const o of objs) freeObject(o);
  S.objects = S.objects.filter((o) => !objs.includes(o));
  // plates after it move one place to the left
  for (const o of S.objects) if (o.plate > i) { o.mesh.position.x -= stride(); o.mesh.updateMatrixWorld(true); }
  S.nPlates--;
  ovOf(S.plate);
  S.plateOv.splice(i, 1);
  S.res = []; S.undo = []; S.merge = {};
  if (S.selected && objs.includes(S.selected)) S.selected = null;
  S.plate = Math.min(S.plate, S.nPlates - 1);
  useOv(S.plate);
  checkBounds(); buildBed(); viewHome(); renderSide();
}

/** Moves an object to another plate (same spot if free, else the nearest free spot). */
export function moveToPlate(o, i) {
  if (i >= S.nPlates) { S.nPlates = i + 1; }
  pushUndo({ type: 'xf', obj: o, m: o.mesh.matrix.clone() });
  o.mesh.position.x += plateX(i) - plateX(o.plate);
  o.mesh.updateMatrixWorld(true);
  o.plate = i;
  if (!placeFree(o, i, true)) F.toast(`Plate ${i + 1} is full; the model is next to the others.`, true);
  checkBounds(); buildBed(); renderSide();
}

export function viewHome() {
  const b = bed();
  const c = new THREE.Vector3(b.x0 + plateX(S.plate) + b.w / 2, b.y0 + b.d / 2, 0);
  const r = Math.max(b.w, b.d);
  camera.position.set(c.x, c.y - r * 1.25, r * 1.0);
  controls.target.copy(c);
  controls.update();
  invalidate();
}

// Move the camera to a front view of plate i: framed on its models, or the whole
// bed when it's empty. Used when you click (or page to) a plate.
function framePlate(i) {
  const b = bed();
  let c = new THREE.Vector3(b.x0 + plateX(i) + b.w / 2, b.y0 + b.d / 2, 0);
  let r = Math.max(b.w, b.d);
  const objs = platesObjects(i);
  if (objs.length) {
    const box = new THREE.Box3();
    objs.forEach((o) => box.union(worldBox(o)));
    c = box.getCenter(new THREE.Vector3());
    r = Math.max(box.getSize(new THREE.Vector3()).length() * 1.15, Math.max(b.w, b.d) * 0.5);
  }
  camera.position.set(c.x, c.y - r * 1.25, c.z + r * 1.0);
  controls.target.copy(c);
  controls.update();
  invalidate();
}
