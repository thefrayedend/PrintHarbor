# Writing a printer plugin

A **printer plugin** adds a printer PrintHarbor can control — status, pause/resume/
cancel, and sending a sliced file — in Python, without a new Go driver. Once it's
installed and on, the printer type shows up in **Add printer** and works like any
built-in one (Moonraker, Bambu, FlashForge…).

The bundled **`example-printer`** plugin is a complete, working *virtual* printer
(no hardware): it accepts a print and shows fake progress. Copy its folder in
`data/plugins/` to start your own.

## The folder

```
data/plugins/my-printer/
  plugin.json     the manifest
  plugin.py       the code (name it whatever "entry" says, default plugin.py)
```

## plugin.json (the manifest)

```json
{
  "id": "my-printer",
  "name": "My Printer (LAN)",
  "kind": "printer",
  "entry": "plugin.py",
  "output": "gcode",
  "driver": {
    "address_hint": "192.168.1.50",
    "needs_key": true,
    "key_label": "API key",
    "needs_serial": false,
    "help": "Where to find the address / key on the printer."
  }
}
```

- **`kind`** must be `"printer"`.
- **`output`** is the sliced-file format the printer wants: `"gcode"` (default) or
  `"gcode.3mf"`.
- **`driver`** controls the **Add printer** screen: the address placeholder, whether
  it needs a key / serial, the key's label, and a help line. (Also supports
  `settings` like any plugin — see [README.md](README.md#settings-any-plugin) — for
  per-install options your code reads from `ctx["settings"]`.)

## plugin.py

Every function gets `ctx` and the configured **`printer`** (a dict with `id`,
`name`, `address`, `api_key`, `serial`, `machine_preset`, and `slots`). Implement:

```python
def status(ctx, printer=None):
    # Return a dict describing the printer right now. Only "state" is required.
    return {
        "state": "printing",        # idle | printing | paused | finished | error | offline
        "progress": 0.42,           # 0..1
        "job_name": "dragon.gcode",
        "nozzle_c": 205, "nozzle_target_c": 205,
        "bed_c": 60,   "bed_target_c": 60,
        "remaining_sec": 3600,
        "stopped": False,           # True when the print that just ended was cancelled,
                                    # not completed (see "How a print ended" below)
        # optional: slots=[{index,type,color}], current_slot, snapshot_url, layer, ...
    }

def upload(ctx, printer=None, file=None, name=None, start=False):
    # `file` is a local path to the sliced file PrintHarbor made. Send it to the
    # printer; if start is true, begin the print. Raise on failure.
    data = open(file, "rb").read()
    # ... POST it to printer["address"] using printer["api_key"] ...
    return {"ok": True}
```

Optional functions (PrintHarbor shows the matching controls only when they exist):

```python
def pause(ctx, printer=None):  ...
def resume(ctx, printer=None): ...
def cancel(ctx, printer=None): ...

def light(ctx, printer=None, on=False): ...          # the Light toggle

def snapshot(ctx, printer=None):                      # the camera
    return {"data": "<base64 jpeg/png>", "content_type": "image/jpeg"}
    # (also return a non-empty "snapshot_url" from status() so the camera shows)
```

- **`status` fields** map directly to PrintHarbor's printer status — use the names
  above. Anything you leave out is just blank in the UI.
- **Errors:** raise `RuntimeError("a clear message")` for something the user should
  see (printer unreachable, wrong key); it's shown as-is.
- **How a print ended:** when your printer says a print was cancelled or aborted,
  return `"stopped": True` with the state it is in afterwards (`idle` or
  `finished`). PrintHarbor then records the print as not finished, sends
  `print_cancelled` instead of `print_finished`, and doesn't charge the filament
  to Spoolman. Without it, PrintHarbor goes by the progress it last saw (99 % or
  more counts as completed).
- **Time limits:** `upload` may take up to 30 minutes (a big file over Wi-Fi).
  Every other function must answer within 45 seconds.
- The `phlib` helper (`import phlib`) gives you HTTP without third-party packages
  (`phlib.get_json`, `phlib.post_json`, `phlib.get_bytes`…).

## Installing and using it

Drop the folder in `data/plugins/` (or install the `.zip`), make sure it's **on**
in **Settings → Plugins**, then **Add printer** and pick your printer's name from
the Connection list. Fill in the address / key and save.

Notes:
- A newly installed or newly enabled printer plugin shows up in **Add printer**
  after the page reloads (or on the next restart).
- Turning a printer plugin **off** removes its type from **Add printer**; a printer
  already configured with it stops working until you turn it back on.

A printer plugin runs Python on your computer with access to its network and the
sliced file — only install ones you trust.
