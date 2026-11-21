# PrintHarbor plugins

PrintHarbor is a lean core with plugins around the edges, the way Kodi is a
media player with add-ons. The core does printer control, the library, slicing,
projects and the queue. Everything that varies from person to person or site to
site — which model sites you browse, one day which exotic printer you own — is
meant to be a plugin you can add, change or turn off without touching the core.

Plugins are written in **Python**. That is on purpose: the maker world already
speaks Python (OctoPrint, Klipper tooling, slicer scripts), the scraping and
automation libraries are there, and PrintHarbor already ships a small Python
service for AI failure detection, so Python is not a new thing in the stack.

## What's a plugin, on disk

Plugins live in the `plugins` folder inside PrintHarbor's data folder
(`data/plugins`, shown in **Settings → Plugins**). One folder per plugin:

```
data/plugins/
  .runtime/            the plugin host + the phlib helper library (don't edit)
  example/             a bundled demo site plugin — copy it to start your own
    plugin.json        the manifest: id, name, kind, capabilities
    plugin.py          the code
  thingiverse/         Thingiverse, as a plugin (the reference for API sites)
  my-cool-site/        anything you drop in here is loaded on the next scan
```

A plugin is just a folder with a `plugin.json` and some Python. Drop one in,
hit **Reload** in Settings → Plugins (or restart), and it appears.

## How it runs

PrintHarbor launches a tiny local **plugin host** (`.runtime/host.py`) — a
Python process that loads the plugins and answers PrintHarbor over a local
HTTP connection on `127.0.0.1`. Nothing is exposed to the network. The core
calls a plugin's functions through that host; the plugin never talks to the
printer or the disk directly, it returns data and PrintHarbor does the rest.

If **Python 3 isn't installed**, the host simply doesn't start: plugins are off,
PrintHarbor's built-in model sites keep working, and Settings → Plugins says so.
Plugins are an enhancement, never a requirement.

## Kinds of plugin

| Kind     | Status      | What it does                                   | Reference |
|----------|-------------|------------------------------------------------|-----------|
| `site`    | **working** | A model site to browse, search and import from | [sites.md](sites.md) |
| `tool`    | **working** | A feature you run on demand (one `run()`)       | [tools.md](tools.md) |
| `printer` | **working** | A printer PrintHarbor can control               | [printers.md](printers.md) |

All three kinds work: a folder, a `plugin.json` with its `kind`, and a Python file
implementing that kind's functions. A printer plugin adds a printer type to the
**Add printer** screen without any new Go code.

## Reacting to events (any kind)

Beyond its kind, **any** plugin can react to things happening in the workshop —
a print starting, finishing or failing, a bed being cleared, a printer changing
state — by adding an `on_event` function. This is the foundation automations
like notifications are built on. See **[events.md](events.md)**.

## Settings (any plugin)

A plugin can declare a `settings` list in its `plugin.json`, and PrintHarbor
shows a form for it (the **Settings** button on a plugin in **Settings →
Plugins**). Each field is `{key, label, type, default, help}`, where `type` is
`text`, `password`, `number`, `bool` or `select` (with `options`). The user's
values are saved in `data/plugins/settings.json` and handed to your code every
call as `ctx["settings"]` — a dict of `key → value`, defaults filled in. Use
this for anything per-install: an API base URL, a units preference, a key.

```json
"settings": [
  { "key": "units", "label": "Units", "type": "select", "options": ["mm", "in"], "default": "mm" }
]
```

```python
def run(ctx, params=None):
    units = ctx["settings"].get("units", "mm")
    ...
```

## Updates (a plugin you install)

Add a `version` and an `update_url` to your `plugin.json` and the **Check for
updates** button appears on the plugin. The `update_url` is an **https** address
that is either:

- the plugin's `.zip` directly (PrintHarbor offers it as the latest), or
- a small JSON manifest it can compare first:

```json
{ "version": "1.4.0", "zip": "https://example.com/my-plugin-1.4.0.zip", "notes": "What changed." }
```

PrintHarbor compares `version` to the installed one (numeric, so `1.10 > 1.9`),
and if newer, downloads the `zip` and reinstalls it in place — the same safe,
validated install as a manual `.zip`. Built-in plugins have no `update_url`;
they update with PrintHarbor itself.

## Trust and safety

A plugin is **code that runs on your computer** with whatever network and data
access its Python can reach. Treat a plugin like any program you install: only
add plugins you trust. A plugin only receives the data PrintHarbor hands it (for
a site, your query and that site's key); it can't read the rest of your settings
or other sites' keys. A site plugin that a manifest marks as touching a given
site is given only that site's key.

### What a plugin can touch (permissions)

So you can see what you're turning on, a plugin declares a `permissions` list in
its `plugin.json`, and PrintHarbor shows it on the plugin and asks you to confirm
before it goes on:

```json
"permissions": ["network", "files"]
```

| Permission | Means |
|------------|-------|
| `network`  | Reach the internet and your local network |
| `files`    | Read and write files on your computer |
| `printers` | Control your printers (send, pause, cancel) |
| `camera`   | Use your printer cameras |
| `events`   | See when prints start, finish and fail |
| `settings` | Read your settings and site keys |

PrintHarbor also **infers** the obvious ones so the list is honest even if the
author forgot: a printer plugin controls `printers`, an `on_event` plugin sees
`events`, a `snapshot` function uses the `camera`, and a plugin that needs a site
key reads `settings`. A plugin that declares nothing and needs nothing shows
"Runs locally only".

This is **transparency, not a sandbox** — Python can do what Python can do, so the
permission list is informed consent, not a cage. Only install plugins you trust.

## Writing one

Start from the bundled `example` plugin — copy the folder, change the `id` in
`plugin.json`, and edit `plugin.py`. The per-kind reference pages list the
functions to implement and the shape of the data to return. The helper library
`phlib` (imported as `import phlib`) gives you HTTP without any third-party
packages.

See **[sites.md](sites.md)** for the site-plugin reference.
