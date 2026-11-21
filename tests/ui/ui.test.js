// Checks the main PrintHarbor page in a real (headless) browser: every page comes up, the dialogs
// open, files are sent compressed and are not sent again on the next load, settings save, a plate
// goes through the queue to a simulated printer, and 3D James loads.
//
//   node tests/ui/ui.test.js <port> <fixture folder from mkfixture.sh>
//
// PrintHarbor must be running on that port with the fixture's config and one simulated printer
// (run.sh does all of that). Run it after slicer.scenario.js to also cover a saved project.
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');
const [port, fx] = process.argv.slice(2);
if (!port || !fx) { console.error('usage: node ui.test.js <port> <fixture folder>'); process.exit(2); }
const base = `http://127.0.0.1:${port}`;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let failed = 0;
const ok = (cond, what) => { if (cond) console.log('  ok   ' + what); else { failed++; console.log('  FAIL ' + what); } };

(async () => {
  const browser = await chromium.launch({ args: ['--use-gl=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] });
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const p = await ctx.newPage();
  const errs = []; const seen = [];
  // Not errors: the refused setting this test sends on purpose, and an icon 3D James asks for that was never part of PrintHarbor.
  const expected = [/^400 \/api\/settings$/, /^404 \/icons\//, /favicon/];
  // (failed requests are counted below by their status; the browser also logs each one, which is skipped here)
  p.on('console', (m) => { if (m.type() === 'error' && !/Failed to load resource/.test(m.text())) errs.push('console: ' + m.text().slice(0, 200)); });
  p.on('pageerror', (e) => errs.push('pageerror: ' + e.message.slice(0, 200)));
  p.on('dialog', (d) => d.accept().catch(() => {}));
  p.on('response', (r) => { const u = r.url().replace(base, ''); seen.push({ u, status: r.status(), enc: r.headers()['content-encoding'] || '', type: r.headers()['content-type'] || '', etag: r.headers()['etag'] || '' }); if (r.status() >= 400 && !expected.some((e) => e.test(r.status() + ' ' + u))) errs.push(r.status() + ' ' + u); });
  const loaded = () => p.waitForFunction(() => typeof state !== 'undefined' && state.info && state.info.version && state.printers.length > 0, null, { timeout: 20000 });

  console.log('loading the page');
  await p.goto(base + '/'); await loaded();
  const scripts = await p.$$eval('script[src]', (l) => l.map((s) => new URL(s.src).pathname));
  ok(scripts.length >= 14, `the page is made of ${scripts.length} script files`);
  const first = (name) => seen.find((x) => x.u === name);
  ok(scripts.every((s) => first(s) && first(s).status === 200 && /javascript/.test(first(s).type)), 'every script arrives as JavaScript');
  ok(['/app.js', '/library.js', '/style.css', '/'].every((n) => first(n) && first(n).enc === 'gzip'), 'the big files are sent compressed');
  ok(scripts.every((s) => first(s).etag), 'every file carries a fingerprint');
  ok(await p.evaluate(() => state.view === 'library' && !document.querySelector('#view-library').classList.contains('hidden')), 'it opens on the Library');
  seen.length = 0;
  await p.reload(); await loaded();
  const again = seen.filter((x) => scripts.includes(x.u) || x.u === '/style.css');
  ok(again.length >= scripts.length && again.every((x) => x.status === 304), `on the next load nothing is downloaded again (${again.filter((x) => x.status === 304).length} of ${again.length} files "not modified")`);
  // Start-up waits for every script: a server that answers instantly must not find half a page.
  ok(await p.evaluate(() => ['renderLibrary', 'renderProjects', 'renderPrinters', 'renderJobs', 'renderSettings', 'prepareSend', 'openSlicer', 'pvStopCamera'].every((f) => typeof window[f] === 'function')), 'every part of the page is there when it starts');

  console.log('the pages');
  for (const v of ['projects', 'printers', 'queue', 'settings', 'library']) {
    await p.evaluate((x) => show(x), v); await sleep(700);
    ok(await p.evaluate((x) => !document.querySelector('#view-' + x).classList.contains('hidden') && document.querySelector('#view-' + x).innerText.trim().length > 10, v), v + ' page');
  }
  await p.evaluate(() => show('printers')); await sleep(500);
  await p.click('#add-printer');
  ok(await p.evaluate(() => document.querySelector('#dlg').open && /printer/i.test(document.querySelector('#dlg').innerText)), 'the add-printer dialog opens');
  await p.evaluate(() => document.querySelector('#dlg').close());
  await p.click('#printer-grid [data-edit]');
  ok(await p.evaluate(() => document.querySelector('#dlg').open && !!document.querySelector('#dlg input')), "a printer's edit dialog opens with its details");
  await p.evaluate(() => document.querySelector('#dlg').close());
  await p.click('#printer-grid [data-open]'); await sleep(900);
  ok(await p.evaluate(() => state.view === 'printer' && document.querySelector('#view-printer').innerText.length > 50), "a printer's own dashboard opens");

  console.log('settings');
  await p.evaluate(() => show('settings')); await sleep(800);
  await p.click('#set-nav [data-settab="printing"]');
  const keep = p.locator('#view-settings input[data-k="job_keep_days"]');
  ok(await keep.count() === 1 && (await keep.inputValue()) === '7', 'sliced files are kept 7 days unless you change it');
  await keep.fill('45'); await p.click('#settings-save'); await sleep(1200);
  const cfg = JSON.parse(fs.readFileSync(path.join(fx, 'config.json'), 'utf8'));
  ok(cfg.job_keep_days === 45 && cfg.listen === `127.0.0.1:${port}`, 'a changed setting is saved to config.json, the rest left as it was');
  ok(!fs.existsSync(path.join(fx, 'config.json.tmp')), 'no half-written config is left behind');
  await p.evaluate(async () => { const s = await api('/api/settings'); s.job_keep_days = -3; try { await api('/api/settings', { json: s }); window.__saved = true; } catch (e) { window.__saved = e.message; } });
  ok(/days/.test(await p.evaluate(() => String(window.__saved))), 'a nonsense number of days is refused');

  console.log('a plate through the queue');
  // Upload the saved 3MF from the slicer scenario when there is one, else the cube.
  const saved = path.join(path.dirname(fx), 'scenario-saved.3mf');
  const upload = fs.existsSync(saved) ? saved : path.join(fx, 'cube20.stl');
  await p.evaluate(() => show('projects')); await sleep(500);
  await p.setInputFiles('#upload', upload);
  await p.waitForFunction(() => state.project && state.project.id, null, { timeout: 30000 });
  const proj = await p.evaluate(() => ({ id: state.project.id, kind: state.project.kind, name: state.project.name }));
  ok(!!proj.id, `opening ${path.basename(upload)} makes a project (${proj.kind})`);
  const job = await p.evaluate(async (id) => {
    const pr = state.printers[0];
    const sug = await api(`/api/projects/${id}/suggest?printer=${pr.id}`).catch((e) => ({ error: e.message }));
    if (sug.error) return sug;
    const j = await api('/api/jobs', { json: { project_id: id, plate: 1, printer_id: pr.id, process_preset: sug.process_preset, filament_presets: sug.filament_presets, send: true, start: false } });
    for (let i = 0; i < 80; i++) { await new Promise((r) => setTimeout(r, 500)); const l = await api('/api/jobs'); const x = l.find((y) => y.id === j.id); if (x && ['done', 'failed', 'canceled'].includes(x.state)) return x; }
    return { state: 'timeout' };
  }, proj.id);
  ok(job.state === 'done', 'a plate is sliced and sent to the simulated printer' + (job.state === 'done' ? '' : ` (${job.state}: ${job.error || ''})`));
  await p.evaluate(() => { if (typeof closeProject === 'function' && state.project) closeProject(); show('queue'); }); await sleep(3500);
  ok(await p.evaluate(() => /Uploaded/.test(document.querySelector('#view-queue').innerText) && !!document.querySelector('#job-list a[href*="/download"]')), 'the Queue page shows it as uploaded, with its download');
  await p.click('#job-list [data-preview]'); await sleep(1500);
  ok(await p.evaluate(() => !!document.querySelector('dialog[open]')), 'its G-code preview opens');
  await p.evaluate(() => document.querySelectorAll('dialog[open]').forEach((d) => d.close()));
  if (job.id) {
    const dl = await p.evaluate(async (id) => { const r = await fetch(`/api/jobs/${id}/download`); return { status: r.status, text: (await r.text()).slice(0, 40) }; }, job.id);
    ok(dl.status === 200 && /FakeOrca/.test(dl.text), 'its G-code can be downloaded');
  }

  console.log('your print defaults');
  await p.evaluate(() => show('settings')); await sleep(900);
  await p.click('#set-nav [data-settab="printing"]'); await sleep(600);
  ok(await p.locator('#pd-body [data-pd]').count() >= 10 && /None set/.test(await p.locator('#pd-count').innerText()), 'Settings → Printing lists the settings you can give a default, none set yet');
  await p.selectOption('#pd-body [data-pd="support_type"]', 'tree(auto)');
  await p.selectOption('#pd-body [data-pd="enable_support"]', '1');
  await p.fill('#pd-body [data-pd="wall_loops"]', '4');
  await p.click('#pd-save'); await sleep(1200);
  const cfg2 = JSON.parse(fs.readFileSync(path.join(fx, 'config.json'), 'utf8'));
  ok(cfg2.print_defaults && cfg2.print_defaults.support_type === 'tree(auto)' && cfg2.print_defaults.wall_loops === '4' && cfg2.job_keep_days === 45, 'they are saved to config.json with the other settings');
  ok(/3\s*set for all printers/.test(await p.locator('#pd-count').innerText()) && await p.locator('#pd-body [data-pd="support_type"]').inputValue() === 'tree(auto)', 'and are shown again');
  ok(await p.evaluate(async () => { const r = await api('/api/profiles/resolve?type=process&name=' + encodeURIComponent('0.20mm Standard @Acme')); return r.support_type === 'tree(auto)' && r.wall_loops === '4'; }), 'the stock profile now comes with your values');
  // One printer's own, on top.
  const prId = await p.evaluate(() => state.printers[0].id);
  await p.selectOption('#pd-scope', prId); await sleep(700);
  ok(/General default \(Tree \(auto\)\)/.test(await p.locator('#pd-body [data-pd="support_type"] option').first().innerText()) && await p.locator('#pd-process').count() === 1, 'picking a printer shows its own settings, with the general default named');
  await p.fill('#pd-body [data-pd="wall_loops"]', '6');
  await p.selectOption('#pd-process', '0.28mm Draft @Acme');
  await p.click('#pd-save'); await sleep(1200);
  ok(await p.evaluate(async (id) => { const e = encodeURIComponent, n = e('0.20mm Standard @Acme'); const own = await api(`/api/profiles/resolve?type=process&name=${n}&printer=${id}`), gen = await api(`/api/profiles/resolve?type=process&name=${n}`); const pr = (await api('/api/printers')).find((x) => x.id === id);
    return own.wall_loops === '6' && own.support_type === 'tree(auto)' && gen.wall_loops === '4' && pr.default_process === '0.28mm Draft @Acme'; }, prId), 'that printer gets 6 walls and its own starting profile; the others keep the general 4');
  await p.evaluate(async (id) => { await api('/api/print-defaults', { json: { printer: id, defaults: {} } }); }, prId);

  console.log('your own profiles');
  await p.click('#mp-new'); await sleep(900);
  await p.fill('#mp-name', 'My hot PLA');
  await p.selectOption('#mp-base', 'Generic PLA @Acme'); await sleep(700);
  ok(await p.locator('#mp-rows [data-mp="nozzle_temperature"]').getAttribute('placeholder') === '210', 'a new filament profile starts from a stock one and shows its values');
  await p.fill('#mp-rows [data-mp="nozzle_temperature"]', '225');
  await p.click('#mp-save'); await sleep(1500);
  ok(/My hot PLA/.test(await p.locator('#mp-list').innerText()) && /225/.test(await p.locator('#mp-list').innerText()), 'it is saved and listed with what it changes');
  ok(await p.evaluate(async () => { const r = await api('/api/profiles/resolve?type=filament&name=' + encodeURIComponent('My hot PLA')); return String(r.nozzle_temperature) === '225'; }), 'and slices with the new temperature');
  await p.click('#mp-list [data-mpdel]'); await sleep(1200);
  ok(!/My hot PLA/.test(await p.locator('#mp-list').innerText()), 'it can be removed again');

  console.log('backups');
  await p.click('#set-nav [data-settab="general"]'); await sleep(300);
  await p.click('#bk-now'); await sleep(2500);
  const bdir = path.join(fx, 'data', 'backups');
  const made = fs.existsSync(bdir) ? fs.readdirSync(bdir).filter((n) => /^\d{4}-\d\d-\d\d_\d{6}$/.test(n)) : [];
  ok(made.length >= 1 && fs.existsSync(path.join(bdir, made[made.length - 1], 'data', 'projects.json')) && fs.existsSync(path.join(bdir, made[made.length - 1], 'config.json')) && !fs.existsSync(path.join(bdir, made[made.length - 1], 'data', 'jobs')), '"Back up now" makes a dated folder with your projects and settings, without the sliced files');
  ok(/Last backup/.test(await p.locator('#bk-info').innerText()), 'and Settings says when the last one was');

  console.log('slice, look at the preview, then print');
  // A plain model, so the ordinary send screen is used.
  await p.evaluate(() => { if (typeof closeProject === 'function' && state.project) closeProject(); show('projects'); }); await sleep(500);
  await p.setInputFiles('#upload', path.join(fx, 'bar.stl'));
  await p.waitForFunction(() => state.project && /bar/i.test(state.project.name), null, { timeout: 30000 });
  await p.evaluate(() => choosePrinter(state.project.data.plates[0].index)); await sleep(800);
  await p.click('#dlg [data-pick]');
  await p.waitForSelector('#slice-only', { timeout: 15000 });
  ok(/Slice & preview/.test(await p.locator('#slice-only').innerText()) && await p.evaluate(() => document.querySelector('#slice-only').classList.contains('primary') && !document.querySelector('#go').classList.contains('primary')), 'the send screen offers "Slice & preview" first');
  ok(await p.evaluate(() => document.querySelector('#dlg [data-key="support_type"]').value === 'tree(auto)'), 'and shows your default support type');
  await p.click('#slice-only'); await sleep(150);
  ok(await p.evaluate(() => { const b = document.querySelector('#slice-wait'); return !!b && /Slicing|Waiting/.test(b.innerText) && !!b.querySelector('#sw-cancel') && !!b.querySelector('.sw-bar'); }), 'while it slices a progress box shows what is happening, with Cancel');
  await p.waitForSelector('#gc-print', { timeout: 40000 });
  ok(await p.evaluate(() => !document.querySelector('#slice-wait')), 'the progress box goes away when the preview opens');
  await p.waitForFunction(() => /layers/.test(document.querySelector('#gc-facts').innerText), null, { timeout: 20000 });
  const facts = await p.locator('#gc-facts').innerText();
  ok(/Supports: \d+% of the printing/.test(facts), 'when it is sliced the preview opens by itself and says how much is supports (' + facts.replace(/\s+/g, ' ') + ')');
  const pj = await p.evaluate(() => { const j = state.jobs[state.jobs.length - 1]; return { id: j.id, send: j.send, state: j.state }; });
  ok(pj.state === 'done' && pj.send === false, 'nothing has been sent to the printer yet');
  const gtxt = await p.evaluate(async (id) => (await (await fetch(`/api/jobs/${id}/download`)).text()), pj.id);
  ok(/support_type = tree\(auto\)/.test(gtxt) && /wall_loops = 4/.test(gtxt) && /;TYPE:Support/.test(gtxt), 'the slicer was given your defaults (tree supports, 4 walls)');
  await p.click('#gc-legend [data-gct]'); await sleep(200);
  ok(await p.evaluate(() => document.querySelector('#gc-legend [data-gct]').classList.contains('off')), 'a kind of line can be hidden in the preview');
  ok(await p.evaluate(() => { const c = document.querySelector('#gc-view canvas'); return !!c && c.width > 600 && c.height > 300; }), 'it is drawn in 3D, filling the dialog');
  await p.click('#gc-only'); await sleep(200);
  ok(await p.evaluate(() => [...document.querySelectorAll('#gc-legend [data-gct]')].filter((b) => !b.classList.contains('off')).map((b) => b.innerText).join('|').startsWith('Support') && /Show everything/.test(document.querySelector('#gc-only').innerText)), '"Only supports" leaves just the supports showing');
  await p.click('#gc-print'); await sleep(2500);
  const after = await p.evaluate(async (id) => (await api('/api/jobs')).find((j) => j.id === id), pj.id);
  ok(after.send === true && after.start === true && after.printing === true && after.in_queue === true, 'Print in the preview sends that same slice, and the Queue shows it printing');
  await p.evaluate(async () => { await api('/api/print-defaults', { json: { defaults: {} } }); });

  console.log('completed prints');
  await p.evaluate(() => show('queue')); await sleep(600);
  await p.click('#queue-history'); await sleep(1200);
  ok(await p.evaluate(() => !!document.querySelector('#hist-list')) && !(await p.locator('header [data-view]').allInnerTexts()).some((t) => /history|completed/i.test(t)), 'the Queue page has a "Completed prints" button (no extra tab)');
  await p.evaluate(async () => { const id = (await api('/api/runs?limit=1'))[0].id; await api(`/api/runs/${id}/review`, { json: { good: false } }).catch(() => {}); });
  await p.evaluate(() => document.querySelector('#dlg').close()); await p.click('#queue-history'); await sleep(1200);
  ok(/bar/i.test(await p.locator('#hist-list').innerText()) && await p.locator('#hist-list [data-again]').count() >= 1, 'it lists prints that have ended, each with "Print again"');
  await p.click('#hist-list [data-again]'); await sleep(1800);
  ok(await p.evaluate(() => state.view === 'projects' && !!state.project && document.querySelector('#dlg').open && /pick a printer/.test(document.querySelector('#dlg-body').innerText)), '"Print again" opens that project at picking a printer');
  await p.evaluate(() => document.querySelector('#dlg').close());

  console.log('3D James');
  seen.length = 0;
  await p.evaluate(() => show('design'));
  const jr = await p.waitForResponse((r) => r.url().endsWith('/3djames.html'), { timeout: 60000 }).catch(() => null);
  ok(!!jr && jr.status() === 200 && jr.headers()['content-encoding'] === 'gzip' && !!jr.headers()['etag'], '3D James is sent compressed, with a fingerprint');
  let jf; for (let i = 0; i < 300 && !jf; i++) { await sleep(200); jf = p.frames().find((f) => /3djames\.html/.test(f.url())); }
  if (jf) { await jf.waitForLoadState('domcontentloaded', { timeout: 120000 }).catch(() => {}); ok(await jf.evaluate(() => document.documentElement.outerHTML.includes('PrintHarbor bridge for 3D James')), "it has PrintHarbor's bridge (open and save in your library)"); } else ok(false, '3D James frame');
  const j2 = await p.evaluate(async (etag) => { const r = await fetch('/3djames.html', { headers: { 'If-None-Match': etag } }); return r.status; }, jr ? jr.headers()['etag'] : '');
  ok(j2 === 304, 'and is not downloaded again while it is unchanged');

  console.log('the app window notice');
  await p.evaluate(() => show('library'));
  const notice = (appBuild) => p.evaluate((b) => { const old = document.querySelector('#app-outdated'); if (old) old.remove(); const st = window.setTimeout; window.setTimeout = (fn) => { fn(); return 0; }; window.__phApp = b !== null; window.__phAppBuild = b === null ? undefined : b; appWindowCheck(); window.setTimeout = st; return !!document.querySelector('#app-outdated'); }, appBuild);
  ok(await p.evaluate(() => state.info.app_build > 0), 'the server says which app window build it came with');
  ok(await notice(null) === false, 'in a normal browser: no notice');
  ok(await notice(1) === true, 'in an app window older than the server: asks you to reopen it');
  ok(await notice(await p.evaluate(() => state.info.app_build)) === false, 'in an up-to-date app window: no notice');

  console.log('a page left open across an update');
  await p.goto(base + '/'); await loaded();
  ok(await p.evaluate(() => typeof state.info.build === 'string' && state.info.build.length >= 8), 'the server says which build it is');
  ok(await p.evaluate(async () => { let n = 0; await checkForUpdate(() => n++); return n === 0 && !document.querySelector('#ph-updated'); }), 'same build: nothing happens');
  await p.evaluate(() => { state.info = { ...state.info, build: 'an-older-build' }; });
  ok(await p.evaluate(async () => { let n = 0; await checkForUpdate(() => n++); return n === 1 && !document.querySelector('#ph-updated'); }), 'updated, nothing open: the page reloads by itself');
  await p.evaluate(() => { document.querySelector('#design-frame').setAttribute('src', 'about:blank'); });
  ok(await p.evaluate(async () => { let n = 0; await checkForUpdate(() => n++); const b = document.querySelector('#ph-updated'); if (n !== 0 || !b) return false; b.querySelector('button').click(); return n === 1 && /updated/.test(b.textContent); }), 'updated, 3D James open: it asks first, and reloads when you say so');
  ok(await p.evaluate(async () => { let n = 0; await checkForUpdate(() => n++); return n === 0 && document.querySelectorAll('#ph-updated').length === 1; }), 'and asks only once');

  ok(errs.length === 0, 'no errors on any page' + (errs.length ? ':\n        ' + errs.slice(0, 8).join('\n        ') : ''));
  await browser.close();
  console.log(failed ? `\n${failed} check(s) FAILED` : '\nall checks passed');
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error('TEST CRASHED', e); process.exit(2); });
