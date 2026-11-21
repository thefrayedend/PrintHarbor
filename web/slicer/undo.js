// Slicer page: undo. See slicer.js for the list of files.
import * as P from './paint.js';
import { S, $ } from './core.js';
import { objGroup, invalidate } from './scene.js';
import { buildBed, setPlate } from './plates.js';
import { mirrorObject, recolor, checkBounds, removeObject, duplicate, select } from './objects.js';
import { renderSide, renderTools } from './toolbar.js';
import { renderXf } from './panels.js';
import { setLayer } from './preview.js';

// ================================================================== undo
export function pushUndo(u) {
  S.undo.push(u);
  if (S.undo.length > 60) {
    const old = S.undo.shift();
    if (old.type === 'remove' && !S.objects.includes(old.obj)) { old.obj.mesh.geometry.dispose(); old.obj.mesh.material.dispose(); }
  }
}
export function undo() {
  const u = S.undo.pop();
  if (!u) return;
  const restore = (obj, m) => { obj.mesh.matrix.copy(m); obj.mesh.matrix.decompose(obj.mesh.position, obj.mesh.quaternion, obj.mesh.scale); obj.mesh.updateMatrixWorld(true); };
  if (u.type === 'xf') restore(u.obj, u.m);
  if (u.type === 'many') for (const it of u.list) restore(it.obj, it.m);
  if (u.type === 'mirror') { const keep = S.undo; S.undo = []; mirrorObject(u.obj); S.undo = keep; return; }
  if (u.type === 'paint') { const arr = P.channel(u.obj, u.ch || 'color', true); for (const [t, v] of u.changed) arr[t] = v; if (u.filament) u.obj.filament = u.filament; recolor(u.obj); }
  if (u.type === 'remove') { objGroup.add(u.obj.mesh); S.objects.push(u.obj); }
  checkBounds(); buildBed(); renderSide(); invalidate();
}

export function onKey(e) {
  if (!$('#view-slicer').classList.contains('active') || e.target.closest('input, select, textarea, dialog')) return;
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'z') { e.preventDefault(); undo(); return; }
  if (S.mode === 'preview') {
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') { e.preventDefault(); setLayer((S.layer || 1) + (e.key === 'ArrowUp' ? 1 : -1)); }
    return;
  }
  if (e.key === 'PageDown' || e.key === 'PageUp') { setPlate(S.plate + (e.key === 'PageDown' ? 1 : -1), true, true); return; }
  if ((e.key === 'Delete' || e.key === 'Backspace') && S.selected && S.mode === 'prepare') { removeObject(S.selected); return; }
  if (S.mode === 'paint') {
    if (/^[0-9]$/.test(e.key)) {
      const n = +e.key;
      if (S.paint.ch === 'color') { if (n <= S.filaments.length) S.paint.filament = n; } else if (n <= 2) S.paint.mark = n;
      renderSide(); return;
    }
    if (e.key === '[' || e.key === ']') { S.paint.size = Math.max(0.3, Math.min(40, +(S.paint.size * (e.key === ']' ? 1.25 : 0.8)).toFixed(2))); renderSide(); return; }
    const t = { c: 'circle', s: 'sphere', t: 'triangle', h: 'height', f: 'fill', g: 'gap' }[e.key.toLowerCase()];
    if (t) { S.paint.tool = t; renderSide(); }
    return;
  }
  if (S.mode === 'prepare' && (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'd') { e.preventDefault(); duplicate(S.selected); return; }
  if (S.mode === 'prepare' && e.key.toLowerCase() === 'x' && S.selected) { mirrorObject(S.selected); return; }
  if (S.mode === 'prepare' && S.selected && /^Arrow(Left|Right|Up|Down)$/.test(e.key)) { // nudge 1 mm, Shift: 10 mm
    e.preventDefault();
    const d = e.shiftKey ? 10 : 1, o = S.selected;
    pushUndo({ type: 'xf', obj: o, m: o.mesh.matrix.clone() });
    o.mesh.position.x += e.key === 'ArrowRight' ? d : e.key === 'ArrowLeft' ? -d : 0;
    o.mesh.position.y += e.key === 'ArrowUp' ? d : e.key === 'ArrowDown' ? -d : 0;
    o.mesh.updateMatrixWorld(true); checkBounds(); if (o.plate !== S.plate) setPlate(o.plate, false); renderXf(); return;
  }
  const keys = { m: 'move', r: 'rotate', s: 'scale', f: 'face' };
  if (S.mode === 'prepare' && keys[e.key.toLowerCase()]) setTool(keys[e.key.toLowerCase()]);
}
export function setTool(t) {
  S.tool = t;
  select(S.selected);
  renderTools();
}
