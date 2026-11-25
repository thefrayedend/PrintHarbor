// Slicer page: what the side panel shows (printer, plates, filaments, paint tools, settings).
// See slicer.js for the list of files.
import * as THREE from 'three';
import * as P from './paint.js';
import { F, S, esc, SIMPLE, LIGHT_AT, $, ENFORCE, BLOCK } from './core.js';
import { minLayerHeight, plateMemory, memBudget } from './memory.js';
import { MAX_FILAMENTS, filColor, pickPreset, savePrefs } from './profiles.js';
import { bed, invalidate } from './scene.js';
import { platesObjects, renderPlateBar, plateX, buildBed } from './plates.js';
import { recolor, worldBox } from './objects.js';
import { fsCard } from './fullspectrum.js';
import { renderSide, PAINT_TOOLS, ic } from './toolbar.js';
import { usedPlates, plateKey, toolFilaments } from './slice.js';

// ================================================================== project mode panels
const MATERIALS = ['PLA', 'PETG', 'ABS', 'ASA', 'TPU', 'PA', 'PC', 'PVA', 'HIPS'];
const famOf = (preset) => F.family(String(preset || 'PLA').replace(/^(Flashforge|Generic|FusRock|Creality|Bambu|Qidi|Snapmaker|Elegoo|Sovol)\s+/i, ''));
/** The printer whose bed the plates are laid out on (any printer can print them later). */
function layoutCard() {
  const i = S.info, list = F.printers();
  const nozzle = (n) => (/(\d\.\d+)\s*(HF\s*)?nozzle/i.exec(n) || [n, n])[0];
  const b = bed();
  return `<div class="card"><h2>Lay out for</h2>
    <div class="row gap">
      <label class="field" style="flex:1.4"><span class="label">Printer bed</span><select data-set="layoutprinter">${list.map((p) => `<option value="${esc(p.id)}" ${p.id === S.printerId ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}</select></label>
      <label class="field" style="flex:1"><span class="label">Nozzle</span><select data-set="machine">${i.nozzles.map((n) => `<option value="${esc(n)}" ${n === i.machine ? 'selected' : ''}>${esc(nozzle(n))}</option>`).join('')}</select></label>
    </div>
    <p class="hint">Plates are arranged on this ${Math.round(b.w)} × ${Math.round(b.d)} mm bed. Any printer the parts fit on can print them: when you send a plate to another printer it is re-arranged on that printer's bed, with your painting, colours and settings.</p></div>`;
}
/** Colours used in the project: a colour and a material each (no printer profiles). */
export function coloursCard() {
  const many = S.filaments.length > 1;
  return `<div class="card"><h2>Colours</h2>
    <div class="sl-fils">${S.filaments.map((f, i) => { const fam = famOf(f.preset); const mats = MATERIALS.includes(fam) ? MATERIALS : [fam, ...MATERIALS];
      return `<div class="sl-fil"><span class="n">${i + 1}</span>
      <input type="color" value="${esc(f.color)}" data-fil-color="${i}" title="Colour ${i + 1}">
      <select data-fil-mat="${i}" title="Material">${mats.map((m) => `<option ${m === fam ? 'selected' : ''}>${esc(m)}</option>`).join('')}</select>
      ${many ? `<button class="btn tiny ghost" data-fil-del="${i}" title="Remove colour ${i + 1} (its parts go to colour 1)">×</button>` : ''}</div>`; }).join('')}</div>
    <div class="row gap wrap" style="align-items:center;margin-top:6px">${S.filaments.length < MAX_FILAMENTS ? '<button class="btn tiny" data-sl="addfil">+ Add colour</button>' : ''}
      <span class="small muted">When you print, you match these to the spools loaded in the printer.</span></div></div>`;
}
/** Shorten a quality-profile name for the dropdown: drop the " @Printer (nozzle)" tail. */
const prettyProcess = (n) => String(n || '').replace(/\s*@.*$/, '').trim() || n;
// The print settings shown right where you print, below "Lay out for" — the same
// set FlashBridge shows in its Quality card: the quality profile, every basic
// setting (SIMPLE), the infill filament and Lighter G-code, with "All settings"
// for everything else. Raft is added (0 = none) because it is the one that
// quietly turns itself on.
export function printSettingsCard() {
  if (!S.info || !S.info.known) return '';
  const procs = S.info.processes || [];
  const byKey = new Map((S.schema || []).map((x) => [x.key, x]));
  const nChanged = Object.keys(S.overrides).filter((k) => k !== 'curr_bed_type').length;
  const quality = procs.length
    ? `<label class="field"><span class="label">Quality profile</span><select data-set="process">${procs.map((n) => `<option value="${esc(n)}" ${n === S.process ? 'selected' : ''}>${esc(prettyProcess(n))}</option>`).join('')}</select></label>`
    : '';
  const rows = [...SIMPLE, ['raft_layers', 'Raft (layers, 0 = none)']];
  return `<div class="card"><h2>Print settings <button class="btn tiny" data-sl="all">All settings${nChanged ? ` (${nChanged} changed)` : ''}</button></h2>
      ${quality}
      ${rows.map(([k, l]) => byKey.has(k) ? settingRow(byKey.get(k), l) : '').join('')}
      <div class="sl-set ${S.infillFil ? 'changed' : ''}"><span class="nm" title="Print the inside of every model with one colour (for example cheap white), also on multi-colour prints. The outside keeps its colours as a skin about 3 mm thick."><span>Infill colour</span></span>
        <select data-set="infill"><option value="0">Same as the model</option>${S.filaments.map((f, k) => `<option value="${k + 1}" ${S.infillFil === k + 1 ? 'selected' : ''}>Colour ${k + 1}</option>`).join('')}</select></div>
      <div class="sl-set ${S.light !== 'auto' ? 'changed' : ''}"><span class="nm" title="Very detailed models (scans, sculpts) make millions of tiny moves. Lighter G-code keeps the paths to 0.05 mm and writes straight moves instead of arcs: about a third less memory when slicing. Auto: only on plates with ${LIGHT_AT / 1000}k triangles or more."><span>Lighter G-code</span></span>
        <select data-set="light"><option value="auto" ${S.light === 'auto' ? 'selected' : ''}>Auto (detailed plates)</option><option value="on" ${S.light === 'on' ? 'selected' : ''}>Always</option><option value="off" ${S.light === 'off' ? 'selected' : ''}>Never</option></select></div>
      <p class="hint">These go with the plate to whichever printer prints it. Values shown are ${esc(S.info.name)}'s profile defaults — change any before you print. Everything else (temperatures, speeds, retraction …) comes from the printer's own profile.</p>
    </div>`;
}
export function platesPanel() {
  const sel = S.selected;
  const list = platesObjects(S.plate);
  return `${layoutCard()}
    ${printSettingsCard()}
    <div class="card"><h2>Plate ${S.plate + 1}${S.nPlates > 1 ? ` <span class="muted small">of ${S.nPlates}</span>` : ''}
        <span class="row gap"><button class="btn tiny" data-sl="addplate" title="Add an empty plate">+ Plate</button>${S.nPlates > 1 ? '<button class="btn tiny ghost-danger" data-sl="delplate" title="Delete this plate">Delete</button>' : ''}</span></h2>
      ${list.length ? `<div class="sl-objs">${list.map((o) => `<div class="sl-obj ${o === sel ? 'sel' : ''}" data-obj="${o.id}">
        ${F.swatch(filColor(o.filament), 'sm')}<span class="nm" title="${esc(o.name)}">${esc(o.name)} ${o.outside ? '<span class="warn">off the plate</span>' : ''}</span>
        <select data-obj-fil="${o.id}" title="Colour">${S.filaments.map((_, i) => `<option value="${i + 1}" ${o.filament === i + 1 ? 'selected' : ''}>${i + 1}</option>`).join('')}</select>
        <button data-obj-del="${o.id}" title="Delete">✕</button></div>`).join('')}</div>
        ${sel && sel.plate === S.plate ? `${xfFields(sel)}<label class="field" style="margin-top:8px"><span class="label">Move to</span><select data-obj-plate="${sel.id}">${plateOptions(sel.plate)}</select></label>` : ''}`
        : `<p class="muted small">${S.objects.length ? 'This plate is empty. Drag a model here, or pick one on another plate and use "Move to".' : 'Nothing on the plate yet. Drop STL, OBJ or 3MF files on the 3D view, or use Add model.'}</p>`}
      <div class="row gap wrap" style="margin-top:8px"><button class="btn small" data-sl="add">Add model</button>${list.length ? '<button class="btn small" data-sl="arrange">Arrange</button><button class="btn small ghost-danger" data-sl="clear">Clear plate</button>' : ''}${S.objects.length ? '<button class="btn small" data-sl="save" title="Download everything (all plates, positions, colours and painting) as a 3MF file you can open in OrcaSlicer or Bambu Studio">Download 3MF</button>' : ''}</div>
    </div>
    ${coloursCard()}
    ${pmFooter()}`;
}
export function settingsPanel() {
  const byKey = new Map((S.schema || []).map((x) => [x.key, x]));
  const nChanged = Object.keys(S.overrides).filter((k) => k !== 'curr_bed_type').length;
  const others = usedPlates().filter((i) => i !== S.plate).length;
  return `<div class="card"><h2>Plate ${S.plate + 1} settings <button class="btn tiny" data-sl="all">All settings${nChanged ? ` (${nChanged} changed)` : ''}</button></h2>
      ${SIMPLE.map(([k, l]) => byKey.has(k) ? settingRow(byKey.get(k), l) : '').join('')}
      <div class="sl-set ${S.infillFil ? 'changed' : ''}"><span class="nm" title="Print the inside of every model with one colour (for example cheap white), also on multi-colour prints. The outside keeps its colours as a skin about 3 mm thick."><span>Infill colour</span></span>
        <select data-set="infill"><option value="0">Same as the model</option>${S.filaments.map((f, k) => `<option value="${k + 1}" ${S.infillFil === k + 1 ? 'selected' : ''}>Colour ${k + 1}</option>`).join('')}</select></div>
      <div class="sl-set ${S.light !== 'auto' ? 'changed' : ''}"><span class="nm" title="Very detailed models (scans, sculpts) make millions of tiny moves. Lighter G-code keeps the paths to 0.05 mm and writes straight moves instead of arcs: about a third less memory when slicing. Auto: only on plates with ${LIGHT_AT / 1000}k triangles or more."><span>Lighter G-code</span></span>
        <select data-set="light"><option value="auto" ${S.light === 'auto' ? 'selected' : ''}>Auto (detailed plates)</option><option value="on" ${S.light === 'on' ? 'selected' : ''}>Always</option><option value="off" ${S.light === 'off' ? 'selected' : ''}>Never</option></select></div>
      ${others ? `<div class="row gap wrap" style="margin-top:10px"><button class="btn small" data-sl="ovall" title="Give every plate exactly these settings">Use these settings on all plates</button></div>` : ''}
      <p class="hint">These go with the plate to whichever printer prints it. Everything you leave alone (temperatures, speeds, retraction …) comes from that printer's own profile. Values shown are ${esc(S.info.name)}'s defaults.</p>
    </div>
    ${pmFooter()}`;
}
/** Mark printed / Unmark. A plate printed for real and checked needs neither. */
function pmMarkButton(pr) {
  if (!pr.needed) return '';
  if (pr.manual > 0) return `<button class="btn tiny" data-pmprinted="0" title="Undo marking this plate printed by hand">Unmark</button>`;
  if (pr.accepted >= pr.needed) return '';
  const tip = pr.review ? 'It finished printing: mark it printed (the parts came out fine)' : 'Mark this plate printed, without sending it to a printer';
  return `<button class="btn tiny" data-pmprinted="1" title="${tip}">Mark printed</button>`;
}
/** Bottom of the panel: how this plate is doing, and Print. */
export function pmFooter() {
  const k = usedPlates().indexOf(S.plate) + 1;
  const pr = S.pmStatus && k ? (S.pmStatus.progress || []).find((x) => x.plate === k) : null;
  const list = platesObjects(S.plate);
  const dis = !list.length || list.some((o) => o.outside);
  const line = pr ? `<div class="pm-track"><span class="copies"><button class="btn tiny" data-pmcopies="-1" title="Fewer copies">−</button> ×${pr.needed} <button class="btn tiny" data-pmcopies="1" title="More copies">+</button></span>
      <span class="small ${pr.accepted >= pr.needed && pr.needed ? 'printed-text' : 'muted'}" style="white-space:nowrap" title="${pr.accepted} of ${pr.needed} printed and done">${pr.accepted >= pr.needed && pr.needed ? '✓ Printed' : '✓'} ${pr.accepted}/${pr.needed}${pr.printing ? ` · ${pr.printing} printing` : ''}${pr.review ? ` · ${pr.review} finished, not checked yet` : ''}${pr.failed ? ` · ${pr.failed} failed` : ''}</span>
      ${pmMarkButton(pr)}</div>` : '';
  return `<div class="sl-go">${line}<button class="btn primary big" data-sl="pmprint" ${dis ? 'disabled' : ''}>Print plate ${S.plate + 1}…</button>
    ${list.some((o) => o.outside) ? '<p class="hint" style="text-align:center">Move everything onto the plate first.</p>' : ''}
    <div class="sl-source">OrcaSlicer engine (AGPL-3.0) · <a href="https://github.com/Helio-Additive/slicer-cli" target="_blank" rel="noopener">source</a></div></div>`;
}
export function setTab(tab) { S.tab = tab === 'settings' ? 'settings' : 'plates'; renderSide(); }
export function setStatus(st) { S.pmStatus = st; buildBed(); if (S.pm) renderSide(); } // buildBed redraws the plate bar too
export function setProjectMode(on) { S.pm = !!on; }
/** Unsaved changes: the project as it would be saved, in short. */
function projectSig() {
  if (!S.info) return '';
  return JSON.stringify([S.filaments, S.infillFil || 0, S.light, usedPlates().map((i) => plateKey(i))]);
}
export function markSaved() { S.savedSig = projectSig(); }
export function isDirty() { return !!S.project && S.savedSig !== '' && projectSig() !== S.savedSig; }
let lastDirty = false;
setInterval(() => {
  if (!S.pm || !S.project || document.hidden) return;
  const d = isDirty();
  if (d !== lastDirty) { lastDirty = d; window.parent.postMessage({ ph: 'pm-dirty', id: S.project.id, dirty: d }, location.origin); }
}, 1500);

