# Browser tests

`go test ./...` checks the server. These check what you actually see: they start a real
PrintHarbor on a throw-away data folder (pretend Orca profiles, a pretend slicer, a simulated
printer) and drive the pages in a real, invisible browser. They never touch your own PrintHarbor,
its data, your printers or the internet.

    tests/ui/run.sh

needs Go, Node, Python 3 and Playwright (`npm install -g playwright && npx playwright install chromium`).

| File | What it checks |
| --- | --- |
| `boot.test.js` | The script the app window adds to model sites (`cmd/printharbor-app/boot.js`): the Back / Library buttons, catching downloads, the right-click menu. Runs against a pretend model site. |
| `slicer.scenario.js` | Walks the slicer page through 65 steps (add STL / OBJ / 3MF, select, copy, drag, arrange, undo, plates, settings, filaments, paint, slice, preview, save as a project, print, reopen) and records what the page looks like after each one. |
| `compare.js` | Compares two such recordings. `baseline.json` is the recording of the current version. |
| `ui.test.js` | The main page: every page and dialog opens, files arrive compressed and are not sent twice, settings save, a plate goes through the queue to the simulated printer, 3D James loads. |
| `mkfixture.sh` | Builds the throw-away set-up. |

## Changing the slicer page without changing what it does

The slicer page is the biggest piece of the UI (`web/slicer/`). To tidy it up, move code between
files, or split a file, and be sure nothing changed:

1. `tests/ui/run.sh` before you start: it must pass.
2. Make the change.
3. `tests/ui/run.sh` again. `0 difference(s)` means every one of the 65 steps left the page in
   exactly the same state as before.

When a change is *meant* to alter the page, look at the differences it reports, and if they are
the ones you intended, `tests/ui/run.sh --record` makes the new behaviour the baseline.
