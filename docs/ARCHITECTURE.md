# Architecture

## Principles

1. **Stock Orca, pinned version.** We call the unmodified OrcaSlicer CLI as a separate
   process. No fork. The command line is built from a template in `config.json`, so a
   CLI change in a new Orca release is a config fix, not a code change.
2. **Light by default.** One Go binary, standard library only, JSON-file storage. The
   browser does 3D work. Orca only runs while it's slicing. Cameras load on demand.
3. **Never touch the original.** An uploaded 3MF is stored read-only. Every
   printer assignment produces its own derived job.
4. **Explain, don't just say yes or no.** Every fit check returns reasons.
5. **Drivers are plug-ins.** Brand-specific code lives only in `internal/printers/`.

## Components

```
browser (web/)  ──HTTP/JSON──►  api  ──►  projects (threemf)  ──►  fit
                                 │                                  │
                                 ├──► printers.Registry ◄───────────┘
                                 │        └─ drivers: moonraker, mock, (bambu, sdcp, ...)
                                 └──► slicer.Queue ──► Orca CLI (separate process)
                                              └──► driver.Upload(gcode, start)
```

| Package | Job |
|---|---|
| `cmd/printharbor` | Entry point: loads config, wires everything, serves HTTP |
| `internal/config` | `config.json` loading, defaults |
| `internal/threemf` | Reads 3MF: plates, parts, bounding boxes, filaments, painting |
| `internal/profiles` | Loads Orca system profiles, resolves `inherits` chains |
| `internal/printers` | Printer model, capability flags, driver interface, registry, drivers |
| `internal/fit` | Plate vs printer: capable / configured / available, with reasons |
| `internal/slicer` | Orca CLI runner and a bounded job queue |
| `internal/store` | Small JSON-on-disk store |
| `internal/api` | HTTP API, projects, dispatch/tracking, library routes, cameras/AI/alerts, workers, assistant |
| `internal/library` | Local model library: folder/ZIP scanning, managed items, search |
| `internal/mesh` | STL/OBJ/3MF triangle loading, pure-Go preview renderer, browser mesh format |
| `internal/mdns` | Answers `harbor.local` on the LAN |

## Three levels of "can this printer print this plate?"

| Level | Meaning | Example reason |
|---|---|---|
| **Capable** | The hardware can do it at all | "Part exceeds build height" (blocker) |
| **Configured** | It can, with what's set up right now | "Load white PETG" (action) |
| **Available** | It's free to start now | "Printing, 42% done" / "Bed not cleared" |

`idle` and `ready` are separate states. A printer that finished a job stays
`needs_clear` until someone clicks "Parts removed". Camera checks can drive that same
state later.

### How a print ended

"The printer stopped printing" is not the same as "the print finished". The
registry (`internal/printers/registry.go`) follows each print and, when it ends,
reports one of four outcomes through `OnPrintEnd`:

| Outcome | When | What PrintHarbor does |
|---|---|---|
| `completed` | The printer said so, or it stopped at 99 % or more | Run "printed", `print_finished`, Spoolman charged |
| `stopped` | The printer reported a cancel, or Cancel was pressed here | Run "failed", `print_cancelled`, nothing charged |
| `failed` | The printer went into an error | Run "failed", `print_failed`, nothing charged |
| `unknown` | It went idle part-way and never said why | Run "printed" (check the parts), `print_finished` with `outcome: unknown`, nothing charged |

A driver signals a cancel by setting `Status.Stopped`. Spoolman is only ever
charged for `completed`, each pinned spool for the weight its own filament used.

## Colors are roles

A project's filaments (`filament 1..N`, with color and type) are *roles*. Each printer
maps roles to its own slots. v0.1 matches on material type and color; the user confirms.

## Slicing

### Whose value wins for a quality setting

