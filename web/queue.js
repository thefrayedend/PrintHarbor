// Queue page: what is being worked on (waiting, slicing, sending, printing) and
// what failed, newest first. Finished jobs are not listed: the server says which
// jobs belong here (in_queue) and which one a printer is on right now (printing).
// Part of the PrintHarbor web UI; see app.js for how the scripts fit together.
"use strict";

function renderJobs() {
  $("#queue-history").onclick = showCompletedPrints;
  $("#queue-info").textContent = `Up to ${state.info.max_concurrent_slices || 1} slice(s) at a time`;
  const jobs = state.jobs.filter((j) => j.in_queue).reverse();
  const camOf = (j) => { const p = state.printers.find((x) => x.id === j.printer_id); return p && p.status && p.status.snapshot_url ? p.id : ""; };
  setHTML($("#job-list"), jobs.length ? jobs.map((j) => {
    const label = { queued: "Waiting", slicing: "Slicing…", sending: "Sending…", done: j.printing ? "Printing" : (j.send ? "Uploaded" : "Sliced"), failed: "Failed", canceled: "Canceled" }[j.state];
    const cam = camOf(j);
    return `<div class="card job"><div class="job-info">
      <h3>${esc(j.project_name)}: plate ${j.plate} → ${esc(j.printer_name)}</h3>
      <div class="muted small">${esc(j.process_preset)} · ${new Date(j.created).toLocaleTimeString()} · <strong>${label}</strong>${j.print_time_sec ? ` · ${dur(j.print_time_sec)} print` : ""}${j.filament_g ? ` · ${j.filament_g.toFixed(1)} g` : ""}</div>
      ${j.error ? `<div class="err">${esc(j.error)}</div>` : ""}${j.note && j.state !== "failed" ? `<div class="notice warn small job-note">${esc(j.note)}</div>` : ""}</div>
      ${cam ? `<button class="job-cam" data-opencam="${esc(cam)}" title="Open ${esc(j.printer_name)}"><img alt="${esc(j.printer_name)} camera"><span class="job-cam-badge">${esc(j.printer_name)}</span></button>` : ""}
      <div class="row" style="flex:0">
        ${["queued", "slicing", "sending"].includes(j.state) ? `<button data-cancel="${j.id}">Cancel</button>` : ""}
        ${["failed", "canceled"].includes(j.state) ? `<button data-retry="${j.id}">Retry</button>` : ""}
        ${j.gcode ? `<button data-preview="${j.id}" ${j.state === "done" && !j.send ? 'class="primary" title="Look at it, then send it to the printer from the preview"' : ""}>${j.state === "done" && !j.send ? "Preview &amp; print" : "Preview"}</button>` : ""}
        ${j.gcode || j.sliced_3mf ? `<a class="btn" href="/api/jobs/${j.id}/download">Download</a>` : ""}
        ${["done", "failed", "canceled"].includes(j.state) && !j.printing ? `<button class="ghost-danger" data-delete="${j.id}">Delete</button>` : ""}
      </div></div>`;
  }).join("") : `<p class="muted">Nothing in the queue.</p>`);
  drawQueueCams();
  $("#job-list").onclick = async (e) => {
    const cam = e.target.closest("[data-opencam]");
    if (cam) return openPrinter(cam.dataset.opencam);
    const t = e.target.dataset;
    try {
      if (t.cancel) await api(`/api/jobs/${t.cancel}/cancel`, { method: "POST" });
      if (t.retry) await api(`/api/jobs/${t.retry}/retry`, { method: "POST" });
      if (t.delete) { if (!confirm("Remove this job from the queue?")) return; await api(`/api/jobs/${t.delete}`, { method: "DELETE" }); }
      if (t.preview) { const j = state.jobs.find((x) => x.id === t.preview); return gcodePreview(t.preview, `${j.project_name}: plate ${j.plate} → ${j.printer_name}`); }
      refresh();
    } catch (err) { toast(err.message, true); }
  };
}

// Live camera thumbnail in each queue card: loads a frame off-screen and swaps
// it in only once it arrives, so nothing flickers (the card HTML stays stable,
// so setHTML never rebuilds it). A camera that fails is retried less and less.
const qcams = {}; // printer id -> { next, busy, fails }
function drawQueueCams() {
  document.querySelectorAll("#job-list .job-cam[data-opencam]").forEach((box) => {
    const p = state.printers.find((x) => x.id === box.dataset.opencam);
    const url = p && p.status && p.status.snapshot_url, img = box.querySelector("img");
    if (!url || !img) return;
    const c = (qcams[box.dataset.opencam] ||= { next: 0, busy: false, fails: 0 });
    if (c.busy || Date.now() < c.next) return;
    c.busy = true;
    const probe = new Image();
    probe.onload = () => { c.busy = false; c.fails = 0; c.next = Date.now() + 3000; img.src = probe.src; };
    probe.onerror = () => { c.busy = false; c.fails++; c.next = Date.now() + Math.min(60000, 4000 * 2 ** c.fails); };
    probe.src = `${url}?t=${Date.now()}`;
  });
}

// "Completed prints": every print that has ended, newest first, with how it
// ended. Opened from the Queue page; it is the other half of the Queue, which
// only lists what is still going on.
async function showCompletedPrints() {
  const body = $("#dlg-body");
  body.innerHTML = `<h2>Completed prints</h2><div id="hist-list"><p class="muted">Loading…</p></div><div class="dlg-actions"><button value="cancel">Close</button></div>`;
  openDialog();
  let runs = [], projects = [];
  try { [runs, projects] = await Promise.all([api("/api/runs?limit=200"), api("/api/projects")]); } catch (e) { $("#hist-list").innerHTML = `<p class="err">${esc(e.message)}</p>`; return; }
  runs = runs.filter((r) => r.state !== "printing");
  const how = (r) => r.state === "accepted" ? ["Finished", "ok"] : r.state === "printed" ? ["Finished: check the parts", "warn"] : r.stopped ? ["Stopped", "bad"] : ["Failed", "bad"];
  const list = $("#hist-list");
  if (!list) return;
  list.innerHTML = runs.length ? runs.map((r) => {
    const [label, cls] = how(r), ended = r.finished && !r.finished.startsWith("0001") ? new Date(r.finished) : null, began = new Date(r.started);
    const took = ended ? dur(Math.max(0, (ended - began) / 1000)) : "";
    const has = projects.some((p) => p.id === r.project_id);
    return `<div class="hist-row"><div class="hist-main"><b>${esc(r.project_name || "A print")}</b>${r.manual ? "" : ` <span class="muted">plate ${r.plate}</span>`}
        <div class="small muted">${esc(r.printer_name || "")} · ${(ended || began).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}${r.manual ? " · marked by hand" : took ? ` · took ${took}` : ""}${r.filament_g ? ` · ${r.filament_g.toFixed(0)} g` : ""}</div></div>
      <span class="pill hist-${cls}">${label}</span>
      <button type="button" data-again="${esc(r.project_id)}" data-plate="${r.plate}" ${has ? "" : "disabled title=\"The project was deleted\""}>Print again</button></div>`;
  }).join("") : `<p class="muted">No prints have ended yet. They are listed here once they do.</p>`;
  list.onclick = async (e) => {
    const t = e.target.closest("[data-again]"); if (!t) return;
    $("#dlg").close();
    await openProject(t.dataset.again);
    if (state.project && state.project.id === t.dataset.again) choosePrinter(+t.dataset.plate);
  };
}
