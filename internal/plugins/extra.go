package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Tools returns the loaded tool plugins (kind "tool"), skipping ones that are
// off or failed to load. Tools are feature plugins the user runs on demand.
func (m *Manager) Tools() []Manifest {
	var out []Manifest
	for _, man := range m.List() {
		if man.Error == "" && !man.Off && man.Kind == "tool" {
			out = append(out, man)
		}
	}
	return out
}

// RunTool runs a tool plugin's run(ctx, params) and returns its JSON result.
func (m *Manager) RunTool(id string, params map[string]any) (json.RawMessage, error) {
	var isTool bool
	for _, man := range m.List() {
		if man.ID == id {
			if man.Off {
				return nil, fmt.Errorf("that plugin is turned off")
			}
			if man.Error != "" {
				return nil, fmt.Errorf("that plugin failed to load: %s", man.Error)
			}
			isTool = man.Kind == "tool"
		}
	}
	if !isTool {
		return nil, fmt.Errorf("%q is not a tool plugin", id)
	}
	var raw json.RawMessage
	if err := m.rpc(id, "run", map[string]any{"params": params}, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// UpdateInfo is the result of an update check.
type UpdateInfo struct {
	ID        string `json:"id"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Notes     string `json:"notes"`
	zipURL    string
}

// CheckUpdate fetches a plugin's update_url and reports whether a newer version
// is offered. The update_url is either a .zip (the plugin itself) or a JSON
// manifest {"version","zip","notes"}. Only https is allowed.
func (m *Manager) CheckUpdate(ctx context.Context, id string) (*UpdateInfo, error) {
	var man Manifest
	found := false
	for _, mm := range m.List() {
		if mm.ID == id {
			man, found = mm, true
		}
	}
	if !found {
		return nil, fmt.Errorf("no plugin %q", id)
	}
	if m.builtin[id] {
		return nil, fmt.Errorf("built-in plugins update with PrintHarbor itself")
	}
	if strings.TrimSpace(man.UpdateURL) == "" {
		return nil, fmt.Errorf("this plugin has no update_url, so it can't check for updates")
	}
	if !strings.HasPrefix(strings.ToLower(man.UpdateURL), "https://") {
		return nil, fmt.Errorf("the update address must start with https://")
	}
	info := &UpdateInfo{ID: id, Current: man.Version}
	// A .zip update_url is the plugin directly: we can't read its version
	// without downloading, so offer it and let the user decide.
	if strings.HasSuffix(strings.ToLower(man.UpdateURL), ".zip") {
		info.zipURL = man.UpdateURL
		info.Latest = ""
		info.Available = true
		return info, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", man.UpdateURL, nil)
	req.Header.Set("User-Agent", "PrintHarbor (+https://github.com/thefrayedend/printharbor)")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't reach the update address: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("the update address returned %s", resp.Status)
	}
	var meta struct {
		Version string `json:"version"`
		Zip     string `json:"zip"`
		Notes   string `json:"notes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return nil, fmt.Errorf("the update address isn't a .zip or a valid update manifest")
	}
	if !strings.HasPrefix(strings.ToLower(meta.Zip), "https://") {
		return nil, fmt.Errorf("the update manifest's zip address must start with https://")
	}
	info.Latest = strings.TrimSpace(meta.Version)
	info.Notes = strings.TrimSpace(meta.Notes)
	info.zipURL = meta.Zip
	info.Available = compareVersions(info.Latest, info.Current) > 0 || info.Current == ""
	return info, nil
}

// Update checks for and, if one is offered, downloads and installs the newer
// version. Returns the update info (with Available reflecting what happened).
func (m *Manager) Update(ctx context.Context, id string) (*UpdateInfo, error) {
	info, err := m.CheckUpdate(ctx, id)
	if err != nil {
		return nil, err
	}
	if !info.Available || info.zipURL == "" {
		return info, nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", info.zipURL, nil)
	req.Header.Set("User-Agent", "PrintHarbor (+https://github.com/thefrayedend/printharbor)")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't download the update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("the update download returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*maxPluginFile+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > 4*maxPluginFile {
		return nil, fmt.Errorf("the update is too big (over %d MB)", 4*maxPluginFile>>20)
	}
	gotID, err := m.Install(data)
	if err != nil {
		return nil, err
	}
	if gotID != id {
		// Install already put it under its own (different) id; refuse the
		// surprise rather than silently installing a different plugin.
		return nil, fmt.Errorf("the update is for a different plugin (%q), not %q", gotID, id)
	}
	return info, nil
}

// compareVersions compares dotted version strings numerically where it can
// (1.10 > 1.9), falling back to a string compare per segment. Returns >0 when a
// is newer, <0 when older, 0 when equal.
func compareVersions(a, b string) int {
	as := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	bs := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ap, bp string
		if i < len(as) {
			ap = as[i]
		}
		if i < len(bs) {
			bp = bs[i]
		}
		an, aerr := strconv.Atoi(ap)
		bn, berr := strconv.Atoi(bp)
		if aerr == nil && berr == nil {
			if an != bn {
				if an > bn {
					return 1
				}
				return -1
			}
			continue
		}
		if ap != bp {
			if ap > bp {
				return 1
			}
			return -1
		}
	}
	return 0
}
