// On the PrintHarbor page: tells it the extension is installed, and lets it
// show MakerWorld inside the Library (MakerWorld normally refuses to be shown
// inside another page; the extension allows it for this tab only).
(function () {
  const mark = () => { document.documentElement.dataset.printharborExt = chrome.runtime.getManifest().version; };
  if (document.documentElement) mark(); else document.addEventListener("DOMContentLoaded", mark);
  window.addEventListener("message", (e) => {
    if (e.source !== window || !e.data || e.data.phx !== "embed") return;
    chrome.runtime.sendMessage({ type: "embed" }).then(
      (r) => window.postMessage({ phx: "embed-ok", ok: !!(r && r.ok), error: r && r.error }, "*"),
      (err) => window.postMessage({ phx: "embed-ok", ok: false, error: String(err) }, "*"));
  });
})();
