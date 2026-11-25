// Projects page: the list of projects, and one project with its plates
// (tracking, copies, spool locks, and the hand-off to the slicer page).
// Part of the PrintHarbor web UI; see app.js for how the scripts fit together.
"use strict";

const MESH_ACCEPT = ".stl,.obj";
const dz = $("#dropzone");
dz.addEventListener("dragover", (e) => { e.preventDefault(); dz.classList.add("over"); });
dz.addEventListener("dragleave", () => dz.classList.remove("over"));
dz.addEventListener("drop", (e) => { e.preventDefault(); dz.classList.remove("over"); if (e.dataTransfer.files.length) upload([...e.dataTransfer.files]); });
$("#upload").addEventListener("change", (e) => e.target.files.length && upload([...e.target.files]));
$("#new-project").addEventListener("click", async () => {
  const name = prompt("Name for the new project:", "New project");
  if (name === null) return;
  try { const p = await api("/api/projects/new", { json: { name } }); openProject(p.id); } catch (e) { toast(e.message, true); }
});

function filesForm(files) {
  const fd = new FormData();
  for (const f of files) fd.append("file", f);
  return fd;
}

async function upload(files) {
  const label = dz.querySelector("strong");
  label.textContent = files.length > 1 ? `Reading ${files.length} files…` : `Reading ${files[0].name}…`;
  try {
    const p = await api("/api/projects", { method: "POST", body: filesForm(files) });
    openProject(p.id);
  } catch (e) {
    toast("Couldn't open that: " + e.message, true);
  } finally {
    label.textContent = "Open a 3MF project, or STL / OBJ files";
    $("#upload").value = "";
  }
}

async function renderProjects() {
  $("#project-list-wrap").classList.remove("hidden");
  $("#project-detail").classList.add("hidden");
  const list = await api("/api/projects");
  setHTML($("#project-list"), list.length ? list.map((p) => `
    <div class="card job">
      <div><h3>${esc(p.name)}</h3>
        <div class="muted small">${p.slicer ? `Set up in the slicer for ${esc(p.slicer.printer_name)}` : p.kind === "mesh" ? `${(p.mesh_parts || []).length} STL/OBJ part(s)` : "3MF"} · ${p.data.plates.length} plate(s) · ${new Date(p.uploaded).toLocaleString()}</div></div>
      <div class="row" style="flex:0">
        <button class="primary" data-open="${p.id}">Open</button>
        <button class="danger" data-del="${p.id}">Delete</button>
      </div>
    </div>`).join("") : `<p class="muted">No projects yet.</p>`);
  $("#project-list").onclick = async (e) => {
    const o = e.target.dataset.open, d = e.target.dataset.del;
    if (o) openProject(o);
    if (d && confirm("Delete this project from PrintHarbor? Your original files on your computer are not affected.")) {
      await api("/api/projects/" + d, { method: "DELETE" });
      renderProjects();
    }
  };
}

async function openProject(id) {
  if (state.project && state.project.id !== id && !(await pmLeave())) { show("projects"); return; }
  state.project = await api("/api/projects/" + id);
  state.progress = await api(`/api/projects/${id}/progress`);
  state.fits = {};
  show("projects");
}

function swatch(color) { return `<span class="swatch" style="background:${esc(color || "#888")}"></span>`; }


// Runs a project edit and redraws with the result.
async function editProject(promise) {
  try { state.project = await promise; renderProject(); } catch (e) { toast(e.message, true); }
}


// A project: one bar on top (name, Plates / Paint / Settings, save, progress, sending) and the editor
// (web/slicer in project mode) below. The editor stays loaded while you use other pages.
state.pm = { id: "", dirty: false, tab: "plates", saving: false };
let m_opened = false; // the editor just opened the project: send it the status again
function pmFrame() { return $("#pm-frame"); }
function pmPost(msg) { const f = pmFrame(); if (f && f.contentWindow) f.contentWindow.postMessage(msg, location.origin); }

