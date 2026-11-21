// Slicer page: what happens when you click or change something in the panels, and the
// "All settings" dialog. See slicer.js for the list of files.
import * as THREE from 'three';
import { S, F, $, esc } from './core.js';
import { minLayerHeight } from './memory.js';
import { savePrefs, useLoaded, loadPrinter, loadValues, pickPreset, loadPrefs, prefKey } from './profiles.js';
import { setPlate, addPlate, deletePlate, platesObjects, buildBed, moveToPlate, plateX } from './plates.js';
import {
  recolor, removeObject, select, autoOrient, arrange, duplicate, mirrorObject, overhangSet, freeObject, checkBounds, worldBox, dropToBed,
} from './objects.js';
import { frameObjects } from './import.js';
import { paintWhole, setPainting, applyOnce, finishStroke, updateClip } from './pointer.js';
import { fsMixNums, fsApply, fsUpdatePreview } from './fullspectrum.js';
import { setTool, undo, pushUndo } from './undo.js';
import { renderSide, applyMode } from './toolbar.js';
import { removeFilament, addFilament, isDirty, valueOf, tidyNumber, fmtVal, settingRow } from './panels.js';
import { usedPlates, saveProject } from './slice.js';
import { newProject, slice, sliceAll, sendToPrinter } from './project.js';
import { colorPreview } from './preview.js';

// ================================================================== events from the panel
export async function onClick(e) {
  const b = e.target.closest('button, [data-obj]');
  if (!b) return;
  const act = b.dataset.sl;
  if (b.dataset.plate !== undefined && b.closest('#sl-plates')) { setPlate(+b.dataset.plate, true, true); return; }
  if (b.dataset.tool) { setTool(b.dataset.tool); return; }
  if (b.dataset.ptool) { S.paint.tool = b.dataset.ptool; renderSide(); return; }
  if (b.dataset.pch) { S.paint.ch = b.dataset.pch; for (const o of S.objects) recolor(o); renderSide(); return; }
  if (b.dataset.pf !== undefined) { S.paint.filament = +b.dataset.pf; renderSide(); return; }
  if (b.dataset.fsmix !== undefined) { const n = +b.dataset.fsmix; const cur = fsMixNums().slice(); const i = cur.indexOf(n); if (i >= 0) { if (cur.length > 1) cur.splice(i, 1); } else cur.push(n); S.paint.fs.mix = cur.sort((x, y) => x - y); renderSide(); return; }
  if (b.dataset.pm !== undefined) { S.paint.mark = +b.dataset.pm; renderSide(); return; }
  if (b.dataset.pc) { S.previewColor = b.dataset.pc; savePrefs(); colorPreview(); renderSide(); return; }
  if (b.dataset.feat !== undefined) { const f = +b.dataset.feat; S.hiddenFeatures.has(f) ? S.hiddenFeatures.delete(f) : S.hiddenFeatures.add(f); colorPreview(); return; }
  if (b.dataset.reset) { delete S.overrides[b.dataset.reset]; changed(); return; }
  if (b.dataset.pmcopies) {
    const k = usedPlates().indexOf(S.plate) + 1, pr = S.pmStatus && (S.pmStatus.progress || []).find((x) => x.plate === k);
    if (pr && S.project) window.parent.postMessage({ ph: 'pm-copies', id: S.project.id, plate: k, copies: Math.max(0, pr.needed + +b.dataset.pmcopies) }, location.origin);
    return;
  }
  if (b.dataset.pmprinted) {
    const k = usedPlates().indexOf(S.plate) + 1;
    if (S.project && k) window.parent.postMessage({ ph: 'pm-printed', id: S.project.id, plate: k, done: b.dataset.pmprinted === '1' }, location.origin);
    return;
  }
  if (b.dataset.filDel !== undefined) { const k = +b.dataset.filDel + 1; if (F.confirmBox) F.confirmBox(`Remove filament ${k}?`, 'Parts painted or set to it go to filament 1.', 'Remove').then((ok) => ok && removeFilament(k)); else removeFilament(k); return; }
  if (b.dataset.objDel) { removeObject(S.objects.find((o) => o.id === +b.dataset.objDel)); return; }
  if (b.dataset.obj && !e.target.closest('select, button')) { if (S.mode === 'prepare') select(S.objects.find((o) => o.id === +b.dataset.obj)); return; }
  const o = S.selected;
  switch (act) {
    case 'add': $('#sl-file').click(); break;
    case 'orient': autoOrient(o); break;
    case 'arrange': arrange(S.plate); break;
    case 'copy': duplicate(o); break;
    case 'mirror': mirrorObject(o); break;
    case 'del': removeObject(o); break;
    case 'undo': undo(); break;
    case 'view': frameObjects(); break;
    case 'addplate': addPlate(); break;
    case 'delplate': deletePlate(S.plate); break;
    case 'loaded': useLoaded(); break;
    case 'addfil': addFilament(); break;
    case 'save': saveProject(); break;
    case 'ovall': for (const i of usedPlates()) if (i !== S.plate) S.plateOv[i] = { ...S.overrides }; F.toast('Every plate now has these settings.'); renderSide(); break;
    case 'pmprint': {
      const k = usedPlates().indexOf(S.plate) + 1;
      if (k && S.project) window.parent.postMessage({ ph: 'pm-print', id: S.project.id, plate: k, dirty: isDirty() || !S.project.savedAt }, location.origin);
      break;
    }
    case 'newproj': newProject(); break;
    case 'remerge': delete S.merge[S.plate]; slice(S.plate); break;
    case 'all': renderAll(); $('#sl-all-dialog').showModal(); break;
    case 'slice': slice(S.plate); break;
    case 'sliceall': sliceAll(); break;
    case 'print': sendToPrinter(S.plate); break;
    case 'retry': loadPrinter(F.current().id); break;
    case 'fillobj': if (o) paintWhole(o, 'color', () => 0, S.paint.filament || o.filament); break;
    case 'fsapply': if (o) await fsApply(o); break;
    case 'clearpaint': if (o) paintWhole(o, S.paint.ch, () => 0); break;
    case 'remap': {
      const from = +$('[data-premap="from"]').value, to = +$('[data-premap="to"]').value;
      if (o) paintWhole(o, 'color', (v) => ((v || o.filament) === from ? to : v), o.filament === from ? to : 0);
      break;
    }
    case 'supon':
      S.overrides.enable_support = '1';
      if (!/manual/.test(String(valueOf('support_type')))) S.overrides.support_type = String(valueOf('support_type')).startsWith('tree') ? 'tree(manual)' : 'normal(manual)';
      changed(); F.toast('Supports on: only where you painted them (Prepare → Quality → Support type).'); break;
    case 'enforceoh': if (o) { const set = overhangSet(o); paintWhole(o, 'support', (v, t) => (set[t] && v === 0 ? 1 : v)); } break;
    case 'gapnow': if (o) { setPainting({ obj: o, ch: S.paint.ch, value: 0, changed: new Map() }); await applyOnce({ object: o.mesh, faceIndex: 0 }); finishStroke(); } break;
    case 'clear':
      if (!(await F.confirmBox(`Clear plate ${S.plate + 1}?`, 'All models on this plate are removed.', 'Clear'))) return;
      for (const x of platesObjects(S.plate)) freeObject(x);
      S.objects = S.objects.filter((x) => x.plate !== S.plate); S.undo = []; select(null); delete S.res[S.plate]; checkBounds(); buildBed(); renderSide(); break;
    default:
  }
}

