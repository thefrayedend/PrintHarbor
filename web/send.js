// "Slice & send": the easy screen for sending a plate to a printer. Picks the
// quality and filament presets, matches project colours to the printer's
// filament slots, and puts the job in the queue.
// Part of the PrintHarbor web UI; see app.js for how the scripts fit together.
"use strict";

// "Level the bed before printing" remembers your last choice across prints.
function levelPref() { try { return localStorage.getItem("ph-level-before-print") === "1"; } catch { return false; } }
function setLevelPref(on) { try { localStorage.setItem("ph-level-before-print", on ? "1" : "0"); } catch { /* private mode */ } }
// The toggle row (sticky), using what the server says this printer can do:
// level_always → it levels every print on its own (Bambu); can_level + level_cmd
// → a firmware command; can_level with no command → native flag (FlashForge);
// not can_level → needs a command set in Edit.
function levelToggleHTML(printer) {
  if (!printer) return "";
  if (printer.level_always) return `<p class="muted small" style="margin-top:10px">${esc(printer.name)} levels the bed before every print automatically.</p>`;
  if (!printer.can_level) return `<p class="muted small" style="margin-top:8px">To level the bed before printing on ${esc(printer.name)}, set its leveling command in the printer's <b>Edit</b>.</p>`;
  const cmd = printer.level_cmd ? ` <span class="muted small">(${esc(printer.level_cmd)})</span>` : "";
  return `<div class="lvl-row"><span>Level the bed before printing${cmd}</span><label class="switch"><input type="checkbox" id="lvl-before" ${levelPref() ? "checked" : ""}><span></span></label></div>`;
}