async function renderProject() {
  const p = state.project;
  $("#project-list-wrap").classList.add("hidden");
  const el = $("#project-detail");
  el.classList.remove("hidden");
  document.body.classList.add("pmview");
  if (!pmFrame() || pmFrame().dataset.id !== p.id) {
    state.pm = { id: p.id, dirty: false, tab: state.pm.id === p.id ? state.pm.tab : "plates", saving: false, plate: state.pm.plate || 1 };
    el.innerHTML = `
      <div class="pm-head">
        <button class="back" id="back" title="All projects">←</button>
        <h2 id="pm-name" title="Click to rename"></h2>
        <nav class="pm-tabs" id="pm-tabs">
          <button data-pmtab="plates" title="Add models and arrange them on plates">Plates</button>
          <button data-pmtab="paint" title="Paint colours, supports and seams onto the models">Paint</button>
          <button data-pmtab="settings" title="Walls, infill, supports … per plate, for any printer">Settings</button>
        </nav>
        <button id="pm-save" class="primary" title="Save placement, painting, colours and settings">Save</button>
        <span id="pm-saved" class="muted small"></span>
        <span class="pm-sp"></span>
        <div id="pm-dispatch" class="pm-dispatch"></div>
        <details class="pm-more"><summary title="More">⋯</summary><div class="pmx-menu">
          <button data-act="rename">Rename</button>
          <button data-act="to-library">Save to library</button>
          <button data-act="delete" class="danger">Delete project</button></div></details>
      </div>
      <div id="pm-review"></div>
      <iframe id="pm-frame" title="Project editor" allow="clipboard-read; clipboard-write; fullscreen"></iframe>`;
    const f = pmFrame();
    f.dataset.id = p.id;
    f.src = `/slicer/index.html?project=${enc(p.id)}&tab=${state.pm.tab}&plate=${state.pm.plate || 1}`;
    el.onclick = onProjectClick;
    el.onchange = onProjectChange;
  }
  const sv = p.slicer;
  setHTML($("#pm-name"), esc(p.name));
  document.querySelectorAll("#pm-tabs [data-pmtab]").forEach((b) => b.classList.toggle("active", b.dataset.pmtab === state.pm.tab));
  const save = $("#pm-save");
  save.textContent = state.pm.saving ? "Saving…" : state.pm.dirty || !sv ? "Save" : "Saved";
  save.disabled = state.pm.saving || (!state.pm.dirty && !!sv);
  save.classList.toggle("dirty", state.pm.dirty || !sv);
  setHTML($("#pm-saved"), sv ? (state.pm.dirty ? "unsaved changes" : "") : "not saved yet");
  const pr = state.progress || [];
  const need = pr.reduce((n, x) => n + x.needed, 0), done = pr.reduce((n, x) => n + x.accepted, 0), printing = pr.reduce((n, x) => n + x.printing, 0);
  setHTML($("#pm-dispatch"), `
    ${need ? `<div class="pm-prog" title="${done} of ${need} plate prints done${printing ? `, ${printing} printing` : ""}"><div class="progress"><div style="width:${Math.round((done / need) * 100)}%"></div></div><span class="small muted">${done}/${need}${printing ? ` · ${printing} printing` : ""}</span></div>` : ""}
    <button data-act="dispatch" title="Every plate that still needs printing goes to an idle printer that can take it (right colours loaded, bed cleared); each is sliced for its printer">Send plates to ready printers</button>
    <label class="pm-auto" title="Keep sending plates as printers become free"><input type="checkbox" id="auto" ${p.auto_dispatch ? "checked" : ""}> Keep going</label>
    <select id="goal" title="Which printer gets a plate first"><option value="small" ${!p.goal || p.goal === "small" ? "selected" : ""}>Smallest printer first</option><option value="fast" ${p.goal === "fast" ? "selected" : ""}>Fastest first</option></select>`);
  const review = pr.flatMap((x) => x.runs.filter((r) => r.state === "printed").map((r) => ({ ...r, plate: x.plate })));
  setHTML($("#pm-review"), review.length ? `<div class="pm-check"><b>Finished: how did ${review.length === 1 ? "it" : "they"} come out?</b>${review.map((r) => `<span class="pm-run">Plate ${r.plate} on ${esc(r.printer_name)}
    <button class="icon" data-review="${r.id}:1">OK</button><button class="icon danger" data-review="${r.id}:0">Failed</button></span>`).join("")}</div>` : "");
  const st = JSON.stringify(pr.map((x) => [x.plate, x.needed, x.accepted, x.manual, x.printing, x.review, x.failed, x.remaining]));
  if (st !== state.pm.statusSent || m_opened) { state.pm.statusSent = st; pmPost({ ph: "pm-status", status: { progress: pr } }); }
}