export function onInput(e) {
  const t = e.target;
  if (t.dataset.pset) {
    const v = +t.value;
    S.paint[t.dataset.pset] = v;
    const show = $(`[data-show="${t.dataset.pset}"]`);
    if (show) show.textContent = v + (t.dataset.unit || '');
    if (t.dataset.pset === 'clip') updateClip();
    if (t.dataset.pset === 'ohAngle' && S.paint.overhang) for (const o of platesObjects(S.plate)) recolor(o);
  }
  if (t.dataset.filColor !== undefined) {
    S.filaments[+t.dataset.filColor].color = t.value;
    for (const o of S.objects) recolor(o);
    if (S.res[S.plate] && S.res[S.plate].g) colorPreview();
  }
  if (t.dataset.fstarget !== undefined) { S.paint.fs.target = t.value; fsUpdatePreview(); }
}

export async function onChange(e) {
  const t = e.target;
  const set = t.dataset.set;
  if (set === 'model') { await loadPrinter(S.printerId, '&model=' + encodeURIComponent(t.value)); applyMode(); return; }
  if (set === 'machine') { savePrefsMachine(t.value); await loadPrinter(S.printerId); return; }
  if (set === 'bed') { S.bedType = t.value; changed(false); return; }
  if (set === 'infill') { S.infillFil = +t.value; changed(false); return; }
  if (set === 'light') { S.light = t.value; changed(false); return; }
  if (set === 'process') { S.process = t.value; S.loading = true; renderSide(); await loadValues(); S.loading = false; changed(); return; }
  if (set === 'paintobj') { S.selected = S.objects.find((o) => o.id === +t.value) || null; renderSide(); return; }
  if (t.dataset.pbool) { S.paint[t.dataset.pbool] = t.checked; if (t.dataset.pbool === 'overhang') for (const o of S.objects) recolor(o); renderSide(); return; }
  if (set === 'layoutprinter') { if (F.usePrinter(t.value)) await loadPrinter(t.value); return; }
  if (t.dataset.filMat !== undefined) { S.filaments[+t.dataset.filMat].preset = pickPreset(t.value); changed(false); return; }
  if (t.dataset.filPreset !== undefined) { S.filaments[+t.dataset.filPreset].preset = t.value; changed(false); return; }
  if (t.dataset.filColor !== undefined) { savePrefs(); renderSide(); return; }
  if (t.dataset.objFil) { const o = S.objects.find((x) => x.id === +t.dataset.objFil); o.filament = +t.value; recolor(o); renderSide(); return; }
  if (t.dataset.objPlate) { const o = S.objects.find((x) => x.id === +t.dataset.objPlate); moveToPlate(o, +t.value); return; }
  if (t.dataset.ov) {
    const key = t.dataset.ov;
    if (t.value === '__other') { S.customLayer = true; renderSide(); return; } // layer height: show a box to type in
    let v = t.type === 'checkbox' ? (t.checked ? '1' : '0') : t.value;
    if (t.dataset.pct !== undefined) v = v + '%';
    v = tidyNumber(v);
    if (key === 'layer_height') {
      // typed in the "Other…" box: back to the dropdown, which then lists this value too
      S.customLayer = false;
      if (!(+v > 0)) { F.toast('Layer height must be a number above 0 (for example 0.15).', true); renderSide(); return; }
      v = String(+(+v).toFixed(3));
      const min = platesObjects(S.plate).length ? minLayerHeight(S.plate) : 0;
      if (min && +v < min - 1e-9) { F.toast(`${v} mm is too fine for this plate on PrintHarbor (not enough memory): set to ${min.toFixed(2)} mm, the finest it allows.`, true); v = String(min); }
    }
    if (String(fmtVal(S.values[key])) === v) delete S.overrides[key]; else S.overrides[key] = v;
    changed();
    return;
  }
  if (t.dataset.xf) { applyXf(t.dataset.xf, +t.value); }
}
function savePrefsMachine(m) { const p = loadPrefs(S.printerId); p.machine = m; try { localStorage.setItem(prefKey(S.printerId), JSON.stringify(p)); } catch (_) { /* */ } }
function changed(rerender = true) {
  savePrefs();
  if (rerender) renderSide();
  if ($('#sl-all-dialog').open) renderAll();
}

