// PrintHarbor Library: your private Printables/MakerWorld.
"use strict";

const lib = { folder: null, open: {}, openDirs: {}, q: "", category: "", tag: "", kind: "", favorites: false, sort: "recent", items: [], total: 0, facets: { categories: [], tags: [], folders: [] }, item: null, viewer: null, loaded: false };
const KIND_LABEL = { stl: "STL", "3mf": "3MF", obj: "OBJ", step: "STEP", gcode: "G-code", zip: "ZIP", scad: "SCAD", cad: "CAD", amf: "AMF", ply: "PLY", other: "File" };
const printable = (n) => /\.(stl|obj|3mf)$/i.test(n);
const fmtSize = (b) => (b > 1 << 20 ? (b / (1 << 20)).toFixed(1) + " MB" : Math.max(1, Math.round(b / 1024)) + " KB");
const enc2 = encodeURIComponent;

async function renderLibrary() {
  const root = $("#view-library");
  if (!lib.loaded) {
    lib.loaded = true;
    root.innerHTML = `
      <div class="src-bar" id="lib-src"></div>
      <div id="lib-online" class="hidden"></div>
      <div class="lib-layout" id="lib-local">
        <aside class="lib-side card collapsed" id="lib-side">
          <button class="primary" id="lib-add" style="width:100%">Add a model</button>
          <button type="button" class="lib-side-toggle" id="lib-side-toggle">Favorites, categories &amp; tags <span>▾</span></button>
          <button id="lib-design" style="width:100%;margin-top:8px" title="Design something new in 3D James; it saves into My designs">New design in 3D James</button>
          <div class="settings-group">Browse</div>
          <a href="#" class="lib-link" data-cat="">All models</a>
          <a href="#" class="lib-link" data-fav="1">★ Favorites</a>
          <div class="settings-group">Categories</div><div id="lib-cats"></div>
          <div class="settings-group">Tags</div><div id="lib-tags" class="lib-tags"></div>
          <button id="lib-rescan" style="width:100%;margin-top:14px" title="Look for new and changed models in your Models folder">Rescan models folder</button>
          <div id="lib-tree" class="hidden"></div><div id="lib-folders" class="hidden"></div>
        </aside>
        <section class="lib-main">
          <div id="lib-browse">
            <div class="lib-bar">
              <input id="lib-q" type="search" placeholder="Search models, tags, designers, file names…">
              <select id="lib-kind"><option value="">All file types</option>${["stl", "3mf", "obj", "step", "gcode", "zip"].map((k) => `<option value="${k}">${KIND_LABEL[k]}</option>`).join("")}</select>
              <select id="lib-sort"><option value="recent">Newest</option><option value="name">Name</option><option value="printed">Most printed</option></select>
            </div>
            <div id="lib-crumbs" class="lib-crumbs"></div>
            <div id="lib-subfolders" class="lib-folders"></div>
            <div class="row gap wrap lib-sub"><div id="lib-filter" class="small muted"></div>
              <label class="also-toggle" title="Show matching models from the online sites below your own (kept separate)"><input type="checkbox" id="lib-also"> Also search online sites</label></div>
            <div id="lib-grid" class="lib-grid"></div>
            <div id="lib-also-results"></div>
          </div>
          <div id="lib-detail" class="hidden"></div>
        </section>
      </div>`;
    let t;
    $("#lib-q").oninput = (e) => { clearTimeout(t); t = setTimeout(() => { lib.q = e.target.value; loadLibrary(); }, 200); };
    $("#lib-kind").onchange = (e) => { lib.kind = e.target.value; loadLibrary(); };
    $("#lib-sort").onchange = (e) => { lib.sort = e.target.value; loadLibrary(); };
    $("#lib-add").onclick = () => addModelDialog();
    $("#lib-design").onclick = () => show("design");
    sitesInit();
    $("#lib-rescan").onclick = async () => { toast("Scanning your folders…"); const r = await api("/api/library/rescan", { method: "POST" }); toast(`Scan done: ${r.added} new, ${r.updated} updated, ${r.removed} removed.${r.errors.length ? " " + r.errors.join("; ") : ""}`); loadLibrary(); };
    const goFolder = (f) => { lib.folder = f; lib.favorites = false; lib.category = ""; lib.tag = ""; const parts = f.split("/"); for (let i = 1; i < parts.length; i++) lib.open[parts.slice(0, i).join("/")] = true; closeItem(); loadLibrary(); };
    root.querySelector(".lib-main").addEventListener("click", (e) => {
      const a = e.target.closest("[data-folder]");
      if (!a) return;
      e.preventDefault();
      goFolder(a.dataset.folder);
    });
    $("#lib-side-toggle").onclick = () => $("#lib-side").classList.toggle("collapsed");
    root.querySelector(".lib-side").onclick = (e) => {
      const tw = e.target.closest("[data-twist]");
      if (tw) { e.preventDefault(); lib.open[tw.dataset.twist] = !lib.open[tw.dataset.twist]; drawFacets(); return; }
      const fo = e.target.closest("[data-folder]");
      if (fo) { e.preventDefault(); goFolder(fo.dataset.folder); return; }
      const a = e.target.closest("[data-cat],[data-tag],[data-fav]");
      if (!a) return;
      e.preventDefault();
      lib.folder = null;
      $("#lib-side").classList.add("collapsed"); // (phones: back to the models)
      if (a.dataset.fav) { lib.favorites = true; lib.category = ""; lib.tag = ""; }
      else if (a.dataset.tag !== undefined) { lib.tag = lib.tag === a.dataset.tag ? "" : a.dataset.tag; lib.favorites = false; }
      else { lib.category = a.dataset.cat; lib.favorites = false; lib.tag = ""; }
      closeItem();
      loadLibrary();
    };
    $("#lib-grid").onclick = (e) => {
      const fav = e.target.closest("[data-star]");
      if (fav) { e.stopPropagation(); toggleFav(fav.dataset.star); return; }
      const c = e.target.closest("[data-item]");
      if (c) openItem(c.dataset.item);
    };
    await loadLibrary();
  }
}

