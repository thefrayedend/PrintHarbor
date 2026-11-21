// Printer control page. Same layout and look as the FlashBridge printer
// screen: camera first with status chips on it, the job card under it, and
// Filament / Temperatures / Controls cards on the side. PrintHarbor adds Move,
// a temperature chart and a console in the same style, for printers whose
// driver supports them.
"use strict";

const pv = { id: "", camNext: 0, camBusy: false, camFails: 0, hist: [], histAt: 0, con: [], conAt: 0, thumbKey: "", thumb: "", step: 10, tempKey: "" };

function openPrinter(id) {
  pv.id = id;
  pv.hist = []; pv.histAt = 0; pv.con = []; pv.conAt = 0; pv.thumbKey = "";
  pvStopCamera();
  $("#pv-console-log").innerHTML = "";
  show("printer");
  window.scrollTo(0, 0);
}

function pvCurrent() { return state.printers.find((p) => p.id === pv.id) || state.printers[0]; }
const pvBusy = (s) => s.state === "printing" || s.state === "paused";

// [dot class, short label]: FlashBridge's jobStatus().
function pvStatus(p) {
  const s = p.status;
  switch (s.state) {
    case "offline": return ["err", "Offline"];
    case "error": return ["err", "Error"];
    case "printing": return ["busy", `Printing ${Math.round(s.progress * 100)}%`];
    case "paused": return ["warn", "Paused"];
    case "finished": return ["on", "Finished"];
  }
  return ["on", "Ready"];
}

function pvTime(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60);
  return h ? `${h}h ${m}m` : m ? `${m}m` : `${sec}s`;
}
const pvName = (f) => String(f || "").split("/").pop().replace(/\.(gcode|bgcode|3mf|gco|g)$/i, "").replace(/\.gcode$/i, "");

async function pvSend(body) {
  try { await api(`/api/printers/${enc(pv.id)}/control`, { json: body }); }
  catch (e) { toast(e.message || "The printer did not accept that.", true); }
  setTimeout(refresh, 600);
}
async function pvAction(action) {
  try { await api(`/api/printers/${enc(pv.id)}/action`, { json: { action } }); }
  catch (e) { toast(e.message, true); }
  setTimeout(refresh, 600);
}

function renderPrinterPage() {
  const p = pvCurrent();
  $("#pv-none").classList.toggle("hidden", !!p);
  $("#pv-view").classList.toggle("hidden", !p);
  if (!p) return;
  pv.id = p.id;
  pvFitHeight();
  const [k, label] = pvStatus(p);
  setHTML($("#pv-state-chip"), `<span class="dot ${k}"></span>${esc(label)}`);
  if (typeof aiDashboard === "function") aiDashboard(p);
  const msg = p.status.message && (p.status.state === "error" || p.status.state === "offline") ? p.status.message : "";
  $("#pv-err").classList.toggle("hidden", !msg);
  if (msg) $("#pv-err-text").textContent = msg;
  pvCamera(p);
  pvJob(p);
  pvSlots(p);
  pvTemps(p);
  pvControls(p);
  pvMove(p);
  pvChart(p);
  pvConsole(p);
}

// ---------------------------------------------------------------- printer menu
// The printer pill in the top bar (like FlashBridge's) opens a menu of every
// printer, so any number of printers fit.
function renderPvTabs() {
  const nav = $("#pv-tabs");
  if (!state.printers.length) { if (nav._html) { nav._html = ""; nav.innerHTML = ""; } return; }
  const cur = pvCurrent();
  const [k, t] = pvStatus(cur);
  const open = nav.classList.contains("open");
  const html = `<button class="pm-btn active" aria-haspopup="true" aria-expanded="${open}"><span class="dot ${k}"></span><span class="pm-name">${esc(cur.name)}</span> <span class="st">${esc(t)}</span><svg class="pm-caret" viewBox="0 0 24 24" width="16" height="16"><path fill="currentColor" d="M7 10l5 5 5-5z"/></svg></button>
    <div class="pm-menu" role="menu">${state.printers.map((p) => { const [pk, pt] = pvStatus(p); return `<button role="menuitem" data-p="${esc(p.id)}" class="${p.id === cur.id ? "sel" : ""}"><span class="dot ${pk}"></span><span class="pm-name">${esc(p.name)}</span><span class="st">${esc(pt)}</span></button>`; }).join("")}<button role="menuitem" data-allp="1" class="pm-all"><span class="dot"></span><span class="pm-name">All printers</span><span class="st">list, add, camera wall</span></button></div>`;
  if (nav._html !== html) { nav._html = html; nav.innerHTML = html; }
}
$("#pv-tabs").addEventListener("click", (e) => {
  const nav = $("#pv-tabs");
  if (e.target.closest(".pm-menu [data-allp]")) { nav.classList.remove("open"); nav._html = ""; renderPvTabs(); show("printers"); return; }
  const item = e.target.closest(".pm-menu [data-p]");
  if (item) { nav.classList.remove("open"); nav._html = ""; openPrinter(item.dataset.p); return; }
  if (e.target.closest(".pm-btn")) {
    nav.classList.toggle("open"); nav._html = ""; renderPvTabs();
  }
});
document.addEventListener("click", (e) => { const nav = $("#pv-tabs"); if (nav.classList.contains("open") && e.target.isConnected && !e.target.closest("#pv-tabs")) { nav.classList.remove("open"); nav._html = ""; renderPvTabs(); } });
document.addEventListener("keydown", (e) => { if (e.key === "Escape") { const nav = $("#pv-tabs"); if (nav.classList.contains("open")) { nav.classList.remove("open"); nav._html = ""; renderPvTabs(); } } });

