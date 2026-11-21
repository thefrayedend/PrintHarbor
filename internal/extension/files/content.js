// PrintHarbor extension, on model sites: reads the model page you are on
// (title, designer, pictures...) so a download from it can be filed in your
// library as <models folder>\designer\model. Nothing is read on other sites.
"use strict";

const MODEL = [
  /printables\.com\/(?:[a-z-]+\/)?model\/\d+/,
  /thingiverse\.com\/thing:\d+/,
  /makerworld\.com\/(?:[a-z-]+\/)?models\/\d+/,
  /cults3d\.com\/(?:[a-z-]+\/)?3d-model\/[^/]+\/[^/?#]+/,
];
const isModel = (u) => MODEL.some((re) => re.test(u));

function readPage() {
  return (() => {
  const out = { url: location.href, title: "", designer: "", description: "", license: "", category: "", images: [], tags: [] };
  const meta = (n) => (document.querySelector(`meta[property="${n}"],meta[name="${n}"]`) || {}).content || "";
  const add = (arr, v) => { if (typeof v === "string" && (v = v.trim()) && !arr.includes(v)) arr.push(v); };
  const nameOf = (a) => !a ? "" : typeof a === "string" ? a : Array.isArray(a) ? nameOf(a[0]) : (a.name || a.alternateName || "");
  const text = (sel) => { const e = document.querySelector(sel); return e ? (e.innerText || e.textContent || "").trim() : ""; };
  const host = location.hostname;

  // MakerWorld (Next.js page data)
  try {
    const el = document.getElementById("__NEXT_DATA__");
    const w = window.__NEXT_DATA__;
      const nd = (w && w.props ? w : null) || (el && el.textContent ? JSON.parse(el.textContent) : null);
    const d = nd && nd.props && nd.props.pageProps && nd.props.pageProps.design;
    if (d) {
      out.title = d.titleTranslated || d.title || "";
      out.designer = (d.designCreator && d.designCreator.name) || "";
      out.description = d.summaryTranslated || d.summary || "";
      out.license = d.license || "";
      (d.tags || []).forEach((t) => add(out.tags, typeof t === "string" ? t : t.name));
      add(out.images, d.coverUrl || d.cover);
      ((d.designExtension && d.designExtension.design_pictures) || []).forEach((p) => add(out.images, p.url));
      if (Array.isArray(d.categories) && d.categories.length) out.category = d.categories.map((c) => c.name).filter(Boolean).join("/");
    }
  } catch (e) {}

  // schema.org JSON-LD (Cults3D, Thingiverse, many others)
  for (const s of document.querySelectorAll('script[type="application/ld+json"]')) {
    let d;
    try { d = JSON.parse(s.textContent); } catch (e) { continue; }
    const list = [].concat(d).flatMap((x) => (x && x["@graph"] ? [x, ...x["@graph"]] : [x]));
    for (const x of list) {
      if (!x || typeof x !== "object") continue;
      const t = [].concat(x["@type"] || []).join(" ");
      if (/BreadcrumbList/i.test(t)) {
        const names = (x.itemListElement || []).map((i) => nameOf(i.item) || i.name).filter(Boolean);
        if (names.length > 2 && !out.category) out.category = names.slice(1, -1).join("/");
        continue;
      }
      if (/Organization|Person|WebSite|WebPage|SearchAction|Offer|Review|Comment/i.test(t) && !/Product|3DModel|CreativeWork/i.test(t)) continue;
      if (!x.name) continue;
      out.title = out.title || nameOf(x);
      out.description = out.description || x.description || "";
      out.designer = out.designer || nameOf(x.author) || nameOf(x.creator) || nameOf(x.brand) || nameOf(x.manufacturer);
      out.license = out.license || nameOf(x.license);
      [].concat(x.image || []).forEach((i) => add(out.images, typeof i === "string" ? i : i && (i.url || i.contentUrl)));
      if (x.keywords) [].concat(typeof x.keywords === "string" ? x.keywords.split(",") : x.keywords).forEach((k) => add(out.tags, String(k)));
      if (x.category && !out.category) out.category = nameOf(x.category).replace(/\s*>\s*/g, "/");
    }
  }

  // Site markup
  if (!out.designer) {
    let a = null;
    if (/cults3d\.com$/.test(host)) a = document.querySelector('main a[href*="/users/"]:not([href*="/users/sign"]), a[href*="/users/"][rel="author"], .creation-creator a[href*="/users/"]');
    else if (/printables\.com$/.test(host)) a = document.querySelector('main a[href^="/@"], a[href*="printables.com/@"]');
    else if (/thingiverse\.com$/.test(host)) a = document.querySelector('a[href*="/designs"][class*="creator" i], a[class*="ThingCreator" i], a[href^="/"][class*="user" i]');
    else if (/makerworld\.com$/.test(host)) a = document.querySelector('a[href*="/@"]');
    if (a) {
      const m = (a.getAttribute("href") || "").match(/\/users\/([^/?#]+)|\/@([^/?#]+)/);
      out.designer = (a.innerText || "").trim().split("\n")[0] || (m && decodeURIComponent(m[1] || m[2])) || "";
    }
  }
  if (/cults3d\.com$/.test(host) && !out.license) {
    const l = Array.from(document.querySelectorAll('a[href*="creativecommons.org"], a[href*="/licenses"]')).map((e) => e.innerText.trim()).find(Boolean);
    if (l) out.license = l;
  }

  // Share tags
  out.title = out.title || meta("og:title") || text("h1") || document.title;
  out.description = out.description || meta("og:description") || meta("description");
  add(out.images, meta("og:image"));
  add(out.images, meta("twitter:image"));
  out.designer = out.designer || meta("author") || meta("article:author") || meta("twitter:creator");
  if (!out.tags.length && meta("keywords")) meta("keywords").split(",").forEach((k) => add(out.tags, k));
  out.images = out.images.filter((u) => /^https?:/.test(u)).slice(0, 16);
  out.tags = out.tags.slice(0, 30);
  return out;
})();
}

let lastURL = "", timers = [];
function check() {
  const u = location.href;
  if (u === lastURL) return;
  lastURL = u;
  // Shown inside PrintHarbor: tell it where we are (for "Open in a new tab").
  if (window.top !== window) { try { window.parent.postMessage({ phx: "frame-url", url: u }, "*"); } catch (e) {} }
  timers.forEach(clearTimeout);
  timers = [];
  if (!isModel(u)) return;
  // Pages fill in their details late: read a few times.
  for (const wait of [300, 2000, 5000, 10000]) {
    timers.push(setTimeout(() => {
      if (location.href !== u) return;
      let p;
      try { p = readPage(); } catch (e) { return; }
      if (!p.title) return;
      chrome.runtime.sendMessage({ type: "page", page: p }).catch(() => {});
    }, wait));
  }
}
check();
setInterval(check, 1000);

// Tell the extension which tab you clicked in (so a download that follows is
// filed under the model shown in this tab, not another one).
document.addEventListener("click", (e) => {
  const t = e.target && e.target.closest && e.target.closest("a,button,[role=button],span.primaryButton,[role=tooltip] *");
  if (t) chrome.runtime.sendMessage({ type: "click", url: location.href }).catch(() => {});
}, true);
