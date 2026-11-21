// AI failure detection: Settings section (install, per-printer switches and
// settings, last analysed picture with boxes) and the dashboard's AI chip and
// alert banner. Built like FlashBridge's AI page.
"use strict";

const aiUI = { data: null, at: 0, busy: false };

async function loadAI(force) {
  if (aiUI.busy || (!force && Date.now() - aiUI.at < 2500)) return aiUI.data;
  aiUI.busy = true;
  try { aiUI.data = await api("/api/ai"); aiUI.at = Date.now(); } catch { /* server restarting */ }
  aiUI.busy = false;
  return aiUI.data;
}

const AI_STATUS_TEXT = {
  off: "Switched off for this printer.", idle: "Waiting for a print to start.", watching: "Watching the print.",
  warning: "Possible failure! Please check the printer.", paused: "The AI paused this print. Check it, then resume or stop it.",
  ignored: "Alerts muted for the rest of this print.", "no-camera": "No camera found for this printer.", "ai-offline": "The AI service is starting…",
  "not-installed": "Install AI failure detection above to start watching.",
};

function aiNotice(kind, text) { return `<div class="notice ${kind || ""}">${text}</div>`; }

function aiServiceHTML(d) {
  const sv = d.service;
  if (d.external) return aiNotice("ok", "Using the Obico ML API set under “AI on another computer” below.");
  if (sv.installing) {
    const pct = Math.round((sv.progress || 0) * 100);
    return aiNotice("", `<b>Installing…</b> ${esc(sv.step || "")}${sv.progress ? ` (${pct}%)` : ""}<div class="progress"><div style="width:${pct}%"></div></div>`);
  }
  if (!sv.installed) {
    return `${sv.error ? aiNotice("err", `<b>Install failed.</b> ${esc(sv.error).replace(/\n/g, "<br>")}`) : ""}
      ${aiNotice("", "AI failure detection isn't installed yet. PrintHarbor downloads Obico's open-source model (about 200 MB) and ONNX Runtime into its own folder. It needs Python 3 on this PC and takes a few minutes.")}
      <button class="btn primary" data-ai-install>Install AI failure detection</button>`;
  }
  if (sv.ready) return aiNotice("ok", `AI model loaded (${esc(sv.backend || "CPU")}). One check takes about ${sv.avg_ms ? Math.round(sv.avg_ms) + " ms" : "a second"}.`);
  if (sv.reachable) return aiNotice("warn", sv.error ? esc(sv.error) : "The AI model is loading. This can take a minute.");
  if (sv.running) return aiNotice("warn", "The AI service is starting…");
  return aiNotice("", "Installed. The AI service starts by itself when a printer has detection switched on.");
}

