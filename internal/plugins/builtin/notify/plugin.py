"""Notifications: the reference event-hook plugin for PrintHarbor.

It reacts to print events (see docs/plugins/events.md) and sends a short message
to the channels you configure — ntfy (which has free iOS/Android apps) and/or a
Discord/Slack-style incoming webhook. Many such plugins can run at once, so this
can sit next to your own; copy this folder to notify somewhere else.

on_event(ctx, event) runs in the background on every event it asked for in
plugin.json. run(ctx, params) sends a test. Both read ctx["settings"].
"""

import json
import urllib.request


def _post(url, obj, timeout=15):
    """POST JSON and ignore the response body (ntfy returns JSON, Discord/Slack
    return an empty 204 — we only care that it was accepted)."""
    body = json.dumps(obj).encode()
    req = urllib.request.Request(
        url, data=body, method="POST",
        headers={"Content-Type": "application/json", "User-Agent": "PrintHarbor/1.0"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.status


# --- how each event turns into a message -------------------------------------
def _message(ev):
    """Return (title, body, tags, priority) for an event, or None to skip it."""
    t = ev.get("type")
    printer = ev.get("printer_name") or "a printer"
    proj = ev.get("project_name") or ev.get("job_name") or "a print"
    plate = ev.get("plate")
    where = (" (plate %s)" % plate) if plate not in (None, "", 0) else ""
    if t == "print_started":
        return ("Print started", "%s started on %s%s." % (proj, printer, where), "arrow_forward", "default")
    if t == "print_finished":
        return ("Print finished", "%s finished on %s%s." % (proj, printer, where), "white_check_mark", "high")
    if t == "print_cancelled":
        return ("Print stopped", "%s was stopped before it finished on %s%s." % (proj, printer, where), "octagonal_sign", "default")
    if t == "print_failed":
        reason = ev.get("reason") or "a problem"
        job = ev.get("job_name")
        what = ("%s: " % job) if job else ""
        return ("Print FAILED", "%s%s on %s." % (what, reason, printer), "rotating_light", "urgent")
    if t == "bed_cleared":
        return ("Bed cleared", "%s is ready for the next print." % printer, "broom", "low")
    return None


# which setting turns each event on/off
_TOGGLE = {
    "print_started": "on_started",
    "print_finished": "on_finished",
    "print_cancelled": "on_cancelled",
    "print_failed": "on_failed",
    "bed_cleared": "on_bed_cleared",
}


def _send(settings, title, body, tags="bell", priority="default"):
    """Send to every configured channel. Returns a list of channels tried."""
    sent = []
    server = str(settings.get("ntfy_server") or "https://ntfy.sh").rstrip("/")
    topic = str(settings.get("ntfy_topic") or "").strip()
    if topic:
        # ntfy publishes as JSON to the server root.
        _post(server, {
            "topic": topic,
            "title": title,
            "message": body,
            "tags": [t for t in str(tags).split(",") if t],
            "priority": {"min": 1, "low": 2, "default": 3, "high": 4, "max": 5, "urgent": 5}.get(priority, 3),
        })
        sent.append("ntfy")
    hook = str(settings.get("webhook_url") or "").strip()
    if hook:
        # Discord and Slack both accept {"content"/"text": "..."}; send both keys.
        line = "**%s** — %s" % (title, body)
        _post(hook, {"content": line, "text": line})
        sent.append("webhook")
    return sent


def on_event(ctx, event=None):
    ev = event or {}
    settings = ctx.get("settings") or {}
    toggle = _TOGGLE.get(ev.get("type"))
    if toggle and settings.get(toggle) is False:
        return  # this event type is switched off
    msg = _message(ev)
    if not msg:
        return
    title, body, tags, priority = msg
    _send(settings, title, body, tags, priority)


def run(ctx, params=None):
    """Send a test notification to the configured channels."""
    settings = ctx.get("settings") or {}
    text = (params or {}).get("message") or "PrintHarbor test notification"
    sent = _send(settings, "PrintHarbor", str(text), tags="bell", priority="default")
    if not sent:
        return {"title": "No channel configured",
                "text": "Set an ntfy topic or a webhook URL in this plugin's settings, then press Run again."}
    return {"title": "Test sent", "text": "Sent to: " + ", ".join(sent)}
