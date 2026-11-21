"""Example PrintHarbor printer plugin: a virtual printer, no hardware needed.

A printer plugin (kind "printer") implements, with the configured printer passed
in each call: status(ctx, printer), upload(ctx, printer, file, name, start),
pause/resume/cancel(ctx, printer), and optionally snapshot/light. status() returns
a dict matching PrintHarbor's printer status (state is required). Here we keep a
little state per printer id so you can watch a print run. Copy this folder and
make the same functions talk to a real printer. See docs/plugins/printers.md."""
import os
import time

_state = {}  # printer id -> dict


def _p(printer):
    return _state.setdefault((printer or {}).get("id", "demo"),
                             {"state": "idle", "name": "", "started": 0.0, "paused_at": 0.0, "paused_total": 0.0})


def status(ctx, printer=None):
    s = _p(printer)
    out = {"state": s["state"], "job_name": s["name"], "nozzle_c": 25, "bed_c": 25}
    if s["state"] in ("printing", "paused"):
        total = 120.0  # a pretend two-minute print
        running = (s["paused_at"] or time.time()) - s["started"] - s["paused_total"]
        prog = max(0.0, min(1.0, running / total))
        out.update({"progress": prog, "nozzle_c": 205, "nozzle_target_c": 205,
                    "bed_c": 60, "bed_target_c": 60, "remaining_sec": int(max(0, total - running))})
        if prog >= 1.0:
            s["state"] = "finished"
            out["state"], out["progress"] = "finished", 1.0
    return out


def upload(ctx, printer=None, file=None, name=None, start=False):
    size = os.path.getsize(file) if file and os.path.exists(file) else 0
    s = _p(printer)
    if start:
        s.update({"state": "printing", "name": name or "print",
                  "started": time.time(), "paused_at": 0.0, "paused_total": 0.0})
    return {"ok": True, "bytes": size}


def pause(ctx, printer=None):
    s = _p(printer)
    if s["state"] == "printing":
        s["state"], s["paused_at"] = "paused", time.time()


def resume(ctx, printer=None):
    s = _p(printer)
    if s["state"] == "paused":
        s["paused_total"] += time.time() - (s["paused_at"] or time.time())
        s["paused_at"], s["state"] = 0.0, "printing"


def cancel(ctx, printer=None):
    _p(printer).update({"state": "idle", "name": "", "started": 0.0, "paused_at": 0.0, "paused_total": 0.0})
