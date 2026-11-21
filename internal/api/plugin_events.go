package api

// emitEvent is the one place PrintHarbor tells plugins that something happened
// (a print started, finished or failed; a bed was cleared; a printer changed
// state). It fills in the printer's name and hands the event to the plugin
// manager, which delivers it to any plugin with an on_event handler. A nil
// manager (Python not installed) makes this a no-op, so callers need no guard.
func (a *App) emitEvent(evType, printerID string, fields map[string]any) {
	if a.Plugins == nil {
		return
	}
	ev := map[string]any{"type": evType}
	if printerID != "" {
		ev["printer_id"] = printerID
		if v, ok := a.Printers.Get(printerID); ok {
			ev["printer_name"] = v.Name
		}
	}
	for k, val := range fields {
		ev[k] = val
	}
	a.Plugins.Emit(ev)
}
