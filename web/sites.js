// Online model sites in the Library: switch between your library (default)
// and Printables / Thingiverse / MakerWorld / Cults3D. They never mix: each
// source is its own view. Search can also look at the online sites, shown in
// separate sections under your own results. "Import" downloads a model into
// <models folder>\<designer>\<model>\ and adds it to your library.
"use strict";

const sx = { sites: [], modelDir: "", src: "mine", q: "", sort: "popular", items: [], page: 0, cursor: "", more: false, busy: false, err: "",
  model: null, imports: [], seenDone: new Set(), polling: false, alsoSeq: 0, ext: "", mwURL: "" };
// Site pages to open in PrintHarbor's browser.
const SITE_SEARCH = {
  printables: (q) => "https://www.printables.com/search/models?q=" + enc(q),
  thingiverse: (q) => "https://www.thingiverse.com/search?type=things&q=" + enc(q),
  makerworld: (q) => "https://makerworld.com/en/search/models?keyword=" + enc(q),
  cults: (q) => "https://cults3d.com/en/search?q=" + enc(q),
};
const SITE_SORTS = [["popular", "Popular"], ["trending", "Trending"], ["newest", "Newest"]];
const siteImg = (u, size = "card") => (u ? "/api/sites/image?s=" + size + "&u=" + enc(u) : "");
const siteName = (id) => (sx.sites.find((s) => s.id === id) || {}).name || id;
const fmtCount = (n) => (n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1).replace(/\.0$/, "") + "k" : String(n || 0));
const lsGet = (k) => { try { return localStorage.getItem(k); } catch { return null; } };
const lsSet = (k, v) => { try { localStorage.setItem(k, v); } catch { /* private mode */ } };

async function sitesInit() {
  try {
    const d = await api("/api/sites");
    sx.sites = d.sites; sx.modelDir = d.model_dir; sx.imports = d.imports || [];
    sx.imports.forEach((j) => j.state !== "running" && sx.seenDone.add(j.id));
  } catch { sx.sites = []; }
  const also = $("#lib-also");
  also.checked = lsGet("ph-lib-also") === "1";
  also.onchange = () => { lsSet("ph-lib-also", also.checked ? "1" : "0"); sitesAlso(lib.q); };
  drawSrcBar();
  if (sx.imports.some((j) => j.state === "running")) pollImports();
}