function applyXf(k, v) {
  const o = S.selected;
  if (!o || !Number.isFinite(v)) return;
  pushUndo({ type: 'xf', obj: o, m: o.mesh.matrix.clone() });
  const m = o.mesh;
  const b = worldBox(o);
  if (k === 'x') m.position.x += v + plateX(o.plate) - (b.min.x + b.max.x) / 2;
  if (k === 'y') m.position.y += v - (b.min.y + b.max.y) / 2;
  if (k === 's') m.scale.setScalar(Math.max(0.01, v / 100));
  if (k === 'sx' || k === 'sy' || k === 'sz') { // a size in mm: scale evenly so that side gets it
    const cur = k === 'sx' ? b.max.x - b.min.x : k === 'sy' ? b.max.y - b.min.y : b.max.z - b.min.z;
    if (cur > 1e-6 && v > 0) m.scale.multiplyScalar(Math.max(0.01, m.scale.x * v / cur) / m.scale.x);
  }
  if (k === 'rx' || k === 'ry' || k === 'rz') {
    const e = new THREE.Euler().setFromQuaternion(m.quaternion, 'XYZ');
    e[k[1]] = THREE.MathUtils.degToRad(v);
    m.quaternion.setFromEuler(e);
  }
  m.updateMatrixWorld(true);
  dropToBed(o); checkBounds(); renderSide();
}

// ================================================================== all settings dialog
export function renderAll() {
  const list = $('#sl-all-list');
  if (!S.schema) return;
  const q = $('#sl-all-search').value.trim().toLowerCase();
  const onlyChanged = $('#sl-all-changed').checked;
  const groups = new Map();
  for (const s of S.schema) {
    if (s.key === 'curr_bed_type') continue;
    if (onlyChanged && S.overrides[s.key] === undefined) continue;
    if (q && !(`${s.label} ${s.key} ${s.category || ''} ${s.tooltip || ''}`.toLowerCase().includes(q))) continue;
    const g = s.category || 'Other';
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(s);
  }
  const order = ['Quality', 'Strength', 'Speed', 'Support', 'Others', 'Advanced'];
  const names = [...groups.keys()].sort((a, b) => (order.indexOf(a) + 1 || 99) - (order.indexOf(b) + 1 || 99));
  list.innerHTML = names.map((g) => `<h3>${esc(g)}</h3>${groups.get(g).sort((a, b) => (a.mode === 'simple' ? 0 : 1) - (b.mode === 'simple' ? 0 : 1) || a.label.localeCompare(b.label)).map((s) => settingRow(s, s.label, true)).join('')}`).join('')
    || '<p class="muted">No setting matches.</p>';
}
