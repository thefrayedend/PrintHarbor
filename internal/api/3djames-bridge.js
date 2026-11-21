// PrintHarbor bridge for 3D James. 3D James runs here in its "server" mode,
// so its Open / Save dialogs show the PrintHarbor library folders (My designs,
// Models...) and everything saved lands in the library. This adds:
//  - opening a library file from PrintHarbor (address #folder=..&path=.. or
//    #url=..&name=.., or a message from the PrintHarbor page),
//  - telling the PrintHarbor page when something was saved.
(function () {
  "use strict";
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const q = (o) => new URLSearchParams(o).toString();

  async function ready() {
    for (let i = 0; i < 600; i++) {
      const j = window.__3dj;
      // Ready once the geometry engine has started (it hides #loading),
      // which is after 3D James has restored your last session.
      const l = document.getElementById("loading");
      if (j && j.app && j.store && j.store.doc && j.commands && j.commands.openFile && l && l.hidden) return j;
      await sleep(100);
    }
    throw new Error("3D James did not start");
  }

  // A file handle for a library file, like 3D James's own server handles,
  // so Ctrl+S saves straight back into the library.
  function libraryHandle(folder, path, etag) {
    return {
      kind: "file", __server: 1, folder, path, etag, loose: false, replaceOnce: false,
      get name() { return this.path.split("/").pop(); },
      async getFile() {
        const r = await fetch("/api/file?" + q({ folder: this.folder, path: this.path }), { cache: "no-store" });
        if (!r.ok) throw new Error("Could not open " + this.name);
        this.etag = r.headers.get("ETag");
        return new File([await r.arrayBuffer()], this.name);
      },
      async createWritable() { const parts = []; return { write: async (d) => { parts.push(d); }, close: async () => this.put(new Blob(parts)), abort: async () => {} }; },
      async put(blob, force) {
        const r = await fetch("/api/file?" + q({ folder: this.folder, path: this.path }), { method: "PUT", body: blob, headers: { "If-Match": force || this.loose || this.replaceOnce ? "any" : this.etag || "new" } });
        if (r.status === 409) { const n = await r.json().catch(() => ({})); throw Object.assign(new Error(this.name + " was changed since you opened it."), { conflict: n }); }
        const d = await r.json().catch(() => ({}));
        if (!r.ok) throw new Error(d.message || "Could not save");
        this.etag = d.etag; this.replaceOnce = false;
      },
    };
  }

  function saved(name) {
    try { window.parent !== window && window.parent.postMessage({ ph: "3dj-saved", name }, location.origin); } catch (e) {}
  }

  // Ask before replacing unsaved work (like 3D James does).
  function askSave(j) {
    return new Promise((resolve) => {
      const back = document.createElement("div");
      back.className = "modal-back";
      back.innerHTML = `<div class="modal"><h2>Save changes?</h2><p>Save the changes to ${(j.store.doc.name || "your project").replace(/[<>&]/g, "")} before opening the other file?</p>
        <div class="btns"><button class="btn" data-a="cancel">Cancel</button><button class="btn" data-a="no">Don't save</button><button class="btn primary" data-a="save">Save</button></div></div>`;
      back.onclick = async (e) => {
        const a = e.target.closest("[data-a]");
        if (!a) return;
        back.remove();
        if (a.dataset.a === "cancel") return resolve(false);
        if (a.dataset.a === "save") { await j.commands.saveDoc(); return resolve(!j.app.dirty); }
        resolve(true);
      };
      document.body.append(back);
    });
  }

  // Open a library file. folder/path: where it is for 3D James (saves go
  // next to it); url: a plain download address (managed library items).
  async function open(o) {
    const j = await ready();
    await sleep(300);
    if (j.app.dirty && !(await askSave(j))) return;
    let file, handle = null;
    const name = o.name || (o.path || "").split("/").pop() || "model.stl";
    const ext = name.split(".").pop().toLowerCase();
    if (o.folder && o.path) {
      handle = libraryHandle(o.folder, o.path, null);
      file = await handle.getFile();
      // Save as… starts in the same folder.
      const dir = o.path.includes("/") ? o.path.slice(0, o.path.lastIndexOf("/")) : "";
      try { localStorage.setItem("3dj.fb.PrintHarbor", JSON.stringify({ folder: o.folder, path: o.folder === "designs" ? "" : dir })); } catch (e) {}
    } else {
      const r = await fetch(o.url, { cache: "no-store" });
      if (!r.ok) throw new Error("Could not download " + name);
      file = new File([await r.arrayBuffer()], name);
    }
    // Only 3D James projects save back over the file itself; models you
    // downloaded are never overwritten (Save asks for a name next to them).
    await j.commands.openFile(file, ext === "3dj" ? handle : null);
  }
  window.__phOpen = (o) => open(o).catch((e) => alert(e.message));

  window.addEventListener("message", (e) => {
    if (e.origin === location.origin && e.data && e.data.ph === "3dj-open") window.__phOpen(e.data);
  });
  const h = new URLSearchParams(location.hash.slice(1));
  if ((h.get("path") || h.get("url")) && new URLSearchParams(location.search).get("convert") !== "1") {
    window.__phOpen({ folder: h.get("folder"), path: h.get("path"), url: h.get("url"), name: h.get("name") });
    history.replaceState(null, "", location.pathname);
  }
  // Saves (including exports made with Save as) are reported to PrintHarbor.
  const origFetch = window.fetch;
  window.fetch = function (input, init) {
    const p = origFetch.apply(this, arguments);
    try {
      const u = typeof input === "string" ? input : input.url;
      if (init && init.method === "PUT" && /^\/api\/file\?/.test(u)) {
        const ps = new URLSearchParams(u.split("?")[1]), path = ps.get("path") || "";
        if (!/(^|\/)\.print\//.test(path)) p.then((r) => {
          if (!r.ok) return;
          saved(path);
        });
      }
    } catch (e) {}
    return p;
  };

  // Builder mode (?convert=1, hidden in the library page, for printing):
  // open one project, build the STL/3MF, hand it to PrintHarbor, report back.
  if (new URLSearchParams(location.search).get("convert") === "1") {
    const c = new URLSearchParams(location.hash.slice(1));
    const done = (ok, error) => window.parent.postMessage({ ph: "3dj-converted", job: c.get("job"), ok, error }, location.origin);
    (async () => {
      const j = await ready();
      const r = await origFetch(c.get("url"), { cache: "no-store" });
      if (!r.ok) throw new Error("Could not read the project");
      await j.commands.openFile(new File([await r.arrayBuffer()], c.get("name") || "project.3dj"), null);
      await sleep(300);
      const data = await j.commands.buildFileData(c.get("format") || "stl");
      const up = await origFetch(c.get("dest"), { method: "POST", body: new Blob([data]) });
      if (!up.ok) throw new Error((await up.json().catch(() => ({}))).error || "Upload failed");
      done(true);
    })().catch((e) => done(false, e.message || String(e)));
    return;
  }
})();