function pmTab(tab) {
  state.pm.tab = tab;
  pmPost({ ph: "pm-tab", tab });
  renderProject();
}

/** Leaving the project (back to the list, another project): unsaved changes? */
async function pmLeave() {
  if (!state.pm.dirty) return true;
  return confirm(`${state.project ? state.project.name : "This project"} has unsaved changes. Leave without saving?`);
}

function onProjectClick(e) {
  const p = state.project;
  const t = e.target.closest("[data-act],[data-pmtab],[data-review],#back,#pm-save,#pm-name");
  if (!t) return;
  if (t.id === "back") { pmLeave().then((ok) => { if (ok) closeProject(); }); return; }
  if (t.id === "pm-save") { pmSave(); return; }
  if (t.id === "pm-name" || t.dataset.act === "rename") {
    const name = prompt("Project name:", p.name);
    if (name) editProject(api(`/api/projects/${p.id}/rename`, { json: { name } }));
    return;
  }
  if (t.dataset.pmtab) { pmTab(t.dataset.pmtab); return; }
  const d = t.dataset;
  if (d.act === "dispatch") {
    const go = () => api(`/api/projects/${p.id}/dispatch`, { method: "POST" }).then((as) => {
      toast(as.length ? `Sent ${as.map((x) => `plate ${x.plate} → ${x.printer_name}`).join(", ")}.` : "No plate could go out: no printer is ready with the right colours loaded, or every plate is done.");
      refresh();
    }).catch((er) => toast(er.message, true));
    if (state.pm.dirty || !p.slicer) pmSave(go); else go();
  }
  if (d.review) {
    const [run, good] = d.review.split(":");
    api(`/api/runs/${run}/review`, { json: { good: good === "1" } }).then(refresh).catch((er) => toast(er.message, true));
  }
  if (d.act === "to-library") {
    api(`/api/projects/${p.id}/to-library`, { json: {} }).then((it) => toast(`Saved "${it.title}" to your library.`)).catch((er) => toast(er.message, true));
  }
  if (d.act === "delete" && confirm(`Delete ${p.name} from PrintHarbor? Your original files on your computer are not affected.`)) {
    api("/api/projects/" + p.id, { method: "DELETE" }).then(() => { state.pm.dirty = false; closeProject(); });
  }
}
function onProjectChange(e) {
  const p = state.project;
  if (e.target.id === "auto" || e.target.id === "goal") editProject(api(`/api/projects/${p.id}/auto`, { json: { on: $("#auto").checked, goal: $("#goal").value } }));
}
function closeProject() {
  state.project = null;
  state.pm = { id: "", dirty: false, tab: "plates", saving: false };
  document.body.classList.remove("pmview");
  $("#project-detail").innerHTML = "";
  render();
}

// Save from the bar; then (optional) run next once it is saved.
let pmAfterSave = null;
function pmSave(next = null) {
  if (state.pm.saving) return;
  state.pm.saving = true;
  pmAfterSave = next;
  renderProject();
  pmPost({ ph: "pm-save" });
}

