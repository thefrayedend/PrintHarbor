// Printers page: every printer as a card (state, camera, alerts), the review
// of finished prints, and the add / edit printer dialog. One printer's own
// dashboard is printer.js.
// Part of the PrintHarbor web UI; see app.js for how the scripts fit together.
"use strict";

$("#add-printer").onclick = () => editPrinter();
$("#cam-wall").onclick = () => { state.camWall = !state.camWall; renderPrinters(); };

function drawAlertList() {
  const el = $("#alert-list");
  if (!el) return;
  const list = (state.alerts || []).slice(-8).reverse();
  setHTML(el, list.length ? `<div class="settings-group">Recent alerts</div>` + list.map((a) => `<div class="alert-row small lvl-${a.level === "error" ? "cannot" : a.level === "warning" ? "needs_action" : "ready"}">
    <strong>${esc(a.printer_name || "PrintHarbor")}</strong> · ${new Date(a.time).toLocaleTimeString()} · ${esc(a.text)}</div>`).join("") : "");
}

// Printer cards update in place: only parts whose content changed are
// redrawn, and camera frames are swapped in only after they've loaded, so
// nothing flickers or jumps every poll.
const cams = {}; // printer id -> { next, fails, busy }

function printerTop(p) {
  const s = p.status;
  return `<div class="toolbar" style="margin:0"><h3>${esc(p.name)}</h3><span class="st-${s.state} small"><strong>${STATE[s.state] || s.state}</strong></span></div>
    <div class="muted small">${esc(p.machine_preset || "No Orca profile chosen")}</div>`;
}

function printerRest(p) {
  const s = p.status, c = p.capabilities;
  const slots = (s.slots && s.slots.length ? s.slots : p.slots) || [];
  return `${s.state === "printing" || s.state === "paused" ? `<div class="small" style="margin-top:8px">${esc(s.job_name)} · ${Math.round(s.progress * 100)}%${s.remaining_sec ? " · " + dur(s.remaining_sec) + " left" : ""}</div>
      <div class="progress"><div style="width:${Math.round(s.progress * 100)}%"></div></div>` : ""}
    ${c.temperatures && s.state !== "offline" ? `<div class="temps"><span>Nozzle ${Math.round(s.nozzle_c)}°${s.nozzle_target_c ? "/" + Math.round(s.nozzle_target_c) + "°" : ""}</span><span>Bed ${Math.round(s.bed_c)}°${s.bed_target_c ? "/" + Math.round(s.bed_target_c) + "°" : ""}</span>${s.chamber_c ? `<span>Chamber ${Math.round(s.chamber_c)}°</span>` : ""}</div>` : ""}
    ${slots.length ? `<div class="slots">${slots.map((sl) => `<span class="slot">${sl.index}: ${swatch(sl.color)} ${esc(sl.type)}</span>`).join("")}</div>` : ""}
    ${s.message && s.state !== "printing" ? `<div class="err">${esc(s.message)}</div>` : ""}
    <div class="controls">
      ${c.pause && s.state === "printing" ? `<button data-act="pause" data-id="${p.id}">Pause</button>` : ""}
      ${c.resume && s.state === "paused" ? `<button data-act="resume" data-id="${p.id}">Resume</button>` : ""}
      ${c.cancel && (s.state === "printing" || s.state === "paused") ? `<button class="danger" data-act="cancel" data-id="${p.id}">Stop</button>` : ""}
      ${c.light && s.state !== "offline" && state.info.show_printer_light ? `<button data-act="${s.light === "on" ? "light_off" : "light_on"}" data-id="${p.id}">${s.light === "on" ? "Light off" : "Light on"}</button>` : ""}
      ${s.snapshot_url && p.bed_clear && s.state === "idle" ? `<button data-emptybed="${p.id}" title="Teach PrintHarbor what this printer's empty bed looks like">This is my empty bed</button>` : ""}
      ${!p.bed_clear && s.state !== "printing" && s.state !== "paused" && (p.driver === "flashforge" || !s.snapshot_url || !p.has_empty_bed) ? `<button class="primary" data-clear="${p.id}">${p.driver === "flashforge" ? "Clear platform" : "Parts removed"}</button>` : ""}
      <button data-slice="${p.id}" title="Open the slicer set up for this printer's profile">Open in slicer</button>
      ${c.start_print ? `<button data-printhere="${p.id}" title="Print a plate from one of your projects on this printer">Print a plate…</button>` : ""}
      <button data-open="${p.id}">Controls</button>
      <button data-edit="${p.id}">Edit</button>
    </div>`;
}