async function prepareSend(pl, fit) {
  if (savedPlate(pl)) return sendSaved(pl, fit);
  const p = state.project;
  const body = $("#dlg-body");
  body.innerHTML = `<p class="muted">Loading Orca profiles…</p>`;
  let sug;
  try {
    sug = await api(`/api/projects/${p.id}/suggest?printer=${enc(fit.printer_id)}`);
  } catch (e) {
    body.innerHTML = `<p class="err">${esc(e.message)}</p><div class="dlg-actions"><button value="cancel">Close</button></div>`;
    return;
  }
  const printer = state.printers.find((x) => x.id === fit.printer_id);
  const overrides = {};
  let base = {};

  const options = (list, sel) => list.map((n) => `<option ${n === sel ? "selected" : ""}>${esc(n)}</option>`).join("");
  const mapFor = (i) => (fit.mapping || []).find((m) => m.filament === i);

  body.innerHTML = `
    <h2>${esc(pl.name)} → ${esc(fit.printer_name)}</h2>
    <p class="muted small">Printer profile: ${esc(sug.machine_preset)}</p>
    ${fit.reasons.filter((r) => r.kind !== "good").length ? `<ul class="reasons">${fit.reasons.filter((r) => r.kind !== "good").map((r) => `<li class="r-${r.kind}">${esc(r.text)}</li>`).join("")}</ul>` : ""}

    <div class="settings-group">Quality preset</div>
    <select id="process">${options(sug.processes, sug.process_preset)}</select>

    <div class="settings-group">Filaments</div>
    ${p.data.filaments.map((f, i) => {
      const m = mapFor(f.index);
      const used = pl.filaments.includes(f.index) || pl.painted;
      const slots = printerSlots(printer);
      const slotSel = slots.length && used ? `<select data-slot-for="${f.index}" class="slot-select" title="Which slot on the printer">
          <option value="0">Slot: printer default</option>${slots.map((sl) => `<option value="${sl.index}" ${m && m.slot === sl.index ? "selected" : ""}>Slot ${sl.index === 99 ? "ext" : sl.index}: ${esc(sl.type)} ${esc(colorName(sl.color))}</option>`).join("")}</select>` : "";
      return `<div class="filrole" ${used ? "" : 'style="opacity:.55"'}>${swatch(f.color)}
        <div><div class="row"><select data-fil="${i}">${options(sug.filament_choices, sug.filament_presets[i])}</select>${slotSel}</div>
        <div class="muted small">Project filament ${f.index}: ${esc(f.type)}${used ? "" : " · not used on this plate"}</div></div></div>`;
    }).join("")}

    <div class="settings-group">Common settings</div>
    <div id="simple"></div>
    <details><summary>More settings</summary><div id="more"></div></details>
    <details><summary>All Orca settings (search)</summary>
      <input id="search" placeholder="Search any Orca setting, e.g. ironing, seam, z_hop" style="margin-top:8px">
      <div id="all" class="small"></div></details>

    ${levelToggleHTML(printer)}
    <div class="dlg-actions">
      <button value="cancel">Cancel</button>
      <button id="save-preset" type="button">Save as my preset</button>
      <button id="slice-only" type="button" class="primary" title="Slices it and shows you the result, supports and all. You send it to the printer from the preview.">Slice &amp; preview</button>
      <button id="upload-only" type="button" ${printer && printer.capabilities.upload ? "" : "disabled"}>Slice &amp; upload</button>
      <button id="go" type="button" title="Sends it to the printer as soon as it is sliced, without showing you the preview first" ${printer && printer.capabilities.start_print ? "" : "disabled"}>Slice &amp; print</button>
    </div>`;

  const settingRow = (s) => {
    const cur = overrides[s.key] ?? first(base[s.key]);
    let ctl;
    // Keys missing from the preset use Orca's built-in default; say so instead of guessing.
    if (s.kind === "bool") ctl = `<input type="checkbox" data-key="${s.key}" ${cur === "1" || cur === "true" ? "checked" : ""}>`;
    else if (s.kind === "choice") ctl = `<select data-key="${s.key}">${cur === "" ? `<option value="" selected>Orca default</option>` : ""}${s.choices.map((c) => `<option value="${esc(c.value)}" ${c.value === cur ? "selected" : ""}>${esc(c.label)}</option>`).join("")}${cur === "" || s.choices.some((c) => c.value === cur) ? "" : `<option selected value="${esc(cur)}">${esc(cur)}</option>`}</select>`;
    else ctl = `<input data-key="${s.key}" value="${esc(cur)}" placeholder="Orca default" ${s.kind === "number" ? 'inputmode="decimal"' : ""}>`;
    return `<div class="setting ${s.key in overrides ? "changed" : ""}"><label>${esc(s.label)}${s.unit ? ` <span class="muted">(${esc(s.unit)})</span>` : ""}${s.help ? `<span class="help">${esc(s.help)}</span>` : ""}</label>${ctl}</div>`;
  };
  const grouped = (list) => {
    let out = "", g = "";
    for (const s of list) { if (s.group !== g) { g = s.group; out += `<div class="settings-group">${esc(g)}</div>`; } out += settingRow(s); }
    return out;
  };
  const procSettings = () => state.catalog.filter((s) => s.for === "process");

  async function loadBase() {
    const name = $("#process").value;
    base = await api(`/api/profiles/resolve?type=process&name=${enc(name)}`);
    // Filament settings show the first filament's values.
    const f0 = body.querySelector("[data-fil]");
    if (f0) Object.assign(base, Object.fromEntries(Object.entries(await api(`/api/profiles/resolve?type=filament&name=${enc(f0.value)}`))
      .filter(([k]) => state.catalog.some((s) => s.for === "filament" && s.key === k))));
    drawSettings();
  }
  function drawSettings() {
    $("#simple").innerHTML = state.catalog.filter((s) => s.simple).map(settingRow).join("");
    $("#more").innerHTML = grouped(state.catalog.filter((s) => !s.simple));
    drawAll();
  }
  function drawAll() {
    const q = ($("#search").value || "").toLowerCase().trim();
    if (q.length < 2) { $("#all").innerHTML = `<p class="muted">Type at least 2 letters. Changes here go straight into Orca's process settings.</p>`; return; }
    const keys = Object.keys(base).filter((k) => k.includes(q) && !["name", "type", "from", "inherits", "instantiation"].includes(k)).sort().slice(0, 60);
    $("#all").innerHTML = keys.map((k) => `<div class="setting ${k in overrides ? "changed" : ""}"><label><code>${esc(k)}</code></label>
      <input data-key="${esc(k)}" value="${esc(overrides[k] ?? (Array.isArray(base[k]) ? base[k].join(",") : base[k]))}"></div>`).join("") || `<p class="muted">No setting matches.</p>`;
  }

  body.addEventListener("change", (e) => {
    const k = e.target.dataset.key;
    if (!k) return;
    const v = e.target.type === "checkbox" ? (e.target.checked ? "1" : "0") : e.target.value;
    if (v === first(base[k])) delete overrides[k]; else overrides[k] = v;
    e.target.closest(".setting")?.classList.toggle("changed", k in overrides);
  });
  $("#process").onchange = loadBase;
  $("#search").oninput = drawAll;

  const job = (send, start) => ({
    project_id: p.id, plate: pl.index, printer_id: fit.printer_id, machine_preset: sug.machine_preset,
    process_preset: $("#process").value,
    filament_presets: [...body.querySelectorAll("[data-fil]")].map((s) => s.value),
    overrides, send, start, level: !!($("#lvl-before") && $("#lvl-before").checked),
    slot_map: p.data.filaments.map((f) => { const sel = body.querySelector(`[data-slot-for="${f.index}"]`); if (sel) return +sel.value; const m = (fit.mapping || []).find((x) => x.filament === f.index); return m ? m.slot : 0; }),
  });
  const submit = async (send, start) => {
    if ($("#lvl-before")) setLevelPref($("#lvl-before").checked);
    try {
      const made = await api("/api/jobs", { json: job(send, start) });
      $("#dlg").close();
      const lvl = $("#lvl-before") && $("#lvl-before").checked ? " It levels the bed first." : "";
      state.previewJob = { id: made.id, title: `${p.name}: ${pl.name} → ${fit.printer_name}`, preview: !send, since: Date.now() };
      checkPreviewJob();
      toast(start ? `Slicing ${pl.name} for ${fit.printer_name}. It will start printing when done.${lvl}` : send ? `Slicing ${pl.name}. Check the queue for progress.` : `Slicing ${pl.name}. The preview opens when it is ready; you send it to ${fit.printer_name} from there.`);
      refresh();
    } catch (e) { toast(e.message, true); }
  };
  $("#go").onclick = () => submit(true, true);
  $("#upload-only").onclick = () => submit(true, false);
  $("#slice-only").onclick = () => submit(false, false);
  $("#save-preset").onclick = async () => {
    if (!Object.keys(overrides).length) return toast("Change a setting first, then save it as your preset.");
    const name = prompt("Name for your preset:", $("#process").value.replace(/ @.*$/, "") + " (mine)");
    if (!name) return;
    const procOv = Object.fromEntries(Object.entries(overrides).filter(([k]) => !state.catalog.some((s) => s.key === k && s.for === "filament")));
    try {
      await api("/api/profiles/save", { json: { type: "process", name, inherits: $("#process").value, overrides: procOv } });
      toast(`Saved "${name}". It will show up in the quality list for compatible printers.`);
    } catch (e) { toast(e.message, true); }
  };
  await loadBase();
}

