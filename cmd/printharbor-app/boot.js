// boot.js runs at the top of every page the PrintHarbor app window loads
// (main_windows.go injects it; boot.go fills in __PH_ORIGIN__ with PrintHarbor's
// own address (http://localhost:<its port>), and __PH_APP_BUILD__ with the
// window's build number).
//
// On every page, PrintHarbor's own included (and the slicer and 3D James inside
// it), it adds a right-click menu with Paste / Copy / Cut / Select all: the app
// window has no right-click menu of its own, so without this you could not
// paste a link into a box with the mouse.
// On PrintHarbor's own pages it also sets a flag, so the UI opens model sites
// in this window instead of a new tab. On a model-site page it also adds:
//   1. two buttons in the upper-left corner (Back, Library),
//   2. download capture: a model download goes into the PrintHarbor library.
//
// It must keep working on any site, so it is plain old JavaScript with no
// dependencies, and every part is wrapped so one failure can't stop the rest.
(function () {
  try {
    window.__phApp = true;
    window.__phAppBuild = __PH_APP_BUILD__;                  // which build of the window this is (internal/appver)
    try { if (window.phLog) window.phLog('boot ' + location.hostname + ' rs=' + document.readyState); } catch (_) {}
    var h = location.hostname;
    if (h === 'localhost' || h === '127.0.0.1') {            // PrintHarbor's own UI
      // Tell the window its own page has just loaded: nothing unsaved can be open at this
      // moment, so it is the safe point for the window to reopen itself after an update.
      try { if (window.top === window.self && window.phOwnPage) window.phOwnPage(); } catch (_) {}
      phRightClickMenu();
      return;
    }
    if (window.top !== window.self) return;                  // only the top page
    var PH_LIB = '__PH_ORIGIN__/?app=1#library';

    // ---------------------------------------------------------------- 1. Back / Library buttons
    function phPill(txt, title) {
      var b = document.createElement('button');
      b.textContent = txt; b.title = title;
      b.style.cssText = 'display:block;background:#1b6ef3;color:#fff;border:0;border-radius:999px;padding:9px 16px;font:700 13px system-ui,-apple-system,sans-serif;box-shadow:0 4px 16px rgba(0,0,0,.5);cursor:pointer;opacity:.95;pointer-events:auto;white-space:nowrap';
      b.onmouseenter = function () { b.style.opacity = '1'; };
      b.onmouseleave = function () { b.style.opacity = '.95'; };
      return b;
    }
    function add() {
      var host = document.body || document.documentElement;   // <html> always exists, even pre-body
      if (!host) return;
      var nav = document.getElementById('__ph_nav');
      if (nav) { if (nav.parentNode !== host) { host.appendChild(nav); } return; }  // re-home if the site moved/dropped it
      nav = document.createElement('div');
      nav.id = '__ph_nav';
      nav.style.cssText = 'position:fixed;left:14px;top:14px;z-index:2147483647;display:flex;flex-direction:column;gap:8px;pointer-events:none';
      var back = phPill('← Back', 'Go back a page');
      back.setAttribute('aria-label', 'Go back');
      back.onclick = function () {
        var u = location.href;
        try { history.back(); } catch (e) {}
        setTimeout(function () { if (location.href === u) location.href = PH_LIB; }, 400);   // nothing to go back to
      };
      var lib = phPill('⌂ Library', 'Return to your PrintHarbor library');
      lib.setAttribute('aria-label', 'Return to library');
      lib.style.background = '#13833c';                      // green so the two read as different
      lib.onclick = function () { location.href = PH_LIB; };
      nav.appendChild(back); nav.appendChild(lib);
      host.appendChild(nav);
      try { if (window.phLog) window.phLog('nav added on ' + host.tagName); } catch (_) {}
    }
    add();                                                   // try right away; no body wait
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', add);
    document.addEventListener('readystatechange', add);
    // Single-page sites (MakerWorld, Printables, Thingiverse) re-render their
    // whole DOM as you browse, which wipes anything we injected. Keep checking
    // so the buttons can't disappear, and re-add them on client-side navigation.
    setInterval(add, 1200);
    ['pushState', 'replaceState'].forEach(function (m) {
      var orig = history[m];
      if (typeof orig === 'function') history[m] = function () { var r = orig.apply(this, arguments); setTimeout(add, 60); return r; };
    });
    window.addEventListener('popstate', function () { setTimeout(add, 60); });

    // ---------------------------------------------------------------- 2. download capture
    // Capture model downloads into PrintHarbor instead of launching a slicer or
    // dropping a file in Downloads. Works on any model site: we watch for a
    // click that leads to a slicer "open in…" hand-off (its URL carries the
    // model file) or straight to a model file, pull that URL, and hand it to
    // the app window's own program (window.phImport), which passes it to the
    // PrintHarbor server; that downloads it and files it in the library.
    function phToast(msg, ok) {
      var d = document.getElementById('__ph_toast');
      if (!d) { d = document.createElement('div'); d.id = '__ph_toast'; if (document.body) document.body.appendChild(d); }
      d.textContent = msg;
      d.style.cssText = 'position:fixed;left:50%;bottom:66px;transform:translateX(-50%);z-index:2147483647;background:' + (ok === false ? '#c0392b' : '#1e8e4e') + ';color:#fff;padding:12px 18px;border-radius:999px;font:600 14px system-ui,-apple-system,sans-serif;box-shadow:0 6px 20px rgba(0,0,0,.5);max-width:82vw;text-align:center';
      clearTimeout(d.__t); d.__t = setTimeout(function () { if (d.parentNode) d.parentNode.removeChild(d); }, 5000);
    }
    function phMeta(sel) { var m = document.querySelector(sel); return m ? (m.getAttribute('content') || '') : ''; }
    function phPage() {
      var img = phMeta('meta[property="og:image"]');
      return {
        url: location.href, title: document.title || '',
        designer: phMeta('meta[name="author"]') || phMeta('meta[property="og:author"]') || '',
        description: phMeta('meta[property="og:description"]') || phMeta('meta[name="description"]') || '',
        images: img ? [img] : [], tags: [], license: '', category: ''
      };
    }
    function phCapture(fileUrl, name) {
      if (typeof window.phImport !== 'function') { phToast('PrintHarbor isn\'t ready — reopen the app.', false); return; }
      phToast('Sending to PrintHarbor…');
      try {
        window.phImport(JSON.stringify({ url: fileUrl, name: name || '', page: phPage() }))
          .then(function (msg) {
            phToast(msg || 'Importing into your PrintHarbor library…');
            setTimeout(function () { location.href = PH_LIB; }, 1100);   // leave the site, land back in the library
          })
          .catch(function (e) { phToast('PrintHarbor import failed: ' + ((e && e.message) || e), false); });
      } catch (e) { phToast('PrintHarbor import failed: ' + e, false); }
    }
    // A slicer hand-off link (bambustudio://open?file=https://…) carries the model file's address.
    function phFromScheme(href) {
      try {
        var i = href.indexOf('?'); var p = new URLSearchParams(i >= 0 ? href.slice(i + 1) : '');
        var f = p.get('file') || p.get('url') || p.get('download') || p.get('link');
        if (!f) { var m = href.match(/(?:file|url|link|download)=([^&]+)/i); if (m) f = decodeURIComponent(m[1]); }
        if (f && /^https?:/i.test(f)) return f;
      } catch (e) {}
      return null;
    }
    var PH_MODEL = /\.(3mf|stl|obj|step|stp|amf|ply|zip|gcode|bgcode)(\?|#|$)/i;
    var PH_SCHEME = /^(bambustudio|orcaslicer|prusaslicer|cura|cults|bambu|makerworld):/i;
    document.addEventListener('click', function (e) {
      var a = e.target.closest && e.target.closest('a[href]'); if (!a) return;
      var href = a.href || a.getAttribute('href') || '';
      if (PH_SCHEME.test(href)) {
        var f = phFromScheme(href);
        if (f) { e.preventDefault(); e.stopPropagation(); phCapture(f, a.getAttribute('download')); }
        return;
      }
      if (PH_MODEL.test(href)) { e.preventDefault(); e.stopPropagation(); phCapture(href, a.getAttribute('download')); }
    }, true);
    var _open = window.open;
    window.open = function (u) {
      if (typeof u === 'string') {
        if (PH_SCHEME.test(u)) { var f = phFromScheme(u); if (f) { phCapture(f); return null; } }
        if (PH_MODEL.test(u)) { phCapture(u); return null; }
      }
      return _open.apply(window, arguments);
    };

    // ---------------------------------------------------------------- 3. right-click menu
    // Paste/Copy/Cut/Select all. Many model sites block the native menu, so we
    // provide our own (and it works in text fields for pasting).
    var phMenu = null;
    phRightClickMenu();
    function phRightClickMenu() {
    function phCloseMenu() { if (phMenu && phMenu.parentNode) phMenu.parentNode.removeChild(phMenu); phMenu = null; }
    function phInsert(el, txt) {
      if (el.isContentEditable) { document.execCommand('insertText', false, txt); return; }
      var s = el.selectionStart || 0, e2 = el.selectionEnd || 0, v = el.value || '';
      el.value = v.slice(0, s) + txt + v.slice(e2); var p = s + txt.length; try { el.setSelectionRange(p, p); } catch (e) {}
      el.dispatchEvent(new Event('input', { bubbles: true }));
    }
    document.addEventListener('contextmenu', function (e) {
      var t = e.target;
      var editable = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
      var sel = (window.getSelection && String(window.getSelection())) || '';
      if (!editable && !sel) return;
      e.preventDefault(); e.stopPropagation(); phCloseMenu();
      phMenu = document.createElement('div');
      phMenu.style.cssText = 'position:fixed;z-index:2147483647;background:#23262b;color:#fff;border:1px solid #3a3f46;border-radius:10px;padding:6px;min-width:150px;box-shadow:0 10px 28px rgba(0,0,0,.55);font:14px system-ui,-apple-system,sans-serif';
      var items = [];
      if (editable) items.push(['Paste', async function () { try { phInsert(t, await navigator.clipboard.readText()); } catch (err) { phToast('Press Ctrl+V to paste', false); } }]);
      if (sel) items.push(['Copy', function () { try { navigator.clipboard.writeText(sel); } catch (e) {} }]);
      if (sel && editable) items.push(['Cut', function () { try { navigator.clipboard.writeText(sel); document.execCommand('delete'); } catch (e) {} }]);
      if (editable) items.push(['Select all', function () { try { t.select && t.select(); } catch (e) {} }]);
      items.forEach(function (it) {
        var b = document.createElement('div'); b.textContent = it[0];
        b.style.cssText = 'padding:9px 12px;border-radius:7px;cursor:pointer';
        b.onmouseenter = function () { b.style.background = '#33373d'; }; b.onmouseleave = function () { b.style.background = ''; };
        b.onmousedown = function (ev) { ev.preventDefault(); }; b.onclick = function () { it[1](); phCloseMenu(); };
        phMenu.appendChild(b);
      });
      phMenu.style.left = Math.max(6, Math.min(e.clientX, (window.innerWidth || 800) - 168)) + 'px';
      phMenu.style.top = Math.max(6, Math.min(e.clientY, (window.innerHeight || 600) - 16 - items.length * 40)) + 'px';
      document.body.appendChild(phMenu);
    }, true);
    document.addEventListener('mousedown', function (e) { if (phMenu && !phMenu.contains(e.target)) phCloseMenu(); }, true);
    window.addEventListener('scroll', phCloseMenu, true);
    }
  } catch (e) { try { if (window.phLog) window.phLog('ERR ' + ((e && e.message) || e)); } catch (_) {} }
})();