function renderPrinters() {
  const g = $("#printer-grid");
  g.classList.toggle("camwall", !!state.camWall);
  const wallBtn = $("#cam-wall");
  if (wallBtn) wallBtn.textContent = state.camWall ? "Show controls" : "Camera wall";
  drawAlertList();
  if (!state.printers.length) { setHTML(g, `<p class="muted">No printers yet. Add one, or add a "Simulated printer" to try things out.</p>`); return; }
  if (g._html) { g.innerHTML = ""; g._html = ""; } // drop the "no printers" message
  const ids = new Set(state.printers.map((p) => p.id));
  [...g.children].forEach((el) => { if (!ids.has(el.dataset.id)) el.remove(); });
  state.printers.forEach((p, i) => {
    let card = g.querySelector(`[data-id="${p.id}"]`);
    if (!card) {
      card = document.createElement("div");
      card.className = "card";
      card.dataset.id = p.id;
      card.innerHTML = `<div class="pc-top"></div><div class="pc-cam"></div><div class="pc-rest"></div>`;
    }
    if (g.children[i] !== card) g.insertBefore(card, g.children[i] || null);
    setHTML(card.querySelector(".pc-top"), printerTop(p));
    setHTML(card.querySelector(".pc-rest"), printerRest(p));
    updateCam(card.querySelector(".pc-cam"), p);
  });
  g.onclick = async (e) => {
    const t = e.target.dataset;
    // A click on the card itself (not one of its buttons) opens the control page.
    if (!e.target.closest("button, a, input, select, label")) {
      const card = e.target.closest(".card[data-id]");
      if (card) return openPrinter(card.dataset.id);
    }
    try {
      if (t.act) {
        if (t.act === "cancel" && !confirm("Stop this print? It can't be resumed.")) return;
        await api(`/api/printers/${t.id}/action`, { json: { action: t.act } });
      }
      if (t.clear) {
        const runs = await api(`/api/runs?printer=${t.clear}&limit=5`);
        const pending = runs.find((r) => r.state === "printed");
        if (pending) return reviewDialog(pending, t.clear);
        await api(`/api/printers/${t.clear}/bed-clear`, { json: { clear: true } });
      }
      if (t.slice) return openSlicerForPrinter(t.slice);
      if (t.printhere) return pickPlateForPrinter(t.printhere);
      if (t.open) return openPrinter(t.open);
      if (t.edit) editPrinter(state.printers.find((p) => p.id === t.edit));
      if (t.emptybed) { const r = await api(`/api/printers/${t.emptybed}/empty-bed`, { method: "POST" }); toast(`Saved. PrintHarbor now has ${r.references} empty-bed picture(s) for this printer.`); }
      refresh();
    } catch (err) { toast(err.message, true); }
  };
}