function printerCard() {
  const i = S.info;
  const nozzle = (n) => (/(\d\.\d+)\s*(HF\s*)?nozzle/i.exec(n) || [n, n])[0];
  const bedEnum = (S.schema || []).find((s) => s.key === 'curr_bed_type');
  return `<div class="card"><h2><span class="sl-printer">${esc(i.model)}</span>
      <span class="lock" title="Only profiles of the open printer can be used">🔒 ${esc(i.name)}</span></h2>
    <div class="row gap">
      <label class="field" style="flex:1"><span class="label">Nozzle</span><select data-set="machine">${i.nozzles.map((n) => `<option value="${esc(n)}" ${n === i.machine ? 'selected' : ''}>${esc(nozzle(n))}</option>`).join('')}</select></label>
      <label class="field" style="flex:1.4"><span class="label">Plate type</span><select data-set="bed">${(bedEnum ? bedEnum.enum : [{ value: S.bedType, label: S.bedType }]).map((e) => `<option value="${esc(e.value)}" ${e.value === S.bedType ? 'selected' : ''}>${esc(e.label)}</option>`).join('')}</select></label>
    </div></div>`;
}

export function filamentsCard(withPresets = true) {
  const p = F.current();
  const heads = headCount();
  const many = S.filaments.length > 1;
  return `<div class="card"><h2>Filaments ${(p && p.slots && p.slots.length) ? '<button class="btn tiny" data-sl="loaded" title="Give filaments 1–' + p.slots.length + ' the spools loaded in the printer">Use loaded</button>' : ''}</h2>
    <div class="sl-fils">${S.filaments.map((f, i) => `<div class="sl-fil"><span class="n">${i + 1}</span>
      <input type="color" value="${esc(f.color)}" data-fil-color="${i}" title="Colour of filament ${i + 1}">
      ${withPresets ? `<select data-fil-preset="${i}">${S.info.filaments.map((x) => `<option ${x === f.preset ? 'selected' : ''}>${esc(x)}</option>`).join('')}</select>` : `<span class="small">${esc(f.preset.replace(/ @.*$/, ''))}</span>`}
      ${many ? `<button class="btn tiny ghost" data-fil-del="${i}" title="Remove filament ${i + 1} (its parts go to filament 1)">×</button>` : ''}</div>`).join('')}</div>
    <div class="row gap wrap" style="align-items:center;margin-top:6px">${S.filaments.length < MAX_FILAMENTS ? '<button class="btn tiny" data-sl="addfil">+ Add filament</button>' : ''}
      <span class="small muted">${S.filaments.length > heads ? `Each plate can use any ${heads} of these (the printer has ${heads} head${heads === 1 ? '' : 's'}); plates may use different ones.` : ''}</span></div></div>`;
}
/** Heads (tools / filament slots) the printer can print with in one go. */
export function headCount() {
  const p = F.current();
  const slots = (p && p.id === S.printerId && p.slots && p.slots.length) || 0;
  const heads = (S.info && S.info.extruders) || 1;
  if (slots) return Math.max(heads, slots);
  return S.info && S.info.singleNozzleMulti ? Math.max(heads, 4) : heads;
}
export function addFilament(render = true) {
  if (S.filaments.length >= MAX_FILAMENTS) return;
  const palette = ['#E8E8E8', '#202020', '#D32F2F', '#1E63D6', '#2E7D32', '#F9A825', '#8E24AA', '#EF6C00', '#00897B', '#6D4C41', '#C2185B', '#546E7A'];
  const color = palette.find((c) => !S.filaments.some((f) => f.color.toUpperCase() === c)) || '#888888';
  S.filaments.push({ color, preset: (S.filaments[0] && S.filaments[0].preset) || pickPreset('PLA') });
  savePrefs();
  if (render) renderSide();
}
export function removeFilament(k) { // k: 1-based
  if (S.filaments.length <= 1) return;
  const fix = (v) => (v === k ? 1 : v > k ? v - 1 : v);
  for (const o of S.objects) {
    o.filament = fix(o.filament);
    const st = o.states;
    for (let i = 0; i < st.length; i++) if (st[i]) st[i] = fix(st[i]);
  }
  S.filaments.splice(k - 1, 1);
  if (S.infillFil) S.infillFil = S.infillFil === k ? 0 : S.infillFil > k ? S.infillFil - 1 : S.infillFil;
  if (S.paint.filament > S.filaments.length) S.paint.filament = 1;
  S.undo = S.undo.filter((u) => u.type !== 'paint'); // paint undo records use the old numbering
  savePrefs();
  for (const o of S.objects) recolor(o);
  renderSide(); invalidate();
}

