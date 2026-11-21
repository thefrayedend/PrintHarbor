// Filament tab: your Spoolman spools, shown the PrintHarbor way — colour, how
// much is left, and a nudge to keep it current automatically. Spoolman is
// optional; without it this tab explains what it's for.
"use strict";

const _fil = { sig: "", loading: false, at: 0, url: "" };

async function renderFilament() {
  const root = $("#view-filament");
  if (!root) return;
  if (!state.info || !state.info.spoolman) {
    if (root.dataset.mode !== "off") {
      root.dataset.mode = "off";
      root.innerHTML = `<div class="fil-empty">
        <h2>Filament</h2>
        <p class="muted">Connect <b>Spoolman</b> to see your spools here — colour, material and how much filament is left on each roll. Spoolman is a free, self-hosted filament tracker lots of makers use; PrintHarbor can show it natively and keep it up to date for you.</p>
        <button class="btn primary" onclick="show('settings')">Set up Spoolman in Settings</button>
      </div>`;
    }
    return;
  }
  const now = Date.now();
  if (_fil.loading || (_fil.sig && now - _fil.at < 8000)) return; // keep what's shown
  _fil.loading = true;
  try {
    const st = await api("/api/spoolman/status");
    _fil.url = st.url || "";
    if (st.ok === false) {
      root.dataset.mode = "err";
      root.innerHTML = `<div class="fil-empty"><h2>Filament</h2><p class="err">Couldn't reach Spoolman: ${esc(st.error || "no answer")}</p>
        <span class="row gap"><button class="btn" onclick="filamentRefresh()">Try again</button><button class="btn" onclick="show('settings')">Settings</button></span></div>`;
      _fil.loading = false; _fil.at = now; _fil.sig = "";
      return;
    }
    const spools = await api("/api/spoolman/spools");
    _fil.loading = false; _fil.at = now;
    const sig = JSON.stringify(spools) + "|" + (state.info.spoolman_writeback ? 1 : 0);
    if (sig === _fil.sig && root.dataset.mode === "list") return; // unchanged — don't rebuild
    _fil.sig = sig; root.dataset.mode = "list";
    drawFilament(root, spools);
  } catch (e) {
    _fil.loading = false;
    root.dataset.mode = "err";
    root.innerHTML = `<div class="fil-empty"><h2>Filament</h2><p class="err">${esc(e.message)}</p><button class="btn" onclick="filamentRefresh()">Try again</button></div>`;
  }
}

function filamentRefresh() { _fil.sig = ""; _fil.at = 0; renderFilament(); }

function drawFilament(root, spools) {
  if (!spools.length) {
    root.innerHTML = `<div class="fil-empty"><h2>Filament</h2><p class="muted">Spoolman is connected, but has no active spools yet. Add some in Spoolman and they'll appear here.</p>
      <span class="row gap"><button class="btn tiny" onclick="filamentRefresh()">Refresh</button>${_fil.url ? `<a class="btn tiny" href="${esc(_fil.url)}" target="_blank" rel="noopener">Open Spoolman</a>` : ""}</span></div>`;
    return;
  }
  const total = spools.reduce((n, s) => n + (s.remaining_g || 0), 0);
  const list = spools.slice().sort((a, b) =>
    (a.material || "").localeCompare(b.material || "") || (a.vendor || "").localeCompare(b.vendor || "") || (a.name || "").localeCompare(b.name || ""));
  const writeback = !!state.info.spoolman_writeback;
  root.innerHTML = `<div class="fil-head">
      <h2>Filament <span class="muted small">${list.length} spool${list.length !== 1 ? "s" : ""} · ${fmtG(total)} total</span></h2>
      <span class="row gap">
        <button class="btn tiny" onclick="filamentRefresh()">Refresh</button>
        ${_fil.url ? `<a class="btn tiny" href="${esc(_fil.url)}" target="_blank" rel="noopener">Open Spoolman</a>` : ""}
      </span>
    </div>
    <div class="fil-grid">${list.map(filCard).join("")}</div>
    <p class="hint">${writeback
      ? "PrintHarbor deducts what each finished print used from the spools you pinned, colour by colour. Pin a project's colour to a spool from the project screen."
      : "Pin a project's colour to a spool from the project screen, and turn on <b>Update Spoolman after each print</b> in Settings to keep these weights current without touching them."}</p>`;
}

function filCard(s) {
  const rem = s.remaining_g || 0, cap = s.capacity_g || 0;
  const frac = cap > 0 ? Math.max(0, Math.min(1, rem / cap)) : null;
  const low = (frac !== null && frac < 0.15) || (cap <= 0 && rem > 0 && rem < 100);
  const empty = rem <= 0;
  const bar = frac !== null
    ? `<div class="fil-bar ${low ? "low" : ""}"><div style="width:${Math.round(frac * 100)}%"></div></div>`
    : "";
  const remTxt = empty ? "empty" : `${fmtG(rem)} left${cap > 0 ? ` of ${fmtG(cap)}` : ""}`;
  return `<div class="card fil-spool ${low ? "low" : ""} ${empty ? "empty" : ""}">
    <div class="fil-top">
      <span class="fil-swatch" style="background:${esc(s.color || "#9aa0a6")}"></span>
      <div class="fil-id">
        <b>${esc((s.vendor ? s.vendor + " " : "") + (s.name || s.material || ("Spool #" + s.id)))}</b>
        <div class="muted small">${esc(s.material || "")}${s.location ? " · " + esc(s.location) : ""} · #${s.id}</div>
      </div>
      ${s.material ? `<span class="pill">${esc(s.material)}</span>` : ""}
    </div>
    ${bar}
    <div class="fil-rem ${low ? "low" : ""}">${esc(remTxt)}${low ? ` <span class="warn">· running low</span>` : ""}</div>
  </div>`;
}

function fmtG(g) { return g >= 1000 ? (g / 1000).toFixed(g >= 10000 ? 0 : 2).replace(/\.00$/, "") + " kg" : Math.round(g) + " g"; }
