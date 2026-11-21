// PrintHarbor web UI. Plain JS, no build step.
//
// The page is a handful of plain scripts that share one global scope, loaded in
// this order by index.html (each only needs what is above it at load time):
//
//   viewer.js        the 3D model viewer
//   app.js           this file: helpers, navigation, the top bar, polling, start-up
//   projects.js      Projects page: the list, one project and its plates
//   send.js          "Slice & send": picking presets and filament slots for a plate
//   printer-list.js  Printers page: the cards, cameras, add / edit a printer
//   queue.js         Queue page
//   frames.js        3D James and the slicer, which run in their own frames
//   library.js       Library page             sites.js   model sites
//   settings.js      Settings page            gcode.js   G-code preview
//   assistant.js     the assistant            printer.js one printer's dashboard
//   ai.js            AI failure detection
"use strict";

// The PrintHarbor desktop app opens the UI as ?app=1; remember it (the flag has
// to survive navigating to a model site and back), so the Library shows model
// sites in the window itself instead of a new tab.
try {
  if (new URLSearchParams(location.search).get("app") === "1") localStorage.setItem("ph-app", "1");
  window.__phApp = localStorage.getItem("ph-app") === "1";
} catch (_) { window.__phApp = /[?&]app=1/.test(location.search); }

const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const enc = encodeURIComponent;

async function api(path, opts = {}) {
  const o = { ...opts };
  if (o.json !== undefined) {
    o.method = o.method || "POST";
    o.headers = { "Content-Type": "application/json" };
    o.body = JSON.stringify(o.json);
  }
  const r = await fetch(path, o);
  const ct = r.headers.get("content-type") || "";
  const data = ct.includes("json") ? await r.json() : await r.text();
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}

function toast(msg, bad) {
  const b = $("#banner");
  b.textContent = msg;
  b.classList.remove("hidden");
  b.classList.toggle("bad", !!bad);
  clearTimeout(toast.t);
  toast.t = setTimeout(() => b.classList.add("hidden"), 6000);
}

// Small helpers every page uses.
// setHTML only touches the page when the HTML actually changed (the pages are redrawn every few seconds).
function setHTML(el, html) { if (el._html !== html) { el.innerHTML = html; el._html = html; } }
// dur: seconds as "1h 5m".
function dur(sec) { const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60); return h ? `${h}h ${m}m` : `${m}m`; }
// openDialog shows the shared dialog (#dlg); its content is filled in first by whoever opens it.
function openDialog() { const d = $("#dlg"); if (!d.open) d.showModal(); }

const state = { view: "projects", printers: [], jobs: [], drivers: [], catalog: [], vendors: {}, info: {}, project: null };
const LEVEL = { ready: "Ready", needs_action: "Needs action", busy: "Busy", cannot: "Can't print" };
const STATE = { idle: "Ready", printing: "Printing", paused: "Paused", finished: "Finished: clear the bed", error: "Error", offline: "Offline" };

// ---------- navigation ----------
document.querySelectorAll("header [data-view]").forEach((b) =>
  b.addEventListener("click", () => show(b.dataset.view)));
$("#brand").addEventListener("click", () => show("printer"));

function show(view) {
  state.view = view;
  document.querySelectorAll("header [data-view]").forEach((b) => b.classList.toggle("active", b.dataset.view === view || (view === "printer" && b.dataset.view === "printers")));
  for (const v of ["projects", "library", "printers", "printer", "slicer", "design", "queue", "settings", "filament"]) $("#view-" + v).classList.toggle("hidden", v !== view);
  document.body.classList.toggle("wide", view === "printer" || view === "design" || view === "slicer");
  document.body.classList.toggle("design", view === "design" || view === "slicer");
  document.body.classList.toggle("pmview", view === "projects" && !!state.project);
  topbarMode();
  // The printer picker only matters on printer/project/queue pages; hide it elsewhere.
  const pvt = $("#pv-tabs");
  if (pvt) pvt.classList.toggle("hidden", ["library", "settings", "design", "slicer"].includes(view));
  if (view === "design") openDesign();
  if (view === "slicer") openSlicer();
  if (view !== "printer" && typeof pvStopCamera === "function") pvStopCamera();
  render();
}

