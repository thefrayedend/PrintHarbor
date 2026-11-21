// Slicer page: the three modes (Prepare, Paint, Preview), the tool bar and the side panel
// frame. See slicer.js for the list of files.
import { $, S, esc } from './core.js';
import { objGroup, previewGroup, tcontrols, invalidate } from './scene.js';
import { platesObjects, buildBed } from './plates.js';
import { select, recolor, checkBounds } from './objects.js';
import { hideCursor, updateClip } from './pointer.js';
import { paintPanel, goButton, filamentsCard, coloursCard, pmFooter, settingsPanel, platesPanel, previewPanel, preparePanel } from './panels.js';

// ================================================================== mode + tool bar
const ICON = {
  add: '<path d="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6z"/>',
  mirror: '<path d="M11 2h2v20h-2zM3 18l6-6-6-6v12zm18 0V6l-6 6 6 6z"/>',
  move: '<path d="M10 9h4V6h3l-5-5-5 5h3v3zm-1 1H6V7l-5 5 5 5v-3h3v-4zm14 2-5-5v3h-3v4h3v3l5-5zm-9 3h-4v3H7l5 5 5-5h-3v-3z"/>',
  rotate: '<path d="M12 5V1L7 6l5 5V7a6 6 0 1 1-6 6H4a8 8 0 1 0 8-8z"/>',
  scale: '<path d="M21 3h-7l2.6 2.6L12 10.2 13.8 12l4.6-4.6L21 10V3zM3 21h7l-2.6-2.6 4.6-4.6L10.2 12l-4.6 4.6L3 14v7z"/>',
  face: '<path d="M4 18h16v2H4zM12 3 5 10l1.4 1.4L11 6.8V16h2V6.8l4.6 4.6L19 10z"/>',
  orient: '<path d="M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/>',
  arrange: '<path d="M3 3h8v8H3zm10 0h8v5h-8zm0 7h8v11h-8zM3 13h8v8H3z"/>',
  copy: '<path d="M16 1H4a2 2 0 0 0-2 2v14h2V3h12V1zm3 4H8a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2zm0 16H8V7h11v14z"/>',
  del: '<path d="M6 19a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/>',
  undo: '<path d="M12.5 8c-2.6 0-5 1-6.9 2.6L2 7v9h9l-3.6-3.6A8 8 0 0 1 20.1 16l2.4-.8A10.5 10.5 0 0 0 12.5 8z"/>',
  home: '<path d="M12 5.7 17 10v8h-3v-5h-4v5H7v-8l5-4.3M12 3 2 12h3v8h6v-6h2v6h6v-8h3L12 3z"/>',
  circle: '<path d="M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zm0 2a7 7 0 1 1 0 14 7 7 0 0 1 0-14z"/>',
  sphere: '<path d="M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zm0 2a7 7 0 0 1 6.6 4.7C16.8 8.6 14.5 8 12 8s-4.8.6-6.6 1.7A7 7 0 0 1 12 5z"/>',
  triangle: '<path d="M12 3 2 20h20L12 3zm0 4 6.5 11h-13L12 7z"/>',
  height: '<path d="M3 9h18v6H3zM11 2h2v5h-2zm0 15h2v5h-2z"/>',
  fill: '<path d="M16.6 8.9 8.1.4 6.7 1.8l2.4 2.4-5.2 5.2a1.5 1.5 0 0 0 0 2.1l5.5 5.5a1.5 1.5 0 0 0 2.1 0l5.5-5.5a1.5 1.5 0 0 0 0-2.1zM5.2 10 10 5.2 14.8 10H5.2zM19 11.5s-2 2.2-2 3.5a2 2 0 0 0 4 0c0-1.3-2-3.5-2-3.5zM2 20h20v4H2z"/>',
  gap: '<path d="M4 4h6v6H4zm10 0h6v6h-6zM4 14h6v6H4zm10 0h6v6h-6z" opacity=".45"/><path d="M10 10h4v4h-4z"/>',
};
export const ic = (n) => `<svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><g fill="currentColor">${ICON[n]}</g></svg>`;
export const PAINT_TOOLS = [['circle', 'Circle', 'C'], ['sphere', 'Sphere', 'S'], ['triangle', 'Triangle', 'T'], ['height', 'Height range', 'H'], ['fill', 'Fill', 'F'], ['gap', 'Gap fill', 'G']];
export function renderTools() {
  const t = $('#sl-tools');
  const sel = !!S.selected;
  if (S.mode === 'prepare') {
    t.innerHTML = `<div class="grp"><button data-sl="add" title="Add a model (STL, OBJ, 3MF)">${ic('add')}</button></div>
      <div class="grp">${[['move', 'Move (M)'], ['rotate', 'Rotate (R)'], ['scale', 'Scale (S)'], ['face', 'Lay on face (F): click the side that should be on the plate']].map(([k, tt]) => `<button data-tool="${k}" class="${S.tool === k ? 'active' : ''}" title="${tt}">${ic(k)}</button>`).join('')}</div>
      <div class="grp"><button data-sl="orient" title="Auto-orient: biggest flat side down" ${sel ? '' : 'disabled'}>${ic('orient')}</button>
        <button data-sl="arrange" title="Arrange this plate (what does not fit goes onto the next plate)" ${platesObjects(S.plate).length ? '' : 'disabled'}>${ic('arrange')}</button>
        <button data-sl="copy" title="Duplicate (Ctrl+D)" ${sel ? '' : 'disabled'}>${ic('copy')}</button>
        <button data-sl="mirror" title="Mirror left-right (X)" ${sel ? '' : 'disabled'}>${ic('mirror')}</button>
        <button data-sl="del" title="Delete (Del)" ${sel ? '' : 'disabled'}>${ic('del')}</button></div>
      <div class="grp"><button data-sl="undo" title="Undo (Ctrl+Z)" ${S.undo.length ? '' : 'disabled'}>${ic('undo')}</button><button data-sl="view" title="Show the whole plate">${ic('home')}</button></div>`;
  } else if (S.mode === 'paint') {
    t.innerHTML = `<div class="grp">${PAINT_TOOLS.map(([k, l, key]) => `<button data-ptool="${k}" class="${S.paint.tool === k ? 'active' : ''}" title="${l} (${key})">${ic(k)}</button>`).join('')}</div>
      <div class="grp"><button data-sl="undo" title="Undo (Ctrl+Z)" ${S.undo.length ? '' : 'disabled'}>${ic('undo')}</button><button data-sl="view" title="Show the whole plate">${ic('home')}</button></div>`;
  } else t.innerHTML = `<div class="grp"><button data-sl="view" title="Show the whole plate">${ic('home')}</button></div>`;
  const touch = matchMedia('(pointer: coarse)').matches;
  const ptip = {
    circle: 'Drag over a model to paint what you see inside the circle',
    sphere: 'Drag to paint everything inside the ball, also around corners',
    triangle: 'Drag over the model to paint whole model triangles',
    height: 'Drag to paint a horizontal band (stripes, colour changes by height)',
    fill: S.paint.smart ? 'Click a surface: fills it up to sharp edges' : 'Click: fills the whole connected area of the same colour',
    gap: 'Click a model: fills small unpainted gaps with the paint around them',
  }[S.paint.tool];
  const hints = {
    prepare: S.tool === 'face' ? 'Tap the side of a model that should lie on the plate' : (S.objects.length ? (touch ? 'Drag a model to move it (past the last plate makes a new one) · one finger on empty space turns the view · two fingers zoom and move' : 'Drag a model to move it, also onto another plate (past the last one makes a new plate) · drag empty space to turn the view · right-drag to pan · wheel to zoom') : ''),
    paint: touch ? ptip : `${ptip} · Shift = erase · Alt+wheel = size · Ctrl+wheel = section`,
    preview: '',
  };
  $('#sl-hint').textContent = S.objects.length || S.mode !== 'prepare' ? hints[S.mode] : '';
}