// ---------------------------------------------------------------- camera
// Live video like FlashBridge: the <img> plays PrintHarbor's MJPEG stream for
// the printer, and the box takes the picture's real shape (no black bars).
function pvCamera(p, force) {
  const img = $("#pv-cam-img"), empty = $("#pv-cam-empty");
  const offline = p.status.state === "offline";
  const want = !offline && p.status.stream_url && !document.hidden && state.view === "printer" ? p.status.stream_url : "";
  if (!want) {
    pvStopCamera();
    empty.classList.remove("hidden");
    $("#pv-cam-empty-text").textContent = offline ? "Printer offline" : "This printer has no camera";
    $("#pv-cam-retry").classList.add("hidden");
    $("#pv-cam-hint").classList.toggle("hidden", offline);
    return;
  }
  if (!force && img.dataset.src === want) return;
  if (img.dataset.shapeFor !== p.id) { img.dataset.shapeFor = p.id; pvCamShapeFor(p.id); }
  img.dataset.src = want;
  img.src = want + "?t=" + Date.now();
  img.classList.remove("hidden");
  empty.classList.add("hidden");
}
function pvStopCamera() { const img = $("#pv-cam-img"); img.removeAttribute("src"); img.dataset.src = ""; img.classList.add("hidden"); }
function pvCamShape(ar) {
  if (!(ar > 0.3 && ar < 4)) return;
  document.documentElement.style.setProperty("--cam-ar", ar.toFixed(4));
}
// Each camera's shape is remembered, so the box is right before the first frame arrives.
function pvCamShapeFor(id) {
  let ar = NaN;
  try { ar = parseFloat(localStorage.getItem("ph-cam-ar:" + id)); } catch { /* no storage */ }
  pvCamShape(ar > 0 ? ar : 16 / 9);
}
// Camera streams don't reliably fire "load": check the picture size a few times a second.
setInterval(() => {
  const img = $("#pv-cam-img");
  if (!img.dataset.src || !img.naturalWidth || !img.naturalHeight) return;
  const ar = img.naturalWidth / img.naturalHeight;
  if (document.documentElement.style.getPropertyValue("--cam-ar") === ar.toFixed(4)) return;
  pvCamShape(ar);
  try { localStorage.setItem("ph-cam-ar:" + pv.id, ar.toFixed(4)); } catch { /* no storage */ }
}, 400);
$("#pv-cam-img").addEventListener("error", () => {
  const img = $("#pv-cam-img");
  if (!img.dataset.src) return;
  const src = img.dataset.src;
  // Say why, when the server knows (a busy FlashForge camera, for example).
  const ac = new AbortController();
  setTimeout(() => ac.abort(), 15000);
  fetch(src + "?t=" + Date.now(), { signal: ac.signal }).then(async (r) => { if (r.ok) { ac.abort(); return; } { const t = (await r.text()).trim(); if (t && !img.dataset.src) $("#pv-cam-empty-text").textContent = t.length > 200 ? "Camera not reachable" : t.charAt(0).toUpperCase() + t.slice(1); } }).catch(() => {});
  img.dataset.src = "";
  img.classList.add("hidden");
  $("#pv-cam-empty").classList.remove("hidden");
  $("#pv-cam-empty-text").textContent = "Camera not reachable";
  $("#pv-cam-retry").classList.remove("hidden");
  $("#pv-cam-hint").classList.add("hidden");
});
$("#pv-cam-retry").onclick = () => { const p = pvCurrent(); if (p) pvCamera(p, true); };
document.addEventListener("visibilitychange", () => { const p = pvCurrent(); if (p && state.view === "printer") pvCamera(p); else pvStopCamera(); });
$("#pv-fullscreen").onclick = () => {
  const c = $("#pv-camera"), img = $("#pv-cam-img");
  if (document.fullscreenElement) document.exitFullscreen();
  else if (c.requestFullscreen) c.requestFullscreen();
  else if (img.webkitEnterFullscreen) img.webkitEnterFullscreen();
};

// ---------------------------------------------------------------- job card
async function pvThumb(p) {
  // A print PrintHarbor sent shows its plate picture, like FlashBridge's job thumbnail.
  const key = p.id + "|" + (p.status.job_name || "");
  if (pv.thumbKey === key) return pv.thumb;
  pv.thumbKey = key; pv.thumb = "";
  if (!p.status.job_name) return "";
  // FlashForge printers: the printer's own picture of the file.
  if (p.status.ff && p.status.ff.has_thumb) { pv.thumb = `/api/printers/${enc(p.id)}/thumb?k=${enc(p.status.job_name)}`; return pv.thumb; }
  try {
    const runs = await api(`/api/runs?printer=${enc(p.id)}&limit=3`);
    const r = runs.find((x) => x.state === "printing" || x.state === "printed");
    if (r && pv.thumbKey === key) { pv.thumb = `/api/projects/${enc(r.project_id)}/plates/${r.plate}/thumb`; pvJob(p); }
  } catch { /* no thumbnail */ }
  return pv.thumb;
}

