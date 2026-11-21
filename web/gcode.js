// The preview of a sliced job: the whole print in 3D, coloured by kind of
// line, like a slicer's preview (the drawing itself is preview3d.js, loaded
// when a preview is first opened). For a job that was sliced but not sent yet
// this is also where you send it: look first, then Print.
"use strict";

let gcView = null; // the open preview, so it can be shut down when the dialog closes

async function gcodePreview(jobId, title) {
  const dlg = $("#dlg"), body = $("#dlg-body");
  const j0 = state.jobs.find((x) => x.id === jobId);
  const canSend = !!(j0 && j0.state === "done" && !j0.send);
  const printer = j0 && state.printers.find((x) => x.id === j0.printer_id);
  const caps = (printer && printer.capabilities) || {};
  if (gcView) { gcView.destroy(); gcView = null; }
  dlg.classList.add("gc3d");
  body.innerHTML = `
    <div class="gc-head"><h2>${esc(title)}</h2><div id="gc-facts" class="small muted"></div>${j0 && j0.note ? `<div class="notice warn small">${esc(j0.note)}</div>` : ""}</div>
    <div class="gc-stage">
      <div id="gc-view" class="gc-view"></div>
      <div id="gc-msg" class="gc-msg">Loading the sliced file…</div>
      <div class="gc-side ${innerWidth < 700 ? "closed" : ""}" id="gc-side">
        <button type="button" id="gc-side-toggle" class="tiny" title="Show or hide the list of colours and kinds of line">Colours and lines</button>
        <div id="gc-mode" class="gc-mode" title="What the colours mean"><button type="button" data-gcmode="filament">Filament</button><button type="button" data-gcmode="type">Line type</button></div>
        <div id="gc-fils" class="gc-legend"></div>
        <div id="gc-legend" class="gc-legend"></div>
        <button type="button" id="gc-only" class="tiny">Only supports</button>
        <div class="gc-help muted">Drag to turn · wheel to zoom · right-drag to move</div>
      </div>
      <div class="gc-layers" title="Drag down to peel layers off the top"><input type="range" id="gc-layer" min="1" max="1" value="1"></div>
    </div>
    <div class="gc-foot">
      <span id="gc-label" class="small muted"></span>
      <button type="button" id="gc-play">Play</button>
      <button type="button" id="gc-reset">Reset view</button>
      <button type="button" id="gc-top">From above</button>
      <span style="flex:1"></span>
      <button value="cancel">${canSend ? "Don't send" : "Close"}</button>
      ${canSend ? `<button type="button" id="gc-upload" ${caps.upload ? "" : "disabled"}>Upload only</button>
      <button type="button" id="gc-print" class="primary" ${caps.start_print ? "" : "disabled"}>Print on ${esc(j0.printer_name)}</button>` : ""}
    </div>`;
  openDialog();
  dlg.addEventListener("close", () => { dlg.classList.remove("gc3d"); if (gcView) { gcView.destroy(); gcView = null; } }, { once: true });
  if (canSend) {
    const send = async (start) => {
      const pr = state.printers.find((x) => x.id === j0.printer_id), st = pr && pr.status && pr.status.state;
      if (start && (st === "printing" || st === "paused") && !confirm(`${j0.printer_name} is printing right now. Send this print to it anyway?`)) return;
      try {
        await api(`/api/jobs/${jobId}/send`, { json: { start } });
        dlg.close();
        if (j0.printer_id) openPrinter(j0.printer_id); // land on the printer it was just sent to
        toast(start ? `Sending ${title}. It starts printing when it arrives.` : `Uploading ${title}.`);
        refresh();
      } catch (e) { toast(e.message, true); }
    };
    $("#gc-print").onclick = () => send(true);
    $("#gc-upload").onclick = () => send(false);
  }
  $("#gc-side-toggle").onclick = () => $("#gc-side").classList.toggle("closed"); // on a phone the list starts folded away, off the print
  const view = $("#gc-view");
  const msg = (t) => { const m = $("#gc-msg"); if (m) { m.textContent = t; m.style.display = t ? "" : "none"; } };
  try {
    const mod = await import("./preview3d.js");
    const made = await mod.showPreview3D(view, `/api/jobs/${jobId}/download`, {
      msg, legend: $("#gc-legend"), fils: $("#gc-fils"), mode: $("#gc-mode"), slider: $("#gc-layer"), label: $("#gc-label"), play: $("#gc-play"), only: $("#gc-only"), reset: $("#gc-reset"), top: $("#gc-top"),
      // What you most want to know before a print starts: how much of it is supports.
      facts: ({ layers, supportShare }) => {
        const j = state.jobs.find((x) => x.id === jobId);
        const f = $("#gc-facts"); if (!f) return;
        f.innerHTML = [j && j.print_time_sec ? `${dur(j.print_time_sec)} print` : "", j && j.filament_g ? `${j.filament_g.toFixed(1)} g` : "", `${layers} layers`,
          supportShare > 0 ? `<b class="gc-sup">Supports: ${supportShare < 0.01 ? "under 1" : Math.round(supportShare * 100)}% of the printing</b>` : "No supports in this print"].filter(Boolean).join(" · ");
      },
    });
    if ($("#gc-view") === view && dlg.open) gcView = made; else made.destroy(); // closed (or showing something else) while it loaded
  } catch (e) { msg("Couldn't show the preview: " + e.message); }
}

