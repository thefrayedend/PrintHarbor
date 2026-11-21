// Slicer page: how much memory the slicing engine will need for a plate, and the smallest layer
// height that still fits. See slicer.js for the list of files.
import { S, LIGHT_AT } from './core.js';
import { platesObjects } from './plates.js';
import { valueOf, nozzleSize } from './panels.js';

// ------------------------------------------------------------------ memory estimate (layer height guard)
// How much memory the engine will need for a plate, worked out before slicing so the page can stop
// layer heights PrintHarbor cannot slice. Fitted to measured slices of a 1M-triangle painted helmet and
// a 200k-triangle vase (lean engine v0.1.0-dev-19-ffb1, x86_64; PrintHarbor needs ~10% less):
//   GB = 0.45 (engine + profiles) + 0.23 per million triangles
//      + 3.73e-5 * surface (cm²) / layer height (mm)          walls and infill, every layer
//        (x1.05 gyroid infill, x2.2 without Lighter G-code: arcs are stored per path)
//      + 9.33e-6 * support column (cm³) * layer height^-1.5    supports: projected overhang area x
//        height above the plate; painted area only when supports are "only where painted"
// Within 3% of every measured slice (0.08 to 0.2 mm, with and without supports).
const MEM = { base: 0.45, perMTri: 0.23, wall: 3.73e-5, gyroid: 1.05, noLight: 2.2, sup: 9.33e-6, supExp: 1.5 };
/** Memory the engine may use on this board (GB): PrintHarbor's RAM less what the system keeps. */
export function memBudget() { const mb = S.memMB || 3847; return (mb - 450) / 1024; }
let featCache = { key: '', f: null };
/** Size and shape numbers of a plate, as placed: triangles, surface (cm²), support columns (cm³). */
function plateFeatures(i) {
  const list = platesObjects(i);
  const thr = +valueOf('support_threshold_angle') || 30;
  const key = JSON.stringify([i, thr, list.map((o) => { o.mesh.updateMatrixWorld(true); return [o.id, o.rev || 0, o.mesh.matrixWorld.elements.map((x) => Math.round(x * 1000))]; })]);
  if (featCache.key === key) return featCache.f;
  const lim = -Math.cos((90 - thr) * Math.PI / 180); // overhang: face normal points down more than this
  let tris = 0, area = 0, col = 0, colPainted = 0;
  for (const o of list) {
    const m = o.mesh.matrixWorld.elements, p = o.orig, n = p.length / 9;
    tris += n;
    let zmin = Infinity;
    for (let k = 2; k < p.length; k += 3) { const z = m[2] * p[k - 2] + m[6] * p[k - 1] + m[10] * p[k] + m[14]; if (z < zmin) zmin = z; }
    const w = new Float32Array(9);
    for (let t = 0; t < n; t++) {
      for (let v = 0; v < 3; v++) {
        const b = t * 9 + v * 3, x = p[b], y = p[b + 1], z = p[b + 2];
        w[v * 3] = m[0] * x + m[4] * y + m[8] * z; w[v * 3 + 1] = m[1] * x + m[5] * y + m[9] * z; w[v * 3 + 2] = m[2] * x + m[6] * y + m[10] * z + m[14] - zmin;
      }
      const ax = w[3] - w[0], ay = w[4] - w[1], az = w[5] - w[2], bx = w[6] - w[0], by = w[7] - w[1], bz = w[8] - w[2];
      const cx = ay * bz - az * by, cy = az * bx - ax * bz, cz = ax * by - ay * bx;
      const a2 = Math.hypot(cx, cy, cz);
      if (!a2) continue;
      area += a2 / 2;
      const nz = cz / a2, zc = (w[2] + w[5] + w[8]) / 3;
      if (zc > 0.3 && nz < 0) {
        const c = (a2 / 2) * -nz * zc; // projected area x height above the plate
        if (nz < lim) col += c;
        if (o.sup) { // painted enforcers count wherever they are (share of the triangle's pieces)
          const lvl = o.levels[t], st = o.start[t], cnt = 4 ** lvl;
          let e = 0;
          for (let q = st; q < st + cnt; q++) if (o.sup[q] === 1) e++;
          if (e) colPainted += c * e / cnt;
        }
      }
    }
  }
  const f = { tris, area: area / 100, col: col / 1000, colPainted: colPainted / 1000 };
  featCache = { key, f };
  return f;
}
/** Estimated engine memory (GB) for plate i at layer height h, with the current settings. */
export function plateMemory(i, h, light) {
  const f = plateFeatures(i);
  if (!f.tris) return 0;
  const supOn = /^(1|true)$/.test(String(valueOf('enable_support')));
  const manual = /manual/.test(String(valueOf('support_type')));
  if (light === undefined) light = S.light === 'on' || (S.light === 'auto' && f.tris >= LIGHT_AT);
  const gy = /gyroid/.test(String(valueOf('sparse_infill_pattern')));
  let gb = MEM.base + MEM.perMTri * f.tris / 1e6 + MEM.wall * f.area / h * (gy ? MEM.gyroid : 1) * (light ? 1 : MEM.noLight);
  if (supOn) gb += MEM.sup * (manual ? f.colPainted : f.col) * h ** -MEM.supExp;
  return gb;
}
/** The finest layer height (mm, 0.01 steps) this plate can be sliced at on this board; null = none fits. */
export function minLayerHeight(i) {
  const max = Math.round(nozzleSize() * 75) / 100, budget = memBudget();
  for (let h = 0.04; h <= max + 1e-9; h = +(h + 0.01).toFixed(2)) if (plateMemory(i, h) <= budget) return h;
  return null;
}
