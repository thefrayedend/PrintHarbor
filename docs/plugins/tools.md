# Writing a tool plugin

A **tool plugin** adds a feature you run on demand — a small utility that takes
some inputs, does something, and shows a result. It's the simplest kind of
plugin: one `run()` function. Tool plugins show up under **Settings → Plugins →
Tools**, each with a form built from the inputs it declares and a **Run** button.

The bundled **`filamentcost`** plugin is a complete, working example — copy its
folder in `data/plugins/` to start your own.

## The folder

```
data/plugins/my-tool/
  plugin.json     the manifest
  plugin.py       the code (name it whatever "entry" says, default plugin.py)
```

## plugin.json (the manifest)

```json
{
  "id": "my-tool",
  "name": "My Tool",
  "kind": "tool",
  "version": "1.0.0",
  "entry": "plugin.py",
  "note": "One line describing what it does.",
  "settings": [
    { "key": "currency", "label": "Currency symbol", "type": "text", "default": "$" }
  ],
  "params": [
    { "key": "grams", "label": "Filament used (g)", "type": "number", "default": 25 }
  ]
}
```

- **`kind`** must be `"tool"`.
- **`params`** are the inputs shown in the Run form. Same field shape as
  `settings`: `{key, label, type, default, help, options}` with `type` one of
  `text`, `password`, `number`, `bool`, `select`. They're passed to `run()` as
  `params` for that one run.
- **`settings`** (optional) are per-install values the user sets once; they
  arrive as `ctx["settings"]`. See [README.md](README.md#settings-any-plugin).
- **`version`** / **`update_url`** (optional) enable updates. See
  [README.md](README.md#updates-a-plugin-you-install).

## plugin.py

Define one function:

```python
def run(ctx, params=None):
    p = params or {}
    grams = float(p.get("grams") or 0)
    sym = ctx["settings"].get("currency", "$")
    return {
        "title": "Filament cost",
        "text": f"{grams} g costs about {sym}{grams * 0.02:.2f}",
    }
```

- **`ctx`** is `{"settings": {...}, "keys": {...}}` — your plugin's settings, and
  the model-site keys (rarely needed by a tool).
- **`params`** is a dict of the input values the user entered (`number` fields
  arrive as numbers, `bool` as true/false).
- **Return** a dict. PrintHarbor shows it like this:
  - `title` — a bold heading (optional).
  - `text` — the main result line(s).
  - `html` — if present, rendered as-is instead of `text` (you own the markup;
    only use it with content you generate).
  - any other fields are ignored by the UI but returned to callers, so a tool
    can also be useful to other code later.
- Raise `RuntimeError("a clear message")` for an error the user should see; the
  message is shown as-is. Other exceptions show their type for debugging.

## Running it

Drop the folder in `data/plugins/`, hit **Reload** in Settings → Plugins (or
restart), and your tool appears under **Tools**. Click **Open**, fill the form,
**Run**. Turn it off like any plugin; it then disappears from Tools.

The `phlib` helper (`import phlib`) gives you HTTP (`phlib.get_json`,
`phlib.get_bytes`, …) without any third-party packages, the same as site
plugins — handy for a tool that calls an API.