// The job you just started from a send screen is kept in state.previewJob, and
// while it is being sliced (and sent) a progress box sits at the bottom of the
// page, whatever page you are on: what it is doing, how long it has been, and
// Cancel. The slicer does not report how far along it is, so the bar goes by how
// long this plate took last time; the first time it just moves to show it is
// alive. For "Slice & preview" the preview opens by itself when it is ready.
function sliceWaitBox() {
  let box = $("#slice-wait");
  if (!box) {
    box = document.createElement("div");
    box.id = "slice-wait";
    box.className = "slice-wait";
    box.innerHTML = `<div class="sw-top"><b id="sw-what"></b><button type="button" id="sw-cancel" class="tiny">Cancel</button></div>
      <div class="sw-bar"><i id="sw-fill"></i></div><div class="small" id="sw-note"></div>`;
    document.body.appendChild(box);
    box.querySelector("#sw-cancel").onclick = async () => {
      const w = state.previewJob; if (!w) return;
      try { await api(`/api/jobs/${w.id}/cancel`, { method: "POST" }); toast("Cancelled."); } catch (e) { toast(e.message, true); }
      refresh();
    };
  }
  return box;
}
const swTime = (s) => (s < 60 ? `${Math.round(s)}s` : `${Math.floor(s / 60)}m ${String(Math.round(s % 60)).padStart(2, "0")}s`);
function drawSliceWait() {
  const w = state.previewJob, box = $("#slice-wait");
  if (!w) { if (box) box.remove(); clearInterval(drawSliceWait.t); drawSliceWait.t = 0; return; }
  const j = state.jobs.find((x) => x.id === w.id) || { state: "queued" };
  const b = sliceWaitBox();
  const began = j.started && !String(j.started).startsWith("0001") ? new Date(j.started).getTime() : w.since;
  const secs = Math.max(0, (Date.now() - began) / 1000);
  // how long the same plate took to slice the last time it was sliced
  if (w.usual === undefined) {
    const prev = state.jobs.filter((x) => x.id !== w.id && x.state === "done" && x.project_id === j.project_id && x.plate === j.plate && x.sliced_sec > 0).pop();
    w.usual = prev ? prev.sliced_sec : 0;
  }
  const stage = j.state === "queued" ? "Waiting for its turn to slice" : j.state === "sending" ? "Sending to the printer" : "Slicing";
  $("#sw-what").textContent = `${stage}: ${w.title}`;
  const fill = $("#sw-fill");
  if (j.state === "slicing" && w.usual) {
    fill.classList.remove("moving");
    fill.style.width = Math.min(96, (secs / w.usual) * 100) + "%";
    $("#sw-note").textContent = `${swTime(secs)} so far · took ${swTime(w.usual)} last time` + (w.preview ? " · the preview opens when it is ready" : "");
  } else {
    fill.classList.add("moving"); fill.style.width = "";
    $("#sw-note").textContent = (j.state === "queued" ? "Another slice is running first." : `${swTime(secs)} so far`) + (w.preview && j.state !== "sending" ? " · the preview opens when it is ready" : "");
  }
  if (!drawSliceWait.t) drawSliceWait.t = setInterval(drawSliceWait, 1000);
}
function checkPreviewJob() {
  const w = state.previewJob;
  if (!w) return drawSliceWait();
  const j = state.jobs.find((x) => x.id === w.id);
  const end = () => { state.previewJob = null; drawSliceWait(); };
  if (!j) { if (Date.now() - w.since > 15000) end(); else drawSliceWait(); return; } // just made: the list hasn't caught up yet
  if (j.state === "canceled") return end();
  if (j.state === "failed") { end(); toast(`${w.title} failed: ${j.error || "see the Queue"}`, true); return; }
  // Auto-switch to the printer's control page once a direct send ("Slice &
  // print" / "Slice & upload") has actually been sliced and is going to the
  // printer. Guarded by w.switched so it happens once. A failed slice is caught
  // above and never reaches here, so a print that never sent never switches.
  if (!w.preview && !w.switched && (j.state === "sending" || j.state === "done") && j.printer_id) {
    w.switched = true;
    openPrinter(j.printer_id);
  }
  if (j.state !== "done") return drawSliceWait();
  if (!w.preview) { end(); return; }                 // sent: the usual message and the Queue take over
  if ($("#dlg").open) { $("#sw-what") && ($("#sw-what").textContent = `Ready: ${w.title}`); return; } // you are busy in another dialog: it waits
  end();
  gcodePreview(j.id, w.title);
}