window.addEventListener("message", async (e) => {
  if (e.origin !== location.origin || !e.data || !state.project) return;
  const m = e.data;
  if (m.id && m.id !== state.project.id) return;
  if (m.ph === "pm-dirty") { state.pm.dirty = m.dirty; renderProject(); }
  if (m.ph === "pm-opened") { m_opened = true; renderProject(); m_opened = false; }
  if (m.ph === "pm-save-done") {
    state.pm.saving = false;
    if (m.ok) { state.pm.dirty = false; try { state.project = await api("/api/projects/" + state.project.id); state.progress = await api(`/api/projects/${state.project.id}/progress`); } catch (_) { /* */ } }
    renderProject();
    const next = pmAfterSave; pmAfterSave = null;
    if (m.ok && next) next();
  }
  if (m.ph === "pm-copies") {
    await api(`/api/projects/${state.project.id}/copies`, { json: { plate: m.plate, copies: m.copies } }).catch((er) => toast(er.message, true));
    state.progress = await api(`/api/projects/${state.project.id}/progress`);
    renderProject();
  }
  if (m.ph === "pm-printed") {
    let ok = true;
    await api(`/api/projects/${state.project.id}/printed`, { json: { plate: m.plate, done: m.done } }).catch((er) => { ok = false; toast(er.message, true); });
    state.progress = await api(`/api/projects/${state.project.id}/progress`);
    renderProject();
    const x = progressFor(m.plate);
    if (ok && x) toast(m.done ? `Plate ${m.plate} marked printed (${x.accepted}/${x.needed}).` : `Plate ${m.plate} is no longer marked printed (${x.accepted}/${x.needed}).`);
  }
  if (m.ph === "pm-print") {
    state.pm.plate = m.plate;
    const go = async () => { state.project = await api("/api/projects/" + state.project.id); choosePrinter(m.plate); };
    if (m.dirty) pmSave(go); else go();
  }
});
window.addEventListener("beforeunload", (e) => { if (state.pm.dirty) { e.preventDefault(); e.returnValue = ""; } });

function progressFor(plate) { return (state.progress || []).find((x) => x.plate === plate); }

function projectProgressBar(p) {
  const pr = state.progress || [];
  const need = pr.reduce((s, x) => s + x.needed, 0), done = pr.reduce((s, x) => s + x.accepted, 0);
  if (!need) return "";
  const printing = pr.reduce((s, x) => s + x.printing, 0), review = pr.reduce((s, x) => s + x.review, 0);
  return `<div class="proj-progress"><div class="progress"><div style="width:${Math.round((done / need) * 100)}%"></div></div>
    <span class="small muted">${done} of ${need} plate print(s) done${printing ? ` · ${printing} printing` : ""}${review ? ` · ${review} to check` : ""}</span></div>`;
}