export const fmtVal = (v) => (Array.isArray(v) ? v[0] : v);
/** Plate i's setting changes (a new plate starts with the current plate's). */
export function ovOf(i) {
  if (i === S.plate) { S.plateOv[i] = S.overrides; return S.overrides; }
  if (!S.plateOv[i]) S.plateOv[i] = { ...S.overrides };
  return S.plateOv[i];
}
/** Makes plate i's settings the current ones (S.overrides), without touching the others. */
export function useOv(i) {
  if (!S.plateOv[i]) S.plateOv[i] = { ...S.overrides };
  S.overrides = S.plateOv[i];
}
/** Runs fn with plate i's settings as the current ones (valueOf … read them). */
export async function withPlateSettings(i, fn) {
  const cur = ovOf(S.plate), mine = ovOf(i);
  S.overrides = mine;
  try { return await fn(); } finally { S.overrides = cur; }
}
export function valueOf(key) { return S.overrides[key] !== undefined ? S.overrides[key] : (key === 'curr_bed_type' ? S.bedType : fmtVal(S.values[key])); }
export function settingRow(s, label, tip = false) {
  const key = s.key, v = String(valueOf(key) ?? ''), changed = S.overrides[key] !== undefined;
  let input, tipLine = '';
  if (s.type === 'bool') input = `<input type="checkbox" data-ov="${key}" ${v === '1' || v === 'true' ? 'checked' : ''}>`;
  else if (s.type === 'enum') input = `<select data-ov="${key}">${s.enum.map((e) => `<option value="${esc(e.value)}" ${e.value === v ? 'selected' : ''}>${esc(e.label)}</option>`).join('')}</select>`;
  else if (s.type === 'percent') input = `<span class="unit-in"><input type="number" data-ov="${key}" data-pct value="${esc(v.replace('%', ''))}" ${s.min !== undefined ? `min="${s.min}"` : ''} ${s.max !== undefined ? `max="${s.max}"` : ''} step="${stepFor(v, 1)}"><span>%</span></span>`;
  else if (key === 'layer_height' && !S.customLayer) {
    // the usual heights for this nozzle, plus "Other…" for anything else
    const cur = +v, list = layerHeights();
    if (cur && !list.includes(cur)) list.push(cur);
    list.sort((a, b) => a - b);
    // heights PrintHarbor cannot slice this plate at (memory) are shown but cannot be picked
    const min = platesObjects(S.plate).length ? minLayerHeight(S.plate) : 0;
    const tooFine = (h) => min === null || (min && h < min - 1e-9);
    input = `<span class="unit-in"><select data-ov="${key}">${list.map((h) => `<option value="${h}" ${h === cur ? 'selected' : ''} ${tooFine(h) && h !== cur ? 'disabled' : ''}>${h.toFixed(2)}${tooFine(h) ? ' – too fine' : ''}</option>`).join('')}<option value="__other">Other…</option></select><span>mm</span></span>`;
    if (min !== 0) {
      const est = plateMemory(S.plate, cur || 0.2);
      tipLine = min === null
        ? `<span class="tip mem warn">This plate needs more memory than PrintHarbor has at any layer height${/^(1|true)$/.test(String(valueOf('enable_support'))) ? ': paint supports only where needed, or' : ':'} put fewer models on it.</span>`
        : `<span class="tip mem ${tooFine(cur) ? 'warn' : ''}">Finest for this plate: ${min.toFixed(2)} mm (memory). This setting needs about ${est.toFixed(1)} of ${memBudget().toFixed(1)} GB.</span>`;
    }
  } else if (s.type === 'int' || s.type === 'float') input = `<span class="unit-in"><input type="number" data-ov="${key}" value="${esc(v)}" ${s.min !== undefined ? `min="${s.min}"` : ''} ${s.max !== undefined && s.max < 1e6 ? `max="${s.max}"` : ''} step="${s.type === 'int' ? 1 : stepFor(v)}" ${key === 'layer_height' ? 'autofocus' : ''}><span>${esc(s.unit || '')}</span></span>`;
  else input = `<input data-ov="${key}" value="${esc(v)}" title="${esc(s.unit || '')}">`;
  return `<div class="sl-set ${changed ? 'changed' : ''}"><span class="nm" title="${esc(s.tooltip || '')}"><span>${esc(label || s.label)}</span>${changed ? `<button class="reset" data-reset="${key}" title="Back to the profile's value">↺</button>` : ''}</span>${input}${tip && s.tooltip ? `<span class="tip">${esc(s.tooltip)}</span>` : ''}${tipLine}</div>`;
}

