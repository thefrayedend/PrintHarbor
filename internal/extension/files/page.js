// Runs in every page of PrintHarbor's browser. Only does something on
// MakerWorld (ported from FlashBridge's app browser, makerworld_script.dart):
// a click on MakerWorld's big "Open in Bambu Studio / Bambu Handy" button
// becomes what you would do by hand: open the arrow menu next to it and pick
// "Download 3MF". That is the page's own download, which PrintHarbor files in
// your library. The arrow still works as before, as does every other button.
// "Get the full app experience" pop-ups are closed, and bambustudio:// links
// that carry a file address download that file instead.
(function () {
  if (window.__printHarborExt) return;
  window.__printHarborExt = true;
  if (!/(^|\.)makerworld\.com$/.test(location.hostname)) return;
  var passing = false;
  var APP = /Bambu (Handy|Studio)|Open in (Bambu|Studio|App)/i;
  function tap(el) { passing = true; try { el.click(); } finally { passing = false; } }
  function shown(el) { var r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; }
  function mainButtons() { return Array.prototype.filter.call(document.querySelectorAll('span.primaryButton'), shown); }
  function entry3mf() {
    var menus = document.querySelectorAll('[role=tooltip],[role=menu],.MuiPopover-paper,.MuiMenu-paper');
    for (var m = 0; m < menus.length; m++) {
      var all = menus[m].querySelectorAll('*');
      for (var i = 0; i < all.length; i++) {
        var t = (all[i].textContent || '').trim();
        if (all[i].children.length === 0 && /3MF/i.test(t) && !/STL/i.test(t) && t.length < 40) return all[i];
      }
    }
    var main = mainButtons();
    for (var j = 0; j < main.length; j++) {
      var s = main[j].textContent || '';
      if (/3MF/i.test(s) && !/STL/i.test(s)) return main[j];
    }
    return null;
  }
  function download3mf(start) {
    var item = entry3mf();
    if (item && item !== start) { tap(item); return; }
    var arrow = null;
    for (var n = start, i = 0; n && i < 4 && !arrow; n = n.parentElement, i++) arrow = n.querySelector('.icon-box');
    var main = mainButtons();
    if (!arrow && main.length) arrow = main[0].parentElement.querySelector('.icon-box');
    if (!arrow) return hint();
    tap(arrow);
    var tries = 0;
    (function look() {
      var found = entry3mf();
      if (found) { tap(found); return; }
      if (++tries > 30) return hint();
      setTimeout(look, 100);
    })();
  }
  function hint() {
    var d = document.createElement('div');
    d.textContent = 'PrintHarbor: open the arrow next to the button and pick "Download 3MF" — it goes straight into your library.';
    d.style.cssText = 'position:fixed;z-index:2147483647;left:50%;top:16px;transform:translateX(-50%);background:#1d6ff2;color:#fff;padding:12px 18px;border-radius:10px;font:600 14px system-ui;box-shadow:0 6px 24px rgba(0,0,0,.3)';
    document.body.appendChild(d);
    setTimeout(function () { d.remove(); }, 8000);
  }
  function splitButton(t) {
    for (var n = t, i = 0; n && n.nodeType === 1 && i < 5; n = n.parentElement, i++) {
      if ((n.textContent || '').trim().length > 60) return null;
      var main = n.matches('span.primaryButton') ? n : n.querySelector('span.primaryButton');
      if (main && (main === n || n.querySelector('.icon-box'))) {
        if (!APP.test(main.textContent || '')) return null;
        var box = main.parentElement;
        return box && box.querySelector('.icon-box') ? main : null;
      }
    }
    return null;
  }
  function menuEntry(t) {
    var menu = t.closest && t.closest('[role=tooltip],[role=menu]');
    if (!menu) return null;
    for (var n = t, i = 0; n && n !== menu && i < 4; n = n.parentElement, i++) {
      var s = (n.textContent || '').trim();
      if (s.length < 40 && APP.test(s)) return n;
    }
    return null;
  }
  // A file address inside bambustudio://open?file=… or bambulab://… links.
  function fileIn(href) {
    var text = String(href || '');
    for (var i = 0; i < 3; i++) { try { var d = decodeURIComponent(text); if (d === text) break; text = d; } catch (e) { break; } }
    var m = text.match(/https?:\/\/[^\s"'<>]+?\.(3mf|stl|obj|step|stp|zip)(\?[^\s"'<>]*)?(?=$|[&\s"'<>])/i);
    return m ? m[0] : null;
  }
  function appPopup() {
    var dialogs = document.querySelectorAll('[role=dialog]');
    for (var d = 0; d < dialogs.length; d++) {
      var buttons = dialogs[d].querySelectorAll('button');
      var app = null, other = null;
      for (var b = 0; b < buttons.length; b++) {
        if (APP.test(buttons[b].textContent || '')) app = buttons[b]; else other = other || buttons[b];
      }
      if (app && other && buttons.length === 2 && !dialogs[d].querySelector('span.primaryButton') && /Handy|app experience/i.test(dialogs[d].textContent || ''))
        return { dialog: dialogs[d], app: app, cancel: other };
    }
    return null;
  }
  var closed = typeof WeakSet === 'function' ? new WeakSet() : null, queued = false;
  function closePopup() {
    queued = false;
    var p = appPopup();
    if (!p || (closed && closed.has(p.dialog))) return;
    if (closed) closed.add(p.dialog);
    tap(p.cancel);
  }
  window.addEventListener('click', function (e) {
    if (passing) return;
    var t = e.target;
    if (!t || t.nodeType !== 1) return;
    var a = t.closest('a[href]');
    if (a && /^(bambustudio|bambulab|orcaslicer):/i.test(a.getAttribute('href'))) {
      var f = fileIn(a.getAttribute('href'));
      e.preventDefault(); e.stopImmediatePropagation();
      if (f) location.assign(f); else download3mf(t);
      return;
    }
    if (t.closest('.icon-box')) return;
    var popup = appPopup();
    if (popup && popup.app.contains(t)) { e.preventDefault(); e.stopImmediatePropagation(); tap(popup.cancel); return; }
    var hit = splitButton(t) || menuEntry(t);
    if (!hit) return;
    e.preventDefault(); e.stopImmediatePropagation();
    download3mf(hit);
  }, true);
  // Pages that hand off to Bambu Studio by setting location to bambustudio://…
  try {
    var assign = window.location.assign.bind(window.location);
    var open = window.open;
    window.open = function (u) {
      if (u && /^(bambustudio|bambulab):/i.test(String(u))) { var f = fileIn(u); if (f) assign(f); return null; }
      return open.apply(window, arguments);
    };
  } catch (e) {}
  function watch() {
    closePopup();
    new MutationObserver(function () { if (!queued) { queued = true; setTimeout(closePopup, 50); } })
      .observe(document.documentElement, { childList: true, subtree: true });
  }
  if (document.documentElement) watch(); else document.addEventListener('DOMContentLoaded', watch);
  ['pushState', 'replaceState'].forEach(function (k) {
    var orig = history[k];
    history[k] = function (state, title, url) {
      try { if (url != null && /\/open-app/.test(new URL(String(url), location.href).pathname)) return; } catch (e) {}
      return orig.apply(this, arguments);
    };
  });
})();
