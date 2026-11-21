"""Cults3D, as a PrintHarbor plugin: the official GraphQL API with HTTP Basic
auth (your Cults nickname + an API key). Cults' API gives details and pictures
but, by Cults' own rules, never the files, so this plugin is browse-only
(can_download: false) and the browser extension captures downloads. Faithful
port of the built-in Go site (internal/sites/cults.go). See docs/plugins/sites.md."""
import base64
import json
import re
import urllib.error
import phlib

API = "https://cults3d.com/graphql"

FIELDS = ("identifier name url illustrationImageUrl likesCount downloadsCount "
          "creator { nick } price(currency: USD) { cents }")

NOTE = ("Cults3D doesn't hand files to apps: open the model in PrintHarbor's browser "
        "and click Download there, and it's filed in your library automatically.")


def _auth(ctx):
    keys = ctx.get("keys") or {}
    user = (keys.get("cults_user") or "").strip()
    key = (keys.get("cults_key") or "").strip()
    if not user or not key:
        raise RuntimeError("Add your Cults3D nickname and API key in Settings → Model sites.")
    token = base64.b64encode(("%s:%s" % (user, key)).encode()).decode()
    return {"Authorization": "Basic " + token}


def _gql(ctx, query, variables=None):
    try:
        env = phlib.post_json(API, {"query": query, "variables": variables or {}}, _auth(ctx))
    except urllib.error.HTTPError as e:
        if e.code in (401, 403):
            raise RuntimeError("Cults3D refused the nickname or API key")
        raise
    data = env.get("data")
    errs = env.get("errors")
    if errs and (data is None or len(json.dumps(data)) < 8):
        raise RuntimeError("Cults3D: " + (errs[0].get("message") or "request failed"))
    return data or {}


def _card(x):
    price = x.get("price")
    c = {
        "site": "cults", "id": x.get("identifier", ""), "title": x.get("name", ""),
        "url": x.get("url", ""), "thumb": x.get("illustrationImageUrl", ""),
        "likes": x.get("likesCount", 0), "downloads": x.get("downloadsCount", 0),
        "free": price is None or price.get("cents", 0) == 0, "author": "",
    }
    creator = x.get("creator")
    if creator:
        c["author"] = creator.get("nick", "")
    return c


def search(ctx, query=None):
    query = query or {}
    text = (query.get("text") or "").strip()
    page = int(query.get("page", 0))
    limit = 30
    sort = {"newest": "BY_PUBLICATION", "trending": "BY_SALES", "popular": "BY_DOWNLOADS"}.get(query.get("sort", ""), "BY_DOWNLOADS")
    data = _gql(ctx,
                "query($q: String!, $limit: Int, $offset: Int, $sort: CreationSearchSort) {"
                " r: creationsSearchBatch(query: $q, limit: $limit, offset: $offset, sort: $sort)"
                " { total results { " + FIELDS + " } } }",
                {"q": text, "limit": limit, "offset": page * limit, "sort": sort})
    r = data.get("r") or {}
    items = [_card(x) for x in (r.get("results") or [])]
    return {"items": items, "more": (page + 1) * limit < r.get("total", 0), "cursor": ""}


def model(ctx, id=None):
    data = _gql(ctx,
                "query($id: String!) { r: creation(slug: $id) { " + FIELDS + " description tags"
                " illustrations { imageUrl } license { name } category { name } } }",
                {"id": str(id)})
    d = data.get("r")
    if not d:
        raise RuntimeError("model not found on Cults3D")
    m = _card(d)
    m.update({"description": _plain(d.get("description") or ""), "tags": list(d.get("tags") or []),
              "images": [], "license": "", "category": "", "files": [], "download_note": NOTE})
    for im in (d.get("illustrations") or []):
        if im.get("imageUrl"):
            m["images"].append(im["imageUrl"])
    if not m["images"] and m["thumb"]:
        m["images"].append(m["thumb"])
    lic = d.get("license")
    if lic:
        m["license"] = lic.get("name", "")
    cat = d.get("category")
    if cat:
        m["category"] = cat.get("name", "")
    return m


def match(ctx, url=None):
    m = re.search(r"cults3d\.com/[^?#]+/([a-z0-9][a-z0-9-]*)", (url or "").lower())
    return m.group(1) if m else None


def _plain(html):
    return re.sub(r"<[^>]+>", " ", html).replace("&nbsp;", " ").strip()
