"""Filament Cost: a worked example of a PrintHarbor tool (feature) plugin.

A tool plugin defines run(ctx, params). ctx["settings"] holds this plugin's own
settings; params holds the values the user entered for this run (both described
in plugin.json). run() returns a dict PrintHarbor shows: {title, text, html?}.
"""


def _num(v, default=0.0):
    try:
        return float(v)
    except (TypeError, ValueError):
        return default


def run(ctx, params=None):
    p = params or {}
    sym = (ctx.get("settings") or {}).get("currency", "$")
    grams = _num(p.get("grams"), 0)
    price = _num(p.get("price"), 0)
    spool_g = _num(p.get("spool_g"), 1000) or 1000
    waste = _num(p.get("waste"), 0)
    used = grams * (1 + waste / 100.0)
    cost = used / spool_g * price
    return {
        "title": "Filament cost",
        "text": "%.1f g (incl. %.0f%% waste) at %s%.2f / %.0f g spool = %s%.2f"
        % (used, waste, sym, price, spool_g, sym, cost),
        "value": round(cost, 2),
    }
