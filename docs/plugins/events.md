# Reacting to events (hooks)

Any plugin — of any kind — can react to things happening in your workshop: a
print starting, finishing or failing, a bed being cleared, a printer changing
state. This is how one plugin can watch the whole workshop and *do* something:
send a notification, log a run, flip a smart plug, nudge a spreadsheet. The
bundled notification plugin is built entirely on this.

A plugin opts in two ways:

1. Add an **`on_event`** function to `plugin.py`.
2. Optionally list the events you care about in **`events`** in `plugin.json`.
   Leave it out (or use `"*"`) to receive every event.

```json
{
  "id": "my-watcher",
  "name": "My Watcher",
  "kind": "tool",
  "events": ["print_finished", "print_failed"]
}
```

```python
def on_event(ctx, event=None):
    # event is a dict; "type" says what happened. Return value is ignored.
    if event["type"] == "print_finished":
        name = event.get("project_name", "a print")
        notify(f"{name} finished on {event.get('printer_name')}")
```

Your `on_event` runs in the background, so a slow or failing handler never holds
up a print. Exceptions are caught and logged; they don't reach PrintHarbor.
Events are delivered **in the order they happened**, one at a time. A handler has
20 seconds per event; one that takes longer is cut off so the events behind it
aren't held up.

## The events

Every event has a **`type`** and a **`time`** (RFC3339). Most also carry
`printer_id` and `printer_name`.

| `type`          | When                                   | Extra fields |
|-----------------|----------------------------------------|--------------|
| `print_started` | A plate was sent to a printer to print | `project_id`, `project_name`, `plate`, `job_id` |
| `print_finished`| A print ran to the end | `outcome` (`completed`, or `unknown` when the printer went idle without saying how far it got), `progress`, and `project_id`, `project_name`, `plate` (when known) |
| `print_cancelled`| A print was stopped before it finished (on the printer, or with Stop in PrintHarbor) | `outcome` (`stopped`), `progress` (0–1, how far it got), and `project_id`, `project_name`, `plate` (when known) |
| `print_failed`  | The printer reported an error, or AI failure detection tripped | `reason`, and for AI: `job_name`, `score`, `paused` |
| `bed_cleared`   | The bed was cleared and the printer is ready again | `by` (`"auto"` or `"camera"`) |
| `printer_state` | A printer changed state (the catch-all) | `from`, `to` (idle / printing / paused / finished / error / offline) |

`printer_state` fires on every state change, so you can react to anything —
offline, paused, resumed — without PrintHarbor having to name a dedicated event
for it. The named events above are the common cases made convenient.

## What a handler can do

`on_event` is ordinary plugin code: it has your plugin's `ctx` (its
`settings` and any keys), and `import phlib` for HTTP without third-party
packages. So a handler can call a webhook, hit a local service, write a file —
whatever your Python can reach. A plugin is code on your computer; only install
ones you trust (see [README.md](README.md#trust-and-safety)).

## Seeing events flow

`GET /api/plugins/events` returns the most recent events (newest last), whether
or not any plugin is listening — handy for checking a new handler is wired up.

## Note

Events are a capability, not a plugin *kind*: a `site`, `tool` or `printer`
plugin can all add an `on_event`. A printer plugin that also watches events can,
for example, react to one of its own prints finishing.
