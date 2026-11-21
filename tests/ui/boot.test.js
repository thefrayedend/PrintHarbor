// Tests the script the app window injects into every page (cmd/printharbor-app/boot.js) in a real
// browser, against a pretend model site. Nothing here touches the internet or PrintHarbor.
//   node tests/ui/boot.test.js
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');

const ORIGIN = 'http://localhost:7654';
const BUILD = 99;
const boot = fs.readFileSync(path.join(__dirname, '..', '..', 'cmd', 'printharbor-app', 'boot.js'), 'utf8')
  .replaceAll('__PH_ORIGIN__', ORIGIN).replaceAll('__PH_APP_BUILD__', String(BUILD));
const SITE = `<!doctype html><html><head><title>Benchy by Ann - ModelSite</title>
  <meta name="author" content="Ann"><meta property="og:description" content="A little boat"><meta property="og:image" content="https://models.example/benchy.jpg">
  </head><body style="padding:140px 0 0 160px"><h1 id="h">Benchy</h1>
  <a id="stl" href="https://files.example/dl/benchy.stl?token=1" download="benchy.stl"><span id="stl-inner">Download STL</span></a>
  <a id="zip" href="/files/pack.ZIP">Download all</a>
  <a id="slicer" href="bambustudio://open?file=https%3A%2F%2Ffiles.example%2Fprofile.3mf&name=x">Open in Bambu Studio</a>
  <a id="noparam" href="orcaslicer://open">Open Orca</a>
  <a id="plain" href="https://models.example/other-page">Another page</a>
  <input id="box" value="hello world"><p id="text">Some words to select</p>
  <iframe id="inner" src="https://models.example/frame"></iframe></body></html>`;