async function lockDialog(p, filament) {
  const f = p.data.filaments.find((x) => x.index === filament);
  const body = $("#dlg-body");
  let spools = [];
  try { spools = await api("/api/spoolman/spools"); } catch (e) {
    body.innerHTML = `<h2>Pin filament ${filament} to a spool</h2><p class="muted">${esc(e.message)}</p><p class="small muted">Spoolman tracks your actual spools. Add its address in Settings to pin a color to one spool, so every copy comes from the same roll.</p><div class="dlg-actions"><button value="cancel">Close</button></div>`;
    openDialog();
    return;
  }
  const cur = (p.locks || {})[filament] || 0;
  body.innerHTML = `<h2>${swatch(f.color)} Filament ${filament} (${esc(f.type)})</h2>
    <p class="muted small">Pin it to one spool and PrintHarbor will only send this project to a printer that has that spool loaded.</p>
    <div class="pk-list">
      <div class="pk-item" data-spool="0"><span></span><div><strong>Any matching filament</strong><div class="muted small">Match by material and color (default)</div></div></div>
      ${spools.map((s) => `<div class="pk-item ${s.id === cur ? "sel" : ""}" data-spool="${s.id}"><span class="swatch big" style="background:${esc(s.color || "#888")}"></span>
        <div><strong>#${s.id} ${esc(s.vendor)} ${esc(s.name)}</strong><div class="muted small">${esc(s.material)} · ${Math.round(s.remaining_g)} g left${s.location ? " · " + esc(s.location) : ""}</div></div></div>`).join("")}
    </div><div class="dlg-actions"><button value="cancel">Close</button></div>`;
  body.querySelector(".pk-list").onclick = async (e) => {
    const c = e.target.closest("[data-spool]");
    if (!c) return;
    $("#dlg").close();
    editProject(api(`/api/projects/${p.id}/locks`, { json: { filament, spool_id: +c.dataset.spool } }));
  };
  openDialog();
}



// What the slicer saved for a plate, in a line: quality and the filament per tool.
function savedPlate(pl) { const sv = state.project && state.project.slicer; return sv ? (sv.plates || []).find((x) => x.index === pl.index) : null; }
function savedLine(pl) {
  const sp = savedPlate(pl);
  if (!sp) return "";
  const ov = Object.keys(sp.opts.overrides || {}).filter((k) => !/^(curr_bed_type|wipe_tower_|resolution$|enable_arc_fitting$|mmu_segmented)/.test(k)).length;
  return `<div class="small" style="margin-top:4px">${esc(sp.opts.process)}${ov ? ` · ${ov} setting${ov === 1 ? "" : "s"} changed` : ""}</div>`;
}

// The line under a printer whose bed still counts as "not cleared" (see choosePrinter).
function bedLine(printerId) {
  const pr = state.printers.find((x) => x.id === printerId), st = pr && pr.status && pr.status.state;
  if (!pr || pr.bed_clear || (st !== "idle" && st !== "finished")) return "";
  return `<div class="bedline small" data-bedline="${esc(printerId)}"><span class="muted">Looking at the bed through the camera…</span> <button type="button" data-bedclear="${esc(printerId)}">The bed is clear</button></div>`;
}

async function choosePrinter(plateIndex) {
  const p = state.project;
  const pl = p.data.plates.find((x) => x.index === plateIndex);
  const fits = await api(`/api/projects/${p.id}/plates/${plateIndex}/fit`);
  const body = $("#dlg-body");
  body.innerHTML = `<h2>${esc(pl.name)}: pick a printer</h2>
    <p class="muted small">Best matches first. Smaller printers are suggested first so big ones stay free for big parts.</p>
    ${fits.length ? fits.map((f) => `
      <div class="fit">
        <div><strong>${esc(f.printer_name)}</strong> <span class="pill lvl-${f.level}">${LEVEL[f.level]}</span>${f.estimate_sec && f.level !== "cannot" ? ` <span class="muted small" title="Rough guess from part size and nozzle; the slicer gives the real time">≈ ${dur(f.estimate_sec)}</span>` : ""}
          <ul class="reasons">${f.reasons.map((r) => `<li class="r-${r.kind}">${esc(r.text)}</li>`).join("")}</ul>${bedLine(f.printer_id)}</div>
        <button ${f.level === "cannot" ? "disabled" : ""} class="${f.level === "ready" ? "primary" : ""}" data-pick="${f.printer_id}">Use</button>
      </div>`).join("") : `<p>No printers yet. <a href="#" id="go-printers">Add one</a>.</p>`}
    <div class="dlg-actions"><button value="cancel">Close</button></div>`;
  // A printer whose bed is still marked "not cleared": look through its camera now, and
  // let it be marked clear right here instead of sending you to the Printers page.
  body.querySelectorAll("[data-bedline]").forEach(async (el) => {
    const id = el.dataset.bedline, pr = state.printers.find((x) => x.id === id), note = el.querySelector("span");
    if (!(pr && pr.status && pr.status.snapshot_url)) { note.textContent = "This printer has no camera to check with."; return; }
    try {
      const r = await api(`/api/printers/${id}/bed-check`);
      note.textContent = r.empty ? "The camera sees an empty bed." : `The camera thinks something is on the bed (it looks ${Math.round(r.difference * 100)}% different from your empty-bed pictures; under 28% counts as empty).`;
      note.className = r.empty ? "ok-text" : "";
    } catch (e) { note.textContent = /no empty-bed pictures/i.test(e.message) ? "The camera hasn't been shown this printer's empty bed yet (Printers page: This is my empty bed), so it can't tell." : "The camera couldn't be checked: " + e.message; }
  });
  body.querySelectorAll("[data-bedclear]").forEach((b) => (b.onclick = async (e) => {
    e.preventDefault();
    try { await api(`/api/printers/${b.dataset.bedclear}/bed-clear`, { json: { clear: true } }); await refresh(); choosePrinter(plateIndex); } catch (err) { toast(err.message, true); }
  }));
  const gp = $("#go-printers");
  if (gp) gp.onclick = (e) => { e.preventDefault(); $("#dlg").close(); show("printers"); };
  body.querySelectorAll("[data-pick]").forEach((b) => (b.onclick = (e) => {
    e.preventDefault();
    const fit = fits.find((f) => f.printer_id === b.dataset.pick);
    prepareSend(pl, fit);
  }));
  openDialog();
}

function settingLabel(k) {
  const c = (state.catalog || []).find((x) => x.key === k);
  if (c) return c.label.toLowerCase();
  return { wall_loops: "walls", sparse_infill_density: "infill", top_shell_layers: "top layers", bottom_shell_layers: "bottom layers", enable_support: "supports", layer_height: "layer height", brim_type: "brim" }[k] || k.replace(/_/g, " ");
}
// A plate set up in the slicer. On the printer it was set up for it prints exactly as saved (only the
// slots are picked here). Any other printer: sliced for that printer when sent, with its own printer
// profile and the quality / filament profiles picked here; painting, colours and setting changes kept.
async function sendSaved(pl, fit) {
  const p = state.project, sp = savedPlate(pl), sv = p.slicer;
  const printer = state.printers.find((x) => x.id === fit.printer_id);
  const exact = printer && (printer.id === sv.printer || (printer.machine_preset && printer.machine_preset === sv.machine));
  const slots = printerSlots(printer);
  const body = $("#dlg-body");
  let sug = null;
  if (!exact) {
    body.innerHTML = `<p class="muted">Loading ${esc(fit.printer_name)}'s profiles…</p>`;
    try { sug = await api(`/api/projects/${p.id}/suggest?printer=${enc(fit.printer_id)}`); } catch (e) {
      body.innerHTML = `<p class="err">${esc(e.message)}</p><div class="dlg-actions"><button value="cancel">Close</button></div>`;
      return;
    }
  }
  const options = (list, sel) => list.map((n) => `<option ${n === sel ? "selected" : ""}>${esc(n)}</option>`).join("");
  const mapFor = (i) => (fit.mapping || []).find((m) => m.filament === i);
  const ov = Object.keys(sp.opts.overrides || {}).filter((k) => !/^(curr_bed_type|wipe_tower_)/.test(k) && !(sp.opts.auto || []).includes(k));
  body.innerHTML = `
    <h2>${esc(pl.name)} → ${esc(fit.printer_name)}</h2>
    ${exact ? `<p class="muted small">Printed exactly as set up in the slicer: ${esc(sp.opts.machine)} · ${esc(sp.opts.process)}, models where you placed them. To change settings, painting or placement, use Edit in slicer.</p>`
      : `<p class="muted small">Set up in the slicer for ${esc(sv.printer_name)}. Sliced for ${esc(fit.printer_name)} when sent: its own profile (${esc(sug.machine_preset)}), the parts arranged on its bed. Painting and colours are kept${ov.length ? `, and so are your setting changes (${ov.map((k) => esc(settingLabel(k))).join(", ")})` : ""}.</p>`}
    ${fit.reasons.filter((r) => r.kind !== "good" && r.kind !== "info").length ? `<ul class="reasons">${fit.reasons.filter((r) => r.kind !== "good" && r.kind !== "info").map((r) => `<li class="r-${r.kind}">${esc(r.text)}</li>`).join("")}</ul>` : ""}
    <div class="settings-group">Match your colours to ${esc(fit.printer_name)}</div>
    ${sp.tools.map((fi, k) => { const f = p.data.filaments[fi - 1] || {}, m = mapFor(fi), fp = sp.opts.filaments[k] || {};
      const sel = slots.length ? `<select data-slot-for="${fi}" class="slot-select" title="The spool in ${esc(fit.printer_name)} that prints this colour"><option value="0">Printer decides</option>${slots.map((sl) => `<option value="${sl.index}" ${m && m.slot === sl.index ? "selected" : ""}>${sl.index === 99 ? "External spool" : "Slot " + sl.index}: ${esc(colorName(sl.color))} ${esc(sl.type)}</option>`).join("")}</select>`
        : `<span class="muted small">${esc(fit.printer_name)} doesn't report its spools: load ${esc(colorName(fp.color || f.color))} ${esc(f.type || "")}${sp.tools.length > 1 ? ` as filament ${k + 1}` : ""}</span>`;
      const have = m && m.slot ? slots.find((x) => x.index === m.slot) : null;
      return `<div class="match-row">${swatch(fp.color || f.color)}<span class="match-what">Colour ${fi} <span class="muted small">${esc(f.type || "")}</span></span><span class="muted">→</span>${have ? swatch(have.color) : ""}${sel}</div>`; }).join("")}
    ${exact ? "" : `<details class="match-profiles"><summary>Profiles used on ${esc(fit.printer_name)}</summary>
      <div class="settings-group">Quality</div><select id="process">${options(sug.processes, sug.process_preset)}</select>
      <div class="settings-group">Filament profiles</div>
      ${sp.tools.map((fi, k) => `<div class="filrole">${swatch((sp.opts.filaments[k] || {}).color)}<select data-fil="${k}">${options(sug.filament_choices, sug.filament_presets[fi - 1])}</select></div>`).join("")}</details>`}
    ${levelToggleHTML(printer)}
    <div class="dlg-actions">
      <button value="cancel">Cancel</button>
      <button id="slice-only" type="button" class="primary" title="Slices it and shows you the result, supports and all. You send it to the printer from the preview.">Slice &amp; preview</button>
      <button id="upload-only" type="button" ${printer && printer.capabilities.upload ? "" : "disabled"}>Slice &amp; upload</button>
      <button id="go" type="button" title="Sends it to the printer as soon as it is sliced, without showing you the preview first" ${printer && printer.capabilities.start_print ? "" : "disabled"}>Slice &amp; print</button>
    </div>`;
  const submit = async (send, start) => {
    if ($("#lvl-before")) setLevelPref($("#lvl-before").checked);
    const slot_map = p.data.filaments.map((f) => { const s = body.querySelector(`[data-slot-for="${f.index}"]`); if (s) return +s.value; const m = mapFor(f.index); return m ? m.slot : 0; });
    const job = { project_id: p.id, plate: pl.index, printer_id: fit.printer_id, slot_map, send, start, level: !!($("#lvl-before") && $("#lvl-before").checked) };
    if (!exact) Object.assign(job, { machine_preset: sug.machine_preset, process_preset: $("#process").value, filament_presets: [...body.querySelectorAll("[data-fil]")].map((s) => s.value) });
    try {
      const made = await api("/api/jobs", { json: job });
      $("#dlg").close();
      state.previewJob = { id: made.id, title: `${p.name}: ${pl.name} → ${fit.printer_name}`, preview: !send, since: Date.now() };
      checkPreviewJob();
      toast(start ? `Slicing ${pl.name} for ${fit.printer_name}. It will start printing when done.` : send ? `Slicing ${pl.name} for ${fit.printer_name}. Check the queue for progress.` : `Slicing ${pl.name}. The preview opens when it is ready; you send it to ${fit.printer_name} from there.`);
      refresh();
    } catch (e) { toast(e.message, true); }
  };
  body.querySelectorAll("[data-slot-for]").forEach((sel) => sel.addEventListener("change", () => {
    const row = sel.closest(".match-row"), sl = slots.find((x) => x.index === +sel.value);
    row.querySelectorAll(".swatch")[1]?.remove();
    if (sl) sel.insertAdjacentHTML("beforebegin", swatch(sl.color));
  }));
  $("#go").onclick = () => submit(true, true);
  $("#upload-only").onclick = () => submit(true, false);
  $("#slice-only").onclick = () => submit(false, false);
}
