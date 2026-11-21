// Slicer page: what every part shares. The state of the page (S), the helpers handed over by the
// page that hosts the slicer (F), and a few constants. See slicer.js for the list of files.
import * as THREE from 'three';
import { computeBoundsTree, disposeBoundsTree, acceleratedRaycast } from './vendor/three-mesh-bvh.js';

THREE.BufferGeometry.prototype.computeBoundsTree = computeBoundsTree;
THREE.BufferGeometry.prototype.disposeBoundsTree = disposeBoundsTree;
THREE.Mesh.prototype.raycast = acceleratedRaycast;
THREE.Object3D.DEFAULT_UP.set(0, 0, 1);

export let F; // helpers from app.js (api, toast, esc, current, …)
export function setHelpers(ffb) { F = ffb; }
export const $ = (s, el = document) => el.querySelector(s);
export const $$ = (s, el = document) => [...el.querySelectorAll(s)];
export const esc = (s) => F.esc(s);

// Settings shown in the side panel (everything else is under "All settings").
export const SIMPLE = [
  ['layer_height', 'Layer height'], ['wall_loops', 'Walls'], ['top_shell_layers', 'Top layers'], ['bottom_shell_layers', 'Bottom layers'],
  ['sparse_infill_density', 'Infill'], ['sparse_infill_pattern', 'Infill pattern'], ['enable_support', 'Supports'], ['support_type', 'Support type'],
  ['support_on_build_plate_only', 'Supports only from the plate'], ['brim_type', 'Brim'], ['enable_prime_tower', 'Prime tower'],
];
/** Plates with this many triangles or more get lighter G-code when that setting is Auto. */
export const LIGHT_AT = 200000;
// Enforcer / blocker colours (as in OrcaSlicer).
export const ENFORCE = '#2f7bff', BLOCK = '#e5383b', BASE = '#c9ccd1', OVERHANG = '#f2a33a';

export const S = {
  ready: false, mode: 'prepare', printerId: '', info: null, schema: null, values: {}, loading: false, err: '',
  process: '', bedType: '', filaments: [], overrides: {},
  objects: [], selected: null, tool: 'move',
  nPlates: 1, plate: 0,
  res: [], // per plate: {job, key, g (parsed G-code), tools (filament number per tool)}
  merge: {}, // per plate: {u: used filaments, map: {extra filament: filament it prints with}}
  paint: { ch: 'color', tool: 'circle', filament: 2, mark: 1, size: 3, height: 2, smart: true, angle: 30, gap: 2, clip: 100, overhang: false, ohAngle: 30, fs: { target: '#7a3cc0', mix: null } },
  previewColor: 'tool', hiddenFeatures: new Set(), layer: 0,
  light: 'auto', // lighter G-code for very detailed plates: 'auto' | 'on' | 'off'
  customLayer: false, // layer height: a box to type in instead of the dropdown (after "Other…")
  undo: [],
  project: null, // the PrintHarbor project open here: {id, name, savedAt}
  pm: false, // project mode (inside a project on the Projects page): Plates / Paint / Settings, no printer
  tab: 'plates', // project mode: 'plates' | 'settings' (Paint is its own mode)
  plateOv: [], // per plate: its setting changes (S.overrides is always the current plate's object)
  savedSig: '', // what the project looked like when last opened / saved (unsaved changes)
  pmStatus: null, // project mode: copies and progress per saved plate, from the Projects page
};
window.__sl = S; // for the automated browser checks (also __slCam)