let failed = 0;
const ok = (cond, what) => { if (cond) console.log('  ok   ' + what); else { failed++; console.log('  FAIL ' + what); } };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext();
  await ctx.addInitScript(boot);
  // phImport / phLog are provided by the app window itself; here they record what they were given.
  const imports = []; const logs = []; let importMode = 'ok';
  await ctx.exposeFunction('phImport', async (payload) => { imports.push(JSON.parse(payload)); if (importMode === 'fail') throw new Error('disk is full'); return 'Downloading into your PrintHarbor library…'; });
  await ctx.exposeFunction('phLog', (m) => { logs.push(m); return 'ok'; });
  let ownPage = 0;
  await ctx.exposeFunction('phOwnPage', () => { ownPage++; return 'ok'; });
  const visited = [];
  await ctx.route('**/*', (route) => {
    const u = route.request().url();
    if (route.request().isNavigationRequest()) visited.push(u);
    if (u.startsWith('https://models.example/frame')) return route.fulfill({ contentType: 'text/html', body: '<html><body>frame</body></html>' });
    if (u.startsWith('https://models.example/')) return route.fulfill({ contentType: 'text/html', body: SITE });
    if (u.startsWith(ORIGIN)) return route.fulfill({ contentType: 'text/html', body: '<html><head><title>PrintHarbor</title></head><body>PrintHarbor UI <input id="link" placeholder="Paste a model link"><p id="words">words to copy</p><div id="blank" style="height:60px">.</div></body></html>' });
    return route.fulfill({ status: 404, body: 'nope' });
  });
  const page = await ctx.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const open = async () => { await page.goto('https://models.example/model/1'); await page.waitForSelector('#__ph_nav'); };
  const toast = () => page.evaluate(() => { const t = document.getElementById('__ph_toast'); return t ? t.textContent : ''; });

  console.log('on a model site');
  await open();
  ok(await page.evaluate(() => window.__phApp === true), 'marks the page as inside the app window');
  ok(await page.evaluate((b) => window.__phAppBuild === b, BUILD), 'tells the page which build the window is');
  const btns = await page.$$eval('#__ph_nav button', (l) => l.map((b) => b.textContent));
  ok(btns.length === 2 && /Back/.test(btns[0]) && /Library/.test(btns[1]), 'adds the Back and Library buttons');
  ok(await page.frames().find((f) => f.url().includes('/frame')).evaluate(() => !document.getElementById('__ph_nav')), 'leaves pages inside frames alone');
  await page.evaluate(() => { document.body.innerHTML = '<p>the site redrew itself</p>'; });
  await page.waitForSelector('#__ph_nav', { timeout: 3000 }).then(() => ok(true, 'puts the buttons back when the site redraws the page'), () => ok(false, 'puts the buttons back when the site redraws the page'));
  await page.evaluate(() => history.pushState({}, '', '/model/2'));
  await sleep(200);
  ok(await page.$$eval('#__ph_nav', (l) => l.length === 1), 'keeps exactly one set of buttons after the site changes page by itself');
  ok(logs.some((l) => l.startsWith('boot models.example')) && logs.some((l) => l.startsWith('nav added on')), 'writes to the app log');
  ok(ownPage === 0, 'does not report a model site as PrintHarbor\'s own page');

  console.log('downloads');
  await open();
  visited.length = 0;
  await page.click('#stl-inner');
  await sleep(300);
  let got = imports[imports.length - 1] || {};
  ok(imports.length === 1 && got.url === 'https://files.example/dl/benchy.stl?token=1' && got.name === 'benchy.stl', 'a click on a model file goes to PrintHarbor, not to Downloads');
  ok(got.page && got.page.url === 'https://models.example/model/1' && got.page.title === 'Benchy by Ann - ModelSite' && got.page.designer === 'Ann' && got.page.description === 'A little boat' && got.page.images[0] === 'https://models.example/benchy.jpg', 'sends the model page details along');
  ok(!visited.some((u) => u.includes('files.example')), 'the browser does not also download the file');
  ok(/Downloading into your PrintHarbor library/.test(await toast()), 'says that the import started');
  await page.waitForURL(ORIGIN + '/?app=1#library', { timeout: 5000 }).then(() => ok(true, 'then returns to the Library'), () => ok(false, 'then returns to the Library'));
  ok(await page.evaluate((b) => window.__phApp === true && window.__phAppBuild === b && !document.getElementById('__ph_nav'), BUILD), "on PrintHarbor's own page: marks it, adds no buttons");
  ok(ownPage === 1, "and tells the window its own page has loaded (the safe moment to reopen after an update)");
  // The app window has no right-click menu of its own: PrintHarbor's own pages need this one to paste with the mouse.
  const menu = () => page.evaluate(() => [...document.querySelectorAll('body > div')].filter((d) => d.style.position === 'fixed' && /Paste|Copy/.test(d.textContent)).map((d) => [...d.children].map((c) => c.textContent).join('|'))[0] || '');
  await page.click('#link', { button: 'right' });
  ok(/^Paste\|Select all$/.test(await menu()), "on PrintHarbor's own page: right-click in a box offers Paste and Select all");
  await page.mouse.click(5, 5); await page.click('#blank', { button: 'right' });
  ok((await menu()) === '', 'right-click on empty space shows no menu (the 3D views keep right-drag)');
  await page.evaluate(() => { const r = document.createRange(); r.selectNodeContents(document.getElementById('words')); const s = getSelection(); s.removeAllRanges(); s.addRange(r); });
  await page.click('#words', { button: 'right' });
  ok(/^Copy$/.test(await menu()), 'right-click on selected words offers Copy');
  await page.mouse.click(5, 5);

  await open();
  await page.click('#zip');
  await sleep(300);
  got = imports[imports.length - 1];
  ok(imports.length === 2 && got.url === 'https://models.example/files/pack.ZIP' && got.name === '', 'file type is recognised whatever the capitals; a relative link is sent as a full address');
  await page.waitForURL(ORIGIN + '/?app=1#library', { timeout: 5000 }).catch(() => {});

  await open();
  await page.click('#slicer');
  await sleep(300);
  got = imports[imports.length - 1];
  ok(imports.length === 3 && got.url === 'https://files.example/profile.3mf', '"Open in Bambu Studio" hands PrintHarbor the file inside the link');
  await page.waitForURL(ORIGIN + '/?app=1#library', { timeout: 5000 }).catch(() => {});

  await open();
  const r = await page.evaluate(() => [window.open('orcaslicer://open?file=https://files.example/via-open.3mf'), window.open('https://files.example/direct.step')]);
  await sleep(300);
  ok(imports.length === 5 && imports[3].url === 'https://files.example/via-open.3mf' && imports[4].url === 'https://files.example/direct.step' && r[0] === null && r[1] === null, 'sites that start the download from a script are caught too');
  await page.waitForURL(ORIGIN + '/?app=1#library', { timeout: 5000 }).catch(() => {});

  console.log('everything else is left alone');
  await open();
  const n = imports.length;
  await page.click('#noparam');
  await sleep(200);
  ok(imports.length === n, 'a slicer link without a file in it is not sent');
  const [popup] = await Promise.all([ctx.waitForEvent('page', { timeout: 5000 }).catch(() => null), page.evaluate(() => { window.open('https://models.example/help'); })]);
  ok(!!popup && imports.length === n, 'an ordinary window.open still opens');
  if (popup) await popup.close();
  await page.click('#plain');
  await page.waitForURL('https://models.example/other-page', { timeout: 5000 }).then(() => ok(imports.length === n, 'an ordinary link still works'), () => ok(false, 'an ordinary link still works'));

  console.log('when the import cannot be done');
  await open();
  importMode = 'fail';
  await page.click('#stl');
  await sleep(1600);
  ok(/import failed: .*disk is full/.test(await toast()), 'shows why');
  ok(page.url() === 'https://models.example/model/1', 'and stays on the model page');
  importMode = 'ok';
  await page.evaluate(() => { window.phImport = undefined; });
  await page.click('#stl');
  await sleep(200);
  ok(/isn't ready/.test(await toast()), 'says so when the window is not ready, instead of doing nothing');

  console.log('the buttons');
  await open();
  await page.click('#__ph_nav button >> nth=1');
  await page.waitForURL(ORIGIN + '/?app=1#library', { timeout: 5000 }).then(() => ok(true, 'Library goes to the PrintHarbor library'), () => ok(false, 'Library goes to the PrintHarbor library'));
  const fresh = await ctx.newPage();
  await fresh.goto('https://models.example/model/9');
  await fresh.waitForSelector('#__ph_nav');
  await fresh.evaluate(() => { history.back = () => {}; }); // as on the first page of a window: there is nowhere to go back to
  await fresh.click('#__ph_nav button >> nth=0');
  await fresh.waitForURL(ORIGIN + '/?app=1#library', { timeout: 5000 }).then(() => ok(true, 'Back with nothing to go back to returns to PrintHarbor'), () => ok(false, 'Back with nothing to go back to returns to PrintHarbor'));
  await fresh.close();
  await open();
  await page.click('#plain');
  await page.waitForURL('https://models.example/other-page');
  await page.waitForSelector('#__ph_nav');
  await page.click('#__ph_nav button >> nth=0');
  await page.waitForURL('https://models.example/model/1', { timeout: 5000 }).then(() => ok(true, 'Back goes back one page when there is one'), () => ok(false, 'Back goes back one page when there is one'));

  console.log('the right-click menu');
  await open();
  await page.click('#box', { button: 'right' });
  let items = await page.$$eval('body > div:last-child > div', (l) => l.map((x) => x.textContent));
  ok(items.includes('Paste') && items.includes('Select all') && !items.includes('Cut'), 'in a text box: Paste and Select all');
  await page.mouse.click(5, 300);
  ok(await page.evaluate(() => ![...document.querySelectorAll('body > div')].some((d) => /Paste/.test(d.textContent) && d.id !== '__ph_nav')), 'closes when you click elsewhere');
  await page.evaluate(() => { const r = document.createRange(); r.selectNodeContents(document.getElementById('text')); const s = getSelection(); s.removeAllRanges(); s.addRange(r); });
  await page.click('#text', { button: 'right' });
  items = await page.$$eval('body > div:last-child > div', (l) => l.map((x) => x.textContent));
  ok(items.length === 1 && items[0] === 'Copy', 'on selected words: Copy');
  await page.mouse.click(5, 300);
  await page.evaluate(() => getSelection().removeAllRanges());
  const before = await page.$$eval('body > div', (l) => l.length);
  await page.click('#h', { button: 'right' });
  ok(await page.$$eval('body > div', (l) => l.length) === before, "nothing selected, not a text box: the site's own menu is left alone");

  ok(errors.length === 0, 'no script errors on any page' + (errors.length ? ': ' + errors.join(' | ') : ''));
  await browser.close();
  console.log(failed ? `\n${failed} check(s) FAILED` : '\nall checks passed');
  process.exit(failed ? 1 : 0);
})().catch((e) => { console.error('TEST CRASHED', e); process.exit(2); });