function drawSrcBar() {
  const bar = $("#lib-src");
  if (!bar) return;
  bar.innerHTML = `<div class="seg-src" role="tablist">
      <button data-src="mine" class="${sx.src === "mine" ? "active" : ""}">My library</button>
      ${sx.sites.map((s) => `<button data-src="${s.id}" class="${sx.src === s.id ? "active" : ""}">${esc(s.name)}</button>`).join("")}
    </div>
    <div class="src-link"><input id="sx-link" type="url" placeholder="Paste a model link from any of these sites…"><button id="sx-link-go">Open</button>
      <button id="sx-ext" title="PrintHarbor's browser extension"><span class="dot"></span>Extension</button></div>
    <div id="sx-imports"></div>`;
  bar.querySelector(".seg-src").onclick = (e) => { const b = e.target.closest("[data-src]"); if (b) setSource(b.dataset.src); };
  const go = async () => {
    const u = $("#sx-link").value.trim();
    if (!u) return;
    try {
      const r = await api("/api/sites/link?url=" + enc(u));
      $("#sx-link").value = "";
      const site = sx.sites.find((x) => x.id === r.site);
      if (site && (!site.ready || site.id === "makerworld")) return r.site === "makerworld" ? showMakerWorld(u) : browseOpen(u);
      await setSource(r.site, true); openSiteModel(r.site, r.id);
    } catch (e) {
      if (/^https?:\/\//.test(u)) browseOpen(u); else toast(e.message, true);
    }
  };
  $("#sx-link-go").onclick = go;
  $("#sx-link").onkeydown = (e) => { if (e.key === "Enter") go(); };
  $("#sx-ext").onclick = () => extDialog();
  drawImports();
  extStatus();
}

async function setSource(src, keepDetail) {
  // In the PrintHarbor app window: a non-API site just shows itself in the window (it IS a browser).
  if (window.__phApp && src !== "mine") {
    const site = sx.sites.find((x) => x.id === src);
    if (site && !site.ready) { appGo(sx.q && SITE_SEARCH[src] ? SITE_SEARCH[src](sx.q) : site.url); return; }
  }
  if (src === sx.src && !keepDetail) { if (src !== "mine") closeSiteModel(); return; }
  sx.src = src;
  document.querySelectorAll("#lib-src [data-src]").forEach((b) => b.classList.toggle("active", b.dataset.src === src));
  const mine = src === "mine";
  $("#lib-local").classList.toggle("hidden", !mine);
  $("#lib-online").classList.toggle("hidden", mine);
  if (mine) { loadLibrary(); return; }
  closeItem();
  sx.model = null;
  drawSiteShell();
  if (!keepDetail && src !== "makerworld") await siteLoad(true);
}

function drawSiteShell() {
  const s = sx.sites.find((x) => x.id === sx.src);
  if (s.id === "makerworld") return drawMakerWorld();
  const el = $("#lib-online");
  el.innerHTML = `
    <div id="sx-browse">
      <div class="lib-bar">
        <input id="sx-q" type="search" placeholder="Search ${esc(s.name)}…" value="${esc(sx.q)}">
        <div class="seg-sort">${SITE_SORTS.map(([k, l]) => `<button data-sort="${k}" class="${sx.sort === k ? "active" : ""}">${l}</button>`).join("")}</div>
        <button data-browse="${esc(s.url)}">Open ${esc(s.name)} in a new tab</button>
      </div>
      ${!s.ready ? `<div class="card sx-browse-card">
          <h3>Browse ${esc(s.name)} in a new tab</h3>
          <p>Opens ${esc(s.name)} in a new tab of this browser, signed in as you normally are. Click the site's Download button like always: ${sx.ext ? `PrintHarbor's extension files every model you download in your library as <code>${esc(modelPath("designer", "model"))}</code>, with its pictures and details.` : `with <a href="#" data-ext-help>PrintHarbor's extension</a> installed, every model you download is filed in your library as <code>${esc(modelPath("designer", "model"))}</code> automatically.`}</p>
          <div class="row gap wrap"><button class="primary" data-browse="${esc(s.url)}">Open ${esc(s.name)}</button>${sx.q ? `<button data-browse="${esc(SITE_SEARCH[s.id](sx.q))}">Search “${esc(sx.q)}” there</button>` : ""}</div>
          <p class="small muted">Optional: add a ${esc(s.name)} key in <a href="#" data-goto-settings>Settings</a> to also list ${esc(s.name)} models right here.</p>
        </div>` : `<div class="sx-hint small muted"><a href="#" data-browse="${esc(s.url)}">Browse ${esc(s.name)} in a new tab</a>${sx.ext ? ": anything you download there goes straight into your library." : ""}</div>`}
      <div id="sx-status" class="small muted"></div>
      <div id="sx-grid" class="lib-grid"></div>
      <div class="row" style="justify-content:center;margin:14px 0"><button id="sx-more" class="hidden">Load more</button></div>
    </div>
    <div id="sx-detail" class="hidden"></div>`;
  let t;
  $("#sx-q").oninput = (e) => { clearTimeout(t); t = setTimeout(() => { sx.q = e.target.value.trim(); if (s.ready) siteLoad(true); }, 400); };
  $("#sx-q").onkeydown = (e) => { if (e.key === "Enter" && !s.ready && e.target.value.trim()) browseOpen(SITE_SEARCH[s.id](e.target.value.trim())); };
  if (!s.ready) { $("#sx-q").placeholder = `Search ${s.name} (opens in a new tab)…`; el.querySelector(".seg-sort").classList.add("hidden"); }
  el.querySelector(".seg-sort").onclick = (e) => {
    const b = e.target.closest("[data-sort]");
    if (!b) return;
    sx.sort = b.dataset.sort;
    el.querySelectorAll(".seg-sort button").forEach((x) => x.classList.toggle("active", x === b));
    siteLoad(true);
  };
  $("#sx-more").onclick = () => siteLoad(false);
  $("#sx-grid").onclick = (e) => { const c = e.target.closest("[data-site-model]"); if (c) openSiteModel(c.dataset.site, c.dataset.siteModel); };
  el.querySelectorAll("[data-goto-settings]").forEach((g) => (g.onclick = (e) => { e.preventDefault(); gotoSiteSettings(); }));
}

function modelPath(...parts) {
  const sep = (sx.modelDir || "").includes("/") && !(sx.modelDir || "").includes("\\") ? "/" : "\\";
  return [sx.modelDir, ...parts].join(sep);
}

// Site pages open in a new tab of your own browser; the extension files
// what you download there.
// In the app, show the site in the window itself; in a browser, open a new tab.
function appGo(u) { location.assign(u); }
function browseOpen(u) {
  if (window.__phApp) { appGo(u); return; }
  window.open(u, "_blank", "noopener");
  if (!sx.ext) toast("Tip: install PrintHarbor's extension (Library → Extension) and your downloads from there go straight into your library.");
  pollImports();
}

function extStatus() {
  sx.ext = document.documentElement.dataset.printharborExt || "";
  const b = $("#sx-ext");
  if (b) {
    b.classList.toggle("on", !!sx.ext);
    b.title = sx.ext ? "PrintHarbor's extension is installed: model downloads go into your library" : "Install PrintHarbor's extension so model downloads go into your library";
  }
  if (sx.ext) pollImports();
}
setTimeout(extStatus, 500);

// Which browser this is, for the install steps.
function whichBrowser() {
  const ua = navigator.userAgent;
  if (navigator.brave) return "brave";
  if (/Firefox\//.test(ua)) return "firefox";
  if (/Edg\//.test(ua)) return "edge";
  if (/OPR\//.test(ua)) return "opera";
  if (/Vivaldi/.test(ua)) return "vivaldi";
  return "chrome";
}

function extSteps(folder) {
  const copy = (t) => `<button class="small" data-copy="${esc(t)}">Copy</button>`;
  const b = whichBrowser();
  if (b === "firefox") {
    return `<ol class="ext-steps">
      <li>Open <code>about:debugging#/runtime/this-firefox</code> in a new tab ${copy("about:debugging#/runtime/this-firefox")}</li>
      <li>Click <b>Load Temporary Add-on…</b> and pick <code>manifest.json</code> in this folder: <code>${esc(folder)}</code> ${copy(folder)}</li>
      <li>Come back here and reload this page.</li>
    </ol>
    <p class="small muted">Firefox only keeps add-ons that aren't from Mozilla's store until you close Firefox, so this step has to be repeated after each Firefox restart (Firefox Developer Edition can keep it).</p>`;
  }
  const page = { brave: "brave://extensions", edge: "edge://extensions", opera: "opera://extensions", vivaldi: "vivaldi://extensions", chrome: "chrome://extensions" }[b];
  const toggle = b === "edge" ? "<b>Developer mode</b> (left side, near the bottom)" : "<b>Developer mode</b> (top right)";
  return `<ol class="ext-steps">
      <li>Open <code>${page}</code> in a new tab ${copy(page)}</li>
      <li>Turn on ${toggle}.</li>
      <li>Click <b>Load unpacked</b> and choose this folder: <code>${esc(folder)}</code> ${copy(folder)}</li>
      <li>Come back here and reload this page.</li>
    </ol>`;
}

async function extDialog() {
  let info = {};
  try { info = await api("/api/capture"); } catch { /* restarting */ }
  const body = $("#dlg-body");
  body.innerHTML = `<h2>PrintHarbor's browser extension</h2>
    ${sx.ext ? `<div class="notice ok">Installed (version ${esc(sx.ext)}). Models you download from Printables, Thingiverse, MakerWorld and Cults3D go into <code>${esc(modelPath("designer", "model"))}</code>.</div>`
      : `<p>Install it once in this browser. From then on every model you download from Printables, Thingiverse, MakerWorld or Cults3D, in any tab, is moved into <code>${esc(modelPath("designer", "model"))}</code> with its pictures and details, and shows up in your library. It also lets the MakerWorld tab show MakerWorld right inside PrintHarbor.</p>`}
    ${extSteps(info.folder || "")}
    <p class="small muted">It only looks at those four sites. Downloads from any other site are ignored and never reported. When PrintHarbor updates, click the extension's reload button on that page.</p>
    <div class="dlg-actions"><button value="cancel">Close</button></div>`;
  body.querySelectorAll("[data-copy]").forEach((b) => (b.onclick = async (e) => {
    e.preventDefault();
    try { await navigator.clipboard.writeText(b.dataset.copy); toast("Copied."); } catch { toast(b.dataset.copy); }
  }));
  openDialog();
}

// MakerWorld inside PrintHarbor (the extension lets MakerWorld show here;
// its "Download 3MF" lands in your library, as in FlashBridge).
function drawMakerWorld(start) {
  const el = $("#lib-online");
  const home = "https://makerworld.com/en";
  if (!sx.ext) {
    el.innerHTML = `<div class="card sx-browse-card"><h3>MakerWorld inside PrintHarbor</h3>
      <p>Install PrintHarbor's browser extension once and MakerWorld shows right here. Click <b>Download 3MF</b> on a model (or the big “Open in Bambu Studio” button) and the 3MF goes straight into your library as <code>${esc(modelPath("designer", "model"))}</code>.</p>
      <div class="row gap wrap"><button class="primary" data-ext-help>Install the extension</button><button data-browse="${home}">Open MakerWorld in a new tab</button></div></div>`;
    return;
  }
  el.innerHTML = `<div class="mw-bar">
      <button data-mw-home title="MakerWorld home">Home</button>
      <input id="mw-q" type="search" placeholder="Search MakerWorld…" value="${esc(sx.q)}">
      <span class="small muted mw-tip">Download 3MF goes straight into your library</span>
      <button data-mw-tab>Open in a new tab</button>
    </div>
    <iframe id="mw-frame" class="mw-frame" title="MakerWorld" allow="clipboard-write; fullscreen" referrerpolicy="strict-origin-when-cross-origin"></iframe>`;
  const frame = $("#mw-frame");
  const go = (u) => { frame.src = u; sx.mwURL = u; };
  window.postMessage({ phx: "embed" }, "*");
  const ready = (e) => {
    if (e.source !== window || !e.data || e.data.phx !== "embed-ok") return;
    window.removeEventListener("message", ready);
    if (!e.data.ok) toast("The extension couldn't open MakerWorld here: " + (e.data.error || "") , true);
    go(start || sx.mwURL || home);
  };
  window.addEventListener("message", ready);
  el.querySelector("[data-mw-home]").onclick = () => go(home);
  el.querySelector("[data-mw-tab]").onclick = () => browseOpen(sx.mwURL || home);
  $("#mw-q").onkeydown = (e) => { if (e.key === "Enter" && e.target.value.trim()) { sx.q = e.target.value.trim(); go(SITE_SEARCH.makerworld(sx.q)); } };
}
window.addEventListener("message", (e) => {
  if (e.data && e.data.phx === "frame-url" && /(^|\.)makerworld\.com$/.test(new URL(e.origin).hostname)) sx.mwURL = e.data.url;
});

function showMakerWorld(u) {
  if (window.__phApp) { appGo(u || "https://makerworld.com/en"); return; }
  sx.mwURL = u;
  if (sx.src === "makerworld") drawMakerWorld(u); else setSource("makerworld");
}

document.addEventListener("click", (e) => {
  const h = e.target.closest("[data-ext-help]");
  if (h) { e.preventDefault(); extDialog(); return; }
  const b = e.target.closest("[data-browse]");
  if (!b) return;
  e.preventDefault();
  if (/makerworld\.com/.test(b.dataset.browse) && sx.ext) showMakerWorld(b.dataset.browse); else browseOpen(b.dataset.browse);
});

function gotoSiteSettings() {
  show("settings");
  setTimeout(() => { const c = $("#sites-settings"); if (c) c.scrollIntoView({ behavior: "smooth", block: "start" }); }, 300);
}

function siteCard(c) {
  return `<div class="lib-card card sx-card" data-site="${esc(c.site)}" data-site-model="${esc(c.id)}">
    <div class="lib-cover">${c.thumb ? `<img loading="lazy" src="${siteImg(c.thumb)}" alt="" onerror="this.replaceWith(Object.assign(document.createElement('div'),{className:'noimg',textContent:'No picture'}))">` : `<div class="noimg">No picture</div>`}</div>
    ${c.in_library ? `<span class="sx-badge in">In your library</span>` : !c.free ? `<span class="sx-badge paid">Paid</span>` : ""}
    <div class="lib-card-body"><div class="lib-title" title="${esc(c.title)}">${esc(c.title)}</div>
      <div class="muted small sx-meta"><span class="sx-author">${esc(c.author || "")}</span><span>♥ ${fmtCount(c.likes)} · ⇩ ${fmtCount(c.downloads)}</span></div></div>
  </div>`;
}

async function siteLoad(reset) {
  const s = sx.sites.find((x) => x.id === sx.src);
  if (!s || sx.busy) return;
  if (reset) { sx.items = []; sx.page = 0; sx.cursor = ""; sx.more = false; $("#sx-grid").innerHTML = ""; }
  if (!s.ready) { $("#sx-status").textContent = ""; return; }
  sx.busy = true;
  const src = sx.src;
  $("#sx-status").textContent = "Loading…";
  $("#sx-more").classList.add("hidden");
  try {
    const qs = new URLSearchParams({ q: sx.q, sort: sx.sort, page: String(sx.page), cursor: sx.cursor });
    const r = await api(`/api/sites/${src}/search?` + qs);
    if (src !== sx.src) return;
    sx.items.push(...r.items); sx.page++; sx.cursor = r.cursor || ""; sx.more = r.more;
    $("#sx-grid").insertAdjacentHTML("beforeend", r.items.map(siteCard).join(""));
    $("#sx-status").textContent = sx.items.length ? `${sx.items.length} model(s) from ${s.name}${sx.q ? ` for “${sx.q}”` : ""}` : "Nothing found.";
    $("#sx-more").classList.toggle("hidden", !sx.more);
  } catch (e) {
    if (src === sx.src) $("#sx-status").innerHTML = `<span class="err">${esc(e.message)}</span>`;
  } finally { sx.busy = false; }
}

function closeSiteModel() {
  sx.model = null;
  const d = $("#sx-detail"), b = $("#sx-browse");
  if (d) d.classList.add("hidden");
  if (b) b.classList.remove("hidden");
}

async function openSiteModel(site, id) {
  if (sx.src !== site) await setSource(site, true);
  $("#sx-browse").classList.add("hidden");
  const el = $("#sx-detail");
  el.classList.remove("hidden");
  el.innerHTML = `<button class="link" data-sx-back>← Back to ${esc(siteName(site))}</button><p class="muted">Loading…</p>`;
  el.querySelector("[data-sx-back]").onclick = () => { closeSiteModel(); if (!sx.items.length) siteLoad(true); };
  let d;
  try { d = await api(`/api/sites/${site}/models/${enc(id)}`); }
  catch (e) { el.insertAdjacentHTML("beforeend", `<div class="card err">${esc(e.message)}</div>`); el.querySelector("p.muted").remove(); return; }
  sx.model = d;
  drawSiteModel();
}

function drawSiteModel() {
  const d = sx.model, m = d.model, el = $("#sx-detail");
  const files = m.files || [];
  const imgs = m.images.length ? m.images : (m.thumb ? [m.thumb] : []);
  const job = sx.imports.find((j) => j.site === m.site && j.model_id === m.id && j.state === "running");
  el.innerHTML = `
    <button class="link" data-sx-back>← Back to ${esc(siteName(m.site))}</button>
    <div class="lib-detail-grid">
      <div>
        <div class="card lib-stage">${imgs.length ? `<img id="sx-stage" src="${siteImg(imgs[0], "big")}" alt="">` : `<div class="noimg">No picture</div>`}</div>
        ${imgs.length > 1 ? `<div class="lib-thumbs">${imgs.slice(0, 24).map((u, i) => `<img src="${siteImg(u)}" data-big="${siteImg(u, "big")}" data-sx-img="${i}" loading="lazy" alt="">`).join("")}</div>` : ""}
        ${m.description ? `<div class="card sx-desc"><h3>About this model</h3><div class="sx-desc-text">${esc(m.description).replace(/\n/g, "<br>")}</div></div>` : ""}
      </div>
      <div class="sx-side">
        <div class="card">
          <h2 style="margin:0 0 4px">${esc(m.title)}</h2>
          <div class="muted">by <strong>${esc(m.author || "unknown")}</strong> on ${esc(siteName(m.site))}</div>
          <div class="small muted" style="margin-top:6px">♥ ${fmtCount(m.likes)} · ⇩ ${fmtCount(m.downloads)}${m.license ? ` · License: ${esc(m.license)}` : ""}${m.category ? ` · ${esc(m.category)}` : ""}</div>
          ${m.tags.length ? `<div class="lib-tags" style="margin-top:8px">${m.tags.slice(0, 20).map((t) => `<span class="tag">#${esc(t)}</span>`).join("")}</div>` : ""}
          <div class="row gap wrap" style="margin-top:12px">
            <button data-browse="${esc(m.url)}">Open on ${esc(siteName(m.site))}</button>
            ${d.in_library ? `<button data-sx-open-lib="${esc(d.in_library)}">Show in my library</button>` : ""}
          </div>
          ${d.in_library ? `<div class="notice ok" style="margin-top:10px">Already in your library.</div>` : ""}
        </div>
        <div class="card">
          <h3>Import to my library</h3>
          <p class="small muted">Saved to <code class="sx-path">${esc(d.folder)}</code> with its pictures and details. Change the models folder in Settings.</p>
          ${d.can_download ? `
            ${files.length ? `<div class="lib-files sx-files">
              <label class="sx-all"><input type="checkbox" id="sx-all" checked> <strong>All model files</strong></label>
              ${files.map((f) => `<label class="lib-file"><span><input type="checkbox" data-sx-file="${esc(f.id)}" ${f.kind !== "gcode" ? "checked" : ""}> ${esc(f.name)}</span><span class="muted small">${f.kind === "gcode" ? "G-code" : ""}</span><span class="muted small">${f.size ? fmtSize(f.size) : ""}</span></label>`).join("")}
            </div>` : `<p class="muted">This model has no files to download.</p>`}
            <div id="sx-progress"></div>
            <button class="primary" id="sx-import" ${!files.length || job ? "disabled" : ""}>${job ? "Importing…" : d.in_library ? "Import again" : "Import to my library"}</button>`
          : `<p>Open the model and click ${esc(siteName(m.site))}'s Download button there.${sx.ext ? " The files go into this folder and your library by themselves." : ` With <a href="#" data-ext-help>PrintHarbor's extension</a> installed, the files go into this folder and your library by themselves.`}</p>
            <button class="primary" data-browse="${esc(m.url)}">Open it on ${esc(siteName(m.site))}</button>
            <div id="sx-progress"></div>
            <details class="small" style="margin-top:12px"><summary>Already downloaded it somewhere else?</summary>
              <label class="sx-drop" id="sx-drop" style="margin-top:8px"><input type="file" id="sx-upload" multiple hidden><span>Drop the zip or files here, or click to choose</span></label></details>`}
        </div>
      </div>
    </div>`;
  el.querySelector("[data-sx-back]").onclick = () => { closeSiteModel(); if (!sx.items.length) siteLoad(true); };
  // The picture box takes the picture's shape: no bars.
  const stage = $("#sx-stage");
  if (stage) stage.onload = () => { stage.parentElement.style.aspectRatio = `${stage.naturalWidth} / ${stage.naturalHeight}`; };
  el.querySelectorAll("[data-sx-img]").forEach((im) => (im.onclick = () => { $("#sx-stage").src = im.dataset.big; }));
  const ol = el.querySelector("[data-sx-open-lib]");
  if (ol) ol.onclick = () => { setSource("mine"); openItem(ol.dataset.sxOpenLib); };
  const all = $("#sx-all");
  if (all) {
    const boxes = [...el.querySelectorAll("[data-sx-file]")];
    all.checked = boxes.every((b) => b.checked || files.find((f) => f.id === b.dataset.sxFile).kind === "gcode");
    all.onchange = () => boxes.forEach((b) => { if (files.find((f) => f.id === b.dataset.sxFile).kind !== "gcode") b.checked = all.checked; });
  }
  const btn = $("#sx-import");
  if (btn) btn.onclick = () => {
    const ids = [...el.querySelectorAll("[data-sx-file]:checked")].map((b) => b.dataset.sxFile);
    if (!ids.length) return toast("Pick at least one file.", true);
    startImport(m, { json: { files: ids } });
  };
  const up = $("#sx-upload"), drop = $("#sx-drop");
  if (up) {
    const send = (list) => {
      if (!list.length) return;
      const fd = new FormData();
      [...list].forEach((f) => fd.append("file", f));
      startImport(m, { method: "POST", body: fd });
    };
    up.onchange = () => send(up.files);
    drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("over"); };
    drop.ondragleave = () => drop.classList.remove("over");
    drop.ondrop = (e) => { e.preventDefault(); drop.classList.remove("over"); send(e.dataTransfer.files); };
  }
  drawImports();
}

async function startImport(m, opts) {
  try {
    const j = await api(`/api/sites/${m.site}/models/${enc(m.id)}/import`, opts);
    sx.imports = sx.imports.filter((x) => x.id !== j.id).concat(j);
    toast(`Importing “${m.title}”…`);
    const b = $("#sx-import");
    if (b) { b.disabled = true; b.textContent = "Importing…"; }
    pollImports();
  } catch (e) { toast(e.message, true); }
}

async function pollImports() {
  if (sx.polling) return;
  sx.polling = true;
  try {
    for (;;) {
      try { sx.imports = await api("/api/sites/imports"); } catch { /* restarting */ }
      drawImports();
      for (const j of sx.imports) {
        if (j.state === "running" || sx.seenDone.has(j.id)) continue;
        sx.seenDone.add(j.id);
        if (j.state === "done") toast(`“${j.title}” is in your library (${j.folder}).`);
        else toast(`Import of “${j.title || "model"}” failed: ${j.error}`, true);
        if (sx.model && sx.model.model.site === j.site && sx.model.model.id === j.model_id && j.state === "done") {
          sx.model.in_library = j.item_id || sx.model.in_library;
          drawSiteModel();
        }
        if (sx.src === "mine") loadLibrary();
        sx.items.forEach((c) => { if (c.site === j.site && c.id === j.model_id && j.item_id) c.in_library = j.item_id; });
      }
      if (!sx.imports.some((j) => j.state === "running")) {
        if (!sx.ext || state.view !== "library") break;
      }
      await new Promise((r) => setTimeout(r, 1500));
    }
  } finally { sx.polling = false; }
}

function drawImports() {
  const box = $("#sx-imports");
  if (!box) return;
  const recent = sx.imports.filter((j) => j.state === "running" || Date.now() - new Date(j.finished) < 60000);
  setHTML(box, recent.map((j) => {
    const pct = j.total ? Math.min(100, Math.round((j.done / j.total) * 100)) : 0;
    return `<div class="sx-imp ${j.state}">
      <span class="sx-imp-title">${esc(j.title || "Model")}</span>
      <span class="small muted">${j.state === "running" ? esc(j.step) + (j.total ? ` · ${pct}%` : "") : j.state === "done" ? "Imported" : esc(j.error || "Failed")}</span>
      ${j.state === "running" ? `<div class="progress"><div style="width:${pct}%"></div></div>` : j.state === "done" && j.item_id ? `<button class="small" data-sx-open-lib="${esc(j.item_id)}">Open</button>` : ""}
    </div>`;
  }).join(""));
  box.querySelectorAll("[data-sx-open-lib]").forEach((b) => (b.onclick = () => { setSource("mine"); openItem(b.dataset.sxOpenLib); }));
  const pr = $("#sx-progress");
  if (pr && sx.model) {
    const m = sx.model.model;
    const j = sx.imports.find((x) => x.site === m.site && x.model_id === m.id && x.state === "running");
    setHTML(pr, j ? `<div class="small muted" style="margin:8px 0 4px">${esc(j.step)}</div><div class="progress"><div style="width:${j.total ? Math.round((j.done / j.total) * 100) : 5}%"></div></div>` : "");
  }
}

// "Also search online sites": separate sections under your own results.
async function sitesAlso(q) {
  const box = $("#lib-also-results");
  if (!box) return;
  const seq = ++sx.alsoSeq;
  q = (q || "").trim();
  if (!$("#lib-also").checked || q.length < 2) { box.innerHTML = ""; return; }
  const ready = sx.sites.filter((s) => s.ready);
  box.innerHTML = ready.map((s) => `<section class="also-sec" data-also="${s.id}"><div class="also-head"><h3>On ${esc(s.name)}</h3>
      <button class="link" data-also-all="${s.id}">See all on ${esc(s.name)} →</button></div><div class="small muted also-st">Searching…</div><div class="lib-grid"></div></section>`).join("")
    + (ready.length < sx.sites.length ? `<p class="small muted also-browse">Search in a new tab: ${sx.sites.filter((s) => !s.ready).map((s) => `<a href="#" data-browse="${esc(SITE_SEARCH[s.id](q))}">${esc(s.name)}</a>`).join(" · ")}</p>` : "");
  box.onclick = (e) => {
    const all = e.target.closest("[data-also-all]");
    if (all) { sx.q = q; setSource(all.dataset.alsoAll); return; }
    if (e.target.closest("[data-goto-settings]")) { e.preventDefault(); gotoSiteSettings(); return; }
    const c = e.target.closest("[data-site-model]");
    if (c) openSiteModel(c.dataset.site, c.dataset.siteModel);
  };
  await Promise.all(ready.map(async (s) => {
    const sec = box.querySelector(`[data-also="${s.id}"]`);
    try {
      const r = await api(`/api/sites/${s.id}/search?` + new URLSearchParams({ q, sort: "" }));
      if (seq !== sx.alsoSeq) return;
      sec.querySelector(".also-st").textContent = r.items.length ? "" : "Nothing found.";
      sec.querySelector(".lib-grid").innerHTML = r.items.slice(0, 8).map(siteCard).join("");
    } catch (e) {
      if (seq === sx.alsoSeq) sec.querySelector(".also-st").innerHTML = `<span class="err">${esc(e.message)}</span>`;
    }
  }));
}

// Keep the import list fresh while the Library is open (downloads you make
// in other tabs show up here).
setInterval(() => { if (sx.ext && state.view === "library" && !sx.polling) pollImports(); }, 5000);
