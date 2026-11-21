# Writing a site plugin

A **site plugin** adds an online model site that PrintHarbor can browse, search,
show models from, and import into your library — like Printables or Thingiverse.
It's a folder in `data/plugins/` with a manifest and a Python file.

The quickest start is to copy the bundled **`example`** plugin and edit it. The
**`thingiverse`** plugin is the reference for a real site that has an API.

## The folder

```
data/plugins/my-site/
  plugin.json     the manifest
  plugin.py       the code (name it whatever "entry" says)
```

## plugin.json (the manifest)

```json
{
  "id": "my-site",
  "name": "My Site",
  "kind": "site",
  "url": "https://mysite.com",
  "needs_key": false,
  "key": "my_site_token",
  "key_help": "Where to get the key and where to paste it.",
  "can_download": true,
  "match": ["mysite\\.com/model/([0-9]+)"],
  "entry": "plugin.py",
  "api": "1"
}
```

| Field          | Meaning |
|----------------|---------|
| `id`           | Unique, lowercase. If it equals a built-in site's id (`printables`, `thingiverse`, `makerworld`, `cults`), your plugin **replaces** that built-in. |
| `name`         | Shown on the Library's site tabs. |
| `kind`         | `"site"`. |
| `url`          | The site's home page. |
| `needs_key`    | `true` if the site needs a credential before it works. |
| `key`          | The Settings field your key comes from (e.g. `thingiverse_token`). PrintHarbor passes it to you in `ctx`. |
| `key_help`     | One sentence telling the user how to get the key. |
| `can_download` | `true` if your plugin can return files to import. `false` means browse-only (the user downloads in the browser; capture handles it). |
| `match`        | List of regexes that recognise this site's model URLs. **Group 1 is the model id.** Used for "paste a link". |
| `entry`        | The Python file. Defaults to `plugin.py`. |
| `api`          | Plugin API version. `"1"`. |

## plugin.py (the code)

Implement any of these functions. Each takes `ctx` first; leave out the ones
your site can't do.

```python
import phlib

def search(ctx, query=None):
    """Browse or search. Return a page of result cards."""
    ...

def model(ctx, id=None):
    """Full details for one model, including its files."""
    ...

def download(ctx, id=None, file_id=None):
    """Where to get one file. Return a direct URL PrintHarbor should fetch."""
    ...

def match(ctx, url=None):
    """Optional. Return the model id for one of this site's URLs, or None.
    If you leave this out, the manifest's "match" regexes are used instead."""
    ...
```

### `ctx`

A dict PrintHarbor passes to every call:

```python
ctx["keys"]   # {"thingiverse_token": "...", ...} — the Settings credentials
```

Read your key with `ctx["keys"].get("my_site_token", "")`.

### `search(ctx, query)` → page

`query` is `{"text": "dragon", "sort": "popular", "page": 0, "cursor": ""}`
(`sort` is one of `popular`, `trending`, `newest`; `page` is 0-based; `cursor`
is whatever you returned last time, for sites that page by cursor).

Return:

```python
{
  "items": [ card, card, ... ],
  "more": True,          # is there another page?
  "cursor": ""           # pass-through for the next page, if you use cursors
}
```

A **card** is a short model summary:

```python
{
  "site": "my-site", "id": "123", "title": "Thing",
  "author": "Someone", "thumb": "https://.../thumb.jpg",
  "url": "https://mysite.com/model/123",
  "likes": 0, "downloads": 0, "free": True
}
```

### `model(ctx, id)` → model

A card plus the full details and the file list:

```python
{
  ...all the card fields...,
  "description": "plain text",
  "images": ["https://.../big1.jpg", ...],
  "tags": ["flexi", "toy"],
  "license": "CC BY",
  "category": "Toys",
  "files": [
    {"id": "file-123", "name": "dragon.stl", "size": 0, "kind": "model"}
  ]
}
```

`kind` is `model` (STL/OBJ/3MF/STEP…), `gcode`, or `other`. The `id` of a file
is yours to define — put whatever `download()` needs into it (Thingiverse packs
the download URL into it).

### `download(ctx, id, file_id)` → where to get the file

Return a direct URL (with any auth headers) and PrintHarbor fetches it:

```python
return {"url": "https://.../dragon.stl", "headers": {"Authorization": "Bearer ..."}}
```

For a plugin with no real file (like the example), you can return a small inline
body instead:

```python
return {"text": "solid x\nendsolid x\n", "name": "demo.stl"}
```

If your site can't hand over files at all (download needs a logged-in browser
session, like Cults3D), set `can_download` to `false` in the manifest and leave
`download` out — the user downloads in the browser and capture files it.

## phlib — the helper library

`import phlib` gives you HTTP without installing anything:

```python
phlib.get_json(url, headers=None)          # GET + parse JSON
phlib.post_json(url, data, headers=None)   # POST JSON, parse JSON reply
phlib.get_bytes(url, headers=None)         # GET raw bytes
phlib.get_text(url, headers=None)          # GET text
phlib.qs({"q": "dragon", "page": 2})       # url-encode query params
```

## A tiny complete example

```python
# plugin.py  (manifest id: "mysite", needs_key: true, key: "mysite_token")
import phlib
API = "https://api.mysite.com"

def _key(ctx):
    k = ctx["keys"].get("mysite_token", "").strip()
    if not k:
        raise RuntimeError("Add your My Site key in Settings → Model sites.")
    return k

def search(ctx, query=None):
    q = (query or {}).get("text", "")
    data = phlib.get_json(f"{API}/search?{phlib.qs({'q': q})}",
                          {"Authorization": "Bearer " + _key(ctx)})
    items = [{"site": "mysite", "id": str(x["id"]), "title": x["name"],
              "author": x.get("by", ""), "thumb": x.get("thumb", ""),
              "url": x["url"], "likes": x.get("likes", 0),
              "downloads": x.get("downloads", 0), "free": True}
             for x in data["results"]]
    return {"items": items, "more": data.get("next") is not None, "cursor": ""}

def model(ctx, id=None):
    d = phlib.get_json(f"{API}/model/{id}", {"Authorization": "Bearer " + _key(ctx)})
    return {
        "site": "mysite", "id": str(id), "title": d["name"], "author": d.get("by", ""),
        "thumb": d.get("thumb", ""), "url": d["url"], "likes": d.get("likes", 0),
        "downloads": d.get("downloads", 0), "free": True,
        "description": d.get("description", ""), "images": d.get("images", []),
        "tags": d.get("tags", []), "license": d.get("license", ""),
        "category": d.get("category", ""),
        "files": [{"id": str(f["id"]) + "|" + f["url"], "name": f["name"],
                   "size": f.get("size", 0), "kind": "model"} for f in d["files"]],
    }

def download(ctx, id=None, file_id=None):
    url = file_id.split("|", 1)[-1]
    return {"url": url, "headers": {"Authorization": "Bearer " + _key(ctx)}}
```

## Testing your plugin

1. Drop the folder in `data/plugins/`.
2. **Settings → Plugins → Reload**. Your site should appear; a load error shows
   there with the Python message.
3. Open the Library, click your site's tab, search, open a model, import a file.
4. Change code, hit Reload again. No PrintHarbor restart needed.

Errors a function raises come back to PrintHarbor as the message the user sees,
so raise `RuntimeError("something readable")` for problems like a missing key.