From weakest to strongest: the stock Orca profile → **your print defaults**
(Settings → Printing, `print_defaults` in config.json) → **that printer's own
defaults** (`print_defaults` on the printer in printers.json; a printer can also
name the quality profile to start with, `default_process`) → a setting saved in one of your own presets → a setting
changed for one print or plate (the job's overrides). Everything that slices or
shows a profile's values goes through `resolveProcess`, so the send screen and
the slicer page show what will really be used. All of it is applied by
`App.resolveProcess(lib, name, printerID)`.

### Backups

`internal/api/backup.go`: once a day (and on "Back up now") a dated folder with
everything that can't be made again: projects, printers, runs, your profiles,
library index, config.json. Unchanged files are hard links to the previous
backup's copy, never to the live file. Sliced files, caches and logs are left
out. Folder and days kept are in Settings → General.

### Look before it prints

"Slice & preview" makes a job that is only sliced. Its preview (web/gcode.js,
drawn in 3D by web/preview3d.js: the whole print, coloured by kind of line)
opens by itself when the slice is ready, and Print there sends that same file
(`POST /api/jobs/{id}/send`, `Queue.SendNow`): nothing is sliced twice.

- One job = one plate sliced for one printer.
- Queue with `max_concurrent_slices` (default 1).
- Command template (defaults written into `config.json`):

```
{orca} --slice {plate} --load-settings "{machine};{process}" --load-filaments "{filaments}"
       --arrange 1 --outputdir {outdir} --export-3mf {out3mf} {input}
```

- Output G-code is read from the sliced 3MF (`Metadata/plate_N.gcode`) or from any
  `.gcode` file Orca writes to the output directory.

> **Needs verification on a real Orca install:** the exact flags of the pinned Orca
> version and how it resolves user profiles that use `inherits`. Our profile loader
> flattens `inherits` chains before handing files to Orca, which is the safe choice either way.

## Drivers

```go
type Driver interface {
    Capabilities() Capabilities
    Status(ctx) (Status, error)
    Pause(ctx) / Resume(ctx) / Cancel(ctx) error
    Upload(ctx, filename string, r io.Reader, start bool) error
}
```

Drivers: Moonraker, Bambu (MQTT 3.1.1 + implicit FTPS + port-6000 JPEG camera / RTSP via
ffmpeg), FlashForge (port 8898 HTTP), Elegoo SDCP (websocket 3030 + chunked upload), PrusaLink
(REST, API key or digest), and a simulator. Optional interfaces: `Snapshotter` (camera
frames, served at `/api/printers/{id}/snapshot`), `Lighter`. The filament mapping for an upload
travels in the context (`printers.WithSlotMap`).

All protocol clients (MQTT, FTPS, websocket, mDNS, digest auth) are written on the Go
standard library: the binary has zero third-party dependencies.

## Dispatch and tracking

`Dispatch` walks plates biggest-first and gives each a printer that is idle, bed-clear and
fully "ready" for it (right filament, fits). A `Run` records each physical print; printer
state transitions move runs to *printed*, and the user marks them accepted or failed (failed
prints count as remaining again). Auto mode re-runs dispatch whenever a printer becomes ready.

## Library

Folder items are indexed in `data/library/index.json`; nothing is written into user folders.
Uploaded items and photos live in `data/library/items/<id>`. Thumbnails are cached in
`data/library/thumbs` (3MF embedded thumbnails first, else a software render).

## Where the web UI and the app window live

| Where | What |
| --- | --- |
| `web/index.html` + `web/*.js` | The main page. Plain scripts sharing one global scope; the list and load order are at the top of `web/app.js`. |
| `web/slicer/` | The slicer page (its own frame, ES modules). `slicer.js` is the way in and lists the other files. |
| `web/3djames.html` | 3D James, served with PrintHarbor's bridge added (`internal/api/james.go`). |
| `cmd/printharbor-app/` | The desktop app window (Windows, WebView2). `boot.js` is the script it adds to every page. |
| `internal/api/static.go` | How the UI files are served: fingerprinted (ETag), gzip-compressed, re-checked on every load. |
| `internal/api/guard.go` | Who may call the API: PrintHarbor's own pages and non-browser programs, never another website. |
| `internal/api/cleanup.go` | Housekeeping: old finished jobs and abandoned working files. |
| `tests/ui/` | Browser tests (`tests/ui/run.sh`); see its README. |

Updates: a new `printharbor-windows-amd64.new.exe` / `printharbor-app.new.exe` next to the running
programs is swapped in when PrintHarbor restarts (`cmd/printharbor/main.go: relaunch`). The app
window notices its program was replaced and reopens itself at the next load of PrintHarbor's own
page (`cmd/printharbor-app/main_windows.go: reopenWhenUpdated`); pages left open notice the server's
new build and reload (`web/app.js: checkForUpdate`). Bump `internal/appver.Build` whenever the app
window's code changes.