function renderAISettings() {
  const root = $("#ai-settings");
  if (!root || state.view !== "settings") return;
  loadAI().then((d) => {
    if (!d) return;
    const ids = state.printers.map((p) => p.id).join(",");
    if (root.dataset.ids !== ids) {
      root.dataset.ids = ids;
      root.innerHTML = `
        <div class="card ai-head">
          <div class="card-head"><h2>AI failure detection</h2></div>
          <p class="muted">While a print runs, PrintHarbor looks at the printer's camera every few seconds with an open-source AI model (from Obico) that recognises “spaghetti” and other failures. It runs on this PC; nothing leaves your network. Switch it on and adjust it for each printer below.</p>
          <div id="ai-service"></div>
        </div>
        <div class="ai-grid">${state.printers.map((p) => `
          <div class="card" id="ai-${esc(p.id)}" data-id="${esc(p.id)}">
            <div class="card-head"><h2>${esc(p.name)}</h2>
              <label class="switch" title="Use AI on this printer"><input type="checkbox" data-ai="ai_detect"><span></span></label></div>
            <div class="ai-cam"><img alt="Last analysed picture" class="hidden"><canvas></canvas><div class="empty muted small">The last picture the AI looked at appears here.</div></div>
            <div class="label">Failure risk</div>
            <div class="progress risk"><div></div></div>
            <div class="ai-msg"></div>
            <div class="hint ai-meta"></div>
            <div class="ai-opts">
              <div class="label">When it sees a likely failure</div>
              <div class="segmented" data-ai="ai_pause"><button data-v="0">Alert me</button><button data-v="1">Alert and pause the print</button></div>
              <div class="label">Sensitivity</div>
              <div class="segmented" data-ai="ai_sensitivity"><button data-v="low">Low</button><button data-v="medium">Normal</button><button data-v="high">High</button></div>
              <div class="hint">“High” catches problems sooner but may raise more false alarms.</div>
              <div class="label">Check every</div>
              <div class="segmented" data-ai="ai_interval"><button data-v="5">5 s</button><button data-v="10">10 s</button><button data-v="20">20 s</button><button data-v="30">30 s</button></div>
            </div>
            <div class="row gap wrap" style="margin-top:12px">
              <button class="btn small" data-ai-test>Test now</button>
              <button class="btn small" data-ai-ignore>Not a failure</button>
            </div>
          </div>`).join("") || '<div class="card muted">Add a printer first.</div>'}</div>`;
    }
    setHTML($("#ai-service"), aiServiceHTML(d));
    for (const p of state.printers) {
      const el = document.getElementById("ai-" + p.id);
      if (!el) continue;
      const a = d.printers[p.id] || {};
      const t = el.querySelector("[data-ai=ai_detect]");
      if (document.activeElement !== t) t.checked = !!p.ai_detect;
      el.classList.toggle("ai-off", !p.ai_detect);
      el.querySelector(".risk > div").style.width = Math.round(a.risk || 0) + "%";
      el.querySelector(".risk").className = "progress risk " + (a.risk >= 100 ? "err" : a.risk >= 50 ? "warn" : "");
      const kind = a.status === "warning" ? "warn" : a.status === "paused" ? "err" : a.status === "watching" ? "ok" : "";
      setHTML(el.querySelector(".ai-msg"), aiNotice(kind, esc(a.message || AI_STATUS_TEXT[a.status] || "")));
      el.querySelector(".ai-meta").textContent = a.last_check
        ? `Last check ${new Date(a.last_check).toLocaleTimeString()} · ${a.inference_ms} ms${a.frames < a.warmup_frames && a.status === "watching" ? ` · warming up ${a.frames}/${a.warmup_frames}` : ""}`
        : "";
      el.querySelector("[data-ai-ignore]").classList.toggle("hidden", !["warning", "paused", "watching"].includes(a.status));
      const seg = (k, v) => el.querySelectorAll(`[data-ai=${k}] button`).forEach((b) => b.classList.toggle("active", b.dataset.v === String(v)));
      seg("ai_pause", p.ai_pause ? 1 : 0);
      seg("ai_sensitivity", p.ai_sensitivity || "medium");
      seg("ai_interval", p.ai_interval || 10);
      if (a.has_image && el.dataset.img !== a.last_check) { el.dataset.img = a.last_check; aiDrawImage(el, p.id, a); }
    }
  });
}