// Loads a new frame off-screen and shows it only if it arrives. A camera that
// fails stays hidden and is retried less and less often (up to every 2 min).
function updateCam(slot, p) {
  const url = p.status.snapshot_url;
  if (!url) { slot.innerHTML = ""; return; }
  const c = (cams[p.id] ||= { next: 0, fails: 0, busy: false });
  if (c.busy || Date.now() < c.next) return;
  c.busy = true;
  const img = new Image();
  img.onload = () => {
    c.busy = false; c.fails = 0; c.next = Date.now() + 5000;
    let shown = slot.querySelector("img");
    if (!shown) { shown = document.createElement("img"); shown.className = "cam"; shown.alt = p.name + " camera"; slot.appendChild(shown); }
    shown.src = img.src;
  };
  img.onerror = () => {
    c.busy = false; c.fails++;
    c.next = Date.now() + Math.min(120000, 5000 * 2 ** c.fails);
    if (c.fails >= 2) slot.innerHTML = "";
  };
  img.src = `${url}?t=${Date.now()}`;
}

// Print a plate on this printer: pick a project and plate, then hand off to the
// same "slice & send" flow the Projects page uses (prepareSend), pre-aimed at
// this printer.
async function pickPlateForPrinter(printerId) {
  const printer = state.printers.find((p) => p.id === printerId);
  if (!printer) return;
  let projects;
  try { projects = await api("/api/projects"); } catch (e) { return toast(e.message, true); }
  projects = (projects || []).filter((p) => p.data && p.data.plates && p.data.plates.length);
  const body = $("#dlg-body");
  if (!projects.length) {
    body.innerHTML = `<h2>Print a plate on ${esc(printer.name)}</h2><p class="muted">No projects with plates yet. Make one on the Projects tab or in the slicer, then come back.</p><div class="dlg-actions"><button value="cancel">Close</button></div>`;
    return openDialog();
  }
  const byId = {}; projects.forEach((pr) => (byId[pr.id] = pr));
  const plateOpts = (pr) => pr.data.plates.map((pl) => `<option value="${pl.index}">Plate ${pl.index}${pl.name && pl.name !== "Plate " + pl.index ? " — " + esc(pl.name) : ""}</option>`).join("");
  body.innerHTML = `<h2>Print a plate on ${esc(printer.name)}</h2>
    <label class="field"><span>Project</span><select id="pp-project">${projects.map((pr) => `<option value="${esc(pr.id)}">${esc(pr.name)}</option>`).join("")}</select></label>
    <label class="field"><span>Plate</span><select id="pp-plate">${plateOpts(projects[0])}</select></label>
    <div class="dlg-actions"><button value="cancel">Cancel</button><button type="button" class="primary" id="pp-go">Set up print</button></div>`;
  $("#pp-project").onchange = (e) => { $("#pp-plate").innerHTML = plateOpts(byId[e.target.value]); };
  $("#pp-go").onclick = async () => {
    const pid = $("#pp-project").value, plateIndex = +$("#pp-plate").value;
    $("#pp-go").disabled = true;
    try {
      state.project = await api("/api/projects/" + enc(pid));
      const pl = state.project.data.plates.find((x) => x.index === plateIndex);
      if (!pl) throw new Error("That plate is no longer in the project.");
      const fits = await api(`/api/projects/${enc(pid)}/plates/${plateIndex}/fit`);
      let fit = (fits || []).find((f) => f.printer_id === printerId);
      if (fit && fit.level === "cannot") {
        const why = fit.reasons.filter((r) => r.kind !== "good").map((r) => r.text).join("; ");
        if (!confirm(`${printer.name} may not be able to print this plate${why ? ": " + why : ""}.\n\nSet it up anyway?`)) { $("#pp-go").disabled = false; return; }
      }
      if (!fit) fit = { printer_id: printerId, printer_name: printer.name, reasons: [], mapping: [] };
      prepareSend(pl, fit); // replaces this dialog's contents with the slice & send screen
    } catch (e) { toast(e.message, true); $("#pp-go").disabled = false; }
  };
  openDialog();
}

