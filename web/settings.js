// Settings tab.
"use strict";

// What a plugin can touch, in plain words (keys match internal/plugins KnownPermissions).
const PERM_LABELS = {
  network: "reach the internet and your network",
  files: "read and write files on your computer",
  printers: "control your printers",
  camera: "use your printer cameras",
  events: "see when prints start, finish and fail",
  settings: "read your settings and site keys",
};
const permLabel = (p) => PERM_LABELS[p] || p;

async function renderSettings() {
  const root = $("#view-settings");
  if (root.dataset.loaded) return;
  root.dataset.loaded = "1";
  const [s, info] = await Promise.all([api("/api/settings"), api("/api/info")]);
  const field = (k, label, help, type = "text", ph = "") => `<label class="field"><span>${label}</span>
    <input data-k="${k}" type="${type}" value="${esc(Array.isArray(s[k]) ? s[k].join(", ") : s[k] ?? "")}" placeholder="${esc(ph)}">${help ? `<small class="muted">${help}</small>` : ""}</label>`;
  const boolField = (k, label, help) => `<label class="row" style="justify-content:flex-start;gap:8px;margin:2px 0"><input data-k="${k}" type="checkbox" ${s[k] ? "checked" : ""} style="flex:0"> <span>${label}</span></label>${help ? `<small class="muted" style="display:block;margin:0 0 6px 26px">${help}</small>` : ""}`;
  root.innerHTML = `
    <div class="toolbar"><h2>Settings</h2></div>
    <div class="set-nav" id="set-nav">
      <button data-settab="general" class="active">General</button>
      <button data-settab="printing">Printing</button>
      <button data-settab="library">Model sites &amp; Plugins</button>
      <button data-settab="ai">AI &amp; Assistant</button>
    </div>

    <section class="set-sec" data-sec="general">
      <div class="card">
        <h3>PrintHarbor</h3>
        <p class="small muted">Version ${esc(info.version)}${info.build_number ? `, build ${esc(info.build_number)}` : ""}, running since ${new Date(info.started_at).toLocaleString()}.</p>
        <p class="small muted">On Windows it lives in the system tray (the anchor icon by the clock): click it to open this page, right-click for Restart, Start with Windows and Quit.</p>
        ${info.can_restart ? `<button id="restart-btn">Restart PrintHarbor</button>
        <p class="small muted">Restarting also installs an update if one is waiting next to the program.</p>` : `<p class="small muted">Restart the PrintHarbor service to apply changes.</p>`}
        <div class="settings-group">Desktop app</div>
        <p class="small muted">Put a PrintHarbor icon on your Desktop and Start menu that you can pin to the taskbar.</p>
        <div class="row" style="justify-content:flex-start;gap:8px">
          <select id="sc-engine" style="width:auto">
            <option value="app">PrintHarbor app window (recommended)</option>
            <option value="edge">Browser app window (Edge/Chrome)</option>
            <option value="firefox">Firefox, with your session</option>
            <option value="default">Default browser</option>
          </select>
          <button id="sc-make">Create shortcut</button>
        </div>
        <p class="small" id="sc-result"></p>
      </div>
      <div class="card" id="backups">
        <h3>Backups</h3>
        <p class="small muted">Once a day PrintHarbor copies what can't be made again: your projects with their painting and settings, printers, print history, your profiles and these settings. Sliced files are left out; they can be sliced again. A file that hasn't changed isn't stored twice, so two weeks of backups take little more room than one.</p>
        ${field("backup_dir", "Backup folder", "Leave empty to keep them in PrintHarbor's data folder. A folder on another drive is safer: it survives this drive failing.", "text", "D:\\PrintHarbor backups")}
        ${field("backup_keep_days", "Keep backups for (days)", "0 turns backups off. The newest three are always kept.", "number")}
        <div class="row" style="justify-content:flex-start;gap:10px"><button type="button" id="bk-now">Back up now</button><span class="small muted" id="bk-info"></span></div>
      </div>
      <div class="card">
        <h3>Network</h3>
        ${field("mdns_name", "Name on your network", `Open PrintHarbor at http://&lt;name&gt;.local:${location.port || 80}. Use "-" to turn this off.`, "text", "harbor")}
        ${field("poll_seconds", "Check printers every (seconds)", "", "number")}
      </div>
    </section>

    <section class="set-sec hidden" data-sec="printing">
      <div class="card" id="print-defaults">
        <h3>Your print defaults</h3>
        <p class="small muted">Your own values in place of what the stock OrcaSlicer profiles say. Set them for <b>all printers</b>, or pick one printer and give it its own: a printer's own values go on top of the general ones. Anything left alone is not touched.</p>
        <p class="small muted">What still wins over these: a setting you change for one print (in the send screen or the slicer), and a setting saved in one of your own profiles.</p>
        <label class="field"><span>Set defaults for</span><select id="pd-scope"></select></label>
        <div id="pd-body"><p class="muted small">Loading…</p></div>
        <div class="row" style="justify-content:flex-start;gap:10px;margin-top:10px"><button type="button" class="primary" id="pd-save">Save these defaults</button><span class="small muted" id="pd-count"></span></div>
      </div>
      <div class="card" id="my-profiles">
        <h3>Your profiles</h3>
        <p class="small muted">Your own filament and quality profiles: a stock profile with the few things you change. They show up everywhere a profile is picked, for the printers the stock one fits.</p>
        <div class="seg" id="mp-type"><button type="button" data-mptype="filament" class="active">Filament</button><button type="button" data-mptype="process">Quality</button></div>
        <div id="mp-list" class="mp-list"></div>
        <button type="button" id="mp-new">New profile</button>
      </div>
      <div class="card">
        <h3>Printer cards</h3>
        ${boolField("show_printer_light", "Show the light on/off button on printer cards", "Off by default — most printers' lights are left alone. Turn this on to control them from the card.")}
      </div>
      <div class="card">
        <h3>OrcaSlicer</h3>
        <p class="small ${info.orca_found ? "ok-text" : "err"}">${info.orca_found ? "Found" : "Not found"}: ${esc(info.orca_path)}</p>
        <p class="small muted">${info.printer_profiles} printer, ${info.process_profiles} quality and ${info.filament_profiles} filament profiles loaded.</p>
        ${field("orca_path", "Orca program", "orca-slicer.exe on Windows")}
        ${field("orca_profiles_dir", "Orca profiles folder", "Orca's resources\\profiles folder")}
        ${field("max_concurrent_slices", "Slices at a time", "1 is right for small computers; raise it on a fast PC", "number")}
        ${field("job_keep_days", "Keep finished jobs for (days)", "Finished jobs are no longer listed in the Queue. The G-code sliced for them is kept on disk this many days, then removed so the disk doesn't fill up. 0 keeps it forever. Your projects, models and print history are never removed.", "number")}
      </div>
      <div class="card">
        <h3>Spoolman</h3>
        <p class="small muted">Optional. Connect your Spoolman to see your spools in the <b>Filament</b> tab, pick real spools for printer slots, and pin project colours to a spool.</p>
        ${field("spoolman_url", "Spoolman address", "", "text", "http://192.168.1.20:7912")}
        <div class="row gap" style="align-items:center;margin:4px 0 8px"><button class="btn tiny" id="spoolman-test" type="button">Test connection</button><span id="spoolman-status" class="small muted"></span></div>
        ${boolField("spoolman_writeback", "Update Spoolman after each print", "When a print finishes, PrintHarbor deducts the filament it used from the spools you pinned, so your weights stay right without touching them. Each pinned colour is charged what that colour used; a colour with no pinned spool is left alone, and so is a print that was cancelled or failed.")}
      </div>
      <div class="card">
        <h3>Slicing workers (optional)</h3>
        <p class="small muted">Let a faster PC on your network do the slicing. Install PrintHarbor there too and set the same worker key on both. This box sends jobs to the first worker that answers, and slices locally if none do.</p>
        ${field("workers", "Worker addresses (comma separated)", "", "text", "http://gaming-pc:7621")}
        ${field("worker_token", "Worker key", "Any shared secret; must match on both computers", "password")}
      </div>
    </section>

    <section class="set-sec hidden" data-sec="library">
      <div class="card" id="sites-settings">
        <h3>Model sites</h3>
        <p class="small muted">Models you import from Printables, Thingiverse, MakerWorld and Cults3D are saved as <code>models folder\\designer\\model\\files</code> and show up in your library.</p>
        ${field("model_dir", "Models folder", "Full path on this PC. Models already imported stay where they are.", "text", "D:\\3D Models")}
        <p class="small muted">Browse the sites in the app or your browser (Library → a site). Downloads you make there land in the models folder by themselves. Keys below are optional; they let PrintHarbor list a site's models inside the Library page too. Printables needs none.</p>
        ${field("thingiverse_token", "Thingiverse App Token", 'thingiverse.com/developers → Create an app → copy the "App Token"', "password")}
        ${field("makerworld_token", "MakerWorld token", 'Sign in at makerworld.com → browser developer tools → Cookies → copy the value of "token"', "password")}
        ${field("cults_user", "Cults3D nickname", "")}
        ${field("cults_key", "Cults3D API key", "cults3d.com/en/api/keys (Cults only lets apps read details; you download the files yourself)", "password")}
      </div>
      <div class="card" id="plugins-card"><h3>Plugins</h3>
        <p class="small muted">Browse model sites and add features as Python plugins — like Kodi add-ons. Turn them on or off, add your own, or remove them. <span id="plugins-dir"></span></p>
        <div id="plugins-list" class="small muted">Loading…</div>
        <div class="row" style="margin-top:8px;gap:8px;flex-wrap:wrap">
          <button id="plugins-install">Install plugin (.zip)…</button>
          <button id="plugins-reload">Reload</button>
          <input type="file" id="plugins-file" accept=".zip,application/zip" hidden>
        </div>
        <p class="small" id="plugins-msg"></p>
        <details class="plug-help" style="margin-top:8px"><summary class="small">Writing a plugin</summary>
          <div class="small muted" style="margin-top:6px">
            <p>A plugin is a folder with a <code>plugin.json</code> (its id, name, kind) and a Python file. Zip the folder and use <b>Install plugin</b>, or drop it straight into the plugins folder shown above and hit Reload.</p>
            <p><b>Site plugins</b> (<code>"kind":"site"</code>) add a model site PrintHarbor can browse, search and import from — <code>search</code>, <code>model</code>, <code>download</code>, <code>match</code>. The bundled <b>example</b> plugin is a template: copy it and change the id.</p>
            <p><b>Tool plugins</b> (<code>"kind":"tool"</code>) add a feature you run on demand — one <code>run(ctx, params)</code> function, with inputs declared under <code>params</code>. They show up in <b>Tools</b> below. The bundled <b>Filament Cost</b> plugin is a worked example.</p>
            <p><b>Printer plugins</b> (<code>"kind":"printer"</code>) add a printer PrintHarbor can control — <code>status</code> / <code>upload</code> / <code>pause</code> / <code>resume</code> / <code>cancel</code> (and optional <code>snapshot</code> / <code>light</code>) — so it shows up in <b>Add printer</b> with no new core code. The bundled <b>Virtual Printer (demo)</b> is a working example (turn it on to try it).</p>
            <p><b>Settings:</b> declare a <code>settings</code> list in <code>plugin.json</code> and PrintHarbor shows a form (the <b>Settings</b> button on a plugin); the values reach your code as <code>ctx["settings"]</code>.</p>
            <p><b>Updates:</b> add <code>"version"</code> and an <code>"update_url"</code> (an https <code>.zip</code>, or a JSON <code>{version, zip, notes}</code>) and the <b>Check for updates</b> button installs newer versions.</p>
            <p>Full reference with worked examples: <code>docs/plugins/</code> — <code>README.md</code>, <code>sites.md</code>, <code>tools.md</code>, <code>printers.md</code> in your PrintHarbor folder. A plugin runs Python on your computer, so only install ones you trust.</p>
          </div>
        </details>
      </div>
      <div class="card" id="tools-card"><h3>Tools</h3>
        <p class="small muted">Feature plugins you run on demand. Add more by installing a tool plugin above.</p>
        <div id="tools-list" class="small muted">Loading…</div>
      </div>
    </section>

    <section class="set-sec hidden" data-sec="ai">
      <div id="ai-settings" class="fbx"></div>
      <div class="card">
        <h3>Alerts</h3>
        <p class="small muted">Get finished prints and AI warnings, with a camera picture, in a Discord channel. Channel settings → Integrations → Webhooks → copy URL.</p>
        ${field("discord_webhook", "Discord webhook URL", "", "text", "https://discord.com/api/webhooks/…")}
        ${field("public_url", "This PrintHarbor's address for other machines (optional)", "Detected automatically; set it if the AI server can't reach PrintHarbor", "text", "http://192.168.1.10:7621")}
      </div>
      <div class="card">
        <h3>AI on another computer (optional)</h3>
        <p class="small muted">PrintHarbor runs AI failure detection itself (see the AI section above). Only if you already run Obico's ML API somewhere else, enter its address to use that instead.</p>
        ${field("obico_ml_url", "Obico ML API address", "", "text", "http://192.168.1.20:3333")}
      </div>
      <div class="card">
        <h3>Assistant (optional)</h3>
        <p class="small muted">Ask questions like "what's printing?" or "what's left on the robot project?". Uses a local model through Ollama; nothing leaves your network.</p>
        ${field("ollama_url", "Ollama address", "", "text", "http://192.168.1.30:11434")}
        ${field("ollama_model", "Model", "A model with tool support, e.g. llama3.1, qwen2.5", "text", "qwen2.5:7b")}
      </div>
    </section>

    <div class="set-save"><button class="primary" id="settings-save">Save settings</button><span class="muted small">PrintHarbor ${esc(info.version)}${info.build_number ? ` (build ${esc(info.build_number)})` : ""}</span></div>`;

  root.querySelectorAll("#set-nav [data-settab]").forEach((b) => b.onclick = () => {
    root.querySelectorAll("#set-nav [data-settab]").forEach((x) => x.classList.toggle("active", x === b));
    root.querySelectorAll(".set-sec").forEach((sec) => sec.classList.toggle("hidden", sec.dataset.sec !== b.dataset.settab));
  });

  // ---- Your print defaults: for all printers, or one printer's own
  const pdScope = $("#pd-scope");
  pdScope.innerHTML = `<option value="">All printers</option>` + state.printers.map((p) => `<option value="${esc(p.id)}">Only ${esc(p.name)}</option>`).join("");
  if (state.pdPrinter && state.printers.some((p) => p.id === state.pdPrinter)) pdScope.value = state.pdPrinter;
  state.pdPrinter = "";
  async function drawPrintDefaults() {
    const id = pdScope.value;
    let pd;
    try { pd = await api("/api/print-defaults" + (id ? "?printer=" + enc(id) : "")); } catch (e) { $("#pd-body").innerHTML = `<p class="err">${esc(e.message)}</p>`; return; }
    const general = pd.general || {};
    const row = (st) => {
      const v = pd.defaults[st.key] ?? "";
      const g = id ? general[st.key] ?? "" : "";
      const label = (val) => { const c = (st.choices || []).find((x) => x.value === val); return st.kind === "bool" ? (val === "1" ? "on" : "off") : c ? c.label : val; };
      const blank = g !== "" ? `General default (${label(g)})` : "Leave it to the profile";
      let ctl;
      if (st.kind === "choice") ctl = `<select data-pd="${esc(st.key)}"><option value="">${esc(blank)}</option>${st.choices.map((c) => `<option value="${esc(c.value)}" ${c.value === v ? "selected" : ""}>${esc(c.label)}</option>`).join("")}</select>`;
      else if (st.kind === "bool") ctl = `<select data-pd="${esc(st.key)}"><option value="">${esc(blank)}</option><option value="1" ${v === "1" ? "selected" : ""}>Always on</option><option value="0" ${v === "0" ? "selected" : ""}>Always off</option></select>`;
      else ctl = `<input data-pd="${esc(st.key)}" value="${esc(String(v).replace(/%$/, ""))}" placeholder="${esc(blank)}" inputmode="decimal">`;
      return `<div class="setting ${v !== "" ? "changed" : ""}"><label>${esc(st.label)}${st.unit ? ` <span class="muted">(${esc(st.unit)})</span>` : ""}${st.help ? `<span class="help">${esc(st.help)}</span>` : ""}</label>${ctl}</div>`;
    };
    const groups = (list) => { let out = "", g = ""; for (const st of list) { if (st.group !== g) { g = st.group; out += `<div class="settings-group">${esc(g)}</div>`; } out += row(st); } return out; };
    const set = pd.settings.filter((st) => (pd.defaults[st.key] ?? "") !== "").length + (pd.default_process ? 1 : 0);
    $("#pd-body").innerHTML = (id ? `<div class="setting ${pd.default_process ? "changed" : ""}"><label>Quality profile to start with<span class="help">The one picked first when you slice for this printer. You can still choose another each time.</span></label>
        <select id="pd-process"><option value="">The printer profile's own</option>${(pd.processes || []).map((n) => `<option ${n === pd.default_process ? "selected" : ""}>${esc(n)}</option>`).join("")}</select></div>` : "")
      + pd.settings.filter((st) => st.simple).map(row).join("")
      + `<details ${pd.settings.some((st) => !st.simple && (pd.defaults[st.key] ?? "") !== "") ? "open" : ""}><summary>More settings</summary>${groups(pd.settings.filter((st) => !st.simple))}</details>`;
    $("#pd-count").innerHTML = set ? `<b>${set}</b> set ${id ? "for this printer" : "for all printers"}.` : id ? "Nothing of its own: it follows the general defaults." : "None set: the profiles are used as they come.";
  }
  pdScope.onchange = drawPrintDefaults;
  $("#pd-save").onclick = async () => {
    const defaults = {};
    root.querySelectorAll("#pd-body [data-pd]").forEach((i) => { if (i.value.trim() !== "") defaults[i.dataset.pd] = i.value.trim(); });
    try {
      await api("/api/print-defaults", { json: { printer: pdScope.value, default_process: $("#pd-process") ? $("#pd-process").value : "", defaults } });
      toast(pdScope.value ? `Saved for ${pdScope.selectedOptions[0].textContent.replace(/^Only /, "")}.` : "Saved for all printers.");
      drawPrintDefaults();
    } catch (e) { toast(e.message, true); }
  };
  drawPrintDefaults();

  // ---- Your profiles: filament and quality profiles of your own
  let mpType = "filament";
  async function drawMyProfiles() {
    root.querySelectorAll("#mp-type [data-mptype]").forEach((b) => b.classList.toggle("active", b.dataset.mptype === mpType));
    let list = [];
    try { list = await api("/api/profiles/mine?type=" + mpType); } catch (_) {}
    const what = (m) => Object.entries(m.values).map(([k, v]) => { const c = state.catalog.find((x) => x.key === k); return `${c ? c.label : k} ${v}`; }).join(" · ");
    $("#mp-list").innerHTML = list.length ? list.map((m) => `<div class="mp-row"><div><b>${esc(m.name)}</b><div class="small muted">Based on ${esc(m.inherits)}${what(m) ? " · " + esc(what(m)) : ""}</div></div>
      <div class="row" style="flex:0;gap:8px"><button type="button" data-mpedit="${esc(m.name)}">Edit</button><button type="button" class="ghost-danger" data-mpdel="${esc(m.name)}">Remove</button></div></div>`).join("")
      : `<p class="muted small">You have no ${mpType === "filament" ? "filament" : "quality"} profiles of your own yet.</p>`;
    $("#mp-list").onclick = async (e) => {
      const t = e.target.dataset;
      if (t.mpedit) editMyProfile(mpType, list.find((m) => m.name === t.mpedit), drawMyProfiles);
      if (t.mpdel && confirm(`Remove your profile "${t.mpdel}"? Prints already sliced with it are not affected.`)) {
        try { await api(`/api/profiles/mine?type=${mpType}&name=${enc(t.mpdel)}`, { method: "DELETE" }); drawMyProfiles(); } catch (err) { toast(err.message, true); }
      }
    };
  }
  $("#mp-type").onclick = (e) => { const t = e.target.dataset.mptype; if (t) { mpType = t; drawMyProfiles(); } };
  $("#mp-new").onclick = () => editMyProfile(mpType, null, drawMyProfiles);
  drawMyProfiles();

  // ---- Backups
  async function drawBackups() {
    try {
      const b = await api("/api/backups");
      const last = b.backups[0];
      $("#bk-info").textContent = (b.keep_days > 0 ? "" : "Backups are off. ") + (last ? `Last backup ${new Date(last.at).toLocaleString()} · ${b.backups.length} kept in ${b.folder}` : `None yet. They go in ${b.folder}`);
    } catch (_) {}
  }
  $("#bk-now").onclick = async () => {
    $("#bk-now").disabled = true; $("#bk-info").textContent = "Backing up…";
    try { const b = await api("/api/backups", { method: "POST" }); toast(`Backed up ${b.files} files.`); } catch (e) { toast(e.message, true); }
    $("#bk-now").disabled = false; drawBackups();
  };
  drawBackups();

  const rb = $("#restart-btn");
  if (rb) rb.onclick = () => restartServer();

  // Render one settings/param field, and read a panel's values back.
  function pfInput(f, val) {
    const v = val != null ? val : (f.default != null ? f.default : "");
    const t = f.type || "text";
    const lab = esc(f.label || f.key), help = f.help ? `<small class="muted">${esc(f.help)}</small>` : "";
    if (t === "bool") return `<label class="field"><span>${lab}</span><input data-pk="${esc(f.key)}" data-pt="bool" type="checkbox" ${v ? "checked" : ""}>${help}</label>`;
    if (t === "select") return `<label class="field"><span>${lab}</span><select data-pk="${esc(f.key)}" data-pt="text">${(f.options || []).map((o) => `<option ${String(v) === String(o) ? "selected" : ""}>${esc(o)}</option>`).join("")}</select>${help}</label>`;
    const type = (t === "password" || f.secret) ? "password" : (t === "number" ? "number" : "text");
    return `<label class="field"><span>${lab}</span><input data-pk="${esc(f.key)}" data-pt="${t === "number" ? "number" : "text"}" type="${type}" value="${esc(v)}">${help}</label>`;
  }
  function pfRead(container) {
    const out = {};
    container.querySelectorAll("[data-pk]").forEach((i) => {
      out[i.dataset.pk] = i.dataset.pt === "bool" ? i.checked : i.dataset.pt === "number" ? (i.value === "" ? null : +i.value) : i.value;
    });
    return out;
  }

  async function drawPlugins() {
    const box = $("#plugins-list"); if (!box) return;
    let d; try { d = await api("/api/plugins"); } catch (_) { box.textContent = "Couldn't read plugins."; return; }
    $("#plugins-dir").textContent = d.dir ? `Add plugins in ${d.dir}` : "";
    const byId = {};
    const rows = (d.plugins || []).map((p) => {
      if (p.error) return `<div class="row" style="justify-content:space-between;gap:8px;align-items:center"><span><b>${esc(p.name || p.id)}</b> <span class="err">failed: ${esc(p.error)}</span></span>${p.id ? `<button class="btn tiny ghost-danger" data-pluguninstall="${esc(p.id)}">Remove</button>` : ""}</div>`;
      byId[p.id] = p;
      const hasSettings = (p.settings || []).length > 0;
      const canUpdate = !p.builtin && p.update_url;
      return `<div class="plug-row">
        <div class="row" style="justify-content:space-between;gap:10px;align-items:center">
          <span><b>${esc(p.name)}</b> <span class="pill">${esc(p.kind || "plugin")}</span>${p.version ? ` <span class="muted small">v${esc(p.version)}</span>` : ""} <span class="muted small">${esc((p.functions || []).join(", "))}</span>${p.builtin ? ' <span class="muted small">· built-in</span>' : ""}${p.needs_key ? ' <span class="muted small">· needs a key</span>' : ""}${p.off ? ' <span class="muted small">· off</span>' : ""}</span>
          <span class="row" style="gap:6px;flex:0 0 auto">
            ${hasSettings ? `<button class="btn tiny" data-plugsettings="${esc(p.id)}">Settings</button>` : ""}
            ${canUpdate ? `<button class="btn tiny" data-plugupdate="${esc(p.id)}">Check for updates</button>` : ""}
            ${p.builtin ? "" : `<button class="btn tiny ghost-danger" data-pluguninstall="${esc(p.id)}" data-name="${esc(p.name)}">Uninstall</button>`}
            <button class="btn tiny ${p.off ? "" : "primary"}" data-plugoff="${esc(p.id)}" data-cur="${p.off ? 1 : 0}" role="switch" aria-checked="${p.off ? "false" : "true"}">${p.off ? "Turn on" : "Turn off"}</button>
          </span>
        </div>
        <div class="plug-perms muted small">${(p.permissions || []).length ? "Can " + p.permissions.map((x) => esc(permLabel(x))).join(" · ") : "Runs locally only"}</div>
        <div class="plug-panel hidden" id="plug-panel-${esc(p.id)}"></div>
      </div>`;
    }).join("");
    box.innerHTML = (d.off ? `<div class="notice warn small">${esc(d.off)}</div>` : "") + (rows || `<p class="muted">No plugins loaded.</p>`);

    box.querySelectorAll("[data-plugoff]").forEach((b) => b.onclick = async () => {
      const p = byId[b.dataset.plugoff], turningOn = b.dataset.cur === "1";
      if (turningOn && p && (p.permissions || []).length) {
        const lines = p.permissions.map((x) => "• " + permLabel(x)).join("\n");
        if (!confirm(`Turn on "${p.name}"?\n\nThis plugin can:\n${lines}\n\nPlugins run on your computer — only turn on ones you trust.`)) return;
      }
      b.disabled = true;
      try { await api("/api/plugins/off", { json: { id: b.dataset.plugoff, off: b.dataset.cur !== "1" } }); } catch (e) { toast(e.message, true); }
      await drawPlugins(); drawTools();
      if (typeof sitesInit === "function" && $("#lib-src")) sitesInit();
    });
    box.querySelectorAll("[data-pluguninstall]").forEach((b) => b.onclick = async () => {
      if (!confirm(`Uninstall ${b.dataset.name || b.dataset.pluguninstall}? Its folder is deleted from the plugins folder.`)) return;
      b.disabled = true; $("#plugins-msg").className = "small muted"; $("#plugins-msg").textContent = "Removing…";
      try { await api("/api/plugins/uninstall", { json: { id: b.dataset.pluguninstall } }); $("#plugins-msg").textContent = ""; }
      catch (e) { $("#plugins-msg").className = "small err"; $("#plugins-msg").textContent = e.message; }
      await drawPlugins(); drawTools();
      if (typeof sitesInit === "function" && $("#lib-src")) sitesInit();
    });
    box.querySelectorAll("[data-plugsettings]").forEach((b) => b.onclick = () => {
      const p = byId[b.dataset.plugsettings], panel = $(`#plug-panel-${CSS.escape(p.id)}`);
      if (!panel.classList.contains("hidden") && panel.dataset.kind === "settings") { panel.classList.add("hidden"); return; }
      panel.dataset.kind = "settings";
      panel.innerHTML = `<div class="settings-group">${esc(p.name)} settings</div>${(p.settings || []).map((f) => pfInput(f, (p.values || {})[f.key])).join("")}<div class="row" style="gap:8px;margin-top:6px"><button class="btn tiny primary" data-savesettings>Save</button><span class="small" data-ssmsg></span></div>`;
      panel.classList.remove("hidden");
      panel.querySelector("[data-savesettings]").onclick = async (ev) => {
        ev.target.disabled = true; const msg = panel.querySelector("[data-ssmsg]");
        try { const r = await api("/api/plugins/settings", { json: { id: p.id, values: pfRead(panel) } }); p.values = r.values; msg.className = "small ok-text"; msg.textContent = "Saved."; }
        catch (e) { msg.className = "small err"; msg.textContent = e.message; }
        ev.target.disabled = false;
      };
    });
    box.querySelectorAll("[data-plugupdate]").forEach((b) => b.onclick = async () => {
      const p = byId[b.dataset.plugupdate], panel = $(`#plug-panel-${CSS.escape(p.id)}`);
      panel.dataset.kind = "update"; panel.classList.remove("hidden");
      panel.innerHTML = `<p class="small muted">Checking ${esc(p.update_url)}…</p>`;
      try {
        const r = await api("/api/plugins/check-update", { json: { id: p.id } });
        const u = r.update || {};
        if (!u.available) { panel.innerHTML = `<p class="small ok-text">You're up to date${u.latest ? ` (v${esc(u.latest)})` : ""}.</p>`; return; }
        panel.innerHTML = `<p class="small">Update available${u.latest ? `: v${esc(u.latest)}` : ""}${u.current ? ` (you have v${esc(u.current)})` : ""}.${u.notes ? ` ${esc(u.notes)}` : ""}</p><div class="row" style="gap:8px"><button class="btn tiny primary" data-doupdate>Install update</button><span class="small" data-upmsg></span></div>`;
        panel.querySelector("[data-doupdate]").onclick = async (ev) => {
          ev.target.disabled = true; const msg = panel.querySelector("[data-upmsg]");
          msg.className = "small muted"; msg.textContent = "Installing…";
          try { await api("/api/plugins/update", { json: { id: p.id } }); await drawPlugins(); drawTools(); if (typeof sitesInit === "function" && $("#lib-src")) sitesInit(); }
          catch (e) { msg.className = "small err"; msg.textContent = e.message; ev.target.disabled = false; }
        };
      } catch (e) { panel.innerHTML = `<p class="small err">${esc(e.message)}</p>`; }
    });
  }

  async function drawTools() {
    const box = $("#tools-list"); if (!box) return;
    let d; try { d = await api("/api/plugins/tools"); } catch (_) { box.textContent = "Couldn't read tools."; return; }
    const tools = d.tools || [];
    if (!tools.length) { box.innerHTML = `<p class="muted">No tool plugins are on. Tool plugins (<code>"kind":"tool"</code>) appear here.</p>`; return; }
    box.innerHTML = tools.map((t) => `<div class="plug-row">
      <div class="row" style="justify-content:space-between;gap:10px;align-items:center">
        <span><b>${esc(t.name)}</b>${t.version ? ` <span class="muted small">v${esc(t.version)}</span>` : ""}${t.note ? ` <span class="muted small">— ${esc(t.note)}</span>` : ""}</span>
        <button class="btn tiny" data-tooltoggle="${esc(t.id)}">Open</button>
      </div>
      <div class="plug-panel hidden" id="tool-panel-${esc(t.id)}"></div>
    </div>`).join("");
    const byId = {}; tools.forEach((t) => byId[t.id] = t);
    box.querySelectorAll("[data-tooltoggle]").forEach((b) => b.onclick = () => {
      const t = byId[b.dataset.tooltoggle], panel = $(`#tool-panel-${CSS.escape(t.id)}`);
      if (!panel.classList.contains("hidden")) { panel.classList.add("hidden"); return; }
      panel.innerHTML = `${(t.params || []).map((f) => pfInput(f, (t.values || {})[f.key])).join("") || '<p class="small muted">No inputs — just run it.</p>'}<div class="row" style="gap:8px;margin-top:6px"><button class="btn tiny primary" data-runtool>Run</button><span class="small" data-toolmsg></span></div><div class="tool-result small" data-toolresult></div>`;
      panel.classList.remove("hidden");
      panel.querySelector("[data-runtool]").onclick = async (ev) => {
        ev.target.disabled = true; const msg = panel.querySelector("[data-toolmsg]"), res = panel.querySelector("[data-toolresult]");
        msg.className = "small muted"; msg.textContent = "Running…"; res.innerHTML = "";
        try {
          const r = await api("/api/plugins/tools/run", { json: { id: t.id, params: pfRead(panel) } });
          msg.textContent = ""; const out = r.result || {};
          res.innerHTML = out.html ? out.html : `${out.title ? `<b>${esc(out.title)}</b><br>` : ""}${esc(out.text != null ? out.text : (typeof out === "object" ? JSON.stringify(out) : out))}`;
        } catch (e) { msg.className = "small err"; msg.textContent = e.message; }
        ev.target.disabled = false;
      };
    });
  }
  drawPlugins(); drawTools();
  const pr = $("#plugins-reload");
  if (pr) pr.onclick = async () => { pr.disabled = true; $("#plugins-list").textContent = "Reloading…"; try { await api("/api/plugins/reload", { method: "POST" }); } catch (_) {} await drawPlugins(); drawTools(); pr.disabled = false; };
  const pi = $("#plugins-install"), pf = $("#plugins-file");
  if (pi && pf) {
    pi.onclick = () => pf.click();
    pf.onchange = async () => {
      const file = pf.files && pf.files[0]; if (!file) return;
      const msg = $("#plugins-msg"); msg.className = "small muted"; msg.textContent = `Installing ${file.name}…`; pi.disabled = true;
      try {
        const fd = new FormData(); fd.append("file", file);
        const r = await fetch("/api/plugins/install", { method: "POST", body: fd });
        const j = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(j.error || `Install failed (${r.status})`);
        msg.className = "small ok-text"; msg.textContent = `Installed "${esc(j.id)}".`;
      } catch (e) { msg.className = "small err"; msg.textContent = e.message; }
      pf.value = ""; pi.disabled = false;
      await drawPlugins(); drawTools();
      if (typeof sitesInit === "function" && $("#lib-src")) sitesInit();
    };
  }
  const sc = $("#sc-make");
  if (sc) sc.onclick = async () => {
    const out = $("#sc-result");
    sc.disabled = true; out.className = "small muted"; out.textContent = "Creating the shortcut…";
    try {
      const r = await api("/api/shortcut", { json: { engine: $("#sc-engine").value } });
      out.className = "small " + (r.ok ? "ok-text" : "err");
      out.textContent = r.message;
    } catch (e) { out.className = "small err"; out.textContent = e.message; }
    sc.disabled = false;
  };
  const collect = () => {
    const out = { ...s };
    root.querySelectorAll("[data-k]").forEach((i) => {
      const k = i.dataset.k;
      out[k] = i.type === "checkbox" ? i.checked : Array.isArray(s[k]) ? i.value.split(",").map((x) => x.trim()).filter(Boolean) : i.type === "number" ? +i.value : i.value;
    });
    return out;
  };
  const stBtn = $("#spoolman-test");
  if (stBtn) stBtn.onclick = async () => {
    const stx = $("#spoolman-status");
    stBtn.disabled = true; stx.className = "small muted"; stx.textContent = "Saving & testing…";
    try {
      await api("/api/settings", { json: collect() });
      const r = await api("/api/spoolman/status");
      if (!r.configured) { stx.className = "small muted"; stx.textContent = "Enter a Spoolman address above first."; }
      else if (r.ok) { stx.className = "small ok-text"; stx.textContent = `✓ Connected — ${r.count} spool${r.count === 1 ? "" : "s"}`; }
      else { stx.className = "small err"; stx.textContent = `✗ ${r.error || "couldn't reach Spoolman"}`; }
    } catch (e) { stx.className = "small err"; stx.textContent = e.message; }
    stBtn.disabled = false;
  };
  $("#settings-save").onclick = async () => {
    const out = collect();
    try {
      const r = await api("/api/settings", { json: out });
      root.dataset.loaded = "";
      if (typeof sitesInit === "function" && $("#lib-src")) sitesInit();
      if (r.restart_needed && info.can_restart && confirm("Saved. Some changes need a restart. Restart PrintHarbor now?")) return restartServer();
      toast(r.restart_needed ? "Saved. Restart PrintHarbor for some changes to take effect." : "Saved.");
      renderSettings();
    } catch (e) { toast(e.message, true); }
  };
}