async function loadLibrary() {
  const facets = await api("/api/library/facets");
  lib.facets = facets;
  const roots = libRoots();
  // one library folder: its contents are the top level
  if (lib.folder === "" && roots.length === 1) lib.folder = roots[0].path;
  const inFolder = lib.folder !== null && lib.folder !== "";
  const qs = new URLSearchParams({ q: lib.q, category: lib.category, tag: lib.tag, kind: lib.kind, sort: lib.sort, favorites: lib.favorites ? "1" : "", limit: "300" });
  if (inFolder) { qs.set("folder", lib.folder); if (lib.q) qs.set("deep", "1"); }
  if (lib.folder === "") qs.set("folder", "-"); // top: folders, plus models added in PrintHarbor
  const res = await api("/api/library?" + qs);
  lib.items = res.items; lib.total = res.total;
  drawFacets();
  drawFolderView();
  const filters = [lib.favorites && "favorites", lib.category && `category “${lib.category}”`, lib.tag && `tag #${lib.tag}`, lib.q && `“${lib.q}”`].filter(Boolean);
  $("#lib-filter").textContent = lib.folder !== null && !lib.total && !lib.q && (lib.folder === "" || folderKids(lib.folder).length) ? ""
    : lib.folder !== null
    ? `${lib.total} model(s) ${lib.folder && lib.folder !== "-" ? (lib.q ? "in this folder and below" : "in this folder") : "added in PrintHarbor"}${filters.length ? " · " + filters.join(" · ") : ""}`
    : `${lib.total} model(s) in your library${filters.length ? " · " + filters.join(" · ") : ""}`;
  sitesAlso(lib.q);
  const g = $("#lib-grid");
  if (!lib.items.length && lib.folder !== null && (lib.folder === "" || folderKids(lib.folder).length)) { g.innerHTML = ""; return; }
  if (!lib.items.length) {
    g.innerHTML = lib.total === 0 && !filters.length && !lib.kind && lib.folder === null
      ? `<div class="lib-empty card"><h3>Your library is empty</h3><p class="muted">Put models in your Models folder as <strong>Models\\Designer\\Model</strong> and click <strong>Rescan models folder</strong>, or click <strong>Add a model</strong> to upload files and photos.</p></div>`
      : `<p class="muted">Nothing matches.</p>`;
    return;
  }
  g.innerHTML = lib.items.map((it) => `
    <div class="lib-card card" data-item="${it.id}">
      <div class="lib-cover"><img loading="lazy" src="/api/library/${it.id}/cover?v=${enc2(it.updated)}" alt="" onerror="this.replaceWith(Object.assign(document.createElement('div'),{className:'noimg',textContent:'No preview'}))"></div>
      <button class="lib-star ${it.favorite ? "on" : ""}" data-star="${it.id}" title="Favorite">★</button>
      <div class="lib-card-body"><div class="lib-title">${esc(it.title)}</div>
        <div class="muted small">${it.category ? esc(it.category.split("/").pop()) : "No category"}${it.designer ? ` · by ${esc(it.designer)}` : ""} · ${it.files.length} file(s)${it.print_count ? ` · printed ${it.print_count}×` : ""}</div></div>
    </div>`).join("");
}

// ---------- folders: the library as it is on disk ----------
function libRoots() { return (lib.facets.tree || []).filter((f) => !f.path.includes("/")); }
function folderKids(path) {
  const pre = path ? path + "/" : "";
  return (lib.facets.tree || []).filter((f) => f.path.startsWith(pre) && !f.path.slice(pre.length).includes("/") && f.path !== path);
}
const folderName = (p) => p.split("/").pop();
const folderIcon = `<svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path fill="currentColor" d="M10 4H4a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-8l-2-2z"/></svg>`;
function drawFolderView() {
  const crumbs = $("#lib-crumbs"), subs = $("#lib-subfolders");
  if (lib.folder === null) { crumbs.innerHTML = ""; subs.innerHTML = ""; return; }
  const roots = libRoots();
  const parts = lib.folder && lib.folder !== "-" ? lib.folder.split("/") : [];
  const top = roots.length === 1 ? 1 : 0; // one library folder: it is the top
  crumbs.innerHTML = `<a href="#" data-folder="${top ? esc(roots[0].path) : ""}">${folderIcon} ${top ? esc(roots[0].path) : "Folders"}</a>` +
    parts.slice(top).map((p, i) => `<span class="muted">›</span><a href="#" data-folder="${esc(parts.slice(0, top + i + 1).join("/"))}">${esc(p)}</a>`).join("");
  if (lib.folder === "-") { subs.innerHTML = ""; return; }
  const kids = lib.folder === "" ? roots : folderKids(lib.folder);
  subs.innerHTML = lib.q ? "" : kids.map((f) => `<a href="#" class="lib-folder card" data-folder="${esc(f.path)}" title="${esc(f.path)}">
      <span class="lib-folder-ic">${folderIcon}</span><span class="lib-folder-name">${esc(folderName(f.path))}</span><span class="muted small">${f.total} model${f.total === 1 ? "" : "s"}</span></a>`).join("")
    + (lib.folder === "" || (top && lib.folder === roots[0].path) ? addedTile() : "");
}
function addedTile() { return lib.facets.added ? `<a href="#" class="lib-folder card" data-folder="-"><span class="lib-folder-ic">★</span><span class="lib-folder-name">Added in PrintHarbor</span><span class="muted small">${lib.facets.added} models</span></a>` : ""; }
function drawTree() {
  const tree = lib.facets.tree || [];
  const roots = libRoots();
  const depthOf = (p) => p.split("/").length - (roots.length === 1 ? 1 : 0);
  const visible = (p) => { const parts = p.split("/"); for (let i = 1; i < parts.length; i++) { const a = parts.slice(0, i).join("/"); if (roots.length === 1 && i === 1) continue; if (!lib.open[a]) return false; } return true; };
  const list = tree.filter((f) => !(roots.length === 1 && f.path === roots[0].path) && visible(f.path));
  const html = list.map((f) => {
    const kids = folderKids(f.path).length;
    return `<div class="lib-node ${lib.folder === f.path ? "active" : ""}" style="padding-left:${4 + (depthOf(f.path) - 1) * 14}px">
      ${kids ? `<button class="twist ${lib.open[f.path] ? "open" : ""}" data-twist="${esc(f.path)}" title="${lib.open[f.path] ? "Close" : "Open"}">▸</button>` : '<span class="twist"></span>'}
      <a href="#" data-folder="${esc(f.path)}" title="${esc(f.path)}">${esc(folderName(f.path))}</a><span class="muted small">${f.total}</span></div>`;
  }).join("");
  const top = roots.length === 1 ? roots[0].path : "";
  $("#lib-tree").innerHTML = `<div class="lib-node ${lib.folder === top || (top === "" && lib.folder === "") ? "active" : ""}"><span class="twist"></span><a href="#" data-folder="${esc(top)}">${folderIcon} ${top ? esc(top) : "All folders"}</a></div>${html}` || `<div class="small muted">No folders yet</div>`;
}

function drawFacets() {
  const f = lib.facets;
  drawTree();
  $("#lib-cats").innerHTML = f.categories.length ? f.categories.map((c) => {
    const depth = c.name.split("/").length - 1, leaf = c.name.split("/").pop();
    return `<a href="#" class="lib-link ${lib.category === c.name ? "active" : ""}" data-cat="${esc(c.name)}" style="padding-left:${8 + depth * 14}px">${esc(leaf)} <span class="muted">${c.count}</span></a>`;
  }).join("") : `<div class="small muted">None yet. Give a model a category in its details.</div>`;
  $("#lib-tags").innerHTML = f.tags.slice(0, 40).map((t) => `<a href="#" class="tag ${lib.tag === t.name ? "active" : ""}" data-tag="${esc(t.name)}">#${esc(t.name)}</a>`).join("") || `<div class="small muted">None yet</div>`;
  $("#lib-folders").innerHTML = f.folders.length ? f.folders.map((p) => `<div class="folder-path" title="${esc(p)}">${esc(p)}</div>`).join("") : "None. Click Folders… to add yours.";
  document.querySelectorAll(".lib-side [data-fav]").forEach((a) => a.classList.toggle("active", lib.favorites));
  document.querySelectorAll('.lib-side [data-cat=""]').forEach((a) => a.classList.toggle("active", lib.folder === null && !lib.favorites && !lib.category && !lib.tag));
}