function printerSlots(pr) { if (!pr) return []; const s = pr.status && pr.status.slots && pr.status.slots.length ? pr.status.slots : pr.slots || []; return s; }

// Rough color names for slot labels (matches the server's list).
const COLOR_NAMES = [["black", 0, 0, 0], ["white", 255, 255, 255], ["grey", 128, 128, 128], ["silver", 192, 192, 192], ["red", 224, 32, 32], ["orange", 255, 140, 0], ["yellow", 255, 224, 0], ["gold", 212, 175, 55], ["green", 32, 160, 64], ["dark green", 11, 93, 30], ["blue", 30, 91, 216], ["navy", 10, 31, 92], ["cyan", 32, 192, 224], ["purple", 123, 47, 190], ["pink", 255, 127, 176], ["brown", 123, 74, 38], ["beige", 232, 216, 176]];
function colorName(hex) {
  const m = /^#?([0-9a-f]{6})/i.exec(hex || ""); if (!m) return "";
  const v = parseInt(m[1], 16), r = v >> 16, g = (v >> 8) & 255, b = v & 255;
  let best = "", bd = Infinity;
  for (const [n, R, G, B] of COLOR_NAMES) { const rm = (r + R) / 2, d = (2 + rm / 256) * (r - R) ** 2 + 4 * (g - G) ** 2 + (2 + (255 - rm) / 256) * (b - B) ** 2; if (d < bd) { bd = d; best = n; } }
  return best;
}

function first(v) { return Array.isArray(v) ? String(v[0] ?? "") : String(v ?? ""); }
