// Slicer page: the printer being sliced for, its Orca profiles and its filaments.
// See slicer.js for the list of files.
import { S, F } from './core.js';
import { invalidate, bed } from './scene.js';
import { buildBed, stride, plateX } from './plates.js';
import { recolor, checkBounds } from './objects.js';
import { renderSide } from './toolbar.js';
import { tidyNumber } from './panels.js';
import { clearPreview } from './preview.js';

// ================================================================== printer + profiles
export const prefKey = (id) => 'ffb-slicer-' + id;
export function savePrefs() {
  try { localStorage.setItem(prefKey(S.printerId), JSON.stringify({ machine: S.info && S.info.machine, process: S.process, bedType: S.bedType, filaments: S.filaments, overrides: S.overrides, color: S.previewColor, infill: S.infillFil || 0, light: S.light })); } catch (_) { /* private mode */ }
}
export function loadPrefs(id) { try { return JSON.parse(localStorage.getItem(prefKey(id)) || '{}'); } catch (_) { return {}; } }

export async function loadPrinter(id, extra = '') {
  try {
    await loadPrinterInner(id, extra);
  } catch (e) {
    // never sit on "Reading the printer profiles…": show what went wrong (and a retry button)
    console.error(e);
    S.loading = false; S.info = null;
    S.err = `Could not open the slicer: ${e && e.message ? e.message : e}. Reload the page. If it keeps happening, take a screenshot of this message.`;
    renderSide();
  }
}
async function loadPrinterInner(id, extra) {
  S.printerId = id;
  S.loading = true; S.err = '';
  renderSide();
  const prefs = loadPrefs(id);
  const q = `id=${encodeURIComponent(id)}${prefs.machine && !extra ? '&machine=' + encodeURIComponent(prefs.machine) : ''}${extra}`;
  // Beds of different printers are different sizes, and the plates stand a
  // bed-width apart, so the whole row of plates is spaced differently per
  // printer. Remember this printer's bed and spacing before switching, so the
  // models can be kept on their own plates on the new bed (see adaptToPrinter).
  const prev = S.info && S.info.bed ? { bed: { ...bed() }, stride: stride() } : null;
  const r = await F.api('slicer/printer?' + q);
  S.loading = false;
  if (!r.ok) { S.info = null; S.err = r.message || 'Could not read the printer profiles.'; renderSide(); return; }
  S.info = r;
  if (!r.known) { renderSide(); return; }
  if (!S.schema) { const sc = await F.api('slicer/schema'); S.schema = sc.settings || []; }
  if (S.pm || S.project) { await adaptToPrinter(r, prev); return; }
  S.process = r.processes.includes(prefs.process) ? prefs.process : r.defaultProcess;
  S.bedType = prefs.bedType || r.defaultBedType || '';
  S.overrides = prefs.overrides && typeof prefs.overrides === 'object' ? prefs.overrides : {};
  // saved settings: numbers as typed (".08") tidied, and ones this engine does not know dropped
  const known = new Set(S.schema.map((x) => x.key));
  for (const k of Object.keys(S.overrides)) { if (known.has(k) || k === 'curr_bed_type') S.overrides[k] = tidyNumber(String(S.overrides[k])); else delete S.overrides[k]; }
  S.previewColor = prefs.color || 'tool';
  S.infillFil = +prefs.infill || 0;
  S.light = ['auto', 'on', 'off'].includes(prefs.light) ? prefs.light : 'auto';
  S.filaments = defaultFilaments(r, prefs.filaments);
  S.plateOv = []; S.plateOv[S.plate] = S.overrides;
  S.res = []; S.merge = {}; // another printer: earlier slices and colour choices do not apply
  if (S.ready) clearPreview();
  await loadValues();
  buildBed();
  for (const o of S.objects) recolor(o);
  checkBounds();
  renderSide();
  invalidate();
}

/**
 * A project laid out for another printer: every model stays on the plate it was on, in the same
 * spot on that plate; colours keep their colour and material (each gets this printer's profile for
 * that material), every plate keeps its setting changes, the quality profile is this printer's with
 * the same layer height. A model too big for the new bed stays on its own plate and shows as not
 * fitting (so you can send that plate to a bigger printer) — it is never shoved onto another plate.
 */
