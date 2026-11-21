// Workshop assistant chat (local model through Ollama).
"use strict";

const chat = { msgs: [], busy: false };

function setupAssistant() {
  if (!state.info.assistant || $("#ask-btn")) return;
  const btn = document.createElement("button");
  btn.id = "ask-btn"; btn.className = "primary ask-btn"; btn.textContent = "Ask";
  const panel = document.createElement("div");
  panel.id = "ask-panel"; panel.className = "ask-panel card hidden";
  panel.innerHTML = `<div class="toolbar" style="margin:0 0 8px"><h3>Workshop assistant</h3><button class="icon" id="ask-close">✕</button></div>
    <div id="ask-log" class="ask-log"><div class="muted small">Try: "What's printing?", "How much is left on the robot project?", "Pause the Mini".</div></div>
    <form id="ask-form" class="row"><input id="ask-in" placeholder="Ask about your printers…" autocomplete="off"><button class="primary" style="flex:0 0 auto">Send</button></form>`;
  document.body.append(btn, panel);
  btn.onclick = () => { panel.classList.toggle("hidden"); $("#ask-in").focus(); };
  $("#ask-close").onclick = () => panel.classList.add("hidden");
  $("#ask-form").onsubmit = async (e) => {
    e.preventDefault();
    const text = $("#ask-in").value.trim();
    if (!text || chat.busy) return;
    $("#ask-in").value = "";
    chat.msgs.push({ role: "user", content: text });
    drawChat("…thinking");
    chat.busy = true;
    try {
      const r = await api("/api/assistant", { json: { messages: chat.msgs } });
      chat.msgs.push({ role: "assistant", content: r.reply, pending: r.pending });
    } catch (er) { chat.msgs.push({ role: "assistant", content: "Sorry: " + er.message }); }
    chat.busy = false;
    drawChat();
  };
  $("#ask-log").onclick = async (e) => {
    const id = e.target.dataset.confirm;
    if (!id) return;
    e.target.disabled = true;
    try { const r = await api(`/api/assistant/confirm/${id}`, { method: "POST" }); e.target.textContent = "Done: " + r.done; refresh(); }
    catch (er) { e.target.textContent = er.message; }
  };
}

function drawChat(thinking) {
  const log = $("#ask-log");
  log.innerHTML = chat.msgs.map((m) => `<div class="ask-msg ${m.role}">${esc(m.content).replace(/\n/g, "<br>")}
    ${(m.pending || []).map((p) => `<div><button class="primary icon" data-confirm="${p.id}">Confirm: ${esc(p.label)}</button></div>`).join("")}</div>`).join("") +
    (thinking ? `<div class="ask-msg assistant muted">${thinking}</div>` : "");
  log.scrollTop = log.scrollHeight;
}
