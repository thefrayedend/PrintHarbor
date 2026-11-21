"""Example PrintHarbor site plugin. It returns made-up models so you can see the
shape of each function without a real website. Copy this folder to start a new
plugin. See docs/plugins/sites.md."""

DEMO = [
    {"id": "1", "title": "Benchy (demo)", "author": "Example", "likes": 42, "downloads": 1000},
    {"id": "2", "title": "Calibration Cube (demo)", "author": "Example", "likes": 7, "downloads": 300},
    {"id": "3", "title": "Articulated Dragon (demo)", "author": "Example", "likes": 99, "downloads": 5000},
]


def _card(d):
    return {
        "site": "example", "id": d["id"], "title": d["title"], "author": d["author"],
        "thumb": "", "url": "https://example.com/model/" + d["id"],
        "likes": d["likes"], "downloads": d["downloads"], "free": True,
    }


def search(ctx, query=None):
    q = (query or {}).get("text", "").lower()
    items = [d for d in DEMO if q in d["title"].lower()] if q else DEMO
    return {"items": [_card(d) for d in items], "more": False, "cursor": ""}


def model(ctx, id=None):
    d = next((d for d in DEMO if d["id"] == str(id)), DEMO[0])
    m = _card(d)
    m.update({
        "description": "A demonstration model from the example plugin.",
        "images": [], "tags": ["demo", "example"], "license": "CC0", "category": "Demo",
        "files": [{"id": "f1", "name": d["title"].split(" (")[0] + ".stl", "size": 0, "kind": "model"}],
    })
    return m


def download(ctx, id=None, file_id=None):
    # A real plugin returns the file's direct URL (PrintHarbor fetches it).
    # The example has no real file, so it hands back a tiny placeholder STL.
    return {"text": "solid demo\nendsolid demo\n", "name": "demo.stl"}


def match(ctx, url=None):
    import re
    m = re.search(r"example\.com/model/([0-9]+)", url or "")
    return m.group(1) if m else None
