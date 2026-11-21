// Package sites browses online model sites (Printables, Thingiverse,
// MakerWorld, Cults3D) and downloads models from them for import into the
// personal library.
package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Info describes a site for the Library page.
type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	NeedsKey    bool   `json:"needs_key"`
	KeyHelp     string `json:"key_help,omitempty"`
	Ready       bool   `json:"ready"`        // usable now (key set when one is needed)
	CanDownload bool   `json:"can_download"` // files can be imported directly
	Note        string `json:"note,omitempty"`
}

// Query is a browse or search request.
type Query struct {
	Text string `json:"text"`
	Sort string `json:"sort"` // popular | trending | newest
	Page int    `json:"page"` // 0-based
	// Cursor continues a previous page on sites that page by cursor.
	Cursor string `json:"cursor"`
}

// Card is one model in a result list.
type Card struct {
	Site      string `json:"site"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Thumb     string `json:"thumb"`
	URL       string `json:"url"`
	Likes     int    `json:"likes"`
	Downloads int    `json:"downloads"`
	Free      bool   `json:"free"`
}

// Page is a list of results.
type Page struct {
	Items  []Card `json:"items"`
	More   bool   `json:"more"`
	Cursor string `json:"cursor,omitempty"`
}

// File is a downloadable file of a model.
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // model | gcode | other
}

// Model is a model's full details.
type Model struct {
	Card
	Description string   `json:"description"`
	Images      []string `json:"images"`
	Tags        []string `json:"tags"`
	License     string   `json:"license"`
	Category    string   `json:"category"`
	Files       []File   `json:"files"`
	// DownloadNote explains why files can't be imported directly (Cults3D).
	DownloadNote string `json:"download_note,omitempty"`
}

// Site is one online model site.
type Site interface {
	Info() Info
	Search(ctx context.Context, q Query) (Page, error)
	Model(ctx context.Context, id string) (Model, error)
	// Download streams one file of a model.
	Download(ctx context.Context, m Model, f File) (io.ReadCloser, error)
}

// Keys are the per-site credentials from Settings.
type Keys struct {
	ThingiverseToken string
	MakerWorldToken  string
	CultsUser        string
	CultsKey         string
}

// All returns every site, configured with keys.
func All(k Keys) []Site {
	return []Site{
		&Printables{},
		&Thingiverse{Token: strings.TrimSpace(k.ThingiverseToken)},
		&MakerWorld{Token: strings.TrimSpace(k.MakerWorldToken)},
		&Cults{User: strings.TrimSpace(k.CultsUser), Key: strings.TrimSpace(k.CultsKey)},
	}
}

const userAgent = "PrintHarbor (+https://github.com/thefrayedend/printharbor)"

var client = &http.Client{Timeout: 30 * time.Second}

// getJSON / postJSON with friendly errors.
func doJSON(ctx context.Context, method, url string, body any, hdr map[string]string, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == 403 && resp.Header.Get("Cf-Mitigated") == "challenge" {
		return fmt.Errorf("the site asked for a browser check (Cloudflare). Open it in your browser once, then try again later")
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("the site refused the request (%s): check the key in Settings", resp.Status)
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 1500 {
			msg = msg[:1500]
		}
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("unexpected answer from the site: %v", err)
	}
	return nil
}

// fetch opens a URL for download.
func fetch(ctx context.Context, url string, hdr map[string]string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	return resp.Body, nil
}

var reTags = regexp.MustCompile(`(?s)<[^>]+>`)
var reBlock = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/h[1-6])\s*/?>`)

// plain turns a site's HTML description into readable text.
func plain(s string) string {
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTags.ReplaceAllString(s, "")
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	s = r.Replace(s)
	lines := strings.Split(s, "\n")
	out := []string{}
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// FileKind sorts a file name into model / gcode / other.
func FileKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".stl"), strings.HasSuffix(n, ".3mf"), strings.HasSuffix(n, ".obj"), strings.HasSuffix(n, ".step"),
		strings.HasSuffix(n, ".stp"), strings.HasSuffix(n, ".zip"), strings.HasSuffix(n, ".scad"), strings.HasSuffix(n, ".f3d"):
		return "model"
	case strings.HasSuffix(n, ".gcode"), strings.HasSuffix(n, ".bgcode"):
		return "gcode"
	}
	return "other"
}

type jsonRaw = json.RawMessage

func unmarshal(b []byte, out any) error {
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ParseLink turns a model page address into (site, id), for "paste a link".
func ParseLink(u string) (site, id string, ok bool) {
	u = strings.TrimSpace(u)
	for _, p := range []struct {
		site string
		re   *regexp.Regexp
	}{
		{"printables", regexp.MustCompile(`printables\.com/(?:[a-z-]+/)?model/(\d+)`)},
		{"thingiverse", regexp.MustCompile(`thingiverse\.com/thing:(\d+)`)},
		{"makerworld", regexp.MustCompile(`makerworld\.com/(?:[a-z-]+/)?models/(\d+)`)},
		{"cults", regexp.MustCompile(`cults3d\.com/(?:[a-z-]+/)?3d-model/[^/]+/([^/?#]+)`)},
	} {
		if m := p.re.FindStringSubmatch(u); m != nil {
			return p.site, m[1], true
		}
	}
	return "", "", false
}
