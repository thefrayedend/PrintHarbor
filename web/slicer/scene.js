// Slicer page: the 3D scene (renderer, camera, controls, the render loop) and the plate
// thumbnails. See slicer.js for the list of files.
import * as THREE from 'three';
import { OrbitControls } from './vendor/OrbitControls.js';
import { TransformControls } from './vendor/TransformControls.js';
import { S, $ } from './core.js';
import { setPlate, buildBed, viewHome } from './plates.js';
import { fixScale, dropToBed, checkBounds, worldBox } from './objects.js';
import { onPointerDown, onWheel, onPointerMove, onPointerUp, hideCursor, applyClip } from './pointer.js';
import { pushUndo } from './undo.js';
import { renderSide } from './toolbar.js';
import { renderXf } from './panels.js';
import { towerSpot } from './slice.js';

export let renderer, scene, camera, controls, tcontrols, bedGroup, objGroup, previewGroup, cursorSphere, cursorRing, cursorBand, raycaster, needRender = true;

// ================================================================== 3D setup
export function setup3d() {
  S.ready = true;
  const stage = $('#sl-stage');
  renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true, preserveDrawingBuffer: false });
  renderer.setPixelRatio(Math.min(2, window.devicePixelRatio || 1));
  renderer.localClippingEnabled = true;
  renderer.domElement.tabIndex = 0;
  stage.prepend(renderer.domElement);
  scene = new THREE.Scene();
  camera = new THREE.PerspectiveCamera(40, 1, 1, 8000);
  camera.up.set(0, 0, 1);
  window.__slCam = camera;
  window.__slTower = (i) => towerSpot(i);
  scene.add(new THREE.HemisphereLight(0xffffff, 0x3a3f47, 1.6));
  const sun = new THREE.DirectionalLight(0xffffff, 1.6); sun.position.set(-150, -250, 400); scene.add(sun);
  const fill = new THREE.DirectionalLight(0xffffff, 0.6); fill.position.set(300, 200, 150); scene.add(fill);
  bedGroup = new THREE.Group(); objGroup = new THREE.Group(); previewGroup = new THREE.Group();
  scene.add(bedGroup, objGroup, previewGroup);
  controls = new OrbitControls(camera, renderer.domElement);
  controls.enableDamping = true; controls.dampingFactor = 0.15;
  controls.mouseButtons = { LEFT: THREE.MOUSE.ROTATE, MIDDLE: THREE.MOUSE.DOLLY, RIGHT: THREE.MOUSE.PAN };
  controls.addEventListener('change', invalidate);
  tcontrols = new TransformControls(camera, renderer.domElement);
  tcontrols.setSize(0.9);
  tcontrols.addEventListener('change', invalidate);
  tcontrols.addEventListener('dragging-changed', (e) => {
    controls.enabled = !e.value;
    if (e.value) pushUndo({ type: 'xf', obj: S.selected, m: S.selected.mesh.matrix.clone() });
    else if (S.selected) { fixScale(S.selected); dropToBed(S.selected); checkBounds(); if (S.selected.plate !== S.plate) setPlate(S.selected.plate, false); renderSide(); }
  });
  tcontrols.addEventListener('objectChange', () => { if (S.selected) { if (tcontrols.mode === 'translate') dropToBed(S.selected); renderXf(); } });
  scene.add(tcontrols.getHelper());
  raycaster = new THREE.Raycaster();
  raycaster.firstHitOnly = true;
  const ghost = (color, opacity) => new THREE.MeshBasicMaterial({ color, transparent: true, opacity, depthWrite: false, side: THREE.DoubleSide });
  cursorSphere = new THREE.Mesh(new THREE.SphereGeometry(1, 24, 16), ghost(0xffffff, 0.25));
  cursorRing = new THREE.Mesh(new THREE.RingGeometry(0.9, 1, 48), ghost(0xffffff, 0.9));
  cursorRing.material.depthTest = false;
  cursorBand = new THREE.Mesh(new THREE.BoxGeometry(1, 1, 1), ghost(0xffffff, 0.18));
  for (const c of [cursorSphere, cursorRing, cursorBand]) { c.visible = false; c.renderOrder = 10; scene.add(c); }
  const el = renderer.domElement;
  // Painting starts before the view controls see the press (capture on the parent).
  stage.addEventListener('pointerdown', onPointerDown, true);
  stage.addEventListener('wheel', onWheel, { capture: true, passive: false });
  el.addEventListener('pointermove', onPointerMove);
  el.addEventListener('pointerup', onPointerUp);
  el.addEventListener('pointerleave', () => { hideCursor(); invalidate(); });
  new ResizeObserver(resize).observe(stage);
  buildBed();
  viewHome();
  const loop = () => {
    requestAnimationFrame(loop);
    if (!$('#view-slicer').classList.contains('active')) return;
    controls.update();
    if (needRender) { needRender = false; renderer.render(scene, camera); }
  };
  loop();
}
export function invalidate() { needRender = true; }
export function resize() {
  if (!renderer) return;
  const st = $('#sl-stage');
  const w = st.clientWidth, h = st.clientHeight;
  if (!w || !h) return;
  renderer.setSize(w, h, false);
  camera.aspect = w / h; camera.updateProjectionMatrix();
  invalidate();
}
export const bed = () => (S.info && S.info.bed) || { x0: 0, y0: 0, w: 256, d: 256, h: 256 };

// ================================================================== thumbnail for the file list / printer screen
let thumbRenderer = null;
// Sizes as OrcaSlicer makes them: plate picture 512x512, small 128x128, G-code thumbnail 140x110.
export function thumbnail(list, w = 512, h = 512) {
  try {
    thumbRenderer ??= new THREE.WebGLRenderer({ antialias: true, alpha: true, preserveDrawingBuffer: true });
    thumbRenderer.setSize(w, h, false);
    const box = new THREE.Box3();
    list.forEach((o) => box.union(worldBox(o)));
    const c = box.getCenter(new THREE.Vector3()), r = box.getSize(new THREE.Vector3()).length() / 2;
    const cam = new THREE.PerspectiveCamera(30, w / h, 1, 5000);
    cam.up.set(0, 0, 1);
    cam.position.set(c.x + r * 1.6, c.y - r * 2.4, c.z + r * 2.0);
    cam.lookAt(c);
    const vis = S.objects.map((o) => o.mesh.visible);
    const was = [bedGroup.visible, previewGroup.visible, objGroup.visible, tcontrols.getHelper().visible];
    bedGroup.visible = false; previewGroup.visible = false; objGroup.visible = true; tcontrols.getHelper().visible = false; hideCursor();
    S.objects.forEach((o) => { o.mesh.visible = list.includes(o); o.mesh.material.emissive.setHex(0); o.mesh.material.clippingPlanes = null; });
    thumbRenderer.render(scene, cam);
    S.objects.forEach((o, k) => { o.mesh.visible = vis[k]; applyClip(o); });
    [bedGroup.visible, previewGroup.visible, objGroup.visible, tcontrols.getHelper().visible] = was;
    checkBounds();
    return thumbRenderer.domElement.toDataURL('image/png');
  } catch (_) { return ''; }
}
