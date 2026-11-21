// 3D James (the CAD app) and the slicer run in their own frames inside the
// page. These open them, hand them files, and listen for what they report back.
// Part of the PrintHarbor web UI; see app.js for how the scripts fit together.
"use strict";

// ---------- 3D James ----------
// The CAD app runs in the 3D James tab (loaded once, kept open while you
// switch tabs). o: a library file to open in it.
function openDesign() {
  const f = $("#design-frame"); // (the space above it is set by topbarMode)
  if (!f.getAttribute("src")) f.src = "/3djames.html";
}
function openIn3DJames(o) {
  const f = $("#design-frame");
  const clean = Object.fromEntries(Object.entries(o || {}).filter(([, v]) => v));
  if (!f.getAttribute("src")) f.src = "/3djames.html#" + new URLSearchParams(clean).toString();
  else f.contentWindow.postMessage({ ph: "3dj-open", ...clean }, location.origin);
  show("design");
}
// The slicer saved a project / wants to go back to it.
window.addEventListener("message", async (e) => {
  if (e.origin !== location.origin || !e.data) return;
  if (e.data.ph === "project-saved") {
    if (state.project && state.project.id === e.data.id) { try { state.project = await api("/api/projects/" + e.data.id); } catch (_) { /* gone */ } }
    return;
  }
  if (e.data.ph === "open-project") { openProject(e.data.id); return; }
});
window.addEventListener("message", (e) => {
  if (e.origin !== location.origin || !e.data || e.data.ph !== "3dj-saved") return;
  toast(`Saved ${e.data.name} to your library.`);
  if (typeof lib !== "undefined") lib.facetsAt = 0;
});

// ---------- Slicer ----------
// FlashBridge's slicer front end (web/slicer), kept open while you switch tabs.
function openSlicer() {
  const f = $("#slicer-frame");
  if (!f.getAttribute("src")) f.src = "/slicer/index.html" + (typeof pv !== "undefined" && pv.id ? "?printer=" + enc(pv.id) : "");
}
// Open the slicer aimed at a specific printer (from a printer card). If it's
// already open, switch its printer in place (keeps whatever is loaded).
function openSlicerForPrinter(pid) {
  const f = $("#slicer-frame");
  if (!f.getAttribute("src")) f.src = "/slicer/index.html?printer=" + enc(pid);
  else f.contentWindow.postMessage({ ph: "slicer-printer", id: pid }, location.origin);
  show("slicer");
}
// Open files in the slicer: what = { item, files } (library) or { project, plate }.
async function openInSlicer(what) {
  if (what.project) {
    const f = $("#slicer-frame");
    const hash = `#slicer/prepare/project=${what.project}&plate=${what.plate || 1}`;
    if (!f.getAttribute("src")) f.src = "/slicer/index.html" + hash;
    else f.contentWindow.postMessage({ ph: "slicer-project", id: what.project, plate: what.plate || 1 }, location.origin);
    show("slicer");
    return;
  }
  try {
    const r = await api(what.project ? "/api/slicer/inbox/project" : "/api/slicer/inbox/library", { json: what });
    if (!r.ok) throw new Error(r.message || "Could not open it in the slicer");
    const f = $("#slicer-frame");
    if (!f.getAttribute("src")) f.src = "/slicer/index.html#slicer/prepare/inbox=" + r.id;
    else f.contentWindow.postMessage({ ph: "slicer-open", id: r.id }, location.origin);
    show("slicer");
  } catch (e) { toast(e.message, true); }
}