async function adaptToPrinter(r, prev) {
  const fam = (p) => F.family(String(p || 'PLA').replace(/^(Flashforge|Generic|FusRock|Creality|Bambu|Qidi|Snapmaker|Elegoo|Sovol)\s+/i, ''));
  const layer = (n) => (/^(\d+\.\d+)/.exec(n || '') || [])[1];
  const want = layer(S.process);
  S.process = (want && r.processes.find((n) => layer(n) === want && /standard|optimal|normal/i.test(n))) || (want && r.processes.find((n) => layer(n) === want)) || r.defaultProcess;
  if (!S.filaments.length) S.filaments = defaultFilaments(r, null);
  else S.filaments = S.filaments.map((f) => ({ color: f.color, preset: r.filaments.includes(f.preset) ? f.preset : pickPreset(fam(f.preset), r) }));
  const bedEnum = (S.schema || []).find((x) => x.key === 'curr_bed_type');
  if (!S.bedType || (bedEnum && !bedEnum.enum.some((e) => e.value === S.bedType))) S.bedType = r.defaultBedType || '';
  S.res = []; S.merge = {};
  if (S.ready) clearPreview();
  await loadValues();
  buildBed();
  keepOnPlates(prev);
  for (const o of S.objects) recolor(o);
  checkBounds();
  renderSide();
  invalidate();
}

/**
 * After the bed changed, move every model so it sits in the same place on the same plate as before.
 * Each plate's new bed is somewhere else in the row (the plates are a bed-width apart, and the bed
 * width changed), so without this the models — whose world positions did not change — would end up
 * off their plates, worse the further along the row they are. prev holds the old bed and spacing;
 * o.plate still holds the plate each model was on. The model keeps its offset from its plate's near
 * corner, so its layout on the plate is unchanged.
 */
function keepOnPlates(prev) {
  if (!prev) return;
  const b = bed();
  for (const o of S.objects) {
    const i = o.plate || 0;
    const oldOriginX = prev.bed.x0 + i * prev.stride;
    const newOriginX = b.x0 + plateX(i);
    o.mesh.position.x += newOriginX - oldOriginX;
    o.mesh.position.y += b.y0 - prev.bed.y0;
    o.mesh.updateMatrixWorld(true);
  }
}
export async function loadValues() {
  const r = await F.api(`slicer/resolved?id=${encodeURIComponent(S.printerId)}&machine=${encodeURIComponent(S.info.machine)}&process=${encodeURIComponent(S.process)}`);
  S.values = r.values || {};
}

/** Filaments: one per printer slot / head, matched to the loaded spools. */
// Max colours/filaments per project. High enough for big multi-AMS tool
// changers (e.g. 24+ colours); the server enforces the same ceiling (maxFilaments).
export const MAX_FILAMENTS = 64;
/** The printer's filament profile for a material (PLA, PETG …): Flashforge's own first. */
export function pickPreset(material, info = S.info) {
  const list = info.filaments;
  const fam = F.family(material || 'PLA');
  const tries = [(f) => f.startsWith(`Flashforge ${fam} Basic`), (f) => f.startsWith(`Generic ${fam} @`), (f) => f.startsWith(`Flashforge ${fam}`) && !/-CF|-GF/.test(f), (f) => F.family(f.replace(/^(Flashforge|Generic)\s+/, '')) === fam];
  for (const t of tries) { const m = list.find(t); if (m) return m; }
  return list.find((f) => /PLA Basic/.test(f)) || list[0];
}
export function defaultFilaments(info, saved) {
  const p = F.current();
  const slots = (p && p.slots) || [];
  const n = Math.max(1, Math.min(MAX_FILAMENTS, Math.max(saved ? saved.length : 0, slots.length || info.extruders || 1)));
  const pick = (material) => pickPreset(material, info);
  const out = [];
  for (let i = 0; i < n; i++) {
    const s = slots[i];
    const keep = saved && saved[i] && info.filaments.includes(saved[i].preset) ? saved[i] : null;
    out.push(keep || { preset: pick(s && s.material), color: (s && s.hasFilament !== false && s.color) || ['#E8E8E8', '#202020', '#D32F2F', '#1E63D6'][i % 4] });
  }
  return out;
}
export function useLoaded() {
  const p = F.current();
  const slots = (p && p.slots) || [];
  if (!slots.length) { F.toast('The printer did not report any loaded filament.', true); return; }
  const loaded = defaultFilaments(S.info, null);
  S.filaments = loaded.concat(S.filaments.slice(loaded.length)); // extra project filaments stay
  savePrefs();
  for (const o of S.objects) recolor(o);
  renderSide(); invalidate();
  F.toast('Filaments set to what is loaded in the printer.');
}
export const filColor = (n) => (S.filaments[n - 1] ? S.filaments[n - 1].color : '#8a8f98');
