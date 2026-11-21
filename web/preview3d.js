// The 3D preview of a sliced file, for the main page (the Queue's Preview and
// "Slice & preview"): the whole print as it will be laid down, in 3D, in the
// colours of its filaments (or coloured by kind of line), the way OrcaSlicer
// shows a sliced plate. Drag to turn it, wheel to zoom,
// right-drag to move. The slider on the right peels layers off the top, and
// the list on the left hides or shows each kind of line ("Only supports" shows
// just the supports).
//
// An ES module, loaded only when a preview is opened (gcode.js imports it), so
// the main page does not carry three.js until it is needed. It shares the
// G-code reader with the slicer page.
import * as THREE from 'three';
import { OrbitControls } from './slicer/vendor/OrbitControls.js';
import { parseGcode, FEATURES } from './slicer/gcode.js';

/** Above this many printed moves the print is drawn as lines; boxes would need ~80 bytes each on the graphics card. */
const BOXES_UP_TO = 600000;
const SUPPORT = new Set([9, 10]); // FEATURES: Support, Support interface
/** Colours for filaments when the file does not say (it nearly always does). */
const PALETTE = ['#e03131', '#1c7ed6', '#f59f00', '#2f9e44', '#9c36b5', '#0c8599', '#e8590c', '#868e96'];

/** What the slicer wrote about the job at the end of the file: filament colours and the bed. */
function fileFacts(text) {
  const tail = text.slice(-600000);
  const one = (key) => { const m = new RegExp('^; ' + key + ' = (.*)$', 'm').exec(tail); return m ? m[1].trim() : ''; };
  const colours = one('filament_colour').split(';').map((c) => c.trim()).filter((c) => /^#[0-9a-f]{6}/i.test(c)).map((c) => c.slice(0, 7));
  let bed = null;
  const pts = one('printable_area').split(',').map((p) => p.split('x').map(Number)).filter((p) => p.length === 2 && p.every(Number.isFinite));
  if (pts.length >= 3) {
    const xs = pts.map((p) => p[0]), ys = pts.map((p) => p[1]);
    bed = { x0: Math.min(...xs), y0: Math.min(...ys), x1: Math.max(...xs), y1: Math.max(...ys) };
    if (bed.x1 - bed.x0 < 20 || bed.y1 - bed.y0 < 20) bed = null;
  }
  return { colours, bed };
}

/** Downloads url as text, telling progress(fraction) as it goes (0 when the size is unknown). */
async function fetchText(url, progress) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(res.statusText || 'not found');
  if (!res.body || !res.body.getReader) return res.text();
  const total = +res.headers.get('Content-Length') || 0;
  const rd = res.body.getReader(), parts = [];
  let got = 0;
  for (;;) {
    const { done, value } = await rd.read();
    if (done) break;
    parts.push(value); got += value.length;
    if (progress) progress(total ? got / total : 0);
  }
  return new Blob(parts).text();
}

/**
 * Shows the sliced file at url inside box (an element with a size).
 * ui: { msg(text), facts({layers, supportShare}), legend: element, fils: element, mode: element (two buttons with data-gcmode),
 *       slider: input, label: element, play: button, only: button, reset: button, top: button }
 * Returns { destroy() }.
 */
