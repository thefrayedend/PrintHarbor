// Slicer page: Full Spectrum, approximating any colour by mixing the loaded filaments across
// neighbouring triangles. See slicer.js for the list of files.
import * as THREE from 'three';
import * as P from './paint.js';
import { S, F, esc } from './core.js';
import { filColor } from './profiles.js';
import { paintWhole } from './pointer.js';
import { contrast } from './panels.js';
import { busy } from './slice.js';

// ================================================================== Full Spectrum
// Approximate any colour your filaments can bracket by assigning each surface triangle
// one of a chosen "mix set" of filaments, error-diffused over the surface so the local
// average reads as the target. Works on any multi-material printer (no mixing hotend).
function fsLin(hex) { const c = new THREE.Color(hex).convertSRGBToLinear(); return [c.r, c.g, c.b]; }
function fsHex(l) { const c = new THREE.Color(); c.setRGB(Math.min(1, Math.max(0, l[0])), Math.min(1, Math.max(0, l[1])), Math.min(1, Math.max(0, l[2]))); c.convertLinearToSRGB(); return '#' + c.getHexString(); }
export function fsMixNums() {
  const all = S.filaments.map((_, i) => i + 1);
  const m = S.paint.fs.mix;
  if (!m || !m.length) return all;
  const keep = m.filter((n) => n >= 1 && n <= S.filaments.length);
  return keep.length ? keep : all;
}
// Euclidean projection onto the probability simplex {w>=0, sum w = 1}.
function simplexProj(v) {
  const n = v.length, u = [...v].sort((a, b) => b - a);
  let css = 0, rho = 0;
  for (let i = 0; i < n; i++) { css += u[i]; if (u[i] + (1 - css) / (i + 1) > 0) rho = i; }
  let s = 0; for (let i = 0; i <= rho; i++) s += u[i];
  const theta = (s - 1) / (rho + 1);
  return v.map((x) => Math.max(0, x - theta));
}
// Best non-negative mix weights (projected gradient) in linear RGB.
function fsWeights(t, cols) {
  const N = cols.length; if (!N) return [];
  let w = new Array(N).fill(1 / N);
  for (let it = 0; it < 250; it++) {
    const mix = [0, 0, 0];
    for (let i = 0; i < N; i++) { mix[0] += w[i] * cols[i][0]; mix[1] += w[i] * cols[i][1]; mix[2] += w[i] * cols[i][2]; }
    const d = [mix[0] - t[0], mix[1] - t[1], mix[2] - t[2]];
    for (let i = 0; i < N; i++) w[i] -= 1.2 * (cols[i][0] * d[0] + cols[i][1] * d[1] + cols[i][2] * d[2]);
    w = simplexProj(w);
  }
  return w;
}
// The closest colour the mix set can make, its weights, and how far off it is.
function fsAchieved(targetHex, mixNums) {
  const cols = mixNums.map((n) => fsLin(filColor(n))), t = fsLin(targetHex);
  const w = fsWeights(t, cols);
  const mix = [0, 0, 0];
  for (let i = 0; i < w.length; i++) { mix[0] += w[i] * cols[i][0]; mix[1] += w[i] * cols[i][1]; mix[2] += w[i] * cols[i][2]; }
  return { hex: fsHex(mix), weights: w, dist: Math.hypot(mix[0] - t[0], mix[1] - t[1], mix[2] - t[2]) };
}
// Order triangles along a 3D Morton (Z-order) curve so error diffusion stays spatially local.
function fsMortonOrder(cen, n) {
  let a = [Infinity, Infinity, Infinity], b = [-Infinity, -Infinity, -Infinity];
  for (let t = 0; t < n; t++) for (let j = 0; j < 3; j++) { const v = cen[t * 3 + j]; if (v < a[j]) a[j] = v; if (v > b[j]) b[j] = v; }
  const s = [1023 / ((b[0] - a[0]) || 1), 1023 / ((b[1] - a[1]) || 1), 1023 / ((b[2] - a[2]) || 1)];
  const part = (v) => { v = (v | 0) & 1023; v = (v | (v << 16)) & 0x030000FF; v = (v | (v << 8)) & 0x0300F00F; v = (v | (v << 4)) & 0x030C30C3; v = (v | (v << 2)) & 0x09249249; return v; };
  const code = new Float64Array(n), idx = new Array(n);
  for (let t = 0; t < n; t++) { const x = part((cen[t * 3] - a[0]) * s[0]), y = part((cen[t * 3 + 1] - a[1]) * s[1]), z = part((cen[t * 3 + 2] - a[2]) * s[2]); code[t] = x | (y << 1) | (z << 2); idx[t] = t; }
  idx.sort((p, q) => code[p] - code[q]);
  return idx;
}
// Per-triangle filament assignment: error-diffused dither of the mix set toward the target.
function fsAssign(o, targetHex, mixNums) {
  const cols = mixNums.map((n) => fsLin(filColor(n))), t = fsLin(targetHex);
  const cen = P.geometry(o).cen, n = o.states.length, order = fsMortonOrder(cen, n);
  const out = new Uint8Array(n);
  let er = 0, eg = 0, eb = 0;
  for (const ti of order) {
    const wr = t[0] + er, wg = t[1] + eg, wb = t[2] + eb;
    let bi = 0, bd = Infinity;
    for (let i = 0; i < cols.length; i++) { const c = cols[i], dr = c[0] - wr, dg = c[1] - wg, db = c[2] - wb, d = dr * dr + dg * dg + db * db; if (d < bd) { bd = d; bi = i; } }
    out[ti] = mixNums[bi];
    const c = cols[bi];
    er = (wr - c[0]) * 0.85; eg = (wg - c[1]) * 0.85; eb = (wb - c[2]) * 0.85;
  }
  return out;
}
export async function fsApply(o) {
  const mixNums = fsMixNums();
  if (mixNums.length < 2) { F.toast('Full Spectrum needs at least two filaments to mix between.', true); return; }
  busy('Mixing colour…', `Dithering ${mixNums.length} filaments across ${esc(o.name)}`, null);
  await new Promise((r) => setTimeout(r, 30));
  const assign = fsAssign(o, S.paint.fs.target, mixNums);
  paintWhole(o, 'color', (v, tt) => assign[tt]);
  busy(null);
  const a = fsAchieved(S.paint.fs.target, mixNums);
  F.toast(a.dist > 0.14 ? 'Mixed — but this colour is near the edge of what these filaments can make.' : 'Mixed across the model.');
}
// Live preview update (without re-rendering the whole panel, so the colour picker keeps focus).
export function fsUpdatePreview() {
  const mixNums = fsMixNums(), a = fsAchieved(S.paint.fs.target, mixNums);
  const tgt = document.querySelector('.sl-fs .fs-sw.fs-target-sw'); if (tgt) tgt.style.background = S.paint.fs.target;
  const got = document.getElementById('fs-got'); if (got) got.style.background = a.hex;
  const reach = document.getElementById('fs-reach'); if (reach) reach.innerHTML = fsReach(a.dist);
  const pct = document.getElementById('fs-pct'); if (pct) pct.textContent = fsPct(a.weights, mixNums);
}
function fsReach(dist) { return dist <= 0.06 ? '<b class="fs-ok">in reach</b>' : dist <= 0.14 ? '<b class="fs-close">close</b>' : '<b class="fs-far">outside your filaments’ range</b>'; }
function fsPct(w, mixNums) {
  const items = w.map((x, i) => ({ n: mixNums[i], w: x })).filter((x) => x.w > 0.005).sort((x, y) => y.w - x.w);
  return items.map((x) => `${x.n}: ${Math.round(x.w * 100)}%`).join(' · ') || '—';
}
export function fsCard(o) {
  const mixNums = fsMixNums(), a = fsAchieved(S.paint.fs.target, mixNums);
  return `<div class="card sl-fs"><h2>Full Spectrum <span class="muted small">mix a colour from your filaments</span></h2>
    <p class="hint">Pick a target colour and which filaments to blend. PrintHarbor dithers them across the surface so, from a normal viewing distance, it reads as the target — any colour your filaments can bracket. No special printer needed.</p>
    <div class="fs-top">
      <label class="fs-pick"><span class="label">Target</span><input type="color" data-fstarget value="${esc(S.paint.fs.target)}" aria-label="Target colour"></label>
      <div class="fs-sw fs-target-sw" style="background:${esc(S.paint.fs.target)}"><span>Target</span></div>
      <div class="fs-sw" id="fs-got" style="background:${esc(a.hex)}"><span>Result · <span id="fs-reach">${fsReach(a.dist)}</span></span></div>
    </div>
    <div class="label fs-lbl">Filaments to mix <span class="muted small">tap to include</span></div>
    <div class="sl-pal fs-mix">${S.filaments.map((f, i) => `<button data-fsmix="${i + 1}" class="${mixNums.includes(i + 1) ? 'active' : ''}" style="background:${esc(f.color)};color:${contrast(f.color)}" title="Filament ${i + 1}">${i + 1}</button>`).join('')}</div>
    <div class="fs-pct small muted" id="fs-pct">${fsPct(a.weights, mixNums)}</div>
    <button class="btn primary fs-apply" data-sl="fsapply" ${o ? '' : 'disabled'}>Mix across ${o ? esc(o.name) : 'the model'}</button>
    <p class="hint">Up close it's a fine speckle of your filaments; finer models look smoother. Undo with Ctrl+Z.</p></div>`;
}
