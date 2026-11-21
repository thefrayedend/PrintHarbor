"""Thingiverse, as a PrintHarbor plugin: the official REST API with a free App
Token (Settings -> Model sites). Reference port of the built-in Go site; shows
how an API-based site plugin is written. See docs/plugins/sites.md."""
import re
import phlib

API = "https://api.thingiverse.com"


def _token(ctx):
    t = (ctx.get("keys") or {}).get("thingiverse_token", "").strip()
    if not t:
        raise RuntimeError("Add your free Thingiverse App Token in Settings -> Model sites.")
    return t


def _get(ctx, path):
    return phlib.get_json(API + path, {"Authorization": "Bearer " + _token(ctx)})


def _card(x):
    cid = str(x.get("id"))
    url = x.get("public_url") or ("https://www.thingiverse.com/thing:" + cid)
    creator = x.get("creator") or {}
    return {
        "site": "thingiverse", "id": cid, "title": x.get("name", ""), "author": creator.get("name", ""),
        "thumb": x.get("preview_image") or x.get("thumbnail") or "", "url": url,
        "likes": x.get("like_count", 0), "downloads": x.get("download_count", 0), "free": True,
    }


def search(ctx, query=None):
    query = query or {}
    text = (query.get("text") or "").strip()
    page = int(query.get("page", 0))
    per = 30
    sort = {"newest": "newest", "trending": "popular", "popular": "popular"}.get(query.get("sort", ""), "")
    if not sort:
        sort = "relevant" if text else "popular"
    path = "/search/%s?type=things&per_page=%d&page=%d&sort=%s" % (phlib.urllib.parse.quote(text), per, page + 1, sort)
    out = _get(ctx, path)
    hits = out.get("hits", [])
    return {"items": [_card(x) for x in hits], "more": (page + 1) * per < out.get("total", 0), "cursor": ""}


def model(ctx, id=None):
    d = _get(ctx, "/things/" + str(id))
    m = _card(d)
    m.update({"description": _plain(d.get("description_html") or d.get("description") or ""),
              "license": d.get("license", ""), "images": [], "tags": [], "category": "", "files": []})
    try:
        cats = _get(ctx, "/things/%s/categories" % id)
        if cats:
            m["category"] = cats[0].get("name", "")
    except Exception:
        pass
    try:
        for t in _get(ctx, "/things/%s/tags" % id):
            m["tags"].append(t.get("name", ""))
    except Exception:
        pass
    try:
        for im in _get(ctx, "/things/%s/images" % id):
            best = ""
            for s in im.get("sizes", []):
                if s.get("type") == "display" and s.get("size") == "large":
                    best = s.get("url", "")
                if not best and s.get("type") == "display":
                    best = s.get("url", "")
            if best:
                m["images"].append(best)
    except Exception:
        pass
    if not m["images"] and m["thumb"]:
        m["images"].append(m["thumb"])
    for f in _get(ctx, "/things/%s/files" % id):
        url = f.get("direct_url") or f.get("download_url") or ""
        m["files"].append({"id": "%s|%s" % (f.get("id"), url), "name": f.get("name", ""),
                           "size": f.get("size", 0), "kind": _kind(f.get("name", ""))})
    return m


def download(ctx, id=None, file_id=None):
    url = (file_id or "").split("|", 1)[-1]
    if not url:
        raise RuntimeError("no download address for this file")
    return {"url": url, "headers": {"Authorization": "Bearer " + _token(ctx)}}


def match(ctx, url=None):
    m = re.search(r"thingiverse\.com/thing:([0-9]+)", url or "")
    return m.group(1) if m else None


def _plain(html):
    return re.sub(r"<[^>]+>", " ", html).replace("&nbsp;", " ").strip()


def _kind(name):
    n = name.lower()
    if n.endswith((".stl", ".obj", ".3mf", ".step", ".stp", ".ply")):
        return "model"
    if n.endswith((".gcode", ".bgcode")):
        return "gcode"
    return "other"