function pvJob(p) {
  const s = p.status, c = p.capabilities;
  const active = pvBusy(s);
  const done = s.state === "finished" || (s.state === "idle" && !p.bed_clear);
  let tsrc = active || done ? (pvThumb(p), pv.thumb) : "";
  if (tsrc && tsrc === pv.thumbFailed) tsrc = "";
  const thumb = $("#pv-job-thumb");
  if (thumb.dataset.src !== tsrc) { thumb.dataset.src = tsrc; if (tsrc) thumb.src = tsrc; else thumb.removeAttribute("src"); }
  thumb.classList.toggle("hidden", !tsrc);
  $("#pv-job").classList.toggle("no-thumb", !tsrc);
  thumb.onerror = () => { pv.thumbFailed = thumb.dataset.src; thumb.classList.add("hidden"); $("#pv-job").classList.add("no-thumb"); };
  let title, sub = "", stats = "", actions = "";
  if (s.state === "offline") {
    title = "Printer offline";
    sub = s.message || "PrintHarbor can't reach it. Check that it's on and the address is right.";
    actions = `<button class="btn" data-pv="edit">Connection settings</button>`;
  } else if (active) {
    title = pvName(s.job_name) || "Printing";
    sub = s.state === "paused" ? "Paused" : "Printing";
    const eta = s.remaining_sec ? new Date(Date.now() + s.remaining_sec * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
    stats = `<span><b>${Math.round(s.progress * 100)}%</b></span>` +
      (s.total_layers ? `<span>Layer <b>${s.layer}/${s.total_layers}</b></span>` : "") +
      (s.remaining_sec ? `<span><b>${pvTime(s.remaining_sec)}</b> left</span><span>Done ~<b>${eta}</b></span>` : "") +
      (s.elapsed_sec ? `<span>${pvTime(s.elapsed_sec)} elapsed</span>` : "");
    actions = (s.state === "paused"
      ? (c.resume ? `<button class="btn primary" data-pv="resume">Resume</button>` : "")
      : (c.pause ? `<button class="btn" data-pv="pause">Pause</button>` : "")) +
      (c.cancel ? `<button class="btn ghost-danger" data-pv="cancel">Stop</button>` : "");
  } else if (done) {
    title = pvName(s.job_name) || "Print finished";
    const ff = pvIsFlashForge(p);
    // The camera marks the bed clear by itself on any printer that has a camera
    // and has been shown its empty bed. The manual button is only for printers
    // that can't do that: FlashForge (it sends the real platform-clear command),
    // printers with no camera, and camera printers not yet taught their empty bed.
    const cam = s.snapshot_url && p.has_empty_bed;
    const showClear = ff || !cam;
    sub = cam && pvAutoClearOn(p) ? "Finished — the printer's own message is cleared by itself; the camera will mark the bed clear once the parts are off."
      : pvAutoClearOn(p) ? "Finished — the printer's own message is cleared by itself. Take the print off, then press Parts removed."
      : cam ? "Finished — the camera will mark the bed clear once the parts are off."
      : ff ? "Finished — take the print off, then press Clear platform."
      : "Finished — take the parts off, then press Parts removed.";
    actions = (showClear ? `<button class="btn primary" data-pv="clear">${ff && !pvAutoClearOn(p) ? "Clear platform" : "Parts removed"}</button>` : "")
      + (c.upload || c.files ? `<button class="btn" data-pv="files">Print a file</button>` : "");
  } else if (s.state === "error") {
    title = "Printer error";
    sub = s.message || "";
    actions = c.upload || c.files ? `<button class="btn" data-pv="files">Print a file</button>` : "";
  } else {
    title = "Ready to print";
    sub = p.machine_preset || "";
    actions = c.upload || c.files ? `<button class="btn primary big" data-pv="files">Print a file</button>` : "";
  }
  $("#pv-job-title").textContent = title;
  $("#pv-job-sub").textContent = sub;
  $("#pv-job-progress").classList.toggle("hidden", !active);
  $("#pv-job-bar").style.width = (active ? s.progress * 100 : 0) + "%";
  setHTML($("#pv-job-stats"), stats);
  setHTML($("#pv-job-actions"), actions);
}

$("#pv-job-actions").addEventListener("click", async (e) => {
  const b = e.target.closest("[data-pv]");
  if (!b) return;
  const act = b.dataset.pv, p = pvCurrent();
  if (act === "files") return pvOpenFiles();
  if (act === "edit") return editPrinter(p);
  if (act === "clear") {
    try {
      const runs = await api(`/api/runs?printer=${enc(p.id)}&limit=5`);
      const pending = runs.find((r) => r.state === "printed");
      if (pending) return reviewDialog(pending, p.id);
      await api(`/api/printers/${enc(p.id)}/bed-clear`, { json: { clear: true } });
      toast("Platform cleared — the printer is ready for the next print.");
    } catch (err) { toast(err.message, true); }
    return refresh();
  }
  if (act === "cancel" && !(await pvConfirm("Stop this print?", "The print is cancelled on the printer. This cannot be undone.", "Stop print"))) return;
  pvAction(act);
});

// ---------------------------------------------------------------- filament
// Slots the printer reports (FlashForge material station) are changed on the printer; slots
// you set up yourself are kept by PrintHarbor.
function pvSlots(p) {
  const live = p.status.slots && p.status.slots.length;
  const slots = live ? p.status.slots : p.slots || [];
  const card = $("#pv-slots").closest(".card");
  card.classList.toggle("hidden", !slots.length);
  if (!slots.length) return;
  const editable = !live || !!p.capabilities.set_slots;
  $("#pv-slots-hint").textContent = editable ? "tap a slot to change it" : "";
  const cur = pvBusy(p.status) ? (p.status.current_slot || 0) : 0;
  setHTML($("#pv-slots"), slots.map((s) => `
    <button class="slot ${s.index === cur ? "current" : ""}" data-slot="${s.index}" ${editable ? "" : "disabled"}>
      ${s.type ? `<span class="swatch" style="background:${esc(s.color || "#888")}"></span>` : '<span class="swatch empty"></span>'}
      <span class="mat" title="${esc(s.name || "")}">${s.type ? esc(s.type) : "Empty"}</span>
      <span class="num">Slot ${s.index}</span>
    </button>`).join(""));
}
$("#pv-slots").addEventListener("click", (e) => { const b = e.target.closest("[data-slot]"); if (b && !b.disabled) pvOpenSlot(+b.dataset.slot); });

// FlashBridge's slot colours (the printer's own list when it gives one).
const PV_PALETTE = [["White", "#FFFFFF"], ["Yellow", "#FFF245"], ["Light Green", "#DEF578"], ["Green", "#21CC3D"], ["Dark Green", "#167A4B"], ["Teal", "#156682"],
  ["Cyan", "#24E4A0"], ["Light Blue", "#7BD9F0"], ["Blue", "#4CAAF8"], ["Dark Blue", "#2E54DD"], ["Purple", "#48358C"], ["Violet", "#A341F7"],
  ["Magenta", "#F435F6"], ["Pink", "#D5B4DE"], ["Coral", "#FA6173"], ["Red", "#F82D29"], ["Brown", "#805003"], ["Orange", "#F9903B"],
  ["Cream", "#FCEBD7"], ["Tan", "#D5C5A1"], ["Dark Brown", "#B17C38"], ["Grey", "#8C8C89"], ["Light Grey", "#BEBEBE"], ["Black", "#1B1B1B"]];
const PV_MATERIALS = ["PLA", "PLA-CF", "PETG", "PETG-CF", "ABS", "ASA", "TPU", "SILK", "PLA+", "PA", "PA-CF", "PC", "HIPS", "PVA"];
function pvColorDiff(a, b) {
  const h = (x) => [1, 3, 5].map((i) => parseInt(String(x).slice(i, i + 2), 16) || 0);
  const [r1, g1, b1] = h(a), [r2, g2, b2] = h(b);
  return (r1 - r2) ** 2 + (g1 - g2) ** 2 + (b1 - b2) ** 2;
}
let pvSlotSel = { index: 0, color: "", live: false };
function pvOpenSlot(index) {
  const p = pvCurrent();
  const live = !!(p.status.slots && p.status.slots.length);
  const s = (live ? p.status.slots : p.slots || []).find((x) => x.index === index);
  if (!s) return;
  const b = p.status.ff || {};
  const pal = b.slot_palette && b.slot_palette.length ? b.slot_palette : PV_PALETTE;
  let best = pal[0][1], bd = Infinity;
  for (const [, hex] of pal) { const d = pvColorDiff(hex, s.color || "#FFFFFF"); if (d < bd) { bd = d; best = hex; } }
  pvSlotSel = { index, color: best, live };
  $("#pv-sd-title").textContent = `Slot ${index}`;
  const mats = b.materials && b.materials.length ? b.materials : PV_MATERIALS;
  const list = s.type && !mats.includes(s.type) ? [s.type, ...mats] : mats;
  $("#pv-sd-material").innerHTML = (live ? "" : `<option value="">Empty</option>`) + list.map((m) => `<option ${m === s.type ? "selected" : ""}>${esc(m)}</option>`).join("");
  pvSlotSel.fixedPalette = !!(b.slot_palette && b.slot_palette.length); // FlashForge firmware only takes its own colours
  $("#pv-sd-palette").innerHTML = pal.map(([n, hex]) => `<button type="button" title="${esc(n)}" data-hex="${esc(hex)}" style="background:${esc(hex)}" class="${hex === best ? "sel" : ""}"></button>`).join("");
  $("#pv-sd-other").classList.toggle("hidden", pvSlotSel.fixedPalette);
  if (!pvSlotSel.fixedPalette && s.color) { pvSlotSel.color = s.color; $("#pv-sd-palette").querySelectorAll("button").forEach((x) => x.classList.toggle("sel", x.dataset.hex.toLowerCase() === s.color.toLowerCase())); }
  $("#pv-sd-color").value = /^#[0-9a-f]{6}$/i.test(pvSlotSel.color) ? pvSlotSel.color.toLowerCase() : "#ffffff";
  $("#pv-slot-dialog").showModal();
}
$("#pv-sd-palette").addEventListener("click", (e) => {
  const b = e.target.closest("[data-hex]");
  if (!b) return;
  pvSlotSel.color = b.dataset.hex;
  $("#pv-sd-color").value = b.dataset.hex.toLowerCase();
  $("#pv-sd-palette").querySelectorAll("button").forEach((x) => x.classList.toggle("sel", x === b));
});
$("#pv-sd-color").addEventListener("input", (e) => { pvSlotSel.color = e.target.value; $("#pv-sd-palette").querySelectorAll("button").forEach((x) => x.classList.remove("sel")); });
$("#pv-slot-form").addEventListener("submit", async () => {
  const p = pvCurrent();
  try {
    if (pvSlotSel.live) {
      await api(`/api/printers/${enc(p.id)}/slot`, { json: { slot: pvSlotSel.index, material: $("#pv-sd-material").value, color: pvSlotSel.color } });
    } else {
      const slots = (p.slots || []).map((s) => s.index === pvSlotSel.index ? { ...s, type: $("#pv-sd-material").value, color: pvSlotSel.color.toUpperCase() } : s);
      await api(`/api/printers/${enc(p.id)}/option`, { json: { slots } });
    }
    toast(`Slot ${pvSlotSel.index} saved.`);
  } catch (e) { toast(e.message, true); }
  refresh();
});

// ---------------------------------------------------------------- temperatures
function pvTemps(p) {
  const s = p.status, c = p.capabilities;
  const card = $("#pv-temps").closest(".card");
  card.classList.toggle("hidden", !c.temperatures);
  if (!c.temperatures) return;
  const t = [];
  // Every nozzle on multi-tool printers (Nozzle 1-4 like FlashBridge), else one.
  if (s.tools && s.tools.length > 1) s.tools.forEach((x, i) => t.push({ key: "tool" + i, label: `Nozzle ${i + 1}`, cur: x.temp_c, target: x.target_c, can: true }));
  else t.push({ key: "nozzle", label: "Nozzle", cur: s.nozzle_c, target: s.nozzle_target_c, can: true });
  t.push({ key: "bed", label: "Bed", cur: s.bed_c, target: s.bed_target_c, can: true });
  if (s.chamber_c || s.chamber_heater) t.push({ key: "chamber", label: "Chamber", cur: s.chamber_c, target: s.chamber_target_c || 0, can: !!s.chamber_heater });
  const online = (c.gcode || !!s.ff) && s.state !== "offline";
  $("#pv-temps-hint").textContent = online ? "tap to set" : "";
  setHTML($("#pv-temps"), t.map((x) => `
    <button class="temp ${x.target > 0 && x.cur < x.target - 2 ? "heating" : ""}" data-temp="${x.key}" data-label="${x.label}" data-target="${Math.round(x.target || 0)}" ${online && x.can ? "" : "disabled"}>
      <div class="t-label">${x.label}</div>
      <div class="t-val">${s.state === "offline" ? "–" : Math.round(x.cur || 0) + "°"}</div>
      <div class="t-target">${x.target > 0 ? "→ " + Math.round(x.target) + "°" : "off"}</div>
    </button>`).join(""));
}
$("#pv-temps").addEventListener("click", (e) => { const b = e.target.closest("[data-temp]"); if (b && !b.disabled) pvOpenTemp(b.dataset.temp, b.dataset.label, +b.dataset.target); });

function pvOpenTemp(key, label, target) {
  pv.tempKey = key;
  $("#pv-td-title").textContent = label;
  $("#pv-td-value").value = target || "";
  $("#pv-td-value").max = key === "bed" ? 130 : key === "chamber" ? 80 : 320;
  const presets = key === "bed" ? [["Off", 0], ["PLA", 60], ["PETG", 80], ["ABS", 100]]
    : key === "chamber" ? [["Off", 0], ["40°", 40], ["50°", 50], ["60°", 60]]
      : [["Off", 0], ["PLA", 210], ["PETG", 240], ["ABS", 260]];
  $("#pv-td-presets").innerHTML = presets.map(([n, v]) => `<button type="button" class="btn small" data-v="${v}">${n}${v && !n.endsWith("°") ? " " + v + "°" : ""}</button>`).join("");
  $("#pv-temp-dialog").showModal();
  setTimeout(() => $("#pv-td-value").select(), 30);
}
$("#pv-td-presets").addEventListener("click", (e) => { const b = e.target.closest("[data-v]"); if (b) { $("#pv-td-value").value = b.dataset.v; $("#pv-temp-form").requestSubmit(); } });
$("#pv-temp-form").addEventListener("submit", () => pvSend({ op: "temp", heater: pv.tempKey, value: Math.max(0, +$("#pv-td-value").value || 0) }));

// ---------------------------------------------------------------- controls
// FlashForge printers wait for "Clear platform" after each print, so only
// they get "Auto clear platform".
const pvIsFlashForge = (p) => p.driver === "flashforge";
const pvAutoClearOn = (p) => pvIsFlashForge(p) && !!p.clear_finished;

// The Controls card, as FlashBridge's renderControls() builds it.
function pvControls(p) {
  const s = p.status, c = p.capabilities, b = s.ff || {};
  const printing = pvBusy(s);
  const ff = pvIsFlashForge(p);
  const key = [p.id, s.state === "offline", printing, c.light, ff, !!b.has_filtration, c.speed_factor, c.z_offset, c.gcode, !!s.ff].join("|");
  const box = $("#pv-controls");
  if (box.dataset.key !== key) {
    box.dataset.key = key;
    let h = "";
    if (c.light) h += `<div class="ctl"><span class="ctl-name">Light</span><label class="switch"><input type="checkbox" data-ctl="light"><span></span></label></div>`;
    if (ff) h += `<div class="ctl"><span class="ctl-name">Clear platform<small>Tell the printer the finished print has been removed (clears the "print finished" message now)</small></span><button class="btn" data-act="clear_platform">Clear</button></div>
      <div class="ctl"><span class="ctl-name">Auto clear platform<small>Clears the "print finished" message on the printer by itself, so it will take the next job. You still say when the parts are off.</small></span><label class="switch"><input type="checkbox" data-ctl="autoclear"><span></span></label></div>`;
    if (b.has_filtration) {
      h += `<div class="ctl"><span class="ctl-name">Filtration – inside<small>Recirculates air through the filter</small></span><label class="switch"><input type="checkbox" data-ctl="fint"><span></span></label></div>
            <div class="ctl"><span class="ctl-name">Filtration – exhaust<small>Blows air out of the printer</small></span><label class="switch"><input type="checkbox" data-ctl="fext"><span></span></label></div>`;
    }
    const fans = c.gcode || !!s.ff;
    if (printing) {
      if (c.speed_factor) h += `<div class="ctl"><span class="ctl-name">Print speed</span><div class="segmented" data-ctl="speed">
              <button data-v="50">50%</button><button data-v="100">100%</button><button data-v="125">125%</button><button data-v="150">150%</button></div></div>`;
      if (c.z_offset) h += `<div class="ctl"><span class="ctl-name">Z-offset<small>Squish the first layers more (−) or less (+)</small></span>
              <div class="stepper"><button data-z="-0.01" aria-label="Lower">−</button><output data-ctl="z"></output><button data-z="0.01" aria-label="Raise">+</button></div></div>`;
      if (fans) h += `<div class="ctl"><span class="ctl-name">Part cooling fan</span><input type="range" min="0" max="100" step="5" data-ctl="cool"></div>`;
      if (s.ff) h += `<div class="ctl"><span class="ctl-name">Chamber fan</span><input type="range" min="0" max="100" step="5" data-ctl="chamberfan"></div>`;
    } else if (c.speed_factor || c.z_offset || fans) {
      h += '<p class="hint">Speed, Z-offset and fans can be changed while a print is running.</p>';
    }
    box.closest(".card").classList.toggle("hidden", !h);
    box.innerHTML = h;
  }
  const set = (sel, fn) => { const el = box.querySelector(sel); if (el && document.activeElement !== el) fn(el); };
  set("[data-ctl=light]", (el) => { el.checked = s.light === "on"; el.disabled = s.state === "offline"; });
  set("[data-ctl=autoclear]", (el) => { el.checked = pvAutoClearOn(p); });
  set("[data-ctl=fint]", (el) => { el.checked = !!b.filtration_internal; });
  set("[data-ctl=fext]", (el) => { el.checked = !!b.filtration_external; });
  const sp = Math.round((s.speed_factor || 1) * 100);
  box.querySelectorAll("[data-ctl=speed] button").forEach((x) => x.classList.toggle("active", +x.dataset.v === sp));
  set("[data-ctl=z]", (el) => { el.textContent = (s.z_offset >= 0 ? "+" : "") + Number(s.z_offset || 0).toFixed(2) + " mm"; });
  set("[data-ctl=cool]", (el) => { el.value = Math.round((s.fan || 0) * 100); });
  set("[data-ctl=chamberfan]", (el) => { el.value = Math.round(b.chamber_fan || 0); });
}
$("#pv-controls").addEventListener("change", async (e) => {
  const c = e.target.dataset.ctl, p = pvCurrent();
  if (c === "light") pvAction(e.target.checked ? "light_on" : "light_off");
  if (c === "cool") pvSend({ op: "fan", value: +e.target.value });
  if (c === "chamberfan") pvSend({ op: "chamber_fan", value: +e.target.value });
  if (c === "fint") pvSend({ op: "filtration", internal: e.target.checked });
  if (c === "fext") pvSend({ op: "filtration", external: e.target.checked });
  if (c === "autoclear") {
    if (e.target.checked && !(await pvConfirm("Clear the platform automatically?",
      "A few seconds after every print ends, the printer is told the plate is clear. Prints sent remotely could then land on a plate that still has a part on it, so take finished prints off promptly.", "Turn on"))) { e.target.checked = false; return; }
    try { await api(`/api/printers/${enc(p.id)}/option`, { json: { clear_finished: e.target.checked } }); toast(e.target.checked ? "Auto clear platform is on." : "Auto clear platform is off."); }
    catch (err) { toast(err.message, true); }
    refresh();
  }
});
$("#pv-controls").addEventListener("click", (e) => {
  const sp = e.target.closest("[data-ctl=speed] button");
  if (sp) pvSend({ op: "speed", value: +sp.dataset.v });
  const z = e.target.closest("[data-z]");
  if (z) pvSend({ op: "zoffset", distance: +z.dataset.z });
  const cl = e.target.closest("[data-act=clear_platform]");
  if (cl) { cl.disabled = true; pvAction("clear_platform"); setTimeout(() => { cl.disabled = false; }, 1500); }
});

// ---------------------------------------------------------------- move (jog / home)
function pvMove(p) {
  const s = p.status, c = p.capabilities;
  const card = $("#pv-move-card");
  const show = c.jog && s.state !== "offline";
  card.classList.toggle("hidden", !show);
  if (!show) return;
  const locked = pvBusy(s);
  const pos = s.position || [];
  const homed = (s.homed || "").toLowerCase();
  setHTML($("#pv-pos"), ["X", "Y", "Z"].map((a, i) =>
    `<span class="${homed.includes(a.toLowerCase()) ? "" : "unhomed"}" title="${homed.includes(a.toLowerCase()) ? "Homed" : "Not homed"}">${a} <b>${pos[i] !== undefined ? Number(pos[i]).toFixed(2) : "–"}</b></span>`).join(""));
  $("#pv-move-hint").textContent = locked ? "locked while printing" : homed.length === 3 ? "" : "home first";
  card.querySelectorAll("button").forEach((b) => { if (!b.closest("#pv-step")) b.disabled = locked; });
  card.querySelectorAll("#pv-step button").forEach((b) => b.classList.toggle("active", +b.dataset.v === pv.step));
}
$("#pv-move-card").addEventListener("click", async (e) => {
  const b = e.target.closest("button");
  if (!b || b.disabled) return;
  if (b.closest("#pv-step")) { pv.step = +b.dataset.v; pvMove(pvCurrent()); return; }
  if (b.dataset.home !== undefined) return pvSend({ op: "home", axis: b.dataset.home });
  if (b.dataset.jog) return pvSend({ op: "jog", axis: b.dataset.jog, distance: +b.dataset.dir * pv.step });
  if (b.dataset.motors !== undefined) {
    if (await pvConfirm("Turn the motors off?", "The printer forgets where the toolhead is. Home it again before moving it.", "Motors off")) pvSend({ op: "motors_off" });
  }
});

// ---------------------------------------------------------------- temperature chart
// One axis (°C). Nozzle = series 1 (blue), bed = series 2 (orange); targets dashed.
async function pvChart(p) {
  const card = $("#pv-chart-card");
  card.classList.toggle("hidden", !p.capabilities.temperatures || p.status.state === "offline");
  if (card.classList.contains("hidden")) return;
  if (Date.now() - pv.histAt > 2500) {
    pv.histAt = Date.now();
    try { pv.hist = await api(`/api/printers/${enc(p.id)}/history`); } catch { /* keep the old one */ }
  }
  pvDrawChart();
}
function pvDrawChart(hoverX) {
  const cv = $("#pv-chart"), h = pv.hist || [];
  const W = cv.clientWidth, H = cv.clientHeight;
  if (!W) return;
  const dpr = window.devicePixelRatio || 1;
  if (cv.width !== W * dpr) { cv.width = W * dpr; cv.height = H * dpr; }
  const g = cv.getContext("2d");
  g.setTransform(dpr, 0, 0, dpr, 0, 0);
  g.clearRect(0, 0, W, H);
  const css = getComputedStyle(cv);
  const col = { n: css.getPropertyValue("--series-1").trim(), b: css.getPropertyValue("--series-2").trim(), grid: css.getPropertyValue("--line").trim(), ink: css.getPropertyValue("--muted").trim(), text: css.getPropertyValue("--text").trim() };
  const L = 34, R = 8, T = 8, B = 20;
  const now = Date.now(), span = 10 * 60 * 1000, t0 = now - span;
  const pts = h.filter((x) => x.t >= t0);
  let max = 60;
  for (const x of pts) max = Math.max(max, x.n, x.nt, x.b, x.bt);
  max = Math.ceil((max + 10) / 50) * 50;
  const X = (t) => L + (t - t0) / span * (W - L - R), Y = (v) => T + (1 - v / max) * (H - T - B);
  g.font = "11px system-ui, sans-serif"; g.fillStyle = col.ink; g.strokeStyle = col.grid; g.lineWidth = 1;
  for (let v = 0; v <= max; v += max > 200 ? 100 : 50) {
    g.beginPath(); g.moveTo(L, Y(v) + .5); g.lineTo(W - R, Y(v) + .5); g.stroke();
    g.textAlign = "right"; g.fillText(v + "°", L - 6, Y(v) + 4);
  }
  g.textAlign = "center";
  for (let m = 10; m >= 0; m -= 2) g.fillText(m ? `-${m}m` : "now", X(now - m * 60000), H - 5);
  const line = (key, color, dash) => {
    g.strokeStyle = color; g.lineWidth = dash ? 1.5 : 2; g.setLineDash(dash ? [5, 4] : []); g.lineJoin = "round";
    g.beginPath(); let started = false;
    for (const x of pts) { if (dash && !x[key]) { started = false; continue; } const px = X(x.t), py = Y(x[key]); started ? g.lineTo(px, py) : g.moveTo(px, py); started = true; }
    g.stroke(); g.setLineDash([]);
  };
  line("nt", col.n, true); line("bt", col.b, true); line("n", col.n); line("b", col.b);
  if (!pts.length) { g.fillStyle = col.ink; g.fillText("Collecting readings…", W / 2, H / 2); return; }
  if (hoverX !== undefined && hoverX >= L) {
    const t = t0 + (hoverX - L) / (W - L - R) * span;
    let best = pts[0];
    for (const x of pts) if (Math.abs(x.t - t) < Math.abs(best.t - t)) best = x;
    const px = X(best.t);
    g.strokeStyle = col.ink; g.lineWidth = 1; g.beginPath(); g.moveTo(px + .5, T); g.lineTo(px + .5, H - B); g.stroke();
    for (const [k, c] of [["n", col.n], ["b", col.b]]) { g.fillStyle = c; g.strokeStyle = css.getPropertyValue("--panel").trim(); g.lineWidth = 2; g.beginPath(); g.arc(px, Y(best[k]), 4, 0, 7); g.fill(); g.stroke(); }
    const tip = $("#pv-chart-tip");
    tip.innerHTML = `<div class="muted">${new Date(best.t).toLocaleTimeString()}</div><div><i style="background:${col.n}"></i>Nozzle <b>${best.n.toFixed(1)}°</b>${best.nt ? ` → ${best.nt}°` : ""}</div><div><i style="background:${col.b}"></i>Bed <b>${best.b.toFixed(1)}°</b>${best.bt ? ` → ${best.bt}°` : ""}</div>`;
    tip.classList.remove("hidden");
    tip.style.left = Math.min(W - tip.offsetWidth - 4, Math.max(4, px + 12)) + "px";
  }
}
$("#pv-chart").addEventListener("mousemove", (e) => { const r = e.target.getBoundingClientRect(); pvDrawChart(e.clientX - r.left); });
$("#pv-chart").addEventListener("mouseleave", () => { $("#pv-chart-tip").classList.add("hidden"); pvDrawChart(); });
window.addEventListener("resize", () => { if (state.view === "printer") pvDrawChart(); });

// ---------------------------------------------------------------- console
async function pvConsole(p) {
  const card = $("#pv-console-card");
  const show = p.capabilities.gcode && p.status.state !== "offline";
  card.classList.toggle("hidden", !show);
  $("#pv-console-log").classList.toggle("hidden", !p.capabilities.console);
  if (!show || !p.capabilities.console || Date.now() - pv.conAt < 2500) return;
  pv.conAt = Date.now();
  try { pv.con = await api(`/api/printers/${enc(p.id)}/console`); } catch { return; }
  const log = $("#pv-console-log");
  const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 30;
  setHTML(log, pv.con.length ? pv.con.slice(-150).map((l) =>
    `<div class="${l.type === "command" ? "cmd" : /^!!|error/i.test(l.message) ? "bad" : ""}"><span class="muted">${new Date(l.time * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}</span> ${l.type === "command" ? "› " : ""}${esc(l.message)}</div>`).join("") : `<div class="muted">No messages yet.</div>`);
  if (atBottom) log.scrollTop = log.scrollHeight;
}
$("#pv-console-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const inp = $("#pv-console-input"), script = inp.value.trim();
  if (!script) return;
  try { await api(`/api/printers/${enc(pv.id)}/gcode`, { json: { script } }); inp.value = ""; pv.conAt = 0; setTimeout(refresh, 400); }
  catch (err) { toast(err.message, true); }
});
$("#pv-console-quick").addEventListener("click", (e) => { const b = e.target.closest("[data-g]"); if (b) { $("#pv-console-input").value = b.dataset.g; $("#pv-console-form").requestSubmit(); } });

// ---------------------------------------------------------------- print a file
async function pvOpenFiles() {
  const p = pvCurrent();
  $("#pv-drop").classList.toggle("hidden", !p.capabilities.upload);
  $("#pv-files-dialog").showModal();
  await pvLoadFiles();
}
async function pvLoadFiles() {
  const p = pvCurrent(), box = $("#pv-file-list");
  if (!p.capabilities.files) { box.innerHTML = `<p class="muted">This printer's connection can't list the files on it. Upload a sliced file above.</p>`; return; }
  box.innerHTML = `<p class="muted">Loading…</p>`;
  try { pv.files = await api(`/api/printers/${enc(p.id)}/files`); } catch (e) { box.innerHTML = `<p class="muted">${esc(e.message)}</p>`; return; }
  pv.files.sort((a, b) => b.modified - a.modified);
  box.innerHTML = pv.files.length ? pv.files.map((f, i) => `
    <div class="file">
      <div style="min-width:0">
        <div class="f-name" title="${esc(f.name)}">${esc(pvName(f.name))}</div>
        <div class="muted small">${(f.size / 1048576).toFixed(1)} MB${f.modified ? " · " + new Date(f.modified * 1000).toLocaleDateString() : ""}</div>
      </div>
      <div class="row gap"><button class="btn small primary" data-print="${i}" ${p.ready ? "" : "disabled"}>Print</button><button class="btn small ghost-danger" data-del="${i}">Delete</button></div>
    </div>`).join("") + (p.ready ? "" : `<p class="hint">Printing is off until the printer is idle and the bed is marked clear.</p>`)
    : `<p class="muted">No files on the printer yet. Upload one above, or send a plate from a project.</p>`;
}
$("#pv-file-list").addEventListener("click", async (e) => {
  const pr = e.target.closest("[data-print]"), del = e.target.closest("[data-del]");
  const p = pvCurrent();
  if (pr) {
    const f = pv.files[+pr.dataset.print];
    try { await api(`/api/printers/${enc(p.id)}/files/start`, { json: { name: f.name } }); $("#pv-files-dialog").close(); toast(`Started ${pvName(f.name)}.`); }
    catch (err) { toast(err.message, true); }
    refresh();
  }
  if (del) {
    const f = pv.files[+del.dataset.del];
    $("#pv-files-dialog").close();
    if (await pvConfirm("Delete this file?", `${pvName(f.name)} is removed from the printer.`, "Delete")) {
      try { await api(`/api/printers/${enc(p.id)}/files?name=${enc(f.name)}`, { method: "DELETE" }); } catch (err) { toast(err.message, true); }
    }
    $("#pv-files-dialog").showModal();
    pvLoadFiles();
  }
});
function pvUpload(file) {
  const p = pvCurrent();
  if (!p || !file) return;
  const start = $("#pv-upload-start").checked;
  if (start && !p.ready) { toast("The printer isn't ready: it's busy or the bed hasn't been marked clear.", true); return; }
  const prog = $("#pv-upload-progress");
  prog.classList.remove("hidden");
  const fd = new FormData();
  fd.append("file", file); fd.append("start", start ? "1" : "0");
  const xhr = new XMLHttpRequest();
  xhr.open("POST", `/api/printers/${enc(p.id)}/upload`);
  xhr.upload.onprogress = (ev) => { if (ev.lengthComputable) $("#pv-upload-bar").style.width = (ev.loaded / ev.total * 100) + "%"; };
  xhr.onloadend = () => {
    prog.classList.add("hidden"); $("#pv-upload-bar").style.width = "0";
    let r = {}; try { r = JSON.parse(xhr.responseText); } catch { /* not JSON */ }
    if (xhr.status !== 200) { toast(r.error || "Upload failed", true); return; }
    toast(start ? `Sent and started ${pvName(file.name)}.` : `${file.name} is on the printer.`);
    if (start) $("#pv-files-dialog").close(); else pvLoadFiles();
    refresh();
  };
  xhr.send(fd);
}
$("#pv-file-input").addEventListener("change", (e) => { pvUpload(e.target.files[0]); e.target.value = ""; });
$("#pv-drop").addEventListener("dragover", (e) => { e.preventDefault(); e.currentTarget.classList.add("over"); });
$("#pv-drop").addEventListener("dragleave", (e) => e.currentTarget.classList.remove("over"));
$("#pv-drop").addEventListener("drop", (e) => { e.preventDefault(); e.currentTarget.classList.remove("over"); pvUpload(e.dataTransfer.files[0]); });

// ---------------------------------------------------------------- dialogs
document.querySelectorAll(".pv-dlg [data-close]").forEach((b) => b.addEventListener("click", () => b.closest("dialog").close()));
function pvConfirm(title, text, ok) {
  return new Promise((resolve) => {
    const d = $("#pv-confirm");
    $("#pv-cf-title").textContent = title; $("#pv-cf-text").textContent = text; $("#pv-cf-ok").textContent = ok;
    d.returnValue = "";
    d.onclose = () => resolve(d.returnValue === "ok");
    d.showModal();
  });
}

// The camera may use the window's height minus the top bar (which can wrap to
// two rows on narrow screens): measured, like FlashBridge's fitCamOnly().
function pvFitHeight() {
  const top = $(".topbar").offsetHeight || 60;
  document.documentElement.style.setProperty("--cam-h", Math.max(160, window.innerHeight - top - 32) + "px");
}
window.addEventListener("resize", pvFitHeight);
pvFitHeight();

$("#pv-ai-ignore").addEventListener("click", async () => {
  await api(`/api/ai/${enc(pv.id)}/ignore`, { method: "POST" }).catch(() => {});
  toast("OK — no more AI alerts for this print.");
  if (typeof aiUI !== "undefined") aiUI.at = 0;
});