// ---------- top bar: hides itself on the full-screen pages (3D James, slicer, a project) ----------
// A thin strip stays at the top: point at it (or tap "PrintHarbor ▾") and the bar slides down over
// the page; it slides away again when the pointer leaves it. The pin keeps it shown.
const topPinned = () => { try { return localStorage.getItem("ph-top-pin") === "1"; } catch (_) { return false; } };
function topbarMode() {
  const full = ["design", "slicer"].includes(state.view) || (state.view === "projects" && !!state.project);
  const auto = full && !topPinned();
  document.body.classList.toggle("autohide", auto);
  if (!auto) document.body.classList.remove("top-show");
  $("#top-pin").classList.toggle("hidden", !full);
  $("#top-pin").classList.toggle("active", topPinned());
  const hd = document.querySelector("header.topbar");
  document.documentElement.style.setProperty("--topbar-h", auto ? "10px" : hd.offsetHeight + "px");
}
let topHideT = 0;
const touchOnly = matchMedia("(hover: none)").matches; // phones / tablets: no pointer to leave the bar
function topShow(on, now = false) {
  clearTimeout(topHideT);
  if (on) {
    document.body.classList.add("top-show");
    if (touchOnly) topHideT = setTimeout(() => topShow(false, true), 6000); // tapped open: closes itself
    return;
  }
  if (now) { if (!$("#pv-tabs").classList.contains("open")) document.body.classList.remove("top-show"); return; }
  topHideT = setTimeout(() => {
    const menuOpen = $("#pv-tabs").classList.contains("open");
    if (!menuOpen && !document.querySelector("header.topbar:hover")) document.body.classList.remove("top-show");
  }, 450);
}
if (!touchOnly) {
  $("#top-handle").addEventListener("mouseenter", () => topShow(true));
  $("#top-handle").addEventListener("mouseleave", () => topShow(false)); // the bar's own mouseenter keeps it
  document.querySelector("header.topbar").addEventListener("mouseenter", () => { if (document.body.classList.contains("autohide")) topShow(true); });
  document.querySelector("header.topbar").addEventListener("mouseleave", () => { if (document.body.classList.contains("autohide")) topShow(false); });
}
$("#top-handle").addEventListener("click", () => document.body.classList.contains("top-show") ? topShow(false, true) : topShow(true));
document.querySelector("header.topbar").addEventListener("pointerdown", () => { if (touchOnly && document.body.classList.contains("top-show")) topShow(true); }); // still using it
document.querySelector("header.topbar").addEventListener("click", (e) => { if (e.target.closest("[data-view]") && document.body.classList.contains("autohide")) setTimeout(() => topShow(false, true), 50); });
document.addEventListener("pointerdown", (e) => { if (document.body.classList.contains("top-show") && !e.target.closest("header.topbar, #top-handle")) topShow(false, true); });
$("#top-pin").addEventListener("click", () => {
  try { localStorage.setItem("ph-top-pin", topPinned() ? "0" : "1"); } catch (_) { /* private mode */ }
  topbarMode();
  toast(topPinned() ? "The bar stays shown." : "The bar hides on full-screen pages: point at the top edge to show it.");
});
window.addEventListener("resize", () => topbarMode());

function render() {
  renderPvTabs();
  if (state.view === "printers") renderPrinters();
  if (state.view === "printer") renderPrinterPage();
  if (state.view === "library") renderLibrary();
  if (state.view === "settings") { renderSettings(); if (typeof renderAISettings === "function") renderAISettings(); }
  if (state.view === "queue") renderJobs();
  if (state.view === "filament" && typeof renderFilament === "function") renderFilament();
  if (state.view === "projects") state.project ? renderProject() : renderProjects();
  const active = state.jobs.filter((j) => ["queued", "slicing", "sending"].includes(j.state)).length;
  const qc = $("#queue-count");
  qc.textContent = active;
  qc.classList.toggle("hidden", !active);
}

// ---------- polling ----------
async function refresh() {
  try {
    [state.printers, state.jobs] = await Promise.all([api("/api/printers"), api("/api/jobs")]);
    if (typeof checkPreviewJob === "function") checkPreviewJob(); // "Slice & preview": open it when it is sliced
    if (state.view === "projects" && state.project) state.progress = await api(`/api/projects/${state.project.id}/progress`);
    const al = await api(`/api/alerts?since=${state.alertSince || 0}`);
    if (al.length) {
      if (state.alertSince !== undefined) for (const a of al) toast(`${a.printer_name ? a.printer_name + ": " : ""}${a.text}`, a.level === "error");
      state.alerts = [...(state.alerts || []), ...al].slice(-50);
      state.alertSince = al[al.length - 1].id;
    } else if (state.alertSince === undefined) state.alertSince = 0;
    if (state.view !== "projects" || !$("#dlg").open) render();
  } catch (e) { /* server restarting; try again next tick */ }
}