async function toggleFav(id) {
  const it = lib.items.find((x) => x.id === id) || lib.item;
  await api("/api/library/" + id, { method: "PUT", json: { favorite: !it.favorite } });
  if (lib.item && lib.item.id === id) lib.item.favorite = !lib.item.favorite;
  loadLibrary();
}

function foldersDialog() {
  const body = $("#dlg-body");
  body.innerHTML = `<h2>Library folders</h2>
    <p class="muted small">Folders on the computer running PrintHarbor. Every folder holding model files (or a files/stls subfolder) becomes a model, and the folders above it become categories. ZIP downloads count too. PrintHarbor only reads these folders; it never changes or deletes anything in them.</p>
    <textarea id="lib-folder-list" rows="6" placeholder="D:\\3D Models&#10;C:\\Users\\you\\Downloads\\Printables">${esc(lib.facets.folders.join("\n"))}</textarea>
    <p class="muted small">One folder per line. Tip: in Windows Explorer, Shift + right-click a folder → "Copy as path".</p>
    <div class="dlg-actions"><button value="cancel">Cancel</button><button type="button" class="primary" id="lib-folders-save">Save &amp; scan</button></div>`;
  $("#lib-folders-save").onclick = async () => {
    try {
      const r = await api("/api/library/folders", { json: { folders: $("#lib-folder-list").value.split("\n") } });
      $("#dlg").close();
      toast(`Scanned: ${r.added} new, ${r.updated} updated, ${r.removed} removed.`);
      loadLibrary();
    } catch (e) { toast(e.message, true); }
  };
  openDialog();
}

// Categories to start with (like Printables'); the ones you use come first.
const BASE_CATEGORIES = ["3D Printers", "Art & Design", "Costumes & Accessories", "Fashion", "Gadgets", "Healthcare", "Hobby & Makers", "Household", "Learning", "Props", "Seasonal & Holidays", "Sports & Outdoors", "Tools", "Toys & Games"];
function categoryChoices() {
  const used = (lib.facets.categories || []).map((c) => c.name);
  return [...used, ...BASE_CATEGORIES.filter((c) => !used.some((u) => u.toLowerCase() === c.toLowerCase()))];
}
function metaForm(it = {}) {
  const cats = categoryChoices().map((c) => `<option value="${esc(c)}">`).join("");
  const tagSugg = (lib.facets.tags || []).slice(0, 24).map((t) => t.name);
  return `
    <label class="field"><span>Where it came from (link)</span>
      <span class="lf-row"><input name="source_url" value="${esc(it.source_url || "")}" placeholder="Paste the model's page, e.g. https://www.printables.com/model/…"><button type="button" class="lf-btn">Fill in from link</button></span>
      <small class="muted">PrintHarbor reads only this page, and only when you click the button.</small></label>
    <div class="lf-result"></div>
    <label class="field"><span>Title</span><input name="title" value="${esc(it.title || "")}"></label>
    <div class="row">
      <label class="field"><span>Category <small class="muted">(type a new one, / for a sub-category)</small></span><input name="category" list="lib-cat-list" value="${esc(it.category || lib.category || "")}" placeholder="Pick or type, e.g. Seasonal & Holidays"><datalist id="lib-cat-list">${cats}</datalist></label>
      <label class="field"><span>Tags <small class="muted">(comma separated)</small></span><input name="tags" value="${esc((it.tags || []).join(", "))}" placeholder="thanksgiving, multicolor"></label>
    </div>
    <div class="meta-chips"><span class="muted small">Category:</span>${categoryChoices().slice(0, 18).map((c) => `<button type="button" class="chip" data-pickcat="${esc(c)}">${esc(c)}</button>`).join("")}</div>
    ${tagSugg.length ? `<div class="meta-chips"><span class="muted small">Your tags:</span>${tagSugg.map((t) => `<button type="button" class="chip" data-picktag="${esc(t)}">#${esc(t)}</button>`).join("")}</div>` : ""}
    <label class="field"><span>Description</span><textarea name="description" rows="4">${esc(it.description || "")}</textarea></label>
    <div class="row">
      <label class="field"><span>Designer</span><input name="designer" value="${esc(it.designer || "")}"></label>
      <label class="field"><span>License</span><input name="license" list="lib-lic" value="${esc(it.license || "")}" placeholder="e.g. CC BY-NC"><datalist id="lib-lic"><option>Personal use</option><option>Commercial license (purchased)</option><option>CC BY</option><option>CC BY-SA</option><option>CC BY-NC</option><option>CC BY-NC-SA</option><option>CC0</option><option>My own design</option></datalist></label>
    </div>
    <label class="field"><span>Notes (print settings that worked, filament, etc.)</span><textarea name="notes" rows="3">${esc(it.notes || "")}</textarea></label>`;
}

// "Fill in from link": the server reads the pasted page (only that page, only
// on click) and we fill in the empty fields. Fields that already have
// something are offered as "replace" choices instead of being overwritten.
const LF_FIELDS = [["title", "Title"], ["designer", "Designer"], ["license", "License"], ["category", "Category"], ["tags", "Tags"], ["description", "Description"]];
async function linkFill(root, hasPhotos) {
  const btn = root.querySelector(".lf-btn"), out = root.querySelector(".lf-result");
  const url = root.querySelector('[name="source_url"]').value.trim();
  if (!url) return toast("Paste the address of the model's page first.");
  btn.disabled = true; btn.textContent = "Reading the page…";
  let info;
  try { info = await api("/api/library/link-info", { json: { url } }); }
  catch (e) { out.innerHTML = `<p class="err small">${esc(e.message)}</p>`; return; }
  finally { btn.disabled = false; btn.textContent = "Fill in from link"; }
  const filled = [], clash = [];
  for (const [k, label] of LF_FIELDS) {
    const val = k === "tags" ? info.tags.join(", ") : info[k] || "";
    if (!val) continue;
    const el = root.querySelector(`[name="${k}"]`);
    if (!el.value.trim()) { el.value = val; filled.push(label); }
    else if (el.value.trim() !== val) clash.push([k, label, val]);
  }
  root._lfInfo = info;
  out.innerHTML = `<div class="lf-box">
    <div class="small"><strong>${esc(info.site)}</strong>: ${filled.length ? "filled in " + filled.join(", ").toLowerCase() + "." : clash.length ? "your details are already filled in." : "nothing new found."}</div>
    ${clash.length ? `<div class="small muted" style="margin-top:6px">Replace what's there with the page's version?</div>
      ${clash.map(([k, label, val]) => `<label class="lf-choice"><input type="checkbox" data-lf="${k}"> <strong>${label}:</strong> <span>${esc(val.length > 140 ? val.slice(0, 140) + "…" : val)}</span></label>`).join("")}
      <button type="button" class="lf-apply" style="margin-top:6px">Replace ticked</button>` : ""}
    ${info.images.length ? `<div class="small muted" style="margin-top:8px">Pictures from the page: tick the ones to save as photos (saved when you ${root.dataset.newItem ? "add the model" : "click Save details"}).</div>
      <div class="lf-imgs">${info.images.map((u, i) => `<label class="lf-img"><input type="checkbox" data-lfimg="${esc(u)}" ${i === 0 && !hasPhotos ? "checked" : ""}><img src="${esc(u)}" alt="" loading="lazy" referrerpolicy="no-referrer" onerror="this.parentElement.remove()"></label>`).join("")}</div>` : ""}
  </div>`;
  const apply = out.querySelector(".lf-apply");
  if (apply) apply.onclick = () => {
    out.querySelectorAll("[data-lf]:checked").forEach((c) => {
      const [, , val] = clash.find((x) => x[0] === c.dataset.lf);
      root.querySelector(`[name="${c.dataset.lf}"]`).value = val;
      c.closest(".lf-choice").remove();
    });
    if (!out.querySelector("[data-lf]")) { apply.remove(); }
  };
}
function wireLinkFill(root, hasPhotos) {
  const btn = root.querySelector(".lf-btn");
  if (btn) btn.onclick = () => linkFill(root, hasPhotos);
}
// Saves the pictures ticked in the link result as photos of the item.
async function saveLinkPhotos(root, id) {
  const urls = [...root.querySelectorAll("[data-lfimg]:checked")].map((c) => c.dataset.lfimg);
  if (!urls.length) return;
  try { await api(`/api/library/${id}/photo-from-link`, { json: { urls } }); }
  catch (e) { toast("Couldn't save the pictures: " + e.message, true); }
  root.querySelectorAll("[data-lfimg]:checked").forEach((c) => { c.checked = false; });
}

