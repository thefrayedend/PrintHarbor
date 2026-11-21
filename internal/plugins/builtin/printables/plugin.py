"""Printables (Prusa), as a PrintHarbor plugin: the public GraphQL API, no
account needed. Faithful port of the built-in Go site (internal/sites/
printables.go). See docs/plugins/sites.md."""
import json
import re
import phlib

API = "https://api.printables.com/graphql/"
MEDIA = "https://media.printables.com/"
HEADERS = {"Origin": "https://www.printables.com", "Referer": "https://www.printables.com/"}

ITEM_FIELDS = ("id name slug likesCount downloadCount image { filePath } "
               "user { publicUsername handle }")


def _gql(query, variables=None):
    env = phlib.post_json(API, {"query": query, "variables": variables or {}}, HEADERS)
    data = env.get("data")
    errs = env.get("errors")
    # Printables often returns partial data alongside errors; only fail when
    # there is effectively no data (matches the Go `len(data) < 8` check).
    if errs and (data is None or len(json.dumps(data)) < 8):
        raise RuntimeError("Printables: " + (errs[0].get("message") or "request failed"))
    return data or {}


def _card(it):
    cid = str(it.get("id"))
    slug = it.get("slug") or ""
    c = {
        "site": "printables", "id": cid, "title": it.get("name", ""),
        "likes": it.get("likesCount", 0), "downloads": it.get("downloadCount", 0),
        "free": True, "url": "https://www.printables.com/model/" + cid + "-" + slug,
        "author": "", "thumb": "",
    }
    img = it.get("image")
    if img and img.get("filePath"):
        c["thumb"] = MEDIA + img["filePath"].lstrip("/")
    user = it.get("user")
    if user:
        c["author"] = user.get("publicUsername") or user.get("handle") or ""
    return c


def _ordering(sort, search):
    if search:
        # SearchChoicesEnum: best_match, latest, popular, rating.
        return {"newest": "latest", "popular": "popular", "trending": "rating"}.get(sort, "best_match")
    return {"newest": "-first_publish", "trending": "-likes_count_7_days"}.get(sort, "-download_count_30_days")


def search(ctx, query=None):
    query = query or {}
    text = (query.get("text") or "").strip()
    page = int(query.get("page", 0))
    cursor = query.get("cursor") or ""
    limit = 36
    if text:
        # searchPrints2 pages by offset and has totalCount.
        data = _gql(
            "query Search($q: String!, $limit: Int, $offset: Int, $ordering: SearchChoicesEnum) {"
            " r: searchPrints2(query: $q, limit: $limit, offset: $offset, ordering: $ordering)"
            " { totalCount items { " + ITEM_FIELDS + " } } }",
            {"q": text, "limit": limit, "offset": page * limit, "ordering": _ordering(query.get("sort", ""), True)})
        r = data.get("r") or {}
        items = [_card(x) for x in (r.get("items") or [])]
        return {"items": items, "more": (page + 1) * limit < r.get("totalCount", 0), "cursor": ""}
    # morePrints pages by cursor.
    data = _gql(
        "query Browse($limit: Int!, $cursor: String, $ordering: String) {"
        " r: morePrints(limit: $limit, cursor: $cursor, ordering: $ordering)"
        " { cursor items { " + ITEM_FIELDS + " } } }",
        {"limit": limit, "cursor": cursor or None, "ordering": _ordering(query.get("sort", ""), False)})
    r = data.get("r") or {}
    items = [_card(x) for x in (r.get("items") or [])]
    next_cursor = r.get("cursor") or ""
    return {"items": items, "more": bool(next_cursor) and len(items) > 0, "cursor": next_cursor}


def model(ctx, id=None):
    data = _gql(
        "query Model($id: ID!) { print(id: $id) { " + ITEM_FIELDS + " summary description"
        " images { filePath } tags { name } license { name abbreviation } category { path { name } }"
        " stls { id name fileSize } gcodes { id name fileSize } otherFiles { id name fileSize } } }",
        {"id": str(id)})
    d = data.get("print")
    if not d:
        raise RuntimeError("model not found on Printables")
    m = _card(d)
    m.update({"description": _plain(d.get("description") or d.get("summary") or ""),
              "images": [], "tags": [], "license": "", "category": "", "files": []})
    for im in (d.get("images") or []):
        if im.get("filePath"):
            m["images"].append(MEDIA + im["filePath"].lstrip("/"))
    if not m["images"] and m["thumb"]:
        m["images"].append(m["thumb"])
    for t in (d.get("tags") or []):
        if t.get("name"):
            m["tags"].append(t["name"])
    lic = d.get("license")
    if lic:
        m["license"] = lic.get("abbreviation") or lic.get("name") or ""
    cat = d.get("category")
    if cat and cat.get("path"):
        m["category"] = "/".join(p.get("name", "") for p in cat["path"])

    def add(lst, kind):
        for f in (lst or []):
            m["files"].append({"id": kind + ":" + str(f.get("id")), "name": f.get("name", ""),
                               "size": f.get("fileSize", 0), "kind": _kind(f.get("name", ""))})
    add(d.get("stls"), "stl")
    add(d.get("otherFiles"), "other")
    add(d.get("gcodes"), "gcode")
    return m


def download(ctx, id=None, file_id=None):
    kind, _, fid = (file_id or "").partition(":")
    types = {"stl": ["stl"], "gcode": ["gcode"], "other": ["other", "stl"]}.get(kind, ["stl"])

    def try_type(file_type):
        data = _gql(
            "mutation($id: ID!, $printId: ID!, $source: DownloadSourceEnum!, $fileType: DownloadFileTypeEnum!) {"
            " r: getDownloadLink(id: $id, printId: $printId, source: $source, fileType: $fileType)"
            " { ok output { link } errors { messages } } }",
            {"id": fid, "printId": str(id), "source": "model_detail", "fileType": file_type})
        r = data.get("r") or {}
        out = r.get("output") or {}
        if not r.get("ok") or not out.get("link"):
            errs = r.get("errors") or []
            msg = "no download link"
            if errs and errs[0].get("messages"):
                msg = errs[0]["messages"][0]
            raise RuntimeError("Printables: " + msg)
        return out["link"]

    last = None
    for t in types:
        try:
            link = try_type(t)
            return {"url": link}
        except Exception as e:
            last = e
    raise last or RuntimeError("Printables: no download link")


def match(ctx, url=None):
    m = re.search(r"printables\.com/model/([0-9]+)", url or "")
    return m.group(1) if m else None


def _plain(html):
    return re.sub(r"<[^>]+>", " ", html).replace("&nbsp;", " ").strip()


def _kind(name):
    n = (name or "").lower()
    if n.endswith((".stl", ".obj", ".3mf", ".step", ".stp", ".ply")):
        return "model"
    if n.endswith((".gcode", ".bgcode")):
        return "gcode"
    return "other"
