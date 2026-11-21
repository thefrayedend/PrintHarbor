# PrintHarbor

**One project, every printer in your workshop.**

PrintHarbor is a free, 100% open-source (AGPL-3.0), self-hosted web app for makers whose
workshops have a mix of 3D printers from different brands and sizes: Bambu, Snapmaker,
FlashForge, Elegoo, Prusa, Klipper machines and more. It wraps the stock **OrcaSlicer**
engine in an easy browser UI, gives every printer one shared control panel, and keeps a
private model library of everything you buy, download and design.

## What it does

**Multi-printer projects**
- Open a multi-plate 3MF, or build a project from STL/OBJ files: add plates, drop parts on
  any plate, move parts between plates, or let PrintHarbor pack them into plates for a printer's bed.
- Every plate lists your printers as *Ready*, *Needs action* or *Can't print*, with the reason
  ("Load gold PLA in slot 2", "Part is 300 × 280 mm, bed is 180 × 180 mm") and a rough time estimate.
- **Send plates to ready printers** in one click, or let it keep going automatically as printers free up.
- Copies per plate, part tracking (printing → printed → *you* accept or mark failed → reprint).
- Pin a project color to a specific Spoolman spool so every copy comes from the same roll.

**Easy Orca settings**
- Brand → Printer → Nozzle picker over Orca's own profiles, compatible quality and filament
  presets only, smart defaults, common settings in plain English, search for any other Orca
  setting, save as your own preset. Your desktop Orca presets show up automatically.
- G-code preview (layer by layer) with Orca's print time and filament weight.

**One control panel for every printer**
| Connection | Printers | Status | Pause/stop | Upload & print | Camera | Filament slots |
|---|---|---|---|---|---|---|
| Klipper (Moonraker) | Voron, Creality K, Elegoo Neptune 4, Sovol, Qidi, Snapmaker U1, FlashBridge, Anycubic with Rinkhals… | ✓ | ✓ | ✓ | ✓ (incl. U1) | config |
| Bambu Lab (LAN mode) | X1, P1, A1, H2 | ✓ | ✓ | ✓ (3MF + AMS mapping) | ✓ (RTSP needs ffmpeg) | ✓ AMS |
| FlashForge (LAN mode) | Adventurer 5M / Pro, AD5X | ✓ | ✓ | ✓ | ✓ | ✓ |
| Elegoo SDCP | Centauri Carbon | ✓ | ✓ | ✓ | ✓ | — |
| PrusaLink | MK4/S, XL, MINI, Core One | ✓ | ✓ | ✓ | URL | — |
| Simulated | for trying things out | ✓ | ✓ | ✓ | — | config |

Plus: camera wall, "bed is clear" detection from the camera (it learns your empty bed),
optional AI failure detection through Obico's ML server, alerts in the app and to Discord
with a camera picture, Klipper toolchanger tool remapping.

**Library: your private Printables/MakerWorld**
- Point it at the folders where you already keep models. Every folder with model files becomes
  a model, the folders above become categories, ZIP downloads work too. PrintHarbor never
  changes or deletes anything there.
- Or upload models with photos. Titles, descriptions, tags, designer, license, source link,
  print notes, favorites, search, 3D preview, auto-rendered thumbnails.
- **Print…** sends a model straight to a new project or onto a plate of an existing one;
  **Save to library** keeps a project for next time.

**Optional extras**
- Slicing workers: let a faster PC on your network do the slicing.
- Workshop assistant: ask "what's printing?" to a local model through Ollama (actions need your confirmation).
- `http://harbor.local:7621` on your network (mDNS).

## Try it on Windows

1. Install OrcaSlicer normally (stock, unmodified).
2. Double-click `start-windows.bat`. It opens `http://localhost:7621`. Your Orca profiles and
   presets are found automatically.
3. **Printers → Add printer** (no printer handy? pick "Simulated printer").
4. **Projects →** drop in a 3MF or some STLs, or **Library → Folders…** to add your model folders.

PrintHarbor runs in the **system tray** (the anchor icon by the clock), not in a console window.
Click the icon to open the page; right-click it for **Restart**, **Start with Windows**,
**Open log file** and **Quit**. Settings also has a **Restart PrintHarbor** button.

Updates: if PrintHarbor is running when a new version arrives, it's saved as
`dist\printharbor-windows-amd64.new.exe`. **Restart** (tray or Settings) installs it; so does
`start-windows.bat` on the next start.

## Other ways to run it

- **Build from source:** Go 1.22+, then `go build -o printharbor ./cmd/printharbor` and `./printharbor`.
- **Docker:** see the `Dockerfile` (bundles stock OrcaSlicer; use `--network host`).
- **Releases:** the GitHub workflow builds Windows, Linux and macOS binaries (amd64 + arm64).

## Hardware

- **8 GB RAM**: the core, one slice at a time, cameras and AI detection.
- **16 GB RAM**: more concurrent slices and the heavy optional modules.
- Anything Go and Orca run on: x86 mini PCs, Raspberry Pi 5, NAS boxes, Windows, macOS, Linux.

## Status and honesty notes

This is young software. Everything above is implemented and covered by automated tests
against simulated printers and a stand-in for Orca. Real-hardware checks so far: Klipper
status and cameras (incl. Snapmaker U1) and the Orca profile loader on real profiles. Still to
verify on real hardware: slicing with your installed Orca CLI, the Bambu, FlashForge, Elegoo
and PrusaLink drivers, and toolchanger remapping.

## Contributing

Drivers are small and self-contained (`internal/printers/`). Each declares its capabilities, so
a half-finished driver is still useful. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## License

AGPL-3.0-or-later. PrintHarbor runs the unmodified OrcaSlicer CLI as a separate program.