function readMeta(root) {
  const v = (n) => root.querySelector(`[name="${n}"]`).value;
  return { title: v("title"), category: v("category"), tags: v("tags").split(","), description: v("description"), designer: v("designer"), license: v("license"), source_url: v("source_url"), notes: v("notes") };
}

function addModelDialog() {
  const body = $("#dlg-body");
  let picked = [];
  body.innerHTML = `<h2>Add a model</h2>
    <label class="dropzone" id="lib-drop" style="padding:22px"><input type="file" id="lib-files" multiple hidden>
      <strong>Drop model files and photos here</strong><span>STL, 3MF, OBJ, STEP, G-code, ZIP… and JPG/PNG photos. Or click to choose.</span></label>
    <div id="lib-picked" class="small" style="margin:8px 0 14px"></div>
    <div id="lib-new-meta" data-new-item="1">${metaForm()}</div>
    <div class="dlg-actions"><button value="cancel">Cancel</button><button type="button" class="primary" id="lib-create">Add to library</button></div>`;
  const show = () => {
    $("#lib-picked").innerHTML = picked.map((f) => `<span class="slot">${/\.(jpe?g|png|webp|gif)$/i.test(f.name) ? "📷" : "📦"} ${esc(f.name)} <span class="muted">${fmtSize(f.size)}</span></span>`).join(" ");
    const t = body.querySelector('[name="title"]');
    const model = picked.find((f) => !/\.(jpe?g|png|webp|gif)$/i.test(f.name));
    if (!t.value && model) t.value = model.name.replace(/\.[^.]+$/, "").replace(/[_-]+/g, " ");
  };
  const drop = $("#lib-drop");
  drop.ondragover = (e) => { e.preventDefault(); drop.classList.add("over"); };
  drop.ondragleave = () => drop.classList.remove("over");
  drop.ondrop = (e) => { e.preventDefault(); drop.classList.remove("over"); picked.push(...e.dataTransfer.files); show(); };
  $("#lib-files").onchange = (e) => { picked.push(...e.target.files); show(); };
  wireLinkFill($("#lib-new-meta"), false);
  $("#lib-create").onclick = async () => {
    const linkPics = body.querySelectorAll("[data-lfimg]:checked").length;
    if (!picked.length && !linkPics) return toast("Add at least one file or photo.");
    const fd = new FormData();
    const m = readMeta(body);
    for (const [k, v] of Object.entries(m)) fd.append(k, Array.isArray(v) ? v.join(",") : v);
    for (const f of picked) fd.append(/\.(jpe?g|png|webp|gif)$/i.test(f.name) ? "photo" : "file", f);
    $("#lib-create").disabled = true;
    try {
      const it = await api("/api/library", { method: "POST", body: fd });
      await saveLinkPhotos($("#lib-new-meta"), it.id);
      $("#dlg").close();
      await loadLibrary();
      openItem(it.id);
    } catch (e) { toast(e.message, true); $("#lib-create").disabled = false; }
  };
  openDialog();
}

function closeItem() {
  if (lib.viewer) { lib.viewer.destroy(); lib.viewer = null; }
  lib.item = null;
  $("#lib-detail").classList.add("hidden");
  $("#lib-browse").classList.remove("hidden");
}

function photoURL(it, kind, name) { return `/api/library/${it.id}/raw/${kind}?name=${enc2(name)}`; }

