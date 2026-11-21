// PrintHarbor extension: when you download a file from Printables,
// Thingiverse, MakerWorld or Cults3D, tells PrintHarbor (on this computer)
// where Chrome saved it and which model page it came from. PrintHarbor moves
// it into <models folder>\designer\model and adds it to your library.
// Downloads from any other site are ignored and never reported.
"use strict";

const SITES = /(^|\.)(printables\.com|thingiverse\.com|makerworld\.com|bblmw\.com|cults3d\.com)$/i;
const PH = ["http://localhost:7621", "http://127.0.0.1:7621"];

const host = (u) => { try { return new URL(u).hostname; } catch { return ""; } };
const store = chrome.storage.session || chrome.storage.local;

// State: the model page each tab last showed, your last click on a model
// site (which tab), and downloads being watched. Changes go through one
// queue so two tabs reporting at once can't overwrite each other.
let cache = null, chain = Promise.resolve();
function update(fn) {
  const run = chain.then(async () => {
    if (!cache) {
      const s = await store.get(["pages", "last", "pending", "click"]);
      cache = { pages: s.pages || {}, last: s.last || null, pending: s.pending || {}, click: s.click || null };
    }
    const r = await fn(cache);
    await store.set(cache);
    return r;
  });
  chain = run.catch(() => {});
  return run;
}

// Show MakerWorld inside the PrintHarbor tab: drop MakerWorld's "don't show
// me inside other pages" headers, only for frames in that one tab, and only
// when the request comes from the PrintHarbor page on this computer.
async function allowEmbed(tabId) {
  const id = 1000 + (tabId % 100000);
  await chrome.declarativeNetRequest.updateSessionRules({
    removeRuleIds: [id],
    addRules: [{
      id, priority: 1,
      action: { type: "modifyHeaders", responseHeaders: [
        { header: "x-frame-options", operation: "remove" },
        { header: "content-security-policy", operation: "remove" },
        { header: "content-security-policy-report-only", operation: "remove" },
      ] },
      condition: { requestDomains: ["makerworld.com", "bambulab.com"], resourceTypes: ["sub_frame"], tabIds: [tabId] },
    }],
  });
}

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  if (msg && msg.type === "embed" && sender.tab && /^http:\/\/(localhost|127\.0\.0\.1)(:\d+)?\//.test(sender.url || sender.tab.url || "")) {
    allowEmbed(sender.tab.id).then(() => reply({ ok: true }), (e) => reply({ ok: false, error: String(e) }));
    return true;
  }
  if (msg && msg.type === "page" && sender.tab) {
    update((st) => {
      const p = { ...msg.page, at: Date.now(), tab: sender.tab.id };
      st.pages[sender.tab.id] = p;
      st.last = p;
    });
  }
  // You clicked something on a model site: a download that starts right
  // after belongs to the model shown in that tab.
  if (msg && msg.type === "click" && sender.tab) {
    update((st) => { st.click = { tab: sender.tab.id, at: Date.now(), url: msg.url }; });
  }
});

chrome.tabs && chrome.tabs.onRemoved && chrome.tabs.onRemoved.addListener((id) => {
  update((st) => { delete st.pages[id]; });
});

function samePage(a, b) {
  const strip = (u) => String(u || "").split("#")[0].split("?")[0].replace(/\/$/, "");
  return strip(a) === strip(b);
}

chrome.downloads.onCreated.addListener((item) => {
  const from = item.referrer || "", url = item.finalUrl || item.url || "";
  if (!SITES.test(host(from)) && !SITES.test(host(url))) return; // not a model site: ignored
  update((st) => {
    // Which model it is, best first:
    //  1. the page the site names as the referrer (when it names the full page),
    //  2. the model page in the tab you just clicked in,
    //  3. the newest model page on the same site, 4. the last model page.
    const site = (u) => host(u).split(".").slice(-2).join(".");
    const pages = Object.values(st.pages).sort((a, b) => b.at - a.at);
    let page = pages.find((p) => samePage(p.url, from)) || null;
    if (!page && st.click && Date.now() - st.click.at < 3 * 60 * 1000) page = st.pages[st.click.tab] || null;
    if (!page) page = pages.find((p) => from && site(p.url) === site(from)) || null;
    if (!page && st.last && Date.now() - st.last.at < 2 * 3600 * 1000) page = st.last;
    st.pending[item.id] = { page, url, referrer: from };
  });
});

chrome.downloads.onChanged.addListener(async (delta) => {
  if (!delta.state || delta.state.current === "in_progress") return;
  const p = await update((st) => {
    const x = st.pending[delta.id];
    delete st.pending[delta.id];
    return x;
  });
  if (!p) return;
  if (delta.state.current !== "complete") return;
  const [item] = await chrome.downloads.search({ id: delta.id });
  if (!item || !item.filename || !item.exists) return;
  let name = item.filename.split(/[\\/]/).pop();
  if (!/\.[a-z0-9]{2,5}$/i.test(name)) { try { name = decodeURIComponent(new URL(item.finalUrl || item.url).pathname.split("/").pop()) || name; } catch (e) {} }
  const body = JSON.stringify({ path: item.filename, name, url: p.url, size: item.fileSize || item.totalBytes || 0, page: p.page });
  for (const base of PH) {
    try {
      const r = await fetch(base + "/api/capture", { method: "POST", headers: { "Content-Type": "application/json", "X-PrintHarbor": "1" }, body });
      if (r.ok || r.status < 500) return;
    } catch (e) { /* try the next address */ }
  }
});