// Restarts the server, waits until the new copy answers, then reloads the page.
async function restartServer() {
  const before = (await api("/api/info").catch(() => ({}))).started_at;
  try { await api("/api/restart", { method: "POST" }); }
  catch (e) {
    if (!/sliced right now/.test(e.message) || !confirm(e.message + ". Restart anyway? The plate is sliced again afterwards.")) return toast(e.message, true);
    try { await api("/api/restart?force=1", { method: "POST" }); } catch (e2) { return toast(e2.message, true); }
  }
  const b = $("#banner");
  b.textContent = "Restarting PrintHarbor…";
  b.classList.remove("hidden");
  clearTimeout(toast.t);
  const t0 = Date.now();
  while (Date.now() - t0 < 90000) {
    await new Promise((r) => setTimeout(r, 1000));
    try {
      const i = await fetch("/api/info", { cache: "no-store" }).then((r) => r.json());
      if (i.started_at && i.started_at !== before) { location.reload(); return; }
    } catch { /* still restarting */ }
  }
  toast("PrintHarbor hasn't come back after 90 seconds. Check the tray icon, or start it again.", true);
}

// Make or change one of your own profiles (type "filament" or "process"): a
// stock profile plus the settings you change. Blank fields keep the stock value,
// which is shown greyed in each box.
async function editMyProfile(type, mine, done) {
  const body = $("#dlg-body");
  const settings = state.catalog.filter((s) => s.for === type);
  const noun = type === "filament" ? "filament" : "quality";
  body.innerHTML = `<h2>${mine ? "Edit" : "New"} ${noun} profile</h2>
    <label class="field"><span>Name</span><input id="mp-name" value="${esc(mine ? mine.name : "")}" placeholder="${type === "filament" ? "e.g. Silk PLA, red brand" : "e.g. Strong parts"}" ${mine ? "readonly" : ""}></label>
    ${mine ? `<p class="small muted">Based on ${esc(mine.inherits)}.</p>` : `
    <label class="field"><span>For which printer?</span><select id="mp-printer">${state.printers.map((p) => `<option value="${esc(p.id)}">${esc(p.name)}</option>`).join("")}</select></label>
    <label class="field"><span>Based on</span><input id="mp-find" placeholder="Type to narrow the list, e.g. PETG" autocomplete="off"><select id="mp-base" size="6" style="margin-top:6px;height:auto"></select></label>`}
    <div class="settings-group">What it changes</div>
    <div id="mp-rows"><p class="muted small">Pick what it is based on.</p></div>
    <div class="dlg-actions"><button value="cancel">Cancel</button><button type="button" class="primary" id="mp-save">Save profile</button></div>`;
  openDialog();
  let base = mine ? mine.inherits : "", all = [];
  const drawRows = async () => {
    if (!base) return;
    let stock = {};
    try { stock = await api(`/api/profiles/resolve?type=${type}&name=${enc(base)}`); } catch (_) {}
    const cur = (mine && mine.values) || {};
    let g = "";
    $("#mp-rows").innerHTML = settings.map((st) => {
      const v = cur[st.key] ?? "", sv = first(stock[st.key]);
      let ctl;
      if (st.kind === "choice") ctl = `<select data-mp="${esc(st.key)}"><option value="">As in the stock profile${sv ? ` (${esc((st.choices.find((c) => c.value === sv) || { label: sv }).label)})` : ""}</option>${st.choices.map((c) => `<option value="${esc(c.value)}" ${c.value === v ? "selected" : ""}>${esc(c.label)}</option>`).join("")}</select>`;
      else if (st.kind === "bool") ctl = `<select data-mp="${esc(st.key)}"><option value="">As in the stock profile (${sv === "1" ? "on" : "off"})</option><option value="1" ${v === "1" ? "selected" : ""}>On</option><option value="0" ${v === "0" ? "selected" : ""}>Off</option></select>`;
      else ctl = `<input data-mp="${esc(st.key)}" value="${esc(String(v).replace(/%$/, ""))}" placeholder="${esc(String(sv).replace(/%$/, "") || "stock")}" inputmode="decimal">`;
      const head = st.group !== g && type !== "filament" ? `<div class="settings-group">${esc(g = st.group)}</div>` : "";
      return head + `<div class="setting ${v !== "" ? "changed" : ""}"><label>${esc(st.label)}${st.unit ? ` <span class="muted">(${esc(st.unit)})</span>` : ""}${st.help ? `<span class="help">${esc(st.help)}</span>` : ""}</label>${ctl}</div>`;
    }).join("");
  };
  if (!mine) {
    const fill = () => {
      const q = $("#mp-find").value.toLowerCase().trim();
      $("#mp-base").innerHTML = all.filter((n) => !q || n.toLowerCase().includes(q)).map((n) => `<option ${n === base ? "selected" : ""}>${esc(n)}</option>`).join("");
    };
    const load = async () => {
      const pr = state.printers.find((p) => p.id === $("#mp-printer").value);
      try { all = (await api(`/api/profiles/compatible?type=${type}&machine=${enc(pr ? pr.machine_preset : "")}`)).map((x) => x.name).sort(); } catch (_) { all = []; }
      fill();
    };
    $("#mp-printer").onchange = load;
    $("#mp-find").oninput = fill;
    $("#mp-base").onchange = () => { base = $("#mp-base").value; drawRows(); };
    await load();
  } else drawRows();
  $("#mp-save").onclick = async () => {
    const name = $("#mp-name").value.trim();
    if (!name) return toast("Give the profile a name.", true);
    if (!base) return toast("Pick the profile it is based on.", true);
    const overrides = {};
    body.querySelectorAll("[data-mp]").forEach((i) => { const st = settings.find((x) => x.key === i.dataset.mp); if (i.value.trim() !== "") overrides[i.dataset.mp] = i.value.trim() + (st.kind === "percent" && !/%$/.test(i.value.trim()) ? "%" : ""); });
    try {
      await api("/api/profiles/save", { json: { type, name, inherits: base, overrides } });
      $("#dlg").close();
      toast(`Saved "${name}".`);
      if (done) done();
    } catch (e) { toast(e.message, true); }
  };
}