async function openItem(id) {
  const it = (lib.item = await api("/api/library/" + id));
  $("#lib-browse").classList.add("hidden");
  const el = $("#lib-detail");
  el.classList.remove("hidden");
  const photos = [...it.added_photos.map((n) => ({ kind: "added", name: n })), ...it.photos.map((n) => ({ kind: "photo", name: n }))];
  const models = it.files.filter((f) => printable(f.name));
  const managed = it.source === "managed";
  // Files 3D James can open, its own projects first.
  const james = it.files.filter((f) => /\.(3dj|stl|3mf|obj|step|stp)$/i.test(f.name)).sort((a, b) => (b.kind === "3dj") - (a.kind === "3dj"));
  const meshParts = it.files.filter((f) => /\.(stl|obj)$/i.test(f.name));
  const labels = partLabels(it.files);
  const md = (lib.facets && lib.facets.model_dir) || "";
  const canRename = managed || (it.source === "folder" && md && (it.path + "\\").toLowerCase().startsWith((md + "\\").toLowerCase()) || (it.path + "/").startsWith(md + "/"));
  el.innerHTML = `
    <button class="back" id="lib-back">← Library</button>
    <div class="toolbar"><div><h2>${esc(it.title)} <button class="lib-star inline ${it.favorite ? "on" : ""}" id="lib-fav" title="Favorite">★</button>
        <button type="button" class="danger small lib-del-top" id="lib-del" title="${managed ? "Delete this model and its files from PrintHarbor" : "Hide this model from the library (its files stay in your folder)"}">${managed ? "Delete model" : "Hide"}</button></h2>
      <div class="muted small">${esc(it.category || "Uncategorized")}${it.designer ? " · by " + esc(it.designer) : ""}${it.print_count ? ` · printed ${it.print_count}×` : ""}${it.source !== "managed" ? ` · from ${esc(it.path)}` : ""}</div></div>
      ${meshParts.length > 1 ? `<div class="row" style="flex:0 0 auto"><button id="lib-slice-all" title="Open all ${meshParts.length} parts in the slicer together">Open all parts in slicer</button><button class="primary" id="lib-print" title="Put all ${meshParts.length} STL/OBJ parts on a plate together">Send all ${meshParts.length} parts to a project…</button></div>` : ""}</div>
    ${managed && !it.files.length ? `<div class="notice warn" style="margin-bottom:12px">This model has no files left. <button type="button" class="danger small" id="lib-del2">Delete it</button></div>` : ""}
    <div class="lib-detail-grid">
      <div>
        <div class="lib-stage card" id="lib-stage"></div>
        <div class="lib-strip"><button type="button" class="strip-nav prev" id="lib-prev" aria-label="Previous pictures">‹</button>
        <div class="lib-thumbs" id="lib-thumbs">
          ${photos.map((p, i) => `<img src="${photoURL(it, p.kind, p.name)}" data-photo="${i}" alt="">`).join("")}
          ${models.map((f, i) => `<div class="lib-model-thumb" data-model="${i}" title="3D view: ${esc((labels[f.name] || {}).label || f.name)}"><img src="/api/library/${it.id}/thumb?name=${enc2(f.name)}" alt=""><span>3D</span></div>`).join("")}
          <label class="lib-add-photo" title="Add photos (or paste an image)"><input type="file" id="lib-photo-in" accept="image/*" multiple hidden>＋ Photo</label>
        </div><button type="button" class="strip-nav next" id="lib-next" aria-label="More pictures">›</button></div>
        <div class="lib-files-head"><div class="settings-group">Files</div>
          ${canRename ? `<button class="small" id="lib-rename" title="Give the files clear names, like ${esc(shortTitle(it.title))} - Body.stl">Tidy file names…</button>` : ""}</div>
        <div class="lib-files">${fileTree(it, labels, managed) || `<p class="muted small">No files.</p>`}</div>
        ${managed ? `<label class="btn" style="display:inline-block;margin-top:8px"><input type="file" id="lib-file-in" multiple hidden>Add files</label>` : `<p class="muted small">These files live in a folder on your drive. Add or remove files there, then click Rescan.</p>`}
      </div>
      <div class="card">
        <div id="lib-meta">${metaForm(it)}</div>
        ${it.source_url ? `<p class="small"><a href="${esc(it.source_url)}" target="_blank" rel="noopener">Open source page ↗</a></p>` : ""}
        <div class="dlg-actions" style="position:static">
          <button type="button" class="primary" id="lib-save" style="margin-left:auto">Save details</button></div>
        ${it.source === "folder" ? `<p class="small muted" style="margin-top:8px">Saved with the files: in printharbor.json inside the model's folder, so the details go wherever the folder goes.</p>` : ""}
      </div>
    </div>`;

  const stage = $("#lib-stage");
  const showPhoto = (i) => {
    if (lib.viewer) { lib.viewer.destroy(); lib.viewer = null; }
    const p = photos[i];
    stage.innerHTML = `<img src="${photoURL(it, p.kind, p.name)}" alt="">
      <div class="lib-stage-actions"><button class="icon" id="lib-cover">Use as cover</button>${p.kind === "added" ? `<button class="icon danger" id="lib-rmphoto">Remove photo</button>` : ""}</div>`;
    $("#lib-cover").onclick = async () => { await api("/api/library/" + it.id, { method: "PUT", json: { cover: `${p.kind}:${p.name}` } }); toast("Cover updated."); };
    const rm = $("#lib-rmphoto");
    if (rm) rm.onclick = async () => { await api(`/api/library/${it.id}/files?photo=1&name=${enc2(p.name)}`, { method: "DELETE" }); openItem(it.id); };
  };
  const show3D = (name) => {
    if (lib.viewer) lib.viewer.destroy();
    lib.viewer = createViewer(stage);
    lib.viewer.load(`/api/library/${it.id}/mesh?name=${enc2(name)}`);
  };
  if (photos.length) showPhoto(0); else if (models.length) show3D(models[0].name); else stage.innerHTML = `<div class="noimg">No photos or previewable files</div>`;

  $("#lib-back").onclick = () => { closeItem(); loadLibrary(); };
  $("#lib-fav").onclick = async () => { await toggleFav(it.id); $("#lib-fav").classList.toggle("on", lib.item.favorite); };
  // One row of pictures; the arrows (or the mouse wheel) scroll it, like Printables.
  const strip = $("#lib-thumbs");
  const navState = () => {
    $("#lib-prev").classList.toggle("off", strip.scrollLeft <= 2);
    $("#lib-next").classList.toggle("off", strip.scrollLeft + strip.clientWidth >= strip.scrollWidth - 2);
  };
  $("#lib-prev").onclick = () => strip.scrollBy({ left: -strip.clientWidth * 0.8, behavior: "smooth" });
  $("#lib-next").onclick = () => strip.scrollBy({ left: strip.clientWidth * 0.8, behavior: "smooth" });
  strip.addEventListener("scroll", navState, { passive: true });
  strip.addEventListener("wheel", (e) => { if (Math.abs(e.deltaY) > Math.abs(e.deltaX) && strip.scrollWidth > strip.clientWidth) { e.preventDefault(); strip.scrollBy({ left: e.deltaY }); } }, { passive: false });
  new ResizeObserver(navState).observe(strip);
  setTimeout(navState, 300);
  const mark = (el) => { strip.querySelectorAll(".cur").forEach((x) => x.classList.remove("cur")); if (el) { el.classList.add("cur"); el.scrollIntoView({ block: "nearest", inline: "nearest", behavior: "smooth" }); } };
  $("#lib-thumbs").onclick = (e) => {
    const ph = e.target.closest("[data-photo]"), md = e.target.closest("[data-model]");
    mark(ph || md);
    if (ph) showPhoto(+ph.dataset.photo);
    if (md) show3D(models[+md.dataset.model].name);
  };
  el.querySelector(".lib-files").onclick = async (e) => {
    const sd = e.target.closest("[data-senddir]");
    if (sd) {
      const pre = sd.dataset.senddir + "/";
      const parts = it.files.filter((f) => f.name.startsWith(pre) && /\.(stl|obj)$/i.test(f.name)).map((f) => f.name);
      if (!parts.length) { toast("No STL or OBJ files in that folder.", true); return; }
      printDialog(it, parts);
      return;
    }
    const dr = e.target.closest("[data-dir]");
    if (dr) {
      const open = lib.openDirs[it.id] || (lib.openDirs[it.id] = {});
      open[dr.dataset.dir] = !open[dr.dataset.dir];
      e.currentTarget.innerHTML = fileTree(it, labels, managed);
      wireFileButtons();
      return;
    }
    const v = e.target.closest("[data-view]"), rmf = e.target.closest("[data-rmfile]");
    if (v) { show3D(v.dataset.view); $("#lib-stage").scrollIntoView({ behavior: "smooth", block: "center" }); }
    if (rmf && confirm(`Delete ${rmf.dataset.rmfile}?`)) { await api(`/api/library/${it.id}/files?name=${enc2(rmf.dataset.rmfile)}`, { method: "DELETE" }); openItem(it.id); }
  };
  const uploadTo = async (files, field) => {
    if (!files.length) return;
    const fd = new FormData();
    for (const f of files) fd.append(field, f, f.name || "pasted.png");
    try { await api(`/api/library/${it.id}/files`, { method: "POST", body: fd }); openItem(it.id); } catch (er) { toast(er.message, true); }
  };
  $("#lib-photo-in").onchange = (e) => uploadTo([...e.target.files], "photo");
  const fin = $("#lib-file-in");
  if (fin) fin.onchange = (e) => uploadTo([...e.target.files], "file");
  // Paste a screenshot / copied image straight in as a photo.
  el.onpaste = (e) => {
    const imgs = [...(e.clipboardData?.files || [])].filter((f) => f.type.startsWith("image/"));
    if (imgs.length && !e.target.closest("input,textarea")) { e.preventDefault(); uploadTo(imgs, "photo"); }
  };
  el.ondragover = (e) => e.preventDefault();
  el.ondrop = (e) => {
    e.preventDefault();
    const fs = [...e.dataTransfer.files];
    const imgs = fs.filter((f) => /\.(jpe?g|png|webp|gif)$/i.test(f.name)), rest = fs.filter((f) => !imgs.includes(f));
    if (imgs.length) uploadTo(imgs, "photo");
    if (rest.length) managed ? uploadTo(rest, "file") : toast("This model lives in a folder on your drive; add files there and Rescan.");
  };
  wireLinkFill($("#lib-meta"), photos.length > 0);
  $("#lib-save").onclick = async () => {
    const pics = $("#lib-meta").querySelectorAll("[data-lfimg]:checked").length;
    try {
      lib.item = await api("/api/library/" + it.id, { method: "PUT", json: readMeta($("#lib-meta")) });
      const id = lib.item.id; // a model imported from a site moves to its new designer\model folder
      if (pics) { await saveLinkPhotos($("#lib-meta"), id); return openItem(id); }
      if (id !== it.id) { toast("Saved and moved to " + lib.item.path); loadLibrary(); return openItem(id); }
      toast("Saved.");
    } catch (er) { toast(er.message, true); }
  };
  const d2 = $("#lib-del2");
  if (d2) d2.onclick = () => $("#lib-del").click();
  $("#lib-del").onclick = async () => {
    const msg = managed ? "Delete this model and its files from PrintHarbor?" : "Hide this model? Its files stay in your folder untouched.";
    if (!confirm(msg)) return;
    await api("/api/library/" + it.id, { method: "DELETE" });
    closeItem(); loadLibrary();
  };
  const pa = $("#lib-print");
  if (pa) pa.onclick = () => printDialog(it, meshParts.map((f) => f.name));
  function wireFileButtons() {
    el.querySelectorAll("[data-send]").forEach((b) => (b.onclick = () => printDialog(it, [b.dataset.send])));
    el.querySelectorAll("[data-slice]").forEach((b) => (b.onclick = () => openInSlicer({ item: it.id, files: [b.dataset.slice] })));
    el.querySelectorAll("[data-james]").forEach((b) => (b.onclick = () => jamesOpen(b.dataset.james)));
    el.querySelectorAll("[data-asproject]").forEach((b) => (b.onclick = () => printDialog(it, [b.dataset.asproject], { as3mf: true })));
  }
  const sa = $("#lib-slice-all");
  if (sa) sa.onclick = () => openInSlicer({ item: it.id, files: meshParts.map((f) => f.name) });
  const jamesOpen = async (name) => {
    try { const o = await api(`/api/library/${it.id}/james?name=${enc2(name)}`); openIn3DJames(o); } catch (er) { toast(er.message, true); }
  };
  wireFileButtons();
  const rn = $("#lib-rename");
  if (rn) rn.onclick = () => renameDialog(it, labels);
}

// ---------- a model's files, in the folders they are in on disk (like Printables) ----------
// Files at the top of the model folder first, then each subfolder as a row that opens to show
// what is in it (its own subfolders open the same way).
function fileRow(it, f, labels, managed) {
          const l = labels[f.name], is3dj = f.kind === "3dj", mesh = /\.(stl|obj)$/i.test(f.name) || is3dj, is3mf = /\.3mf$/i.test(f.name) || is3dj;
          const cad = /\.(3dj|stl|3mf|obj|step|stp)$/i.test(f.name);
          return `<div class="lf">
            <span class="pill lf-kind">${KIND_LABEL[f.kind] || f.kind}</span>
            <div class="lf-main">
              <div class="lf-top"><b>${esc(l.label)}</b>${l.version ? `<span class="lf-ver">${esc(l.version)}</span>` : ""}
                <span class="lf-meta muted">${f.size_mm && f.size_mm[0] ? `${f.size_mm.map(Math.round).join(" × ")} mm · ` : ""}${fmtSize(f.size)}</span></div>
              ${l.label !== l.file ? `<small class="muted lf-file" title="${esc(f.name)}">${esc(l.file)}</small>` : ""}
              <div class="lf-actions">
                ${mesh ? `<button class="lf-btn main" data-send="${esc(f.name)}" title="${is3dj ? "Put it on a plate of a project (3D James builds it on the fly)" : "Put this part on a plate of a project (new or existing)"}"><svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 2 3 7v10l9 5 9-5V7l-9-5zm0 2.3L18.6 8 12 11.7 5.4 8 12 4.3zM5 9.7l6 3.4v6.6l-6-3.4V9.7zm8 10v-6.6l6-3.4v6.6l-6 3.4z"/></svg> Send to project</button>` : ""}
                ${is3mf ? `<button class="lf-btn ${is3dj ? "" : "main"}" data-asproject="${esc(f.name)}" title="${is3dj ? "Open it as its own project, each body its own object with its color" : "Open this 3MF as its own project, with its plates and settings"}"><svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 2 3 7v10l9 5 9-5V7l-9-5zm0 2.3L18.6 8 12 11.7 5.4 8 12 4.3zM5 9.7l6 3.4v6.6l-6-3.4V9.7zm8 10v-6.6l6-3.4v6.6l-6 3.4z"/></svg> Open as project</button>` : ""}
                ${printable(f.name) ? `<button class="lf-btn" data-slice="${esc(f.name)}" title="Open in the slicer: place it, paint colors, change walls, infill and other settings, preview the layers and print"><svg viewBox="0 0 24 24"><path fill="currentColor" d="m12 16.5-8-4.4L2 13.2l10 5.6 10-5.6-2-1.1-8 4.4zm0-3.6L2 7.4 12 2l10 5.4-10 5.5zm0 7.6-8-4.4L2 17.2 12 22.7l10-5.5-2-1.1-8 4.4z"/></svg> Open in slicer</button>` : ""}
                ${cad ? `<button class="lf-btn" data-james="${esc(f.name)}" title="Edit or remix it in 3D James (saves go next to it in your library)"><svg viewBox="0 0 24 24"><path fill="currentColor" d="M3 17.2V21h3.8l11-11-3.8-3.8-11 11zM20.7 7a1 1 0 0 0 0-1.4l-2.3-2.3a1 1 0 0 0-1.4 0l-1.8 1.8 3.8 3.8L20.7 7z"/></svg> ${f.kind === "3dj" ? "Edit in 3D James" : "Open in 3D James"}</button>` : ""}
                ${printable(f.name) ? `<button class="lf-btn" data-view="${esc(f.name)}" title="Show it in the 3D view above"><svg viewBox="0 0 24 24"><path fill="currentColor" d="M12 5c-5 0-9 4.5-10 7 1 2.5 5 7 10 7s9-4.5 10-7c-1-2.5-5-7-10-7zm0 11a4 4 0 1 1 0-8 4 4 0 0 1 0 8zm0-2a2 2 0 1 0 0-4 2 2 0 0 0 0 4z"/></svg> View</button>` : ""}
                <a class="lf-btn" href="/api/library/${it.id}/raw/file?name=${enc2(f.name)}" download title="Download this file"><svg viewBox="0 0 24 24"><path fill="currentColor" d="M11 3h2v9.2l3.3-3.3 1.4 1.4L12 16l-5.7-5.7 1.4-1.4 3.3 3.3V3zM4 18h16v2H4z"/></svg> Download</a>
                ${managed ? `<button class="lf-btn danger" data-rmfile="${esc(f.name)}" title="Delete this file"><svg viewBox="0 0 24 24"><path fill="currentColor" d="M18.3 5.7 12 12l6.3 6.3-1.4 1.4L10.6 13.4 4.3 19.7 2.9 18.3 9.2 12 2.9 5.7l1.4-1.4 6.3 6.3 6.3-6.3z"/></svg> Delete</button>` : ""}
              </div>
            </div>
          </div>`;
        }
function fileTree(it, labels, managed) {
  const root = { dirs: {}, files: [] };
  for (const f of it.files) {
    const parts = f.name.split("/");
    let n = root;
    for (const d of parts.slice(0, -1)) n = n.dirs[d] || (n.dirs[d] = { dirs: {}, files: [] });
    n.files.push(f);
  }
  const count = (n) => n.files.length + Object.values(n.dirs).reduce((k, d) => k + count(d), 0);
  const open = (lib.openDirs[it.id] = lib.openDirs[it.id] || {});
  // only one folder and nothing beside it (a "files" folder from a download): open it
  const keys = Object.keys(root.dirs);
  if (!root.files.length && keys.length === 1 && open[keys[0]] === undefined) open[keys[0]] = true;
  const draw = (n, path) => {
    const files = n.files.map((f) => fileRow(it, f, labels, managed)).join("");
    const dirs = Object.keys(n.dirs).sort((x, y) => x.localeCompare(y, undefined, { numeric: true })).map((d) => {
      const p = path ? path + "/" + d : d, sub = n.dirs[d], c = count(sub), isOpen = !!open[p];
      return `<div class="lf-dir ${isOpen ? "open" : ""}">
        <div class="lf-dir-row" data-dir="${esc(p)}" title="${isOpen ? "Close" : "Open"} ${esc(d)}">
          <span class="lf-dir-ic">${folderIcon}</span><b>${esc(d)}</b>
          <button class="lf-btn" data-senddir="${esc(p)}" title="Put every STL/OBJ in this folder on a plate of a project">Send folder to project</button>
          <span class="muted lf-dir-n">${c} file${c === 1 ? "" : "s"}</span><span class="lf-dir-caret">▸</span></div>
        ${isOpen ? `<div class="lf-dir-body">${draw(sub, p)}</div>` : ""}</div>`;
    }).join("");
    return files + dirs;
  };
  return draw(root, "");
}

// Choose: new project, or a plate of an existing STL project.
async function printDialog(it, files, opts = {}) {
  if (!files.length) return;
  const is3mf = files.length === 1 && (/\.3mf$/i.test(files[0]) || !!opts.as3mf);
  const labels = partLabels(it.files);
  const what = files.length === 1 ? (labels[files[0]] || {}).label || files[0] : `${files.length} parts`;
  const all = is3mf ? [] : await api("/api/projects").catch(() => []);
  const projects = all.filter((p) => p.kind === "mesh");
  const body = $("#dlg-body");
  body.innerHTML = `<h2>${is3mf ? "Open as a project" : "Send to a project"}</h2>
    <p class="muted">${esc(what)} <span class="small">from ${esc(it.title)}</span></p>
    <div class="sp-list">
      <label class="sp-opt"><input type="radio" name="sp" value="" checked>
        <span><b>New project</b><small class="muted">${is3mf ? "Keeps the 3MF's plates, colors and settings." : "Starts a project with this on plate 1."}</small>
          <input class="sp-name" id="sp-name" value="${esc(it.title)}" placeholder="Project name"></span></label>
      ${projects.map((p) => `<label class="sp-opt"><input type="radio" name="sp" value="${esc(p.id)}">
        <span><b>${esc(p.name)}</b><small class="muted">${p.data.plates.length} plate${p.data.plates.length === 1 ? "" : "s"}</small>
          <select class="sp-plate" data-plates="${esc(p.id)}">${(() => {
            // An empty plate is the natural place; otherwise a new plate.
            const empty = p.data.plates.find((pl) => !(pl.parts || []).length);
            return `<option value="0" ${empty ? "" : "selected"}>New plate</option>` + p.data.plates.map((pl) => {
              const n = (pl.parts || []).length;
              return `<option value="${pl.index}" ${empty && pl.index === empty.index ? "selected" : ""}>Add to ${esc(pl.name || "Plate " + pl.index)} (${n ? n + " part" + (n === 1 ? "" : "s") : "empty"})</option>`;
            }).join("");
          })()}</select></span></label>`).join("")}
    </div>
    ${!is3mf && all.length > projects.length ? `<p class="small muted">Projects opened from a 3MF aren't listed: they keep that file exactly as it is, so parts can't be added to them.</p>` : ""}
    <div class="dlg-actions"><button value="cancel">Cancel</button><button type="button" class="primary" id="pd-go">${is3mf ? "Open project" : "Send"}</button></div>`;
  // Picking a plate also picks its project.
  body.querySelectorAll(".sp-plate, .sp-name").forEach((x) => (x.onfocus = () => { x.closest(".sp-opt").querySelector("input[type=radio]").checked = true; }));
  $("#pd-go").onclick = async () => {
    const pid = body.querySelector('[name="sp"]:checked').value;
    const plate = pid ? +body.querySelector(`[data-plates="${CSS.escape(pid)}"]`).value : 0;
    const go = $("#pd-go");
    go.disabled = true; go.textContent = "Working…";
    try {
      const send = () => api(`/api/library/${it.id}/print`, { json: { files, project_id: pid, plate, as_3mf: is3mf } });
      let p;
      try { p = await send(); } catch (e) {
        if (!/built by 3D James/.test(e.message)) throw e;
        // A 3D James project: 3D James builds the STL/3MF on the fly, then go.
        for (const f of files.filter((n) => /\.3dj$/i.test(n))) {
          go.textContent = "3D James is building it…";
          await makePrintable(it, f, is3mf ? "3mf" : "stl");
        }
        go.textContent = "Working…";
        p = await send();
      }
      const name = $("#sp-name").value.trim();
      if (!pid && name && name !== p.name) p = await api(`/api/projects/${p.id}/rename`, { json: { name } }).catch(() => p);
      $("#dlg").close();
      toast(pid ? `Added to ${p.name}.` : `Project “${p.name}” created.`);
      closeItem();
      show("projects");
      openProject(p.id);
    } catch (e) { go.disabled = false; go.textContent = is3mf ? "Open project" : "Send"; toast(e.message, true); }
  };
  openDialog();
}

// Picker used from a project plate: "Add from library".
async function libraryPicker(onPick) {
  const body = $("#dlg-body");
  body.innerHTML = `<h2>Add from library</h2>
    <input id="pk-q" type="search" placeholder="Search your library…" style="margin-bottom:10px">
    <div id="pk-list" class="pk-list"></div>
    <div class="dlg-actions"><button value="cancel">Close</button></div>`;
  const draw = async () => {
    const res = await api("/api/library?limit=60&q=" + enc2($("#pk-q").value));
    const items = res.items.filter((it) => it.files.some((f) => /\.(stl|obj)$/i.test(f.name)));
    $("#pk-list").innerHTML = items.map((it) => `<div class="pk-item" data-pk="${it.id}"><img loading="lazy" src="/api/library/${it.id}/cover" alt=""><div><strong>${esc(it.title)}</strong>
      <div class="muted small">${it.files.filter((f) => /\.(stl|obj)$/i.test(f.name)).map((f) => esc(f.name)).join(", ")}</div></div></div>`).join("") || `<p class="muted">No STL/OBJ models found.</p>`;
  };
  let t;
  $("#pk-q").oninput = () => { clearTimeout(t); t = setTimeout(draw, 200); };
  $("#pk-list").onclick = (e) => { const c = e.target.closest("[data-pk]"); if (c) onPick(c.dataset.pk); };
  await draw();
  openDialog();
}


// ---------- clear names for a model's files ----------
// "MMM_Caterpillar_Connector_Head_v01.stl" → "Connector Head" (v01): split
// the name into words, pull out the version, and drop the leading words most
// of the files share (designer tags, the model's name).
function nameWords(base) {
  return base.replace(/([a-z])([A-Z])/g, "$1 $2").split(/[\s_\-.,()+[\]]+/).filter(Boolean);
}
const VERSION = /^(v|ver|rev|version)?\d+([._]\d+)*$/i;
function partLabels(files) {
  const rows = files.map((f) => {
    const file = f.name.split("/").pop();
    const base = file.replace(/\.[^.]+$/, "");
    let words = nameWords(base), version = "";
    words = words.filter((w) => {
      if (/^(v|ver|rev|version)\d+([._]\d+)*$/i.test(w)) { version = w.toLowerCase().startsWith("v") ? w : "v" + w; return false; }
      return true;
    });
    return { name: f.name, file, words, version };
  });
  // Drop shared leading words (MMM, Caterpillar...) while most files start with them.
  const need = Math.max(2, Math.ceil(rows.length / 2));
  for (let guard = 0; guard < 6 && rows.length > 1; guard++) {
    const counts = {};
    for (const r of rows) if (r.words.length > 1) { const w = r.words[0].toLowerCase(); counts[w] = (counts[w] || 0) + 1; }
    const [top, n] = Object.entries(counts).sort((a, b) => b[1] - a[1])[0] || [];
    if (!top || n < need) break;
    for (const r of rows) if (r.words.length > 1 && r.words[0].toLowerCase() === top) r.words.shift();
  }
  const nice = (w) => (/^[a-z]/.test(w) && w.length > 1 && !/\d/.test(w) ? w[0].toUpperCase() + w.slice(1) : w);
  const out = {};
  for (const r of rows) {
    const label = r.words.map(nice).join(" ") || r.file;
    out[r.name] = { label, version: r.version, file: r.file };
  }
  return out;
}

function shortTitle(t) {
  return (String(t || "Model").split(/\s*[,:|(–—]\s*|\s+-\s+/)[0] || "Model").trim().slice(0, 40);
}

function renameDialog(it, labels) {
  const head = shortTitle(it.title);
  const body = $("#dlg-body");
  const suggest = (f) => {
    const l = labels[f.name];
    const ext = l.file.match(/\.[^.]+$/)?.[0] || "";
    const part = l.label.toLowerCase() === head.toLowerCase() ? "" : l.label;
    return (part ? `${head} - ${part}` : head) + (l.version ? ` ${l.version}` : "") + ext;
  };
  body.innerHTML = `<h2>Tidy file names</h2>
    <p class="muted small">New names for the files of ${esc(it.title)}. Edit any of them, or clear a box to keep that file's name.</p>
    <div class="rn-list">${it.files.map((f, i) => `
      <div class="rn-row"><small class="muted" title="${esc(f.name)}">${esc(labels[f.name].file)}</small>
        <input data-rn="${i}" value="${esc(suggest(f))}"></div>`).join("")}</div>
    <div class="dlg-actions"><button value="cancel">Cancel</button><button type="button" class="primary" id="rn-go">Rename</button></div>`;
  $("#rn-go").onclick = async () => {
    const names = {};
    body.querySelectorAll("[data-rn]").forEach((inp) => {
      const f = it.files[+inp.dataset.rn];
      const v = inp.value.trim();
      if (v && v !== labels[f.name].file) names[f.name] = v;
    });
    if (!Object.keys(names).length) { $("#dlg").close(); return; }
    try {
      await api(`/api/library/${it.id}/rename`, { json: { names } });
      $("#dlg").close();
      toast("Files renamed.");
      openItem(it.id);
    } catch (e) { toast(e.message, true); }
  };
  openDialog();
}


// Has 3D James (hidden, with its own storage so your open work is safe)
// build the STL/3MF of a .3dj project for printing. Nothing is kept.
function makePrintable(it, name, format) {
  return new Promise((resolve, reject) => {
    const job = Math.random().toString(36).slice(2);
    const h = new URLSearchParams({ url: `/api/library/${it.id}/raw/file?name=${encodeURIComponent(name)}`, name: name.split("/").pop(), format,
      dest: `/api/library/${it.id}/printable?name=${encodeURIComponent(name)}&format=${format}`, job });
    const f = document.createElement("iframe");
    f.className = "james-converter";
    f.setAttribute("aria-hidden", "true");
    const end = (err) => { clearTimeout(t); window.removeEventListener("message", on); f.remove(); err ? reject(err) : resolve(); };
    const on = (e) => {
      if (e.origin !== location.origin || !e.data || e.data.ph !== "3dj-converted" || e.data.job !== job) return;
      e.data.ok ? end() : end(new Error("3D James couldn't build it: " + (e.data.error || "unknown problem") + ". Open it in 3D James to check it."));
    };
    const t = setTimeout(() => end(new Error("3D James took too long to build it.")), 180000);
    window.addEventListener("message", on);
    f.src = "/3djames.html?convert=1#" + h.toString();
    document.body.appendChild(f);
  });
}

// Category / tag quick picks under the details form (model page and Add a model).
document.addEventListener("click", (e) => {
  const c = e.target.closest("[data-pickcat],[data-picktag]");
  if (!c) return;
  const form = c.closest("#lib-meta, .dialog-body, #dlg-body") || document;
  if (c.dataset.pickcat !== undefined) {
    const inp = form.querySelector('[name="category"]');
    if (inp) { inp.value = c.dataset.pickcat; inp.dispatchEvent(new Event("input", { bubbles: true })); }
  } else {
    const inp = form.querySelector('[name="tags"]');
    if (inp) {
      const have = inp.value.split(",").map((x) => x.trim()).filter(Boolean);
      if (!have.some((x) => x.toLowerCase() === c.dataset.picktag)) have.push(c.dataset.picktag);
      inp.value = have.join(", ");
    }
  }
});