/** Numbers as people type them: ".08" -> "0.08", "-.5%" -> "-0.5%", "2." -> "2". Anything else unchanged. */
export function tidyNumber(v) {
  const m = /^(-?)(\d*)(\.\d*)?(%?)$/.exec(v.trim());
  if (!m || (!m[2] && (m[3] || '').length < 2)) return v;
  return m[1] + (m[2] || '0') + (m[3] === '.' ? '' : m[3] || '') + m[4];
}
/** Arrow step for a decimal field: the precision the value is written in (0.8 → 0.1, 0.45 → 0.01). */
function stepFor(v, whole = 0.1) { const d = (String(v).split('.')[1] || '').length; return d ? +(10 ** -d).toFixed(d) : whole; }
/** Nozzle size (mm) of the selected machine profile, from its name ("0.4 nozzle"). */
export function nozzleSize() { const m = /(\d\.\d+)\s*(HF\s*)?nozzle/i.exec(S.info && S.info.machine || ''); return m ? +m[1] : 0.4; }
const LAYER_HEIGHTS = { 0.2: [0.05, 0.06, 0.08, 0.1, 0.12, 0.14], 0.25: [0.06, 0.08, 0.1, 0.12, 0.16, 0.18], 0.4: [0.08, 0.1, 0.12, 0.16, 0.2, 0.24, 0.28], 0.6: [0.12, 0.16, 0.2, 0.24, 0.3, 0.36, 0.42], 0.8: [0.2, 0.24, 0.3, 0.4, 0.5, 0.6] };
/** The usual layer heights for the selected nozzle (a quarter to three quarters of its size). */
function layerHeights() {
  const n = nozzleSize();
  const list = LAYER_HEIGHTS[n] ? LAYER_HEIGHTS[n].slice() : [];
  if (!list.length) for (let h = Math.round(n * 25) / 100; h <= n * 0.75 + 1e-9; h = +(h + 0.04).toFixed(2)) list.push(h);
  const def = +fmtVal(S.values.layer_height);
  if (def && !list.includes(def)) list.push(def);
  return list;
}
const plateOptions = (cur) => Array.from({ length: S.nPlates }, (_, i) => `<option value="${i}" ${i === cur ? 'selected' : ''}>Plate ${i + 1}</option>`).join('') + `<option value="${S.nPlates}">New plate</option>`;
export function preparePanel() {
  const byKey = new Map((S.schema || []).map((s) => [s.key, s]));
  const nChanged = Object.keys(S.overrides).filter((k) => k !== 'curr_bed_type').length;
  const sel = S.selected;
  const list = platesObjects(S.plate);
  return `${printerCard()}${filamentsCard()}
    <div class="card"><h2>Quality <button class="btn tiny" data-sl="all">All settings${nChanged ? ` (${nChanged} changed)` : ''}</button></h2>
      <label class="field"><span class="label">Profile</span><select data-set="process">${S.info.processes.map((p) => `<option ${p === S.process ? 'selected' : ''}>${esc(p)}</option>`).join('')}</select></label>
      ${SIMPLE.map(([k, l]) => byKey.has(k) ? settingRow(byKey.get(k), l) : '').join('')}
      <div class="sl-set ${S.infillFil ? 'changed' : ''}"><span class="nm" title="Print the inside of every model with one filament (for example cheap white), also on multi-colour prints. The outside keeps its colours as a skin about 3 mm thick."><span>Infill filament</span></span>
        <select data-set="infill"><option value="0">Same as the model</option>${S.filaments.map((f, k) => `<option value="${k + 1}" ${S.infillFil === k + 1 ? 'selected' : ''}>Filament ${k + 1}</option>`).join('')}</select></div>
      <div class="sl-set ${S.light !== 'auto' ? 'changed' : ''}"><span class="nm" title="Very detailed models (scans, sculpts) make millions of tiny moves, and PrintHarbor runs out of memory writing them. Lighter G-code keeps the paths to 0.05 mm (an eighth of the nozzle, invisible on the print) and writes straight moves instead of arcs: about a third less memory. Auto: only on plates with ${LIGHT_AT / 1000}k triangles or more."><span>Lighter G-code</span></span>
        <select data-set="light"><option value="auto" ${S.light === 'auto' ? 'selected' : ''}>Auto (detailed plates)</option><option value="on" ${S.light === 'on' ? 'selected' : ''}>Always</option><option value="off" ${S.light === 'off' ? 'selected' : ''}>Never</option></select></div>
    </div>
    <div class="card"><h2>Plate ${S.plate + 1}${S.nPlates > 1 ? ` <span class="muted small">of ${S.nPlates}</span>` : ''}
        <span class="row gap"><button class="btn tiny" data-sl="addplate" title="Add an empty plate">+ Plate</button>${S.nPlates > 1 ? '<button class="btn tiny ghost-danger" data-sl="delplate" title="Delete this plate">Delete</button>' : ''}</span></h2>
      ${list.length ? `<div class="sl-objs">${list.map((o) => `<div class="sl-obj ${o === sel ? 'sel' : ''}" data-obj="${o.id}">
        ${F.swatch(filColor(o.filament), 'sm')}<span class="nm" title="${esc(o.name)}">${esc(o.name)} ${o.outside ? '<span class="warn">off the plate</span>' : ''}</span>
        <select data-obj-fil="${o.id}" title="Filament">${S.filaments.map((_, i) => `<option value="${i + 1}" ${o.filament === i + 1 ? 'selected' : ''}>${i + 1}</option>`).join('')}</select>
        <button data-obj-del="${o.id}" title="Delete">✕</button></div>`).join('')}</div>
        ${sel && sel.plate === S.plate ? `${xfFields(sel)}<label class="field" style="margin-top:8px"><span class="label">Move to</span><select data-obj-plate="${sel.id}">${plateOptions(sel.plate)}</select></label>` : ''}`
        : `<p class="muted small">${S.objects.length ? 'This plate is empty. Drag a model here, or pick one on another plate and use "Move to".' : 'Nothing on the plate yet. Drop STL, OBJ or 3MF files on the 3D view.'}</p>`}
      <div class="row gap wrap" style="margin-top:8px"><button class="btn small" data-sl="add">Add model</button>${list.length ? '<button class="btn small" data-sl="arrange">Arrange</button><button class="btn small ghost-danger" data-sl="clear">Clear plate</button>' : ''}${S.objects.length ? '<button class="btn small" data-sl="save" title="Download everything (all plates, positions, colours and painting) as a 3MF file you can open here, in OrcaSlicer or Bambu Studio">Download 3MF</button><button class="btn small ghost-danger" data-sl="newproj" title="Start over: removes every model and plate (printer, filament and quality settings stay)">New project</button>' : ''}</div>
    </div>
    ${goButton()}`;
}
function xfFields(o) {
  const m = o.mesh, b = worldBox(o);
  const e = new THREE.Euler().setFromQuaternion(m.quaternion, 'XYZ');
  const deg = (r) => Math.round(THREE.MathUtils.radToDeg(r) * 10) / 10;
  const r1 = (v) => Math.round(v * 10) / 10;
  const px = plateX(o.plate);
  return `<div class="sl-xf" id="sl-xf">
    <label>X mm<input type="number" step="1" data-xf="x" value="${r1((b.min.x + b.max.x) / 2 - px)}"></label>
    <label>Y mm<input type="number" step="1" data-xf="y" value="${r1((b.min.y + b.max.y) / 2)}"></label>
    <label>Turn °<input type="number" step="15" data-xf="rz" value="${deg(e.z)}"></label>
    <label>Size %<input type="number" step="5" min="1" data-xf="s" value="${Math.round(m.scale.x * 1000) / 10}"></label>
    <label>Tilt X °<input type="number" step="15" data-xf="rx" value="${deg(e.x)}"></label>
    <label>Tilt Y °<input type="number" step="15" data-xf="ry" value="${deg(e.y)}"></label>
    <label>Width mm<input type="number" step="1" min="0.1" data-xf="sx" value="${r1(b.max.x - b.min.x)}" title="Size along X; the model is scaled evenly"></label>
    <label>Depth mm<input type="number" step="1" min="0.1" data-xf="sy" value="${r1(b.max.y - b.min.y)}" title="Size along Y; the model is scaled evenly"></label>
    <label>Height mm<input type="number" step="1" min="0.1" data-xf="sz" value="${r1(b.max.z - b.min.z)}" title="Size along Z; the model is scaled evenly"></label>
  </div><p class="hint"></p>`;
}
export function renderXf() {
  const box = $('#sl-xf');
  if (box && S.selected && !box.contains(document.activeElement)) box.outerHTML = xfFields(S.selected).replace(/<p class="hint">.*<\/p>$/, '');
}

