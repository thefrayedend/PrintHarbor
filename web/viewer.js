// Tiny WebGL viewer for PrintHarbor meshes (no libraries).
// Mesh format from the server: uint32 triangle count, then float32 x,y,z
// per vertex (centered on X/Y, bottom at Z=0).
"use strict";

function createViewer(container) {
  const canvas = document.createElement("canvas");
  canvas.className = "viewer-canvas";
  container.innerHTML = "";
  container.appendChild(canvas);
  const hint = document.createElement("div");
  hint.className = "viewer-hint";
  hint.textContent = "Loading 3D view…";
  container.appendChild(hint);

  const gl = canvas.getContext("webgl", { antialias: true });
  if (!gl) { hint.textContent = "3D view needs WebGL, which this browser doesn't offer."; return { load() {}, destroy() {} }; }

  const vs = `attribute vec3 p; attribute vec3 n; uniform mat4 mvp; uniform mat4 rot; varying vec3 vn; varying float vz;
    void main(){ gl_Position = mvp * vec4(p,1.0); vn = (rot * vec4(n,0.0)).xyz; vz = p.z; }`;
  const fs = `precision mediump float; varying vec3 vn; varying float vz; uniform vec3 col; uniform float uFlat;
    void main(){ if (uFlat > 0.5) { gl_FragColor = vec4(col,1.0); return; }
      vec3 N = normalize(vn); if (!gl_FrontFacing) N = -N;
      float d = max(dot(N, normalize(vec3(-0.4,0.5,0.8))), 0.0);
      float rim = pow(1.0 - abs(N.z), 3.0) * 0.15;
      gl_FragColor = vec4(col * (0.35 + 0.65*d) + rim, 1.0); }`;
  const sh = (type, src) => { const s = gl.createShader(type); gl.shaderSource(s, src); gl.compileShader(s); return s; };
  const prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, vs));
  gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, fs));
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) { hint.textContent = "3D view failed to start: " + gl.getProgramInfoLog(prog); return { load() {}, destroy() {} }; }
  gl.useProgram(prog);
  const loc = { p: gl.getAttribLocation(prog, "p"), n: gl.getAttribLocation(prog, "n"), mvp: gl.getUniformLocation(prog, "mvp"),
    rot: gl.getUniformLocation(prog, "rot"), col: gl.getUniformLocation(prog, "col"), flat: gl.getUniformLocation(prog, "uFlat") };

  let mesh = null, grid = null, radius = 50, center = [0, 0, 0];
  let yaw = -0.6, pitch = 0.9, dist = 3, raf = 0;
  const dark = matchMedia("(prefers-color-scheme: dark)").matches;

  function buffers(pos, nor) {
    const b = { count: pos.length / 3 };
    b.p = gl.createBuffer(); gl.bindBuffer(gl.ARRAY_BUFFER, b.p); gl.bufferData(gl.ARRAY_BUFFER, pos, gl.STATIC_DRAW);
    if (nor) { b.n = gl.createBuffer(); gl.bindBuffer(gl.ARRAY_BUFFER, b.n); gl.bufferData(gl.ARRAY_BUFFER, nor, gl.STATIC_DRAW); }
    return b;
  }

  function mul(a, b) { const r = new Float32Array(16); for (let i = 0; i < 4; i++) for (let j = 0; j < 4; j++) { let s = 0; for (let k = 0; k < 4; k++) s += a[k * 4 + j] * b[i * 4 + k]; r[i * 4 + j] = s; } return r; }
  function persp(f, asp, n, fa) { const t = 1 / Math.tan(f / 2); return new Float32Array([t / asp, 0, 0, 0, 0, t, 0, 0, 0, 0, (fa + n) / (n - fa), -1, 0, 0, 2 * fa * n / (n - fa), 0]); }
  function rotX(a) { const c = Math.cos(a), s = Math.sin(a); return new Float32Array([1, 0, 0, 0, 0, c, s, 0, 0, -s, c, 0, 0, 0, 0, 1]); }
  function rotZ(a) { const c = Math.cos(a), s = Math.sin(a); return new Float32Array([c, s, 0, 0, -s, c, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1]); }
  function trans(x, y, z) { return new Float32Array([1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, x, y, z, 1]); }

  function draw() {
    raf = 0;
    const w = canvas.clientWidth, h = canvas.clientHeight, dpr = devicePixelRatio || 1;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(h * dpr)) { canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr); }
    gl.viewport(0, 0, canvas.width, canvas.height);
    const bg = dark ? [0.1, 0.12, 0.14] : [0.93, 0.94, 0.96];
    gl.clearColor(bg[0], bg[1], bg[2], 1);
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    gl.enable(gl.DEPTH_TEST);
    if (!mesh) return;
    const rot = mul(rotX(-pitch), rotZ(yaw));
    const view = mul(trans(0, 0, -radius * dist), mul(rot, trans(-center[0], -center[1], -center[2])));
    const mvp = mul(persp(0.7, w / Math.max(h, 1), radius * 0.05, radius * 20), view);
    gl.uniformMatrix4fv(loc.mvp, false, mvp);
    gl.uniformMatrix4fv(loc.rot, false, rot);
    // Ground grid.
    gl.uniform1f(loc.flat, 1);
    gl.uniform3fv(loc.col, dark ? [0.25, 0.28, 0.32] : [0.78, 0.8, 0.84]);
    gl.disableVertexAttribArray(loc.n);
    gl.bindBuffer(gl.ARRAY_BUFFER, grid.p); gl.enableVertexAttribArray(loc.p); gl.vertexAttribPointer(loc.p, 3, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.LINES, 0, grid.count);
    // Model.
    gl.uniform1f(loc.flat, 0);
    gl.uniform3fv(loc.col, [0.33, 0.55, 0.85]);
    gl.bindBuffer(gl.ARRAY_BUFFER, mesh.p); gl.vertexAttribPointer(loc.p, 3, gl.FLOAT, false, 0, 0);
    gl.bindBuffer(gl.ARRAY_BUFFER, mesh.n); gl.enableVertexAttribArray(loc.n); gl.vertexAttribPointer(loc.n, 3, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.TRIANGLES, 0, mesh.count);
  }
  const redraw = () => { if (!raf) raf = requestAnimationFrame(draw); };

  // Orbit (drag), zoom (wheel / pinch).
  let drag = null, pinch = 0;
  canvas.addEventListener("pointerdown", (e) => { drag = [e.clientX, e.clientY]; canvas.setPointerCapture(e.pointerId); });
  canvas.addEventListener("pointermove", (e) => {
    if (!drag) return;
    yaw += (e.clientX - drag[0]) * 0.01;
    pitch = Math.max(0.05, Math.min(3.0, pitch - (e.clientY - drag[1]) * 0.01));
    drag = [e.clientX, e.clientY]; redraw();
  });
  canvas.addEventListener("pointerup", () => (drag = null));
  canvas.addEventListener("wheel", (e) => { e.preventDefault(); dist = Math.max(0.6, Math.min(12, dist * (e.deltaY > 0 ? 1.1 : 0.9))); redraw(); }, { passive: false });
  canvas.addEventListener("touchmove", (e) => {
    if (e.touches.length !== 2) return;
    const d = Math.hypot(e.touches[0].clientX - e.touches[1].clientX, e.touches[0].clientY - e.touches[1].clientY);
    if (pinch) { dist = Math.max(0.6, Math.min(12, dist * pinch / d)); redraw(); }
    pinch = d;
  }, { passive: true });
  canvas.addEventListener("touchend", () => (pinch = 0));
  const ro = new ResizeObserver(redraw); ro.observe(canvas);

  async function load(url) {
    hint.textContent = "Loading 3D view…"; hint.style.display = "";
    try {
      const r = await fetch(url);
      if (!r.ok) throw new Error((await r.text()) || r.statusText);
      const buf = await r.arrayBuffer();
      const n = new DataView(buf).getUint32(0, true);
      const pos = new Float32Array(buf, 4, n * 9);
      const nor = new Float32Array(n * 9);
      let minx = Infinity, miny = Infinity, maxx = -Infinity, maxy = -Infinity, maxz = 0;
      for (let t = 0; t < n; t++) {
        const o = t * 9;
        const ux = pos[o + 3] - pos[o], uy = pos[o + 4] - pos[o + 1], uz = pos[o + 5] - pos[o + 2];
        const vx = pos[o + 6] - pos[o], vy = pos[o + 7] - pos[o + 1], vz = pos[o + 8] - pos[o + 2];
        let nx = uy * vz - uz * vy, ny = uz * vx - ux * vz, nz = ux * vy - uy * vx;
        const l = Math.hypot(nx, ny, nz) || 1; nx /= l; ny /= l; nz /= l;
        for (let k = 0; k < 3; k++) {
          nor[o + k * 3] = nx; nor[o + k * 3 + 1] = ny; nor[o + k * 3 + 2] = nz;
          const x = pos[o + k * 3], y = pos[o + k * 3 + 1], z = pos[o + k * 3 + 2];
          if (x < minx) minx = x; if (x > maxx) maxx = x; if (y < miny) miny = y; if (y > maxy) maxy = y; if (z > maxz) maxz = z;
        }
      }
      mesh = buffers(pos, nor);
      center = [(minx + maxx) / 2, (miny + maxy) / 2, maxz / 2];
      radius = Math.max(Math.hypot(maxx - minx, maxy - miny, maxz) / 2, 1);
      // Grid sized to the model, 10 mm squares (or coarser for big parts).
      const ext = Math.max(maxx - minx, maxy - miny) * 0.75 + 10;
      const step = ext > 300 ? 50 : ext > 120 ? 20 : 10;
      const g = [];
      for (let v = -Math.ceil(ext / step) * step; v <= ext; v += step) { g.push(v, -ext, 0, v, ext, 0, -ext, v, 0, ext, v, 0); }
      grid = buffers(new Float32Array(g.map((x, i) => (i % 3 === 0 ? x + center[0] : i % 3 === 1 ? x + center[1] : 0))), null);
      dist = 2.6;
      hint.textContent = `${Math.round(maxx - minx)} × ${Math.round(maxy - miny)} × ${Math.round(maxz)} mm · drag to rotate, scroll to zoom`;
      hint.classList.add("ok");
      redraw();
    } catch (e) {
      hint.textContent = "Couldn't show this model: " + e.message;
    }
  }
  return { load, destroy() { ro.disconnect(); if (raf) cancelAnimationFrame(raf); const ext = gl.getExtension("WEBGL_lose_context"); if (ext) ext.loseContext(); } };
}
