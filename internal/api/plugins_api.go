package api

import (
	"fmt"
	"io"
	"net/http"

	"github.com/thefrayedend/printharbor/internal/plugins"
)

var errPlugins = fmt.Errorf("plugins are not available (Python not installed?)")

func (a *App) pluginRoutes(mux *http.ServeMux) {
	// What plugins are loaded (and why they might be off).
	mux.HandleFunc("GET /api/plugins", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			writeJSON(w, 200, map[string]any{"plugins": []any{}, "off": "plugins are not available"})
			return
		}
		writeJSON(w, 200, map[string]any{"plugins": a.Plugins.List(), "off": a.Plugins.Off(), "dir": a.Plugins.Dir()})
	})
	// Turn a plugin on or off.
	mux.HandleFunc("POST /api/plugins/off", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		var in struct {
			ID  string `json:"id"`
			Off bool   `json:"off"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if in.ID == "" {
			fail(w, 400, fmt.Errorf("which plugin?"))
			return
		}
		a.Plugins.SetOff(in.ID, in.Off)
		plugins.RegisterPrinterDrivers(a.Plugins) // a printer plugin toggled on becomes selectable
		writeJSON(w, 200, map[string]any{"ok": true, "plugins": a.Plugins.List(), "off": a.Plugins.Off()})
	})
	// Install a plugin from an uploaded .zip.
	mux.HandleFunc("POST /api/plugins/install", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			fail(w, 400, err)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			fail(w, 400, fmt.Errorf("attach a plugin .zip file"))
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 32<<20))
		if err != nil {
			fail(w, 400, err)
			return
		}
		id, err := a.Plugins.Install(data)
		if err != nil {
			fail(w, 400, err)
			return
		}
		a.Plugins.Reload(r.Context())
		writeJSON(w, 200, map[string]any{"ok": true, "id": id, "plugins": a.Plugins.List(), "off": a.Plugins.Off()})
	})
	// Remove a user-installed plugin.
	mux.HandleFunc("POST /api/plugins/uninstall", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if err := a.Plugins.Uninstall(in.ID); err != nil {
			fail(w, 400, err)
			return
		}
		a.Plugins.Reload(r.Context())
		writeJSON(w, 200, map[string]any{"ok": true, "plugins": a.Plugins.List(), "off": a.Plugins.Off()})
	})
	// Rescan the plugins folder (after a plugin is added or changed).
	mux.HandleFunc("POST /api/plugins/reload", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		a.Plugins.Reload(r.Context())
		writeJSON(w, 200, map[string]any{"ok": true, "plugins": a.Plugins.List(), "off": a.Plugins.Off()})
	})
	// Save a plugin's settings values.
	mux.HandleFunc("POST /api/plugins/settings", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		var in struct {
			ID     string         `json:"id"`
			Values map[string]any `json:"values"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if in.ID == "" {
			fail(w, 400, fmt.Errorf("which plugin?"))
			return
		}
		values := a.Plugins.SetSettings(in.ID, in.Values)
		writeJSON(w, 200, map[string]any{"ok": true, "values": values})
	})
	// Recent lifecycle events (print started/finished/failed, bed cleared, …),
	// newest last — so you can confirm event hooks are flowing to plugins.
	mux.HandleFunc("GET /api/plugins/events", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			writeJSON(w, 200, map[string]any{"events": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"events": a.Plugins.RecentEvents()})
	})
	// The tool (feature) plugins that are on.
	mux.HandleFunc("GET /api/plugins/tools", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			writeJSON(w, 200, map[string]any{"tools": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"tools": a.Plugins.Tools()})
	})
	// Run a tool plugin with the given params.
	mux.HandleFunc("POST /api/plugins/tools/run", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		var in struct {
			ID     string         `json:"id"`
			Params map[string]any `json:"params"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		if in.ID == "" {
			fail(w, 400, fmt.Errorf("which tool?"))
			return
		}
		result, err := a.Plugins.RunTool(in.ID, in.Params)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "result": result})
	})
	// Check whether a newer version of a plugin is offered at its update_url.
	mux.HandleFunc("POST /api/plugins/check-update", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		info, err := a.Plugins.CheckUpdate(r.Context(), in.ID)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "update": info})
	})
	// Download and install the newer version, then reload.
	mux.HandleFunc("POST /api/plugins/update", func(w http.ResponseWriter, r *http.Request) {
		if a.Plugins == nil {
			fail(w, 400, errPlugins)
			return
		}
		var in struct {
			ID string `json:"id"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		info, err := a.Plugins.Update(r.Context(), in.ID)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if info.Available {
			a.Plugins.Reload(r.Context())
		}
		writeJSON(w, 200, map[string]any{"ok": true, "update": info, "plugins": a.Plugins.List(), "off": a.Plugins.Off()})
	})
}