export function goButton() {
  const r = S.res[S.plate];
  const busyNow = r && r.job && r.job.state === 'slicing';
  const list = platesObjects(S.plate);
  const dis = !list.length || list.some((o) => o.outside) || busyNow;
  return `<div class="sl-go"><button class="btn primary big" data-sl="slice" ${dis ? 'disabled' : ''}>${busyNow ? 'Slicing…' : (S.nPlates > 1 ? `Slice plate ${S.plate + 1}` : 'Slice & preview')}</button>
    ${list.some((o) => o.outside) ? '<p class="hint" style="text-align:center">Move everything onto the plate first.</p>' : ''}
    <div class="sl-source">OrcaSlicer engine (AGPL-3.0) · <a href="https://github.com/Helio-Additive/slicer-cli" target="_blank" rel="noopener">source</a></div></div>`;
}

export const contrast = (hex) => { const h = hex.replace('#', ''); const l = (0.299 * parseInt(h.substr(0, 2), 16) + 0.587 * parseInt(h.substr(2, 2), 16) + 0.114 * parseInt(h.substr(4, 2), 16)); return l > 150 ? '#111' : '#fff'; };
function slider(label, set, min, max, step, value, unit) {
  return `<label class="field"><span class="label">${label}: <b data-show="${set}">${value}${unit}</b></span><input type="range" min="${min}" max="${max}" step="${step}" value="${value}" data-pset="${set}" data-unit="${unit}"></label>`;
}
export function paintPanel() {
  const p = S.paint, o = S.selected && S.selected.plate === S.plate ? S.selected : null;
  const list = platesObjects(S.plate);
  if (!list.length) return `<div class="card"><h2>Paint</h2><p class="muted">There is nothing on plate ${S.plate + 1} to paint. Add a model in Prepare first.</p></div>`;
  const chTabs = `<div class="segmented sl-chs">${Object.entries(P.CHANNELS).map(([k, c]) => `<button data-pch="${k}" class="${p.ch === k ? 'active' : ''}">${c.label}</button>`).join('')}</div>`;
  const tools = `<div class="sl-ptools">${PAINT_TOOLS.map(([k, l, key]) => `<button data-ptool="${k}" class="${p.tool === k ? 'active' : ''}" title="${l} (${key})">${ic(k)}<span>${l}</span></button>`).join('')}</div>`;
  const pal = p.ch === 'color'
    ? `<div class="sl-pal">${S.filaments.map((f, i) => `<button data-pf="${i + 1}" class="${p.filament === i + 1 ? 'active' : ''}" style="background:${esc(f.color)};color:${contrast(f.color)}" title="Filament ${i + 1} (key ${i + 1})">${i + 1}</button>`).join('')}
        <button data-pf="0" class="erase ${p.filament === 0 ? 'active' : ''}" title="Eraser: back to the object's own filament (key 0, or hold Shift)">⌫</button></div>`
    : `<div class="sl-pal wide"><button data-pm="1" class="${p.mark === 1 ? 'active' : ''}" style="background:${ENFORCE};color:#fff">${p.ch === 'support' ? 'Enforce support' : 'Seam here'}</button>
        <button data-pm="2" class="${p.mark === 2 ? 'active' : ''}" style="background:${BLOCK};color:#fff">${p.ch === 'support' ? 'Block support' : 'No seam here'}</button>
        <button data-pm="0" class="erase ${p.mark === 0 ? 'active' : ''}" title="Eraser (or hold Shift)">⌫</button></div>`;
  let opts = '';
  if (p.tool === 'circle' || p.tool === 'sphere') opts = slider('Brush size', 'size', 0.3, 40, 0.1, p.size, ' mm');
  if (p.tool === 'height') opts = slider('Band height', 'height', 0.1, 50, 0.1, p.height, ' mm');
  if (p.tool === 'fill') opts = `<label class="check"><input type="checkbox" data-pbool="smart" ${p.smart ? 'checked' : ''}> Edge detection (smart fill): stop at sharp edges</label>${p.smart ? slider('Edge angle', 'angle', 0, 90, 1, p.angle, '°') : ''}`;
  if (p.tool === 'gap') opts = `${slider('Gap area', 'gap', 0.1, 50, 0.1, p.gap, ' mm²')}<button class="btn small" data-sl="gapnow" ${o ? '' : 'disabled'}>Fill gaps on ${o ? esc(o.name) : 'the model'}</button>`;
  const section = slider('Section view (cut from the top)', 'clip', 0, 100, 1, p.clip, '%');
  const supOff = String(valueOf('enable_support')) !== '1';
  const support = p.ch === 'support' ? `${supOff ? `<div class="notice warn">Supports are switched off, so painted supports do nothing yet. <button class="btn tiny" data-sl="supon">Switch on: only where painted</button></div>` : ''}<div class="card"><h2>Overhangs</h2>
      <label class="check"><input type="checkbox" data-pbool="overhang" ${p.overhang ? 'checked' : ''}> Highlight overhangs (orange)</label>
      ${slider('Overhang angle', 'ohAngle', 0, 90, 1, p.ohAngle, '°')}
      <button class="btn small" data-sl="enforceoh" ${o ? '' : 'disabled'}>Enforce support on all of them</button>
      <p class="hint">Painted supports are used when Supports is switched on (Prepare → Quality). With support type "Normal (manual)" or "Tree (manual)" only the painted areas get support; blockers always keep support away.</p></div>` : '';
  const seam = p.ch === 'seam' ? '<p class="hint">The seam (where each layer starts and ends) goes onto blue areas and never onto red ones.</p>' : '';
  const remap = p.ch === 'color' ? `<div class="row gap wrap" style="align-items:center"><span class="small">Change</span><select data-premap="from" style="width:auto">${S.filaments.map((_, i) => `<option value="${i + 1}">${i + 1}</option>`).join('')}</select><span class="small">to</span><select data-premap="to" style="width:auto">${S.filaments.map((_, i) => `<option value="${i + 1}" ${i === 1 ? 'selected' : ''}>${i + 1}</option>`).join('')}</select><button class="btn tiny" data-sl="remap" ${o ? '' : 'disabled'}>Change</button></div>` : '';
  return `<div class="card"><h2>Paint ${chTabs}</h2>
      ${tools}${pal}${opts}${section}${seam}
    </div>
    ${support}
    <div class="card"><h2>Model</h2>
      <label class="field"><span class="label">Model on plate ${S.plate + 1}</span><select data-set="paintobj">${list.map((x) => `<option value="${x.id}" ${x === o ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select></label>
      <div class="row gap wrap">
        ${p.ch === 'color' ? `<button class="btn small" data-sl="fillobj" ${o ? '' : 'disabled'}>Whole model in filament ${p.filament || (o && o.filament) || 1}</button>` : ''}
        <button class="btn small ghost-danger" data-sl="clearpaint" ${o ? '' : 'disabled'}>Erase all ${P.CHANNELS[p.ch].label.toLowerCase()} paint</button>
      </div>
      ${remap}
    </div>
    ${p.ch === 'color' ? fsCard(o) : ''}
    ${filamentsCard(false)}
    <details class="card sl-keys"><summary>Mouse and keys</summary>
      <div class="kv small"><div>Left drag</div><div>Paint (on a model) / turn the view (next to it)</div><div>Shift + drag</div><div>Erase</div><div>Right drag</div><div>Move the view</div>
      <div>Alt + wheel</div><div>Brush size / band height</div><div>Ctrl + wheel</div><div>Section view</div><div>1–9 / 0</div><div>Pick filament / eraser</div>
      <div>C S T H F G</div><div>Circle, Sphere, Triangle, Height, Fill, Gap fill</div><div>[ ]</div><div>Smaller / bigger brush</div><div>Ctrl + Z</div><div>Undo</div></div></details>
    ${goButton()}`;
}

export function previewPanel() {
  const r = S.res[S.plate], j = r && r.job;
  const list = platesObjects(S.plate);
  const others = Array.from({ length: S.nPlates }, (_, i) => i).filter((i) => platesObjects(i).length);
  const allBtn = others.length > 1 ? `<button class="btn small" data-sl="sliceall">Slice all ${others.length} plates</button>` : '';
  if (!list.length) return `<div class="card"><h2>Plate ${S.plate + 1}</h2><p class="muted">This plate is empty.</p><a class="btn" href="#slicer/prepare">Back to Prepare</a> ${allBtn}</div>`;
  if (!j) return `<div class="card"><h2>Plate ${S.plate + 1}</h2><p class="muted">Not sliced yet.</p><button class="btn primary" data-sl="slice">Slice plate ${S.plate + 1}</button> ${allBtn}</div>`;
  if (j.state === 'error') return `<div class="card"><h2>Slicing failed</h2><div class="notice err">${esc(j.error || j.message)}</div><a class="btn" href="#slicer/prepare">Back to Prepare</a> <button class="btn" data-sl="slice">Try again</button></div>`;
  if (j.state !== 'done') return `<div class="card"><h2>Plate ${S.plate + 1}</h2><p class="muted">${esc(j.message || 'Slicing…')}</p></div>`;
  const st = j.stats || {};
  const used = (st.grams || []).map((g, i) => [i, g, (st.meters || [])[i]]).filter((x) => x[1] > 0);
  const stale = r.key !== plateKey(S.plate);
  const fil = toolFilaments(S.plate);
  const mg = S.merge[S.plate];
  const mergeNote = mg && Object.keys(mg.map).length ? `<p class="small muted" style="margin-top:6px">${Object.entries(mg.map).map(([a, b]) => `Colour ${a} prints with filament ${b}`).join(' · ')} (the printer holds ${headCount()}). <button class="btn tiny" data-sl="remerge">Change</button></p>` : '';
  const q = `plate=${S.plate + 1}`;
  return `<div class="card"><h2>${esc(j.name.replace(/\.gcode(\.3mf)?$/, ''))}</h2>
      ${stale ? '<div class="notice warn">The plate changed since this slice. <button class="btn tiny" data-sl="slice">Slice again</button></div>' : ''}
      <div class="sl-stats"><div><span class="muted small">Print time</span><div class="big">${esc(st.time || '–')}</div></div>
      <div class="row gap wrap small muted"><span>${st.layers || 0} layers</span><span>${(st.totalGrams || 0).toFixed(1)} g</span>${st.changes ? `<span>${st.changes} filament changes</span>` : ''}</div></div>
      ${allBtn ? `<div style="margin-top:10px">${allBtn}</div>` : ''}
    </div>
    <div class="card"><h2>Filament use</h2>${used.map(([t, g, m]) => { const f = fil[t] || t + 1; return `<div class="sl-use">${F.swatch(filColor(f), 'sm')}<span>${f} · ${esc((S.filaments[f - 1] || {}).preset ? S.filaments[f - 1].preset.replace(/ @.*$/, '') : '')}</span><span>${g.toFixed(1)} g${m ? ` · ${m} m` : ''}</span></div>`; }).join('') || '<p class="muted small">–</p>'}
      ${mergeNote}${r.infill ? `<p class="small muted" style="margin-top:6px">The inside of the models is printed with filament ${r.infill}; their colours form a skin about 3 mm thick.</p>` : ''}${r.towerWall ? `<p class="small muted" style="margin-top:6px">The prime tower has ${r.towerWall === 'rib' ? 'ribbed' : 'plain'} walls on this plate: the usual cone-shaped one (it widens with the print height) does not fit beside the models.</p>` : ''}${r.light && S.light === 'auto' ? '<p class="small muted" style="margin-top:6px">Lighter G-code was used: this plate is very detailed, so paths are kept to 0.05 mm and written as straight moves (saves PrintHarbor about a third of its memory; Prepare → Quality to change).</p>' : ''}${r.noTower ? '<p class="small muted" style="margin-top:6px">No prime tower on this plate: the models leave no room for it. Each colour has its own nozzle, so it prints without one (a colour may leave a small string or blob where it starts).</p>' : ''}</div>
    ${(j.warnings || []).length ? `<div class="card"><h2>Check</h2>${j.warnings.map((w) => `<div class="notice warn small">${esc(w)}</div>`).join('')}</div>` : ''}
    <div class="card"><h2>Show</h2><div class="segmented"><button data-pc="tool" class="${S.previewColor === 'tool' ? 'active' : ''}">Filament</button><button data-pc="feature" class="${S.previewColor === 'feature' ? 'active' : ''}">Line type</button></div></div>
    <div class="sl-go"><div class="row gap"><a class="btn" href="/api/slicer/gcode?${q}&download=1" download title="Save the G-code (for a USB stick or another program)">G-code</a>${j.hasProject ? `<a class="btn" href="/api/slicer/gcode?${q}&download=3mf" download title="Save the sliced project (.gcode.3mf, opens in OrcaSlicer)">3MF</a>` : ''}<button class="btn primary big" style="flex:1" data-sl="print">Print${S.nPlates > 1 ? ` plate ${S.plate + 1}` : ''}…</button></div>
      <div class="sl-source">OrcaSlicer engine (AGPL-3.0) · <a href="https://github.com/Helio-Additive/slicer-cli" target="_blank" rel="noopener">source</a></div></div>`;
}
