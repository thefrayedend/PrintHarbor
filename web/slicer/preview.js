// Slicer page: the layer-by-layer G-code preview. See slicer.js for the list of files.
import * as THREE from 'three';
import { parseGcode, FEATURES } from './gcode.js';
import { S, $, F, esc } from './core.js';
import { filColor } from './profiles.js';
import { invalidate, previewGroup } from './scene.js';
import { platesObjects, plateX } from './plates.js';
import { renderSide } from './toolbar.js';
import { plateKey, busy, toolFilaments } from './slice.js';
import { slice } from './project.js';

// ================================================================== preview
/** Shows plate i in the preview, slicing it first when it changed since its last slice. */
export function previewPlate(i) {
  const r = S.res[i];
  clearPreview();
  // big parsed G-code of other plates is dropped (re-read in a few seconds when needed): several
  // large plates would otherwise hold hundreds of MB
  S.res.forEach((x, k) => { if (x && k !== i && x.g && x.g.count > LINE_PREVIEW_AT) x.g = null; });
  if (!platesObjects(i).length) { renderSide(); return; }
  if (!r || !r.job || r.key !== plateKey(i)) {
    if (!(r && r.job && r.job.state === 'slicing')) { slice(i); return; }
  }
  if (r.job.state === 'done') { if (r.g) buildPreview(r.g, i); else loadPreview(i); }
  renderSide();
}
export function clearPreview() {
  disposePreview();
  $('#sl-layers').classList.add('hidden');
  $('#sl-legend').classList.add('hidden');
  invalidate();
}
export async function loadPreview(i) {
  busy('Loading the preview…', 'Reading the G-code', null);
  try {
    const res = await fetch(`/api/slicer/gcode?plate=${i + 1}`, { cache: 'no-store' });
    if (!res.ok) throw new Error('The G-code could not be fetched.');
    const text = await res.text();
    await new Promise((r) => setTimeout(r, 20));
    const g = parseGcode(text);
    S.res[i].g = g;
    if (S.plate === i) buildPreview(g, i);
  } catch (e) {
    F.toast(e.message, true);
  } finally { busy(null); }
}
let segMesh = null; // InstancedMesh of boxes (small prints) or LineSegments (big ones)
/** Above this many printed segments the preview is drawn as lines: boxes would need ~80 bytes each on the GPU. */
const LINE_PREVIEW_AT = 800000;
function disposePreview() {
  if (segMesh) { segMesh.geometry.dispose(); segMesh.material.dispose(); segMesh = null; }
  previewGroup.clear();
}
function buildPreview(g, i) {
  disposePreview();
  previewGroup.position.set(plateX(i), 0, 0);
  const n = g.count;
  if (!n) { F.toast('The G-code has no printed paths.', true); return; }
  if (n > LINE_PREVIEW_AT) {
    const seg = g.seg, pos = new Float32Array(n * 6);
    for (let k = 0; k < n; k++) {
      const o = k * 7, zz = seg[o + 4] - Math.max(0.05, seg[o + 5]) / 2;
      pos[k * 6] = seg[o]; pos[k * 6 + 1] = seg[o + 1]; pos[k * 6 + 2] = zz;
      pos[k * 6 + 3] = seg[o + 2]; pos[k * 6 + 4] = seg[o + 3]; pos[k * 6 + 5] = zz;
    }
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(pos, 3));
    geo.setAttribute('color', new THREE.BufferAttribute(new Float32Array(n * 8), 4)); // RGBA: alpha 0 hides a line type
    segMesh = new THREE.LineSegments(geo, new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, alphaTest: 0.5 }));
    segMesh.userData.lines = true;
    segMesh.frustumCulled = false;
    previewGroup.add(segMesh);
    S.previewG = g;
    colorPreview();
    const sl = $('#sl-layer');
    sl.max = String(g.layerEnd.length);
    setLayer(g.layerEnd.length);
    $('#sl-layers').classList.toggle('hidden', S.mode !== 'preview');
    $('#sl-legend').classList.toggle('hidden', S.mode !== 'preview');
    invalidate();
    return;
  }
  segMesh = new THREE.InstancedMesh(new THREE.BoxGeometry(1, 1, 1), new THREE.MeshLambertMaterial(), n);
  const m = new THREE.Matrix4(), q = new THREE.Quaternion(), p = new THREE.Vector3(), s = new THREE.Vector3(), z = new THREE.Vector3(0, 0, 1);
  const seg = g.seg;
  for (let k = 0; k < n; k++) {
    const o = k * 7, x0 = seg[o], y0 = seg[o + 1], x1 = seg[o + 2], y1 = seg[o + 3], zz = seg[o + 4], h = Math.max(0.05, seg[o + 5]), w = Math.max(0.1, seg[o + 6]);
    const len = Math.hypot(x1 - x0, y1 - y0);
    p.set((x0 + x1) / 2, (y0 + y1) / 2, zz - h / 2);
    q.setFromAxisAngle(z, Math.atan2(y1 - y0, x1 - x0));
    s.set(len + w * 0.5, w, h * 0.9);
    m.compose(p, q, s);
    segMesh.setMatrixAt(k, m);
  }
  segMesh.instanceMatrix.needsUpdate = true;
  segMesh.frustumCulled = false;
  previewGroup.add(segMesh);
  S.previewG = g;
  colorPreview();
  const sl = $('#sl-layer');
  sl.max = String(g.layerEnd.length);
  setLayer(g.layerEnd.length);
  $('#sl-layers').classList.toggle('hidden', S.mode !== 'preview');
  $('#sl-legend').classList.toggle('hidden', S.mode !== 'preview');
  invalidate();
}
export function colorPreview() {
  const g = S.previewG;
  if (!g || !segMesh) return;
  const c = new THREE.Color();
  const fil = toolFilaments(S.plate);
  const tools = fil.map((f) => new THREE.Color(filColor(f)));
  const feats = FEATURES.map(([, col]) => new THREE.Color(col));
  const hidden = new THREE.Color(0, 0, 0);
  const byTool = S.previewColor === 'tool';
  if (segMesh.userData.lines) {
    const col = segMesh.geometry.attributes.color, a = col.array;
    for (let i = 0; i < g.count; i++) {
      const f = g.feature[i];
      const hid = S.hiddenFeatures.has(f);
      if (!hid) c.copy(byTool ? (tools[g.tool[i]] || feats[14]) : feats[f]);
      const k = i * 8, al = hid ? 0 : 1;
      a[k] = a[k + 4] = c.r; a[k + 1] = a[k + 5] = c.g; a[k + 2] = a[k + 6] = c.b; a[k + 3] = a[k + 7] = al;
    }
    col.needsUpdate = true;
    legend();
    return;
  }
  for (let i = 0; i < g.count; i++) {
    const f = g.feature[i];
    if (S.hiddenFeatures.has(f)) { segMesh.setColorAt(i, hidden); continue; }
    c.copy(byTool ? (tools[g.tool[i]] || feats[14]) : feats[f]);
    segMesh.setColorAt(i, c);
  }
  segMesh.instanceColor.needsUpdate = true;
  if (S.hiddenFeatures.size || segMesh.userData.hid) {
    const m = new THREE.Matrix4(), zero = new THREE.Matrix4().makeScale(0, 0, 0);
    if (!segMesh.userData.saved) { segMesh.userData.saved = segMesh.instanceMatrix.array.slice(); }
    const saved = segMesh.userData.saved;
    for (let i = 0; i < g.count; i++) {
      if (S.hiddenFeatures.has(g.feature[i])) segMesh.setMatrixAt(i, zero);
      else { m.fromArray(saved, i * 16); segMesh.setMatrixAt(i, m); }
    }
    segMesh.instanceMatrix.needsUpdate = true;
    segMesh.userData.hid = S.hiddenFeatures.size > 0;
  }
  legend();
  function legend() {
    g.used ??= { tool: [...new Set(g.tool)].sort((a, b) => a - b), feature: [...new Set(g.feature)].sort((a, b) => a - b) };
    const used = byTool ? g.used.tool : g.used.feature;
    $('#sl-legend').innerHTML = byTool
      ? used.map((t) => `<div class="li"><i style="background:${esc(filColor(fil[t] || t + 1))}"></i>Filament ${fil[t] || t + 1}</div>`).join('')
      : used.map((f) => `<button class="li ${S.hiddenFeatures.has(f) ? 'off' : ''}" data-feat="${f}" title="Show / hide"><i style="background:${FEATURES[f][1]}"></i>${FEATURES[f][0]}</button>`).join('');
    invalidate();
  }
}
export function setLayer(n) {
  const g = S.previewG;
  if (!g || !segMesh) return;
  n = Math.max(1, Math.min(g.layerEnd.length, n));
  S.layer = n;
  if (segMesh.userData.lines) segMesh.geometry.setDrawRange(0, g.layerEnd[n - 1] * 2);
  else segMesh.count = g.layerEnd[n - 1];
  $('#sl-layer').value = String(n);
  $('#sl-layer-n').textContent = n;
  $('#sl-layer-z').textContent = g.layerZ[n - 1].toFixed(2);
  invalidate();
}