export async function showPreview3D(box, url, ui) {
  ui.msg('Loading the sliced file…');
  const text = await fetchText(url, (f) => ui.msg(f ? `Loading the sliced file… ${Math.round(f * 100)}%` : 'Loading the sliced file…'));
  ui.msg('Reading the layers…');
  await new Promise((r) => setTimeout(r, 30));
  const g = parseGcode(text);
  const facts = fileFacts(text);
  if (!g.count || !g.layerEnd.length) { ui.msg('No printing moves found in this file.'); return { destroy() {} }; }

  // ---- what the print is made of (by length of printed line)
  const lenBy = new Float64Array(FEATURES.length);
  for (let i = 0; i < g.count; i++) { const o = i * 7; lenBy[g.feature[i]] += Math.hypot(g.seg[o + 2] - g.seg[o], g.seg[o + 3] - g.seg[o + 1]); }
  const total = lenBy.reduce((a, b) => a + b, 0) || 1;
  const used = [...lenBy.keys()].filter((f) => lenBy[f] > 0);
  const lenByTool = new Float64Array(64);
  for (let i = 0; i < g.count; i++) { const o = i * 7; lenByTool[g.tool[i]] += Math.hypot(g.seg[o + 2] - g.seg[o], g.seg[o + 3] - g.seg[o + 1]); }
  const tools = [...lenByTool.keys()].filter((t) => lenByTool[t] > 0);
  const toolColour = (t) => facts.colours[t] || PALETTE[t % PALETTE.length];
  // Like the slicer: the print in its filaments' colours. One filament only: by kind of line says more.
  let mode = tools.length > 1 ? 'filament' : 'type';
  const hiddenTools = new Set();
  const supportShare = [...SUPPORT].reduce((a, f) => a + lenBy[f], 0) / total;
  ui.facts({ layers: g.layerEnd.length, supportShare });

  // ---- the scene
  const dark = matchMedia('(prefers-color-scheme: dark)').matches || getComputedStyle(document.body).colorScheme === 'dark'
    || luminance(getComputedStyle(document.body).backgroundColor) < 0.4;
  let renderer;
  try { renderer = new THREE.WebGLRenderer({ antialias: true }); } catch (e) { ui.msg('The 3D preview needs WebGL, which this browser does not offer.'); return { destroy() {} }; }
  renderer.setPixelRatio(Math.min(devicePixelRatio || 1, 2));
  renderer.setClearColor(dark ? 0x15181c : 0xeef1f5);
  box.appendChild(renderer.domElement);
  renderer.domElement.style.cssText = 'display:block;width:100%;height:100%;touch-action:none';
  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(32, 1, 1, 5000);
  camera.up.set(0, 0, 1);
  scene.add(new THREE.AmbientLight(0xffffff, 1.5));
  const sun = new THREE.DirectionalLight(0xffffff, 2.2);
  scene.add(sun);

  // The bed, at the size the file says the printer's is. (When it doesn't say: prints sit around
  // the middle of the bed, so it reaches as far past the print as the print is from 0.)
  const b = g.bounds;
  const B = facts.bed || { x0: 0, y0: 0, x1: Math.max(b.maxX + Math.max(b.minX, 10), 60), y1: Math.max(b.maxY + Math.max(b.minY, 10), 60) };
  const bedW = B.x1 - B.x0, bedD = B.y1 - B.y0;
  const bed = new THREE.Group();
  const plate = new THREE.Mesh(new THREE.PlaneGeometry(bedW, bedD), new THREE.MeshBasicMaterial({ color: dark ? 0x2a2f37 : 0xd9dee6 }));
  plate.position.set(B.x0 + bedW / 2, B.y0 + bedD / 2, -0.05);
  bed.add(plate);
  const gridPts = [];
  for (let x = B.x0; x <= B.x1 + 0.01; x += 10) gridPts.push(x, B.y0, 0, x, B.y1, 0);
  for (let y = B.y0; y <= B.y1 + 0.01; y += 10) gridPts.push(B.x0, y, 0, B.x1, y, 0);
  const gridGeo = new THREE.BufferGeometry();
  gridGeo.setAttribute('position', new THREE.Float32BufferAttribute(gridPts, 3));
  bed.add(new THREE.LineSegments(gridGeo, new THREE.LineBasicMaterial({ color: dark ? 0x3a4049 : 0xc3c9d3 })));
  scene.add(bed);

  // ---- the printed lines
  const n = g.count, seg = g.seg;
  const lines = n > BOXES_UP_TO;
  const feats = FEATURES.map(([, col]) => new THREE.Color(col));
  const toolCols = Array.from({ length: 64 }, (_, t) => new THREE.Color(toolColour(t)));
  const hidden = new Set();
  const gone = (i) => hidden.has(g.feature[i]) || hiddenTools.has(g.tool[i]);
  const colourOf = (i) => (mode === 'filament' ? toolCols[g.tool[i]] : feats[g.feature[i]]);
  let mesh, saved = null;
  if (lines) {
    const pos = new Float32Array(n * 6);
    for (let k = 0; k < n; k++) {
      const o = k * 7, zz = seg[o + 4] - Math.max(0.05, seg[o + 5]) / 2;
      pos[k * 6] = seg[o]; pos[k * 6 + 1] = seg[o + 1]; pos[k * 6 + 2] = zz;
      pos[k * 6 + 3] = seg[o + 2]; pos[k * 6 + 4] = seg[o + 3]; pos[k * 6 + 5] = zz;
    }
    const geo = new THREE.BufferGeometry();
    geo.setAttribute('position', new THREE.BufferAttribute(pos, 3));
    geo.setAttribute('color', new THREE.BufferAttribute(new Float32Array(n * 8), 4)); // alpha 0 hides a line
    mesh = new THREE.LineSegments(geo, new THREE.LineBasicMaterial({ vertexColors: true, transparent: true, alphaTest: 0.5 }));
  } else {
    mesh = new THREE.InstancedMesh(new THREE.BoxGeometry(1, 1, 1), new THREE.MeshLambertMaterial(), n);
    const m = new THREE.Matrix4(), q = new THREE.Quaternion(), p = new THREE.Vector3(), s = new THREE.Vector3(), up = new THREE.Vector3(0, 0, 1);
    for (let k = 0; k < n; k++) {
      const o = k * 7, x0 = seg[o], y0 = seg[o + 1], x1 = seg[o + 2], y1 = seg[o + 3], h = Math.max(0.05, seg[o + 5]), w = Math.max(0.1, seg[o + 6]);
      p.set((x0 + x1) / 2, (y0 + y1) / 2, seg[o + 4] - h / 2);
      q.setFromAxisAngle(up, Math.atan2(y1 - y0, x1 - x0));
      s.set(Math.hypot(x1 - x0, y1 - y0) + w * 0.5, w, h * 0.9);
      mesh.setMatrixAt(k, m.compose(p, q, s));
    }
    mesh.instanceMatrix.needsUpdate = true;
  }
  mesh.frustumCulled = false;
  scene.add(mesh);

  // Colours by kind of line. Lines have no lighting of their own, so each is shaded by the way
  // it runs: walls facing different ways get different brightness and the shape reads as solid.
  function paint() {
    const c = new THREE.Color();
    if (lines) {
      const col = mesh.geometry.attributes.color, a = col.array;
      for (let i = 0; i < n; i++) {
        const k = i * 8;
        if (gone(i)) { a[k + 3] = a[k + 7] = 0; continue; }
        const o = i * 7, dx = seg[o + 2] - seg[o], dy = seg[o + 3] - seg[o + 1], l = Math.hypot(dx, dy) || 1;
        const shade = 0.62 + 0.38 * Math.abs((-dy / l) * 0.55 + (dx / l) * 0.83); // the line's side against a light from the front right
        c.copy(colourOf(i)).multiplyScalar(shade);
        a[k] = a[k + 4] = c.r; a[k + 1] = a[k + 5] = c.g; a[k + 2] = a[k + 6] = c.b; a[k + 3] = a[k + 7] = 1;
      }
      col.needsUpdate = true;
    } else {
      const m = new THREE.Matrix4(), zero = new THREE.Matrix4().makeScale(0, 0, 0);
      saved ??= mesh.instanceMatrix.array.slice();
      for (let i = 0; i < n; i++) {
        if (gone(i)) { mesh.setMatrixAt(i, zero); continue; }
        mesh.setMatrixAt(i, m.fromArray(saved, i * 16));
        mesh.setColorAt(i, colourOf(i));
      }
      mesh.instanceMatrix.needsUpdate = true;
      if (mesh.instanceColor) mesh.instanceColor.needsUpdate = true;
    }
    legend();
    draw();
  }
  function legend() {
    const pct = (v) => (v / total < 0.01 ? '<1' : Math.round(v / total * 100));
    ui.mode.querySelectorAll('[data-gcmode]').forEach((bt) => bt.classList.toggle('active', bt.dataset.gcmode === mode));
    ui.fils.innerHTML = tools.map((t) => `<button type="button" class="gc-li ${hiddenTools.has(t) ? 'off' : ''}" data-gcf="${t}" title="Click to hide or show this filament"><i style="background:${toolColour(t)}"></i><span>Filament ${t + 1}</span><em>${pct(lenByTool[t])}%</em></button>`).join('');
    ui.fils.style.display = tools.length > 1 || mode === 'filament' ? '' : 'none';
    ui.legend.innerHTML = used.map((f) => `<button type="button" class="gc-li ${hidden.has(f) ? 'off' : ''}" data-gct="${f}" title="Click to hide or show"><i style="background:${mode === 'type' ? FEATURES[f][1] : 'transparent'};${mode === 'type' ? '' : 'outline:1px solid currentColor;opacity:.5'}"></i><span>${FEATURES[f][0]}</span><em>${pct(lenBy[f])}%</em></button>`).join('');
    const onlySup = used.some((f) => SUPPORT.has(f)) && used.every((f) => SUPPORT.has(f) !== hidden.has(f));
    ui.only.textContent = onlySup ? 'Show everything' : 'Only supports';
    ui.only.disabled = !used.some((f) => SUPPORT.has(f));
  }
  ui.legend.onclick = (e) => {
    const t = e.target.closest('[data-gct]'); if (!t) return;
    const f = +t.dataset.gct; if (hidden.has(f)) hidden.delete(f); else hidden.add(f);
    paint();
  };
  ui.fils.onclick = (e) => {
    const t = e.target.closest('[data-gcf]'); if (!t) return;
    const k = +t.dataset.gcf; if (hiddenTools.has(k)) hiddenTools.delete(k); else hiddenTools.add(k);
    paint();
  };
  ui.mode.onclick = (e) => {
    const t = e.target.closest('[data-gcmode]'); if (!t || t.dataset.gcmode === mode) return;
    mode = t.dataset.gcmode;
    paint();
  };
  ui.only.onclick = () => {
    const showAll = ui.only.textContent === 'Show everything';
    hidden.clear();
    if (!showAll) used.forEach((f) => { if (!SUPPORT.has(f)) hidden.add(f); });
    paint();
  };

  // ---- layers
  const L = g.layerEnd.length;
  ui.slider.min = '1'; ui.slider.max = String(L); ui.slider.value = String(L);
  function setLayer(v) {
    v = Math.max(1, Math.min(L, v | 0));
    ui.slider.value = String(v);
    if (lines) mesh.geometry.setDrawRange(0, g.layerEnd[v - 1] * 2); else mesh.count = g.layerEnd[v - 1];
    ui.label.textContent = `Layer ${v} of ${L} · ${g.layerZ[v - 1].toFixed(2)} mm`;
    draw();
  }
  ui.slider.oninput = () => setLayer(+ui.slider.value);
  let timer = 0;
  const stop = () => { if (timer) { clearInterval(timer); timer = 0; ui.play.textContent = 'Play'; } };
  ui.play.onclick = () => {
    if (timer) return stop();
    if (+ui.slider.value >= L) setLayer(1);
    ui.play.textContent = 'Pause';
    const step = Math.max(1, Math.round(L / 300)); // the whole print in about ten seconds
    timer = setInterval(() => { const v = +ui.slider.value; if (v >= L) return stop(); setLayer(Math.min(L, v + step)); }, 33);
  };

  // ---- camera
  const ctl = new OrbitControls(camera, renderer.domElement);
  ctl.enableDamping = false;
  ctl.zoomSpeed = 1.2;
  const mid = new THREE.Vector3((b.minX + b.maxX) / 2, (b.minY + b.maxY) / 2, b.maxZ / 2);
  const radius = Math.max(20, Math.hypot(b.maxX - b.minX, b.maxY - b.minY, b.maxZ) / 2);
  function view(dir) {
    const d = radius / Math.sin((camera.fov / 2) * Math.PI / 180) * 1.08;
    ctl.target.copy(mid);
    camera.position.copy(mid).addScaledVector(dir.clone().normalize(), d);
    camera.near = d / 200; camera.far = d * 20; camera.updateProjectionMatrix();
    ctl.update();
    draw();
  }
  ui.reset.onclick = () => view(new THREE.Vector3(0.75, -1, 0.62));
  ui.top.onclick = () => view(new THREE.Vector3(0, -0.001, 1));
  ctl.addEventListener('change', draw);

  let raf = 0, dead = false;
  function draw() {
    if (raf || dead) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      if (dead) return;
      sun.position.copy(camera.position); // lit from where you look, so nothing you face is in the dark
      renderer.render(scene, camera);
    });
  }
  const fit = () => {
    const w = box.clientWidth, h = box.clientHeight;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    camera.aspect = w / h; camera.updateProjectionMatrix();
    draw();
  };
  const ro = new ResizeObserver(fit);
  ro.observe(box);
  fit();
  ui.msg('');
  paint();
  setLayer(L);
  view(new THREE.Vector3(0.75, -1, 0.62));

  return {
    destroy() {
      dead = true; stop(); ro.disconnect(); ctl.dispose();
      mesh.geometry.dispose(); mesh.material.dispose(); gridGeo.dispose(); plate.geometry.dispose();
      renderer.dispose(); renderer.forceContextLoss(); renderer.domElement.remove();
    },
  };
}

function luminance(css) {
  const m = /rgba?\((\d+)[, ]+(\d+)[, ]+(\d+)/.exec(css || '');
  return m ? (0.2126 * m[1] + 0.7152 * m[2] + 0.0722 * m[3]) / 255 : 1;
}