async function boot() {
  // Running inside the PrintHarbor phone app (it sets this user-agent): mark the
  // page so it can adapt for the phone, the same way __phApp marks the desktop
  // window. The app also watches the printers and sends notifications natively.
  if (/PrintHarborApp/.test(navigator.userAgent)) {
    window.__phApp = true;
    document.documentElement.classList.add("in-app", "mobile-app");
  }
  [state.info, state.drivers, state.catalog, state.vendors] = await Promise.all([
    api("/api/info"), api("/api/drivers"), api("/api/profiles/catalog"), api("/api/profiles/vendors"),
  ]);
  const i = state.info;
  if (!i.orca_found) toast(`OrcaSlicer wasn't found at ${i.orca_path}. Set orca_path in config.json to slice.`, true);
  else if (!i.printer_profiles) toast(`No Orca printer profiles found in ${i.profiles_dir}. Set orca_profiles_dir in config.json.`, true);
  await refresh();
  // Deep link: the desktop app's Back / Library buttons navigate here with a
  // #view hash (e.g. #library) so the window opens on that page. Otherwise land
  // on the Library - it's where a print usually starts.
  const deep = (location.hash.match(/^#(library|projects|printers|printer|queue|design|settings|filament)\b/) || [])[1];
  if (deep) history.replaceState(null, "", location.pathname + location.search);
  show(deep || "library");
  setInterval(refresh, 3000);
  if (typeof setupAssistant === "function") setupAssistant();
  appWindowCheck();
  setInterval(() => checkForUpdate(), 15000);
}

// A page left open while PrintHarbor was updated (restarted from the tray icon,
// or from another device) is still running the old version's code. When nothing
// can be lost it reloads by itself; when 3D James, the slicer, a project or a
// dialog is open it says so instead and you choose the moment.
async function checkForUpdate(reload = () => location.reload()) {
  let i;
  try { i = await fetch("/api/info", { cache: "no-store" }).then((r) => r.json()); } catch (_) { return; } // restarting right now
  if (!i.build || !state.info.build || i.build === state.info.build || $("#ph-updated")) return;
  const open = $("#dlg").open || !!state.project || !!$("#design-frame").getAttribute("src") || !!$("#slicer-frame").getAttribute("src");
  if (!open) { reload(); return; }
  const n = document.createElement("div");
  n.id = "ph-updated";
  n.className = "banner";
  n.style.cssText = "bottom:auto;top:10px;z-index:101";
  n.innerHTML = `PrintHarbor was updated${i.build_number && i.build_number !== state.info.build_number ? " to build " + esc(i.build_number) : (i.version && i.version !== state.info.version ? " to " + esc(i.version) : "")}. Save what you have open, then <button class="primary" style="margin-left:8px">Reload</button>`;
  n.querySelector("button").onclick = () => reload();
  document.body.appendChild(n);
}

// The desktop app window is its own program (printharbor-app.exe). After an
// update it reopens itself; if this window is still an older one a little
// while after the page loaded, it couldn't, so say what to do. window.__phAppBuild
// is set by the window itself; app_build is the build this server came with.
function appWindowCheck() {
  if (!window.__phApp) return;
  setTimeout(() => {
    if ((window.__phAppBuild || 0) >= (state.info.app_build || 0) || $("#app-outdated")) return;
    const n = document.createElement("div");
    n.id = "app-outdated";
    n.className = "banner";
    n.style.cssText = "bottom:auto;top:10px;z-index:101;cursor:pointer";
    n.title = "Click to hide this";
    n.textContent = "This PrintHarbor window was updated too. Close this window and open PrintHarbor again to finish the update.";
    n.onclick = () => n.remove();
    document.body.appendChild(n);
  }, 20000);
}

// Start only once every script on the page has loaded: boot() calls into all
// of them (the library, the printer page, ...). Started any earlier, a fast
// answer from the server could arrive before the later files had loaded and
// the page came up dead.
function start() { boot().catch((e) => toast("Couldn't reach PrintHarbor: " + e.message, true)); }
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
else start();