function aiDrawImage(el, id, a) {
  const img = el.querySelector(".ai-cam img"), canvas = el.querySelector(".ai-cam canvas");
  const n = new Image();
  n.onload = () => {
    img.src = n.src;
    img.classList.remove("hidden");
    el.querySelector(".ai-cam .empty").classList.add("hidden");
    // The box takes the picture's shape: no bars.
    el.querySelector(".ai-cam").style.aspectRatio = `${n.naturalWidth} / ${n.naturalHeight}`;
    requestAnimationFrame(() => {
      const rect = img.getBoundingClientRect();
      canvas.width = rect.width; canvas.height = rect.height;
      const ctx = canvas.getContext("2d");
      ctx.clearRect(0, 0, canvas.width, canvas.height);
      const iw = a.image_width || n.naturalWidth, ih = a.image_height || n.naturalHeight;
      const sx = rect.width / iw, sy = rect.height / ih;
      for (const d of a.detections || []) {
        const x = (d.xc - d.w / 2) * sx, y = (d.yc - d.h / 2) * sy;
        ctx.strokeStyle = d.confidence > 0.5 ? "#ff3b30" : "#ffb300";
        ctx.lineWidth = 3;
        ctx.strokeRect(x, y, d.w * sx, d.h * sy);
        ctx.fillStyle = ctx.strokeStyle;
        ctx.font = "bold 13px system-ui";
        ctx.fillText(Math.round(d.confidence * 100) + "%", x + 4, Math.max(14, y - 4));
      }
    });
  };
  n.src = `/api/ai/${enc(id)}/image?t=${Date.now()}`;
}

async function aiSave(id, patch) {
  try { await api(`/api/printers/${enc(id)}/option`, { json: patch }); toast("AI settings saved."); }
  catch (e) { toast(e.message, true); }
  await refresh();
  aiUI.at = 0;
  renderAISettings();
}

document.addEventListener("click", async (e) => {
  if (e.target.closest("[data-ai-install]")) {
    try { await api("/api/ai/install", { method: "POST" }); toast("Installing AI failure detection… this takes a few minutes."); }
    catch (err) { toast(err.message, true); }
    aiUI.at = 0; renderAISettings();
    return;
  }
  const card = e.target.closest("#ai-settings [data-id]");
  if (!card) return;
  const id = card.dataset.id;
  const segBtn = e.target.closest(".segmented[data-ai] button");
  if (segBtn) {
    const k = segBtn.parentElement.dataset.ai, v = segBtn.dataset.v;
    aiSave(id, { [k]: k === "ai_pause" ? v === "1" : k === "ai_interval" ? +v : v });
    return;
  }
  const test = e.target.closest("[data-ai-test]");
  if (test) {
    test.disabled = true; test.textContent = "Checking…";
    try {
      const r = await api(`/api/ai/${enc(id)}/test`, { method: "POST" });
      const n = (r.detections || []).length;
      toast(n ? `The AI marked ${n} area(s) as possible failure.` : "Checked: no failure seen.");
      aiDrawImage(card, id, r);
    } catch (err) { toast(err.message, true); }
    test.disabled = false; test.textContent = "Test now";
    return;
  }
  if (e.target.closest("[data-ai-ignore]")) {
    await api(`/api/ai/${enc(id)}/ignore`, { method: "POST" }).catch(() => {});
    toast("OK — no more AI alerts for this print.");
    aiUI.at = 0; renderAISettings();
  }
});
document.addEventListener("change", (e) => {
  const t = e.target.closest("#ai-settings [data-ai=ai_detect]");
  if (t) aiSave(t.closest("[data-id]").dataset.id, { ai_detect: t.checked });
});

// Dashboard: the AI chip on the camera and the alert banner (FlashBridge's).
function aiDashboard(p) {
  loadAI().then((d) => {
    const a = d && d.printers[p.id];
    const chips = { watching: ["on", "AI watching"], warning: ["warn", "AI: possible failure"], paused: ["err", "AI paused the print"],
      ignored: ["", "AI muted for this print"], "ai-offline": ["warn", "AI starting"], idle: ["", "AI ready"] };
    const c = p.ai_detect && a && chips[a.status];
    setHTML($("#pv-ai-chip"), c ? `<span class="dot ${c[0]}"></span>${esc(c[1])}` : "");
    const alarm = p.ai_detect && a && (a.status === "warning" || a.status === "paused");
    $("#pv-ai-banner").classList.toggle("hidden", !alarm);
    if (alarm) $("#pv-ai-banner-text").textContent = a.status === "paused" ? "The AI paused this print because it looks like it failed. Check the camera." : (a.message || "The AI thinks this print may be failing. Check the camera.");
  });
}