export function applyMode() {
  const prev = S.mode === 'preview';
  objGroup.visible = !prev;
  previewGroup.visible = prev;
  hideCursor();
  $('#sl-layers').classList.toggle('hidden', !prev || !(S.res[S.plate] && S.res[S.plate].g));
  $('#sl-legend').classList.toggle('hidden', !prev || !(S.res[S.plate] && S.res[S.plate].g));
  if (S.mode !== 'prepare') tcontrols.detach(); else select(S.selected);
  updateClip();
  for (const o of S.objects) recolor(o);
  checkBounds();
  buildBed();
  renderTools(); renderSide();
  invalidate();
}

// ================================================================== side panel
export function renderSide() {
  const side = $('#sl-side');
  if (!side || (!$('#view-slicer').classList.contains('active') && side.innerHTML)) return;
  if (S.loading) { side.innerHTML = '<div class="card"><p class="muted">Reading the printer profiles…</p><p class="small muted">The first time after an update or engine download this can take up to a minute.</p></div>'; return; }
  if (S.err) { side.innerHTML = `<div class="card"><div class="notice err">${esc(S.err)}</div><button class="btn small" data-sl="retry">Try again</button></div>`; return; }
  if (!S.info) { side.innerHTML = '<div class="card"><p class="muted">Add a printer first (Settings → Advanced → Printers).</p></div>'; return; }
  if (!S.info.known) {
    side.innerHTML = `<div class="card"><h2>Which printer is ${esc(S.info.name)}?</h2>
      <p class="muted small">The printer has not told PrintHarbor its model yet (is it switched on?). Pick it once; only its own profiles are used.</p>
      <select data-set="model"><option value="">Choose…</option>${S.info.models.map((m) => `<option>${esc(m)}</option>`).join('')}</select></div>`;
    return;
  }
  const keep = side.scrollTop;
  if (S.pm && S.mode === 'paint') side.innerHTML = paintPanel().replace(goButton(), '').replace(filamentsCard(false), coloursCard()) + pmFooter();
  else if (S.pm) side.innerHTML = S.tab === 'settings' ? settingsPanel() : platesPanel();
  else if (S.mode === 'paint') side.innerHTML = paintPanel();
  else if (S.mode === 'preview') side.innerHTML = previewPanel();
  else side.innerHTML = preparePanel();
  side.scrollTop = keep;
  renderTools();
}
