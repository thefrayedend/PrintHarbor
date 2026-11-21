"""phlib: helpers PrintHarbor gives every plugin, so plugins need no third-party
packages. Import it from a plugin: `import phlib`."""
import json, urllib, urllib.request, urllib.error, urllib.parse

UA = "PrintHarbor-Plugin/1.0"

def _req(method, url, headers=None, data=None):
    h = {"User-Agent": UA}
    if headers:
        h.update(headers)
    body = None
    if data is not None:
        if isinstance(data, (dict, list)):
            body = json.dumps(data).encode()
            h.setdefault("Content-Type", "application/json")
        elif isinstance(data, str):
            body = data.encode()
        else:
            body = data
    return urllib.request.Request(url, data=body, headers=h, method=method)

def get_bytes(url, headers=None, timeout=30):
    """Fetch a URL, returning raw bytes."""
    with urllib.request.urlopen(_req("GET", url, headers), timeout=timeout) as r:
        return r.read()

def get_json(url, headers=None, timeout=30):
    """GET a URL and parse JSON."""
    return json.loads(get_bytes(url, headers, timeout).decode("utf-8", "replace"))

def post_json(url, data, headers=None, timeout=30):
    """POST JSON and parse the JSON response."""
    with urllib.request.urlopen(_req("POST", url, headers, data), timeout=timeout) as r:
        return json.loads(r.read().decode("utf-8", "replace"))

def get_text(url, headers=None, timeout=30):
    return get_bytes(url, headers, timeout).decode("utf-8", "replace")

def qs(params):
    """URL-encode a dict of query parameters."""
    return urllib.parse.urlencode({k: v for k, v in params.items() if v not in (None, "")})
