// Drives the slicer page through a fixed scenario in a real (headless) browser and records what
// it sees after every step: the models and where they stand, paint, plates, settings, the side
// panel, the slice result. Compare two recordings with compare.js: after a change that should
// not alter behaviour (tidying up, splitting a file) they must be identical.
//
//   node tests/ui/slicer.scenario.js <port> <out.json> <fixture folder from mkfixture.sh>
//
// PrintHarbor must be running on that port with the fixture's config and one simulated printer
// (run.sh does all of that).
const { chromium } = require('playwright');
const fs = require('fs');
const [port, outFile, fx] = process.argv.slice(2);
if (!port || !outFile || !fx) { console.error('usage: node slicer.scenario.js <port> <out.json> <fixture folder>'); process.exit(2); }
const base = `http://127.0.0.1:${port}`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const b = await chromium.launch({ args: ['--use-gl=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] });
  const ctx = await b.newContext({ viewport: { width: 1400, height: 900 } });
  const p = await ctx.newPage();
  const errs = []; const log = [];
  p.on('console', (m) => { if (m.type() === 'error') errs.push('console: ' + m.text().slice(0, 300)); });
  p.on('pageerror', (e) => errs.push('pageerror: ' + e.message.slice(0, 300)));
  p.on('response', (r) => { if (r.status() >= 400) errs.push(r.status() + ' ' + r.url().replace(base, '').replace(/[0-9a-f]{12}/g, 'ID')); });
  p.on('dialog', (d) => { log.push('dialog: ' + d.type() + ' ' + d.message().slice(0, 80)); d.accept(d.type() === 'prompt' ? 'Scenario project' : undefined).catch(() => {}); });
  await p.goto(base + '/'); await p.waitForFunction(() => typeof state !== 'undefined' && state.printers && state.printers.length > 0, null, { timeout: 15000 });
  await p.evaluate(() => show('slicer'));
  let f;
  for (let i = 0; i < 100 && !f; i++) { await sleep(100); f = p.frames().find((x) => /\/slicer\//.test(x.url())); }
  await f.waitForFunction(() => window.__sl && window.__sl.ready && window.__sl.info && !window.__sl.loading, null, { timeout: 20000 });
  const steps = [];
  const snap = async (name) => {
    await sleep(350);
    const s = await f.evaluate(() => {
      const S = window.__sl; const r3 = (x) => Math.round(x * 1000) / 1000;
      const h = (str) => { let x = 0; for (let i = 0; i < str.length; i++) x = (x * 31 + str.charCodeAt(i)) | 0; return x; };
      const txt = (sel) => { const e = document.querySelector(sel); return e ? e.innerText.replace(/\s+/g, ' ').trim() : null; };
      return {
        mode: S.mode, tool: S.tool, nPlates: S.nPlates, plate: S.plate, process: S.process, bedType: S.bedType, tab: S.tab, pm: S.pm,
        filaments: S.filaments.map((x) => [x.preset, x.color]), overrides: S.overrides, undo: S.undo.length, layer: S.layer,
        selected: S.selected ? S.selected.name : null,
        objects: S.objects.map((o) => ({ name: o.name, plate: o.plate, tris: o.states.length, fil: o.filament,
          pos: o.mesh.position.toArray().map(r3), rot: o.mesh.rotation.toArray().slice(0, 3).map(r3), scale: o.mesh.scale.toArray().map(r3),
          painted: Array.from(o.states).filter((x) => x).length, statesHash: h(Array.from(o.states).join(',')), sup: o.sup ? Array.from(o.sup).filter((x) => x).length : 0, seam: o.seam ? Array.from(o.seam).filter((x) => x).length : 0, visible: o.mesh.visible })),
        res: S.res.map((r) => (r ? { hasG: !!r.g, layers: r.g ? r.g.layerZ.length : 0, segs: r.g ? r.g.count : 0, tools: r.tools || null } : null)),
        paint: { ch: S.paint.ch, tool: S.paint.tool, filament: S.paint.filament, mark: S.paint.mark },
        project: S.project ? { name: S.project.name } : null,
        side: txt('#sl-side'), sideHash: h(document.querySelector('#sl-side').innerHTML.replace(/[0-9a-f]{12}/g, 'ID')), tools: h(document.querySelector('#sl-tools').innerHTML), plates: txt('#sl-plates'), hint: txt('#sl-hint'),
        empty: !document.querySelector('#sl-empty').classList.contains('hidden'), layersBar: !document.querySelector('#sl-layers').classList.contains('hidden'), legend: txt('#sl-legend'),
        busy: !document.querySelector('#sl-busy').classList.contains('hidden'), toast: txt('#toast'), allOpen: document.querySelector('#sl-all-dialog').open, hash: location.hash.replace(/[0-9a-f]{12}/g, 'ID'),
      };
    });
    steps.push({ name, s });
  };
  const click = async (sel) => { await f.locator(sel).first().click({ timeout: 8000 }); };
  const act = async (a) => click(`#view-slicer [data-sl="${a}"]`);
  const waitObjs = (n) => f.waitForFunction((k) => window.__sl.objects.length === k && !window.__sl.loading, n, { timeout: 30000 });
  const screenOf = (i) => f.evaluate((k) => {
    const o = window.__sl.objects[k]; const cam = window.__slCam; cam.updateMatrixWorld();
    o.mesh.geometry.computeBoundingBox(); o.mesh.updateMatrixWorld();
    const c = o.mesh.geometry.boundingBox.getCenter(o.mesh.position.clone()).applyMatrix4(o.mesh.matrixWorld).project(cam);
    const r = document.querySelector('#sl-stage canvas').getBoundingClientRect();
    return { x: r.left + (c.x + 1) / 2 * r.width, y: r.top + (1 - c.y) / 2 * r.height };
  }, i);
  const frameBox = await (await f.frameElement()).boundingBox();
  const mouse = async (pt, fn) => fn(frameBox.x + pt.x, frameBox.y + pt.y);
  const step = async (name, fn) => { try { await fn(); } catch (e) { log.push(`step "${name}" failed: ${String(e.message).split('\n')[0].slice(0, 200)}`); } await snap(name); };

  await snap('opened');
  await step('add cube', async () => { await f.locator('#sl-file').setInputFiles(fx + '/cube20.stl'); await waitObjs(1); });
  await step('add bar', async () => { await f.locator('#sl-file').setInputFiles(fx + '/bar.stl'); await waitObjs(2); });
  await step('select cube from the list', async () => { const id = await f.evaluate(() => window.__sl.objects[0].id); await click(`#sl-side [data-obj="${id}"]`); });
  await step('copy', async () => { await act('copy'); await waitObjs(3); });
  await step('mirror', async () => { await act('mirror'); });
  await step('rotate tool', async () => { await click('#sl-tools [data-tool="rotate"]'); });
  await step('scale tool', async () => { await click('#sl-tools [data-tool="scale"]'); });
  await step('move tool', async () => { await click('#sl-tools [data-tool="move"]'); });
  await step('click the bar in 3D', async () => { const pt = await screenOf(1); await mouse(pt, (x, y) => p.mouse.click(x, y)); });
  await step('drag the bar', async () => { const pt = await screenOf(1); await mouse(pt, (x, y) => p.mouse.move(x, y)); await p.mouse.down(); for (let i = 1; i <= 8; i++) await mouse({ x: pt.x + i * 12, y: pt.y + i * 5 }, (x, y) => p.mouse.move(x, y)); await p.mouse.up(); });
  await step('auto orient', async () => { await act('orient'); });
  await step('arrange', async () => { await act('arrange'); });
  await step('undo', async () => { await act('undo'); });
  await step('undo again', async () => { await act('undo'); });
  await step('delete selected', async () => { await act('del'); });
  await step('undo delete', async () => { await act('undo'); });
  await step('home view', async () => { await act('view'); });
  await step('add plate', async () => { await act('addplate'); });
  await step('back to plate 1', async () => { await click('#sl-plates [data-plate="0"]'); });
  await step('layer height 0.28', async () => { await f.locator('#sl-side select[data-ov="layer_height"]').first().selectOption({ label: '0.28' }, { timeout: 5000 }); });
  await step('walls 3', async () => { const inp = f.locator('#sl-side input[data-ov="wall_loops"]').first(); await inp.fill('3', { timeout: 5000 }); await inp.dispatchEvent('change'); });
  await step('supports on', async () => { await f.locator('#sl-side input[type="checkbox"][data-ov="enable_support"]').first().check({ timeout: 5000 }); });
  await step('infill 25', async () => { const inp = f.locator('#sl-side input[data-ov="sparse_infill_density"]').first(); await inp.fill('25', { timeout: 5000 }); await inp.dispatchEvent('change'); });
  await step('reset walls', async () => { await click('#sl-side [data-reset="wall_loops"]'); });
  await step('second nozzle', async () => { await f.locator('#sl-side select[data-set="machine"]').first().selectOption({ index: 1 }, { timeout: 5000 }); await f.waitForFunction(() => !window.__sl.loading, null, { timeout: 15000 }); });
  await step('first nozzle', async () => { await f.locator('#sl-side select[data-set="machine"]').first().selectOption({ index: 0 }, { timeout: 5000 }); await f.waitForFunction(() => !window.__sl.loading, null, { timeout: 15000 }); });
  await step('use loaded filaments', async () => { await act('loaded'); });
  await step('object to filament 2', async () => { await f.locator('#sl-side select[data-obj-fil]').first().selectOption({ index: 1 }, { timeout: 5000 }); });
  await step('select the last object', async () => { const id = await f.evaluate(() => window.__sl.objects[window.__sl.objects.length - 1].id); await click(`#sl-side [data-obj="${id}"]`); });
  await step('type a position', async () => { const inp = f.locator('#sl-side input[data-xf="x"]').first(); await inp.fill('60', { timeout: 5000 }); await inp.dispatchEvent('change'); });
  await step('type a turn', async () => { const inp = f.locator('#sl-side input[data-xf="rz"]').first(); await inp.fill('30', { timeout: 5000 }); await inp.dispatchEvent('change'); });
  await step('type a size', async () => { const inp = f.locator('#sl-side input[data-xf="s"]').first(); await inp.fill('150', { timeout: 5000 }); await inp.dispatchEvent('change'); });
  await step('object to plate 2', async () => { await f.locator('#sl-side select[data-obj-plate]').first().selectOption({ index: 1 }, { timeout: 5000 }); });
  await step('plate 2', async () => { await click('#sl-plates [data-plate="1"]'); });
  await step('plate 1 again', async () => { await click('#sl-plates [data-plate="0"]'); });
  await step('add filament', async () => { await act('addfil'); });
  await step('all settings', async () => { await act('all'); });
  await step('search all settings', async () => { await f.locator('#sl-all-search').fill('seam'); });
  await step('close all settings', async () => { await click('#sl-all-dialog [data-close]'); });
  await step('paint mode', async () => { await click('#mode-tabs [data-mode="paint"]'); await f.waitForFunction(() => window.__sl.mode === 'paint', null, { timeout: 5000 }); });
  await step('fill object with filament 2', async () => { await act('fillobj'); });
  await step('paint stroke', async () => { const pt = await screenOf(0); await mouse(pt, (x, y) => p.mouse.move(x, y)); await p.mouse.down(); for (let i = 1; i <= 6; i++) await mouse({ x: pt.x + i * 3, y: pt.y + i * 2 }, (x, y) => p.mouse.move(x, y)); await p.mouse.up(); });
  await step('paint tools: sphere, triangle, fill, height', async () => { for (const t of ['sphere', 'triangle', 'fill', 'height', 'circle']) { await click(`#sl-side [data-ptool="${t}"]`); } });
  await step('full spectrum apply', async () => { await act('fsapply'); });
  await step('supports channel', async () => { await click('#sl-side [data-pch="support"]'); });
  await step('support stroke', async () => { const pt = await screenOf(0); await mouse(pt, (x, y) => p.mouse.move(x, y)); await p.mouse.down(); for (let i = 1; i <= 4; i++) await mouse({ x: pt.x - i * 3, y: pt.y + i * 2 }, (x, y) => p.mouse.move(x, y)); await p.mouse.up(); });
  await step('seam channel', async () => { await click('#sl-side [data-pch="seam"]'); });
  await step('colour channel', async () => { await click('#sl-side [data-pch="color"]'); });
  await step('clear paint', async () => { await act('clearpaint'); });
  await step('undo clear', async () => { await act('undo'); });
  await step('prepare mode', async () => { await click('#mode-tabs [data-mode="prepare"]'); await f.waitForFunction(() => window.__sl.mode === 'prepare', null, { timeout: 5000 }); });
  await step('slice', async () => { await act('slice'); await f.waitForFunction(() => { const S = window.__sl; return S.mode === 'preview' && S.res[S.plate] && S.res[S.plate].g; }, null, { timeout: 60000 }); });
  await step('layer 5', async () => { await f.evaluate(() => { const r = document.querySelector('#sl-layer'); r.value = 5; r.dispatchEvent(new Event('input', { bubbles: true })); }); });
  await step('colour by feature', async () => { await click('#sl-side [data-pc="feature"], #sl-legend [data-pc="feature"]'); });
  await step('hide a feature', async () => { await click('#sl-legend [data-feat="0"], #sl-side [data-feat="0"]'); });
  await step('back to prepare', async () => { await click('#mode-tabs [data-mode="prepare"]'); await f.waitForFunction(() => window.__sl.mode === 'prepare', null, { timeout: 5000 }); });
  await step('slice all', async () => { await click('#mode-tabs [data-mode="preview"]'); await f.waitForFunction(() => window.__sl.mode === 'preview', null, { timeout: 8000 }); await sleep(600); await act('sliceall'); await f.waitForFunction(() => { const S = window.__sl; return !document.querySelector('#sl-busy') || document.querySelector('#sl-busy').classList.contains('hidden'); }, null, { timeout: 60000 }); await sleep(1500); });
  let saved3mf = '';
  await step('prepare again', async () => { await click('#mode-tabs [data-mode="prepare"]'); await f.waitForFunction(() => window.__sl.mode === 'prepare', null, { timeout: 8000 }); });
  await step('download 3mf', async () => { const [dl] = await Promise.all([p.waitForEvent('download', { timeout: 20000 }), act('save')]); saved3mf = require('path').join(require('path').dirname(outFile), 'scenario-saved.3mf'); await dl.saveAs(saved3mf); });
  await step('save as project', async () => { await click('#ph-project [data-ph="save-new"]'); const d = f.locator('dialog.dialog'); await d.locator('input[name="v"]').fill('Scenario project'); await d.locator('button[value="ok"]').click(); await f.waitForFunction(() => window.__sl.project && window.__sl.project.name && !!window.__sl.project.savedAt, null, { timeout: 30000 }); });
  await step('print from the preview', async () => { await click('#mode-tabs [data-mode="preview"]'); await f.waitForFunction(() => window.__sl.mode === 'preview', null, { timeout: 8000 }); await sleep(800); await act('print'); await sleep(500); if (await f.locator('#confirm-dialog[open]').count()) await f.locator('#cf-yes').click(); await sleep(1500); });
  await step('new project', async () => { await click('#mode-tabs [data-mode="prepare"]'); await sleep(400); await act('newproj'); await sleep(400); if (await f.locator('#confirm-dialog[open]').count()) await f.locator('#cf-yes').click(); await f.waitForFunction(() => window.__sl.objects.length === 0, null, { timeout: 10000 }); });
  await step('open the downloaded 3mf', async () => { if (!saved3mf) throw new Error('no 3mf'); await f.locator('#sl-file').setInputFiles(saved3mf); await f.waitForFunction(() => window.__sl.objects.length >= 3 && !window.__sl.loading, null, { timeout: 60000 }); await sleep(800); });
  await step('add obj', async () => { const n = await f.evaluate(() => window.__sl.objects.length); await f.locator('#sl-file').setInputFiles(fx + '/cube.obj'); await waitObjs(n + 1); });
  // what the server now has
  const projects = await p.evaluate(async () => (await (await fetch('/api/projects')).json()).map((x) => ({ name: x.name, kind: x.kind, plates: x.data && x.data.plates ? x.data.plates.length : null, slicer: !!x.slicer, splates: x.slicer ? x.slicer.plates.map((pl) => ({ n: pl.index, objs: (pl.objects || []).length, machine: pl.opts && pl.opts.machine, process: pl.opts && pl.opts.process, fil: pl.opts && (pl.opts.filaments || []).length })) : null })));
  const pid = await p.evaluate(async () => { const l = await (await fetch('/api/projects')).json(); return l.length ? l[0].id : ''; });
  const thumb = pid ? await p.evaluate(async (id) => { const r = await fetch(`/api/projects/${id}/plates/1/thumb`); const bl = await r.blob(); return { status: r.status, type: r.headers.get('content-type'), big: bl.size > 500 }; }, pid) : null;
  // open it from the Projects page (project mode: the slicer in its own frame)
  let pm = null;
  if (pid) {
    try {
      await p.evaluate((id) => { show('projects'); return openProject(id); }, pid);
      await sleep(1500);
      let pf; for (let i = 0; i < 100 && !pf; i++) { await sleep(100); pf = p.frames().find((x) => /\/slicer\//.test(x.url()) && x !== f && !x.isDetached()); }
      if (pf) {
        await pf.waitForFunction(() => window.__sl && window.__sl.ready && window.__sl.project && window.__sl.objects.length > 0 && !window.__sl.loading, null, { timeout: 30000 });
        await sleep(800);
        pm = await pf.evaluate(() => { const S = window.__sl; return { pm: S.pm, tab: S.tab, mode: S.mode, nPlates: S.nPlates, objects: S.objects.map((o) => [o.name, o.plate, o.states.length, Array.from(o.states).filter((x) => x).length]), filaments: S.filaments.length, overrides: S.overrides, process: S.process, side: document.querySelector('#sl-side').innerText.replace(/\s+/g, ' ').trim().slice(0, 600), dirty: null }; });
      } else log.push('project frame not found');
    } catch (e) { log.push('project mode failed: ' + String(e.message).split('\n')[0].slice(0, 200)); }
  }
  // the other pages of the main UI still come up
  const pages = {};
  for (const v of ['library', 'projects', 'printers', 'queue', 'settings', 'printer']) {
    try { await p.evaluate((x) => { if (x === 'projects' && typeof closeProject === 'function' && state.project) closeProject(); show(x); }, v); await sleep(900);
      pages[v] = await p.evaluate((x) => { const e = document.querySelector('#view-' + x); return { shown: !e.classList.contains('hidden'), text: e.innerText.replace(/\s+/g, ' ').trim().length > 20 }; }, v); } catch (e) { pages[v] = 'ERR ' + String(e.message).split('\n')[0].slice(0, 120); }
  }
  await p.screenshot({ path: outFile.replace(/\.json$/, '.png') });
  fs.writeFileSync(outFile, JSON.stringify({ steps, projects, thumb, pm, pages, errs, log }, null, 1));
  console.log(`port ${port}: ${steps.length} steps, ${errs.length} error(s), ${log.length} note(s)`);
  for (const e of errs) console.log('  ERR', e);
  for (const l of log) console.log('  note', l);
  await b.close();
})().catch((e) => { console.error('SCENARIO CRASHED', e); process.exit(1); });
