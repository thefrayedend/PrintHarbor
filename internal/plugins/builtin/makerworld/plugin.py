"""MakerWorld (Bambu Lab), as a PrintHarbor plugin: Bambu's design-service API
with your MakerWorld account token (the "token" cookie). Faithful port of the
built-in Go site (internal/sites/makerworld.go). See docs/plugins/sites.md."""
import re
import urllib.error
import urllib.parse
import phlib

API = "https://api.bambulab.com/v1"


def _token(ctx):
    t = (ctx.get("keys") or {}).get("makerworld_token", "").strip()
    if not t:
        raise RuntimeError("Add your MakerWorld token in Settings → Model sites.")
    return t


def _hdr(tok):
    return {"Authorization": "Bearer " + tok, "Origin": "https://makerworld.com",
            "Referer": "https://makerworld.com/", "x-bbl-app-source": "makerworld"}


def _get(ctx, path):
    tok = _token(ctx)
    try:
        return phlib.get_json(API + path, _hdr(tok))
    except urllib.error.HTTPError as e:
        if e.code == 418:
            raise RuntimeError("MakerWorld asked for a robot check (captcha). Open the model on "
                               "makerworld.com while signed in, pass the check there, then try again")
        raise


def _card(d):
    did = str(d.get("id"))
    c = {
        "site": "makerworld", "id": did,
        "title": d.get("titleTranslated") or d.get("title") or "",
        "thumb": d.get("cover") or d.get("coverUrl") or "",
        "likes": d.get("likeCount", 0), "downloads": d.get("downloadCount", 0),
        "free": True, "url": "https://makerworld.com/en/models/" + did, "author": "",
    }
    creator = d.get("designCreator")
    if creator:
        c["author"] = creator.get("name", "")
    return c


def search(ctx, query=None):
    query = query or {}
    text = (query.get("text") or "").strip()
    page = int(query.get("page", 0))
    limit = 30
    order = {"newest": "time", "trending": "hotScore", "popular": "downloadCount"}.get(query.get("sort", ""), "hotScore")
    if text:
        path = ("/search-service/select/design2?keyword=" + urllib.parse.quote(text) +
                "&limit=%d&offset=%d&orderBy=%s" % (limit, page * limit, order))
    else:
        path = ("/search-service/select/design/nav?navKey=Trending&limit=%d&offset=%d&orderBy=%s"
                % (limit, page * limit, order))
    try:
        out = _get(ctx, path)
    except RuntimeError as e:
        raise RuntimeError("%s (you can still paste a MakerWorld link above)" % e)
    hits = out.get("hits") or []
    return {"items": [_card(d) for d in hits], "more": (page + 1) * limit < out.get("total", 0), "cursor": ""}


def model(ctx, id=None):
    d = _get(ctx, "/design-service/design/" + urllib.parse.quote(str(id), safe=""))
    m = _card(d)
    m.update({"description": _plain(d.get("summaryTranslated") or d.get("summary") or ""),
              "license": d.get("license", ""), "tags": list(d.get("tags") or []),
              "images": [], "category": "", "files": []})
    ext = d.get("designExtension")
    if ext:
        for p in (ext.get("design_pictures") or []):
            if p.get("url"):
                m["images"].append(p["url"])
    if not m["images"] and m["thumb"]:
        m["images"].append(m["thumb"])
    # Each "print profile" (instance) is a ready 3MF.
    for inst in (d.get("instances") or []):
        name = inst.get("titleTranslated") or inst.get("title") or ("Print profile " + str(inst.get("id")))
        if inst.get("isDefault"):
            name += " (default)"
        m["files"].append({"id": str(inst.get("id")), "name": name + ".3mf", "size": 0, "kind": "model"})
    return m


def download(ctx, id=None, file_id=None):
    r = _get(ctx, "/design-service/instance/" + urllib.parse.quote(str(file_id), safe="") + "/f3mf?type=download")
    url = r.get("url") or ""
    if not url:
        raise RuntimeError("MakerWorld gave no download address")
    return {"url": url}


def match(ctx, url=None):
    m = re.search(r"makerworld\.com/[^?#]*models/([0-9]+)", url or "")
    return m.group(1) if m else None


def _plain(html):
    return re.sub(r"<[^>]+>", " ", html).replace("&nbsp;", " ").strip()
