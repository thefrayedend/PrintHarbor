package plugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/thefrayedend/printharbor/internal/sites"
)

// Sites returns every loaded site plugin as a sites.Site.
func (m *Manager) Sites() []sites.Site {
	var out []sites.Site
	for _, man := range m.List() {
		if man.Error != "" || man.Off || (man.Kind != "" && man.Kind != "site") {
			continue
		}
		mm := man
		out = append(out, &pySite{m: m, man: mm})
	}
	return out
}

// Match finds the plugin and model id for a URL, using the manifests' regexes
// (no Python needed). Returns ("","",false) when nothing matches.
func (m *Manager) Match(url string) (site, id string, ok bool) {
	for _, man := range m.List() {
		if man.Error != "" || man.Off {
			continue
		}
		for _, pat := range man.Match {
			re, err := regexp.Compile(pat)
			if err != nil {
				continue
			}
			if g := re.FindStringSubmatch(url); g != nil {
				v := ""
				if len(g) > 1 {
					v = g[1]
				}
				return man.ID, v, true
			}
		}
	}
	return "", "", false
}

// pySite adapts one site plugin to sites.Site.
type pySite struct {
	m   *Manager
	man Manifest
}

func (s *pySite) Info() sites.Info {
	ready := true
	if s.man.NeedsKey {
		ready = strings.TrimSpace(s.m.keysCopy()[s.man.Key]) != ""
	}
	note := s.man.Note
	if note == "" {
		note = "Plugin"
	}
	return sites.Info{
		ID: s.man.ID, Name: s.man.Name, URL: s.man.URL,
		NeedsKey: s.man.NeedsKey, KeyHelp: s.man.KeyHelp, Ready: ready,
		CanDownload: s.man.CanDL, Note: note,
	}
}

func (s *pySite) Search(ctx context.Context, q sites.Query) (sites.Page, error) {
	var pg sites.Page
	err := s.m.rpc(s.man.ID, "search", map[string]any{"query": q}, &pg)
	if pg.Items == nil {
		pg.Items = []sites.Card{}
	}
	return pg, err
}

func (s *pySite) Model(ctx context.Context, id string) (sites.Model, error) {
	var mo sites.Model
	err := s.m.rpc(s.man.ID, "model", map[string]any{"id": id}, &mo)
	if mo.Site == "" {
		mo.Site = s.man.ID
	}
	return mo, err
}

// dlResult is what a plugin's download() returns: a direct URL to fetch, or a
// small inline body (text) for plugins without a real file.
type dlResult struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Text    string            `json:"text"`
	Name    string            `json:"name"`
}

func (s *pySite) Download(ctx context.Context, m sites.Model, f sites.File) (io.ReadCloser, error) {
	var r dlResult
	if err := s.m.rpc(s.man.ID, "download", map[string]any{"id": m.ID, "file_id": f.ID}, &r); err != nil {
		return nil, err
	}
	if r.Text != "" && r.URL == "" {
		return io.NopCloser(strings.NewReader(r.Text)), nil
	}
	if r.URL == "" {
		return nil, fmt.Errorf("the plugin gave no download address for %s", f.Name)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", r.URL, nil)
	if err != nil {
		return nil, err
	}
	// Default to PrintHarbor's own User-Agent, matching the built-in sites'
	// fetch(): many model CDNs 403 Go's default "Go-http-client" agent. A
	// plugin can still override it (or any header) via its download() headers.
	req.Header.Set("User-Agent", "PrintHarbor (+https://github.com/thefrayedend/printharbor)")
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.m.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed (%s)", resp.Status)
	}
	return resp.Body, nil
}
