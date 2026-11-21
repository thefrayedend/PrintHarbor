"""PrintHarbor plugin host. Loads plugins from a folder and answers JSON-RPC
over HTTP on 127.0.0.1. Launched by PrintHarbor; not meant to be run by hand.

A site plugin is a folder with plugin.json (manifest) and a Python entry file
that defines any of: search(ctx, query), model(ctx, id),
download(ctx, id, file_id), match(ctx, url). See docs/plugins/sites.md."""
import importlib.util, json, os, sys, traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)  # so plugins can `import phlib`

PLUGINS = {}   # id -> {"manifest":..., "module":..., "dir":...}
ERRORS = {}    # id -> error string


def load_plugins(root):
    PLUGINS.clear(); ERRORS.clear()
    if not os.path.isdir(root):
        return
    for name in sorted(os.listdir(root)):
        if name.startswith("."):
            continue  # .runtime, and half-finished installs (.install-*)
        pdir = os.path.join(root, name)
        man_path = os.path.join(pdir, "plugin.json")
        if not os.path.isfile(man_path):
            continue
        try:
            with open(man_path, encoding="utf-8") as f:
                man = json.load(f)
            pid = man.get("id") or name
            entry = os.path.join(pdir, man.get("entry", "plugin.py"))
            spec = importlib.util.spec_from_file_location("ph_plugin_" + pid, entry)
            mod = importlib.util.module_from_spec(spec)
            sys.path.insert(0, pdir)
            try:
                spec.loader.exec_module(mod)
            finally:
                sys.path.remove(pdir)
            man.setdefault("name", pid)
            man["dir"] = pdir
            PLUGINS[pid] = {"manifest": man, "module": mod, "dir": pdir}
        except Exception:
            ERRORS[name] = traceback.format_exc()


def manifests():
    out = []
    for pid, p in PLUGINS.items():
        m = dict(p["manifest"])
        fns = [fn for fn in ("search", "model", "download", "match", "run",
                             "status", "upload", "pause", "resume", "cancel", "snapshot", "light",
                             "on_event")
               if hasattr(p["module"], fn)]
        m["functions"] = fns
        out.append(m)
    for name, err in ERRORS.items():
        out.append({"id": name, "name": name, "error": err.strip().splitlines()[-1] if err else "failed to load"})
    return out


def call(plugin, fn, ctx, args):
    p = PLUGINS.get(plugin)
    if not p:
        raise KeyError("no plugin %r" % plugin)
    f = getattr(p["module"], fn, None)
    if f is None:
        raise AttributeError("plugin %r has no %r" % (plugin, fn))
    return f(ctx, **(args or {}))


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self._send(200, {"ok": True, "plugins": manifests()})
        else:
            self._send(404, {"ok": False, "error": "not found"})

    def do_POST(self):
        if self.path != "/rpc":
            self._send(404, {"ok": False, "error": "not found"}); return
        try:
            n = int(self.headers.get("Content-Length", "0"))
            req = json.loads(self.rfile.read(n) or b"{}")
            result = call(req.get("plugin"), req.get("fn"), req.get("ctx") or {}, req.get("args") or {})
            self._send(200, {"ok": True, "result": result})
        except Exception as e:
            # Plugins raise RuntimeError for clean, user-facing messages; show
            # those as-is. Other exceptions keep their type for debugging.
            msg = str(e) if isinstance(e, RuntimeError) else "%s: %s" % (type(e).__name__, e)
            self._send(200, {"ok": False, "error": msg})


def main():
    import argparse
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, required=True)
    ap.add_argument("--plugins", required=True)
    a = ap.parse_args()
    load_plugins(a.plugins)
    srv = ThreadingHTTPServer(("127.0.0.1", a.port), Handler)
    srv.serve_forever()


if __name__ == "__main__":
    main()