function reviewDialog(run, printerId) {
  const body = $("#dlg-body");
  body.innerHTML = `<h2>${esc(run.project_name)}, plate ${run.plate}</h2><p>Printed on ${esc(run.printer_name)}. How did the parts come out?</p>
    <p class="muted small">Failed prints are counted again in the project, so the plate gets reprinted.</p>
    <div class="dlg-actions"><button type="button" class="danger" data-good="0">Failed: reprint</button><button type="button" class="primary" data-good="1">Parts OK</button></div>`;
  body.querySelector(".dlg-actions").onclick = async (e) => {
    const g = e.target.dataset.good;
    if (g === undefined) return;
    await api(`/api/runs/${run.id}/review`, { json: { good: g === "1" } });
    await api(`/api/printers/${printerId}/bed-clear`, { json: { clear: true } });
    $("#dlg").close();
    refresh();
  };
  openDialog();
}

function editPrinter(p) {
  p = p ? structuredClone(p) : { name: "", driver: "moonraker", address: "", slots: [], machine_preset: "" };
  p.slots = p.slots || [];
  const body = $("#dlg-body");
  // Work out brand/model of the current preset for the pickers.
  let brand = "", model = "";
  for (const [b, models] of Object.entries(state.vendors)) for (const [m, presets] of Object.entries(models)) if (presets.includes(p.machine_preset)) { brand = b; model = m; }
  const brands = Object.keys(state.vendors).sort();

  body.innerHTML = `
    <h2>${p.id ? "Edit printer" : "Add a printer"}</h2>
    <label class="field"><span>Name</span><input id="p-name" value="${esc(p.name)}" placeholder="e.g. Big Bambu"></label>
    <div class="settings-group">Which Orca profile slices for it?</div>
    <div class="row">
      <label class="field"><span>Brand</span><select id="p-brand"><option value="">Choose…</option>${brands.map((b) => `<option ${b === brand ? "selected" : ""}>${esc(b)}</option>`).join("")}</select></label>
      <label class="field"><span>Printer</span><select id="p-model"></select></label>
      <label class="field"><span>Nozzle / variant</span><select id="p-preset"></select></label>
    </div>
    <div id="p-machine" class="muted small"></div>
    ${p.id ? `<div class="row" style="justify-content:flex-start;margin-top:8px"><button type="button" id="p-printset" title="Its own quality profile to start with, support type, walls, infill…">Print settings for this printer…</button></div>` : ""}
    <div class="settings-group">How PrintHarbor talks to it</div>
    <div class="row">
      <label class="field"><span>Connection</span><select id="p-driver">${state.drivers.map((d) => `<option value="${d.name}" ${d.name === p.driver ? "selected" : ""}>${esc(d.label)}</option>`).join("")}</select></label>
      <label class="field"><span>Address</span><input id="p-address" value="${esc(p.address)}"></label>
    </div>
    <label class="field" id="p-key-wrap"><span id="p-key-label">API key</span><input id="p-key" value="${esc(p.api_key || "")}"></label>
    <label class="field" id="p-serial-wrap"><span>Serial number</span><input id="p-serial" value="${esc(p.serial || "")}" placeholder="Printer screen → Settings → Device"></label>
    <p class="muted small" id="p-help"></p>
    <div class="row" style="justify-content:flex-start"><button type="button" id="p-test">Test connection</button><span id="p-test-result" class="small"></span></div>
    <div id="p-capture-wrap" class="hidden" style="margin-top:10px">
      <div class="settings-group">Learn this printer's protocol (for new models like the H3C)</div>
      <p class="muted small">Record what the printer reports and the commands Bambu's own app sends it, so PrintHarbor can be taught to do the same (tool changes, multi-colour). Start it, then drive the printer from Bambu Studio or Handy, then stop and send the file.</p>
      <div class="row" style="justify-content:flex-start"><button type="button" id="p-cap-toggle">Start capture</button><span id="p-cap-status" class="small muted"></span><a id="p-cap-dl" class="btn small hidden" download>Download</a></div>
    </div>
    <div class="settings-group">Loaded filament</div>
    <p class="muted small">For printers that can't report it. PrintHarbor uses this to match project colors.</p>
    <div id="p-slots"></div>
    <button type="button" id="p-add-slot">Add slot</button>
    <div class="settings-group">Hardware</div>
    <label class="row" style="justify-content:flex-start"><input type="checkbox" id="p-hard" ${p.hardened_nozzle ? "checked" : ""} style="flex:0"> Hardened nozzle (for CF / GF filaments)</label>
    <label class="row" style="justify-content:flex-start"><input type="checkbox" id="p-enc" ${p.enclosed ? "checked" : ""} style="flex:0"> Enclosed</label>
    <div id="p-lvl-wrap">
      <div class="settings-group">Bed leveling</div>
      <label class="field"><span>Command to level the bed before a print (optional)</span>
        <input id="p-lvl" value="${esc(p.level_gcode || "")}" placeholder="">
        <small class="muted">Used when you switch on "Level the bed before printing" in the print dialog. It runs as part of the print's start. Firmware-specific — Klipper: <code>BED_MESH_CALIBRATE</code> · Marlin: <code>G29</code> · Prusa: <code>G80</code>. Leave blank to use the default for this printer type.</small></label>
      <p class="muted small" id="p-lvl-native" style="display:none">This printer levels through its own print controls, so there's no command to set — just use "Level the bed before printing" in the print dialog.</p>
    </div>
    <div class="settings-group">Camera helpers</div>
    <p class="muted small" style="margin:2px 0 8px">Bed-clear detection is automatic for any printer with a camera: once you press <b>This is my empty bed</b> on the printer's card (bed clear, printer idle), PrintHarbor marks the bed clear by itself after each print as soon as the camera sees the parts are off.${p.snapshot_url ? (p.has_empty_bed ? " <b>On</b> — this printer has been shown its empty bed." : " Not set up yet for this printer.") : " This printer has no camera."}</p>
    <label class="row" style="justify-content:flex-start"><input type="checkbox" id="p-ai" ${p.ai_detect ? "checked" : ""} style="flex:0"> AI failure detection (needs the Obico ML server in Settings)</label>
    <label class="row" style="justify-content:flex-start"><input type="checkbox" id="p-aipause" ${p.ai_pause ? "checked" : ""} style="flex:0"> …and pause the print when it's confident</label>
    <label class="field" style="margin-top:12px"><span>Camera snapshot URL (optional: only if the camera isn't found automatically)</span>
      <input id="p-cam" value="${esc(p.snapshot_url && !p.snapshot_url.startsWith("/api/") ? p.snapshot_url : "")}" placeholder="http://192.168.1.50/webcam/?action=snapshot"></label>
    <div class="dlg-actions">
      ${p.id ? `<button type="button" class="danger" id="p-del" style="margin-right:auto">Remove</button>` : ""}
      <button value="cancel">Cancel</button><button type="button" class="primary" id="p-save">Save</button>
    </div>`;

  if ($("#p-printset")) $("#p-printset").onclick = () => {
    $("#dlg").close();
    state.pdPrinter = p.id; // Settings opens its print defaults on this printer
    $("#view-settings").dataset.loaded = "";
    show("settings");
    setTimeout(() => { const b = document.querySelector('#set-nav [data-settab="printing"]'); if (b) b.click(); const c = $("#print-defaults"); if (c) c.scrollIntoView({ block: "start" }); }, 600);
  };
  const fillModels = () => {
    const b = $("#p-brand").value, models = Object.keys(state.vendors[b] || {}).sort();
    $("#p-model").innerHTML = models.map((m) => `<option ${m === model ? "selected" : ""}>${esc(m)}</option>`).join("");
    fillPresets();
  };
  const fillPresets = () => {
    const presets = (state.vendors[$("#p-brand").value] || {})[$("#p-model").value] || [];
    $("#p-preset").innerHTML = presets.map((n) => `<option ${n === p.machine_preset ? "selected" : ""}>${esc(n)}</option>`).join("");
    showMachine();
  };
  const showMachine = async () => {
    const n = $("#p-preset").value;
    if (!n) { $("#p-machine").textContent = ""; return; }
    try {
      const m = await api("/api/profiles/machine?name=" + enc(n));
      $("#p-machine").textContent = `Bed ${m.bed_x_mm} × ${m.bed_y_mm} mm, max height ${m.max_z_mm} mm, ${m.nozzle_mm} mm nozzle, ${m.extruders} extruder(s)`;
    } catch (e) { $("#p-machine").textContent = e.message; }
  };
  const fillDriver = () => {
    const d = state.drivers.find((x) => x.name === $("#p-driver").value) || {};
    $("#p-address").placeholder = d.address_hint || "";
    $("#p-key-wrap").classList.toggle("hidden", !d.key_label && !d.needs_key);
    $("#p-serial-wrap").classList.toggle("hidden", !d.needs_serial);
    $("#p-help").textContent = d.help || "";
    $("#p-key-label").textContent = d.key_label || "API key";
    // FlashForge/Bambu level through their own print controls (native flag /
    // always-on), so the g-code command field doesn't apply to them.
    const dv = $("#p-driver").value, nativeLvl = dv === "flashforge" || dv === "bambu";
    const cmdWrap = $("#p-lvl").closest("label"), nativeNote = $("#p-lvl-native");
    if (cmdWrap) cmdWrap.style.display = nativeLvl ? "none" : "";
    if (nativeNote) nativeNote.style.display = nativeLvl ? "" : "none";
    const lvl = $("#p-lvl"); if (lvl) lvl.placeholder = { moonraker: "BED_MESH_CALIBRATE (default)", elegoo: "BED_MESH_CALIBRATE (default)", prusalink: "G80 (default)" }[dv] || "e.g. G29 — blank = none";
    // Protocol capture is Bambu-only and needs a saved printer (for the stored code).
    $("#p-capture-wrap").classList.toggle("hidden", !($("#p-driver").value === "bambu" && p.id));
  };

  const printerPayload = () => ({ ...p, name: $("#p-name").value, driver: $("#p-driver").value, address: $("#p-address").value,
    api_key: $("#p-key").value, serial: $("#p-serial").value.trim(), machine_preset: $("#p-preset").value || "" });
  $("#p-test").onclick = async () => {
    const btn = $("#p-test"), out = $("#p-test-result");
    btn.disabled = true; out.className = "small muted"; out.textContent = "Trying to reach the printer…";
    try {
      const r = await api("/api/printers/test", { json: printerPayload() });
      out.className = "small " + (r.ok ? "ok-text" : "err");
      out.textContent = (r.ok ? "✓ " : "✕ ") + r.message;
    } catch (e) { out.className = "small err"; out.textContent = "✕ " + e.message; }
    btn.disabled = false;
  };

  let capTimer = null;
  const drawCapture = async () => {
    if (!p.id) return;
    let s; try { s = await api(`/api/printers/${p.id}/capture`); } catch (_) { return; }
    const tog = $("#p-cap-toggle"), st = $("#p-cap-status"), dl = $("#p-cap-dl");
    if (!tog) { clearInterval(capTimer); return; }
    tog.textContent = s.running ? "Stop capture" : "Start capture";
    tog.classList.toggle("danger", !!s.running);
    if (s.running) {
      st.textContent = `Recording — ${s.count} message(s). Drive the printer from Bambu's app now.`;
      dl.classList.add("hidden");
    } else if (s.file) {
      st.textContent = `Saved ${s.count || ""} message(s).`;
      dl.href = "/api/captures/" + enc(s.file); dl.classList.remove("hidden");
    } else { st.textContent = ""; dl.classList.add("hidden"); }
  };
  const capBtn = $("#p-cap-toggle");
  if (capBtn) capBtn.onclick = async () => {
    const running = capBtn.textContent.startsWith("Stop");
    try { await api(`/api/printers/${p.id}/capture${running ? "/stop" : ""}`, { method: "POST" }); }
    catch (e) { toast(e.message, true); return; }
    drawCapture();
  };
  if (p.id) { drawCapture(); capTimer = setInterval(drawCapture, 2000); $("#dlg").addEventListener("close", () => clearInterval(capTimer), { once: true }); }
  let spools = null;
  if (state.info.spoolman) api("/api/spoolman/spools").then((s) => { spools = s; drawSlots(); }).catch(() => {});
  const drawSlots = () => {
    $("#p-slots").innerHTML = p.slots.map((s, i) => `<div class="row" style="margin-bottom:6px">
      <span style="flex:0 0 22px">${i + 1}</span>
      ${spools ? `<select data-spool-slot="${i}" title="Spoolman spool"><option value="0">No spool</option>${spools.map((sp) => `<option value="${sp.id}" ${s.spool_id === sp.id ? "selected" : ""}>#${sp.id} ${esc(sp.vendor)} ${esc(sp.name)}</option>`).join("")}</select>` : ""}
      <input data-slot="${i}" data-f="type" value="${esc(s.type)}" placeholder="PLA">
      <input type="color" data-slot="${i}" data-f="color" value="${esc(s.color || "#ffffff")}" style="flex:0 0 44px">
      <button type="button" data-rm="${i}" style="flex:0">✕</button></div>`).join("");
  };
  $("#p-brand").onchange = () => { model = ""; fillModels(); };
  $("#p-model").onchange = fillPresets;
  $("#p-preset").onchange = showMachine;
  $("#p-driver").onchange = fillDriver;
  $("#p-add-slot").onclick = () => { p.slots.push({ index: p.slots.length + 1, type: "PLA", color: "#ffffff" }); drawSlots(); };
  $("#p-slots").oninput = (e) => { const i = e.target.dataset.slot; if (i !== undefined) p.slots[i][e.target.dataset.f] = e.target.value; };
  $("#p-slots").onchange = (e) => {
    const i = e.target.dataset.spoolSlot;
    if (i === undefined) return;
    const sp = (spools || []).find((x) => x.id === +e.target.value);
    p.slots[i].spool_id = sp ? sp.id : 0;
    if (sp) { p.slots[i].type = sp.material; p.slots[i].color = sp.color || p.slots[i].color; p.slots[i].name = `${sp.vendor} ${sp.name}`.trim(); }
    drawSlots();
  };
  $("#p-slots").onclick = (e) => { const i = e.target.dataset.rm; if (i !== undefined) { p.slots.splice(+i, 1); p.slots.forEach((s, j) => (s.index = j + 1)); drawSlots(); } };
  $("#p-save").onclick = async () => {
    const out = { ...p, name: $("#p-name").value, driver: $("#p-driver").value, address: $("#p-address").value, api_key: $("#p-key").value, serial: $("#p-serial").value.trim(),
      machine_preset: $("#p-preset").value || "", hardened_nozzle: $("#p-hard").checked, enclosed: $("#p-enc").checked, snapshot_url: $("#p-cam").value.trim(),
      level_gcode: $("#p-lvl").value.trim(), ai_detect: $("#p-ai").checked, ai_pause: $("#p-aipause").checked };
    delete out.status; delete out.capabilities;
    try { await api("/api/printers", { json: out }); $("#dlg").close(); refresh(); } catch (e) { toast(e.message, true); }
  };
  const del = $("#p-del");
  if (del) del.onclick = async () => {
    if (!confirm(`Remove ${p.name} from PrintHarbor?`)) return;
    await api("/api/printers/" + p.id, { method: "DELETE" }); $("#dlg").close(); refresh();
  };
  fillModels(); fillDriver(); drawSlots();
  openDialog();
}
