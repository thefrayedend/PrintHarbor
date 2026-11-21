package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/thefrayedend/printharbor/internal/library"
)

// Link auto-fill. When someone pastes a model's web page into a library item
// and clicks "Fill in from link", PrintHarbor fetches that one page and reads
// the information the site publishes for link previews (Open Graph tags,
// schema.org JSON-LD, Next.js page data). Nothing happens unless they click:
// PrintHarbor never looks at browser history, downloads or file origins.

// LinkInfo is what a model page says about itself.
type LinkInfo struct {
	URL         string   `json:"url"`
	Site        string   `json:"site"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Designer    string   `json:"designer"`
	License     string   `json:"license"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
	Images      []string `json:"images"`
}

const linkUA = "Mozilla/5.0 (compatible; PrintHarbor/" + Version + "; +https://github.com/thefrayedend/printharbor)"

var linkClient = &http.Client{Timeout: 20 * time.Second}

func checkLinkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("paste the full address of the model's page, starting with https://")
	}
	return u, nil
}

// FetchLinkInfo reads one model page.
func FetchLinkInfo(ctx context.Context, raw string) (LinkInfo, error) {
	u, err := checkLinkURL(raw)
	if err != nil {
		return LinkInfo{}, err
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "printables.com" {
		if info, err := printablesInfo(ctx, u); err == nil && info.Title != "" {
			return info, nil
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", linkUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en")
	resp, err := linkClient.Do(req)
	if err != nil {
		return LinkInfo{}, fmt.Errorf("couldn't reach %s: %v", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return LinkInfo{}, fmt.Errorf("%s answered %s. Some sites block programs from reading their pages; fill this one in by hand", u.Host, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return LinkInfo{}, err
	}
	info := parseLinkPage(resp.Request.URL, string(body))
	if info.Title == "" && info.Description == "" {
		return info, fmt.Errorf("%s didn't include any model information in its page (it may need a browser to show it); fill this one in by hand", u.Host)
	}
	return info, nil
}

var (
	reMeta   = regexp.MustCompile(`(?is)<meta\s[^>]*>`)
	reAttr   = regexp.MustCompile(`(?is)([a-z][a-z0-9:_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	reLD     = regexp.MustCompile(`(?is)<script[^>]*type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reNext   = regexp.MustCompile(`(?is)<script[^>]*id\s*=\s*["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
	reTitle  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reTags   = regexp.MustCompile(`(?s)<[^>]+>`)
	reBlock  = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/h[1-6])\s*/?>`)
	reSpaces = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines  = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// parseLinkPage pulls model information out of a page's HTML.
func parseLinkPage(base *url.URL, page string) LinkInfo {
	info := LinkInfo{URL: base.String(), Site: strings.TrimPrefix(base.Hostname(), "www.")}
	metas := map[string][]string{}
	for _, tag := range reMeta.FindAllString(page, -1) {
		attrs := map[string]string{}
		for _, m := range reAttr.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = html.UnescapeString(m[2] + m[3])
		}
		key := strings.ToLower(attrs["property"])
		if key == "" {
			key = strings.ToLower(attrs["name"])
		}
		if key == "" || strings.TrimSpace(attrs["content"]) == "" {
			continue
		}
		metas[key] = append(metas[key], strings.TrimSpace(attrs["content"]))
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := metas[k]; len(v) > 0 {
				return v[0]
			}
		}
		return ""
	}
	siteName := first("og:site_name", "application-name")

	// Structured data is the most precise, so it goes first.
	for _, m := range reLD.FindAllStringSubmatch(page, -1) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) == nil {
			walkLD(v, &info, base)
		}
	}
	if m := reNext.FindStringSubmatch(page); m != nil {
		var v any
		if json.Unmarshal([]byte(m[1]), &v) == nil {
			nextData(v, &info, base)
		}
	}

	set := func(dst *string, v string) {
		if *dst == "" {
			*dst = strings.TrimSpace(v)
		}
	}
	set(&info.Title, first("og:title", "twitter:title"))
	if info.Title == "" {
		if m := reTitle.FindStringSubmatch(page); m != nil {
			info.Title = html.UnescapeString(strings.TrimSpace(m[1]))
		}
	}
	set(&info.Description, first("og:description", "twitter:description", "description"))
	set(&info.Designer, first("author", "article:author", "twitter:creator"))
	set(&info.License, first("license", "dc.rights"))
	if len(info.Tags) == 0 {
		info.Tags = append(info.Tags, metas["article:tag"]...)
		if kw := first("keywords"); kw != "" && len(info.Tags) == 0 {
			info.Tags = strings.Split(kw, ",")
		}
	}
	for _, k := range []string{"og:image", "og:image:url", "og:image:secure_url", "twitter:image", "twitter:image:src"} {
		for _, img := range metas[k] {
			info.Images = append(info.Images, absURL(base, img))
		}
	}
	tidyLinkInfo(&info, siteName)
	return info
}

func absURL(base *url.URL, s string) string {
	u, err := base.Parse(strings.TrimSpace(s))
	if err != nil {
		return s
	}
	return u.String()
}

// walkLD reads schema.org objects (Product, CreativeWork, 3DModel...).
func walkLD(v any, info *LinkInfo, base *url.URL) {
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			walkLD(x, info, base)
		}
	case map[string]any:
		if g, ok := t["@graph"]; ok {
			walkLD(g, info, base)
		}
		typ := strings.ToLower(fmt.Sprint(t["@type"]))
		useful := false
		for _, k := range []string{"product", "creativework", "3dmodel", "imageobject", "article", "thing", "mediaobject"} {
			if strings.Contains(typ, k) {
				useful = true
			}
		}
		if !useful || strings.Contains(typ, "breadcrumb") || strings.Contains(typ, "organization") || strings.Contains(typ, "website") {
			return
		}
		if info.Title == "" {
			info.Title = ldText(t["name"])
		}
		if info.Description == "" {
			info.Description = ldText(t["description"])
		}
		if info.Designer == "" {
			for _, k := range []string{"author", "creator", "brand"} {
				if s := ldName(t[k]); s != "" {
					info.Designer = s
					break
				}
			}
		}
		if info.License == "" {
			info.License = ldName(t["license"])
		}
		if len(info.Tags) == 0 {
			switch kw := t["keywords"].(type) {
			case string:
				info.Tags = strings.Split(kw, ",")
			case []any:
				for _, x := range kw {
					info.Tags = append(info.Tags, ldText(x))
				}
			}
		}
		if info.Category == "" {
			info.Category = ldName(t["category"])
		}
		info.Images = append(info.Images, ldImages(t["image"], base)...)
	}
}

func ldText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprint(t)
	}
	return ""
}

func ldName(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if s := ldText(t["name"]); s != "" {
			return s
		}
		return ldText(t["url"])
	case []any:
		if len(t) > 0 {
			return ldName(t[0])
		}
	}
	return ""
}

func ldImages(v any, base *url.URL) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = append(out, absURL(base, t))
	case map[string]any:
		if s := ldText(t["url"]); s != "" {
			out = append(out, absURL(base, s))
		} else if s := ldText(t["contentUrl"]); s != "" {
			out = append(out, absURL(base, s))
		}
	case []any:
		for _, x := range t {
			out = append(out, ldImages(x, base)...)
		}
	}
	return out
}

// nextData looks through a Next.js page's data for a model ("design" on
// MakerWorld): an object with a title and a creator or summary.
func nextData(v any, info *LinkInfo, base *url.URL) {
	var best map[string]any
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if best != nil || depth > 12 {
			return
		}
		switch t := v.(type) {
		case map[string]any:
			if _, ok := t["title"].(string); ok {
				_, c1 := t["designCreator"]
				_, c2 := t["summary"]
				_, c3 := t["creator"]
				if c1 || c2 || c3 {
					best = t
					return
				}
			}
			for _, x := range t {
				walk(x, depth+1)
			}
		case []any:
			for _, x := range t {
				walk(x, depth+1)
			}
		}
	}
	walk(v, 0)
	if best == nil {
		return
	}
	if info.Title == "" {
		info.Title = ldText(best["title"])
	}
	if info.Description == "" {
		for _, k := range []string{"summary", "description", "details"} {
			if s := ldText(best[k]); s != "" {
				info.Description = s
				break
			}
		}
	}
	if info.Designer == "" {
		for _, k := range []string{"designCreator", "creator", "user", "author"} {
			if c, ok := best[k].(map[string]any); ok {
				if s := ldText(c["name"]); s != "" {
					info.Designer = s
				} else if s := ldText(c["handle"]); s != "" {
					info.Designer = s
				}
				if info.Designer != "" {
					break
				}
			}
		}
	}
	if info.License == "" {
		info.License = ldName(best["license"])
	}
	if len(info.Tags) == 0 {
		if tags, ok := best["tags"].([]any); ok {
			for _, x := range tags {
				if s := ldName(x); s != "" {
					info.Tags = append(info.Tags, s)
				}
			}
		}
	}
	for _, k := range []string{"coverUrl", "cover", "thumbnail", "image"} {
		if s := ldText(best[k]); s != "" {
			info.Images = append(info.Images, absURL(base, s))
		}
	}
	if ext, ok := best["designExtension"].(map[string]any); ok {
		if pics, ok := ext["design_pictures"].([]any); ok {
			for _, p := range pics {
				if m, ok := p.(map[string]any); ok {
					if s := ldText(m["url"]); s != "" {
						info.Images = append(info.Images, absURL(base, s))
					}
				}
			}
		}
	}
}

// printablesInfo uses Printables' public GraphQL API, which has the full
// description, tags and category that the page itself only renders in the
// browser.
func printablesInfo(ctx context.Context, u *url.URL) (LinkInfo, error) {
	m := regexp.MustCompile(`/model/(\d+)`).FindStringSubmatch(u.Path)
	if m == nil {
		return LinkInfo{}, fmt.Errorf("not a model page")
	}
	q := map[string]any{
		"operationName": "PrintProfile",
		"query": `query PrintProfile($id: ID!) { print(id: $id) { id name summary description
			tags { name } license { name abbreviation } user { publicUsername }
			category { path { name } } images { filePath } } }`,
		"variables": map[string]string{"id": m[1]},
	}
	b, _ := json.Marshal(q)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.printables.com/graphql/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", linkUA)
	resp, err := linkClient.Do(req)
	if err != nil {
		return LinkInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return LinkInfo{}, fmt.Errorf("printables: %s", resp.Status)
	}
	var out struct {
		Data struct {
			Print *struct {
				Name, Summary, Description string
				Tags                       []struct{ Name string }
				License                    *struct{ Name, Abbreviation string }
				User                       *struct{ PublicUsername string }
				Category                   *struct{ Path []struct{ Name string } }
				Images                     []struct{ FilePath string }
			} `json:"print"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil || out.Data.Print == nil {
		return LinkInfo{}, fmt.Errorf("printables: no data")
	}
	p := out.Data.Print
	info := LinkInfo{URL: u.String(), Site: "printables.com", Title: p.Name, Description: p.Description}
	if info.Description == "" {
		info.Description = p.Summary
	}
	for _, t := range p.Tags {
		info.Tags = append(info.Tags, t.Name)
	}
	if p.License != nil {
		info.License = p.License.Abbreviation
		if info.License == "" {
			info.License = p.License.Name
		}
	}
	if p.User != nil {
		info.Designer = p.User.PublicUsername
	}
	if p.Category != nil {
		var parts []string
		for _, c := range p.Category.Path {
			parts = append(parts, c.Name)
		}
		info.Category = strings.Join(parts, "/")
	}
	for _, im := range p.Images {
		if im.FilePath != "" {
			info.Images = append(info.Images, "https://media.printables.com/"+strings.TrimPrefix(im.FilePath, "/"))
		}
	}
	tidyLinkInfo(&info, "Printables")
	return info, nil
}

var reCC = regexp.MustCompile(`(?i)creativecommons\.org/(licenses|publicdomain)/([a-z-]+)/?([0-9.]+)?`)

// tidyLinkInfo cleans up what a site gave us.
func tidyLinkInfo(info *LinkInfo, siteName string) {
	info.Title = cleanTitle(html.UnescapeString(info.Title), info.Site, siteName, &info.Designer)
	info.Description = plainText(info.Description)
	if len(info.Description) > 4000 {
		info.Description = strings.TrimSpace(info.Description[:4000]) + "…"
	}
	info.Designer = strings.TrimPrefix(strings.TrimSpace(html.UnescapeString(info.Designer)), "@")
	if strings.HasPrefix(info.Designer, "http") { // an author URL, not a name
		info.Designer = strings.Trim(path.Base(info.Designer), "@")
	}
	if m := reCC.FindStringSubmatch(info.License); m != nil {
		if strings.EqualFold(m[1], "publicdomain") {
			info.License = "CC0"
		} else {
			info.License = strings.TrimSpace("CC " + strings.ToUpper(m[2]) + " " + m[3])
		}
	}
	seen := map[string]bool{}
	var tags []string
	for _, t := range info.Tags {
		t = strings.TrimSpace(html.UnescapeString(t))
		if t == "" || len(t) > 40 || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
		if len(tags) == 15 {
			break
		}
	}
	info.Tags = tags
	if info.Tags == nil {
		info.Tags = []string{}
	}
	seenImg := map[string]bool{}
	var imgs []string
	for _, s := range info.Images {
		if s == "" || seenImg[s] || !(strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) {
			continue
		}
		seenImg[s] = true
		imgs = append(imgs, s)
		if len(imgs) == 8 {
			break
		}
	}
	info.Images = imgs
	if info.Images == nil {
		info.Images = []string{}
	}
}

var reBy = regexp.MustCompile(`^(.+?) by ([^|]+)$`)

// cleanTitle drops the site name sites add to page titles ("Benchy |
// Printables.com", "Benchy by Maker - Thingiverse").
func cleanTitle(t, host, siteName string, designer *string) string {
	t = strings.Join(strings.Fields(t), " ")
	label := strings.ToLower(strings.Split(host, ".")[0])
	cut := false
	for _, sep := range []string{" | ", " - ", " – ", " — ", " :: "} {
		if i := strings.LastIndex(t, sep); i > 0 {
			tail := strings.ToLower(t[i+len(sep):])
			if (label != "" && strings.Contains(tail, label)) || (siteName != "" && strings.Contains(tail, strings.ToLower(siteName))) {
				t, cut = strings.TrimSpace(t[:i]), true
			}
		}
	}
	// "Model by Someone" is how Thingiverse and Cults name pages.
	if cut || label == "thingiverse" || label == "cults3d" {
		if m := reBy.FindStringSubmatch(t); m != nil {
			if *designer == "" {
				*designer = strings.TrimSpace(m[2])
			}
			t = strings.TrimSpace(m[1])
		}
	}
	return t
}

// plainText turns a site's HTML description into readable plain text.
func plainText(s string) string {
	if strings.Contains(s, "</") || reBlock.MatchString(s) { // HTML, not text that happens to have a "<"
		s = reBlock.ReplaceAllString(s, "\n")
		s = reTags.ReplaceAllString(s, "")
	}
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	s = strings.Join(lines, "\n")
	s = reLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// downloadPhoto saves a picture from a model page as a library photo.
func (a *App) downloadPhoto(ctx context.Context, itemID, raw string, n int) error {
	u, err := checkLinkURL(raw)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", linkUA)
	req.Header.Set("Accept", "image/*")
	resp, err := linkClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't download the picture: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("couldn't download the picture: %s", resp.Status)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}[ct]
	if ext == "" {
		if e := strings.ToLower(path.Ext(u.Path)); library.IsImage("x" + e) {
			ext = e
		} else {
			return fmt.Errorf("that link isn't a picture (%s)", ct)
		}
	}
	tmp, err := os.CreateTemp(a.Store.Dir(), "lib-*")
	if err != nil {
		return err
	}
	nbytes, err := io.Copy(tmp, io.LimitReader(resp.Body, 25<<20+1))
	tmp.Close()
	if err == nil && nbytes > 25<<20 {
		err = fmt.Errorf("the picture is too big")
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	name := fmt.Sprintf("from-link-%d%s", n, ext)
	if _, err := a.Library.AddPhotos(itemID, []library.Incoming{{Name: name, Tmp: tmp.Name()}}); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (a *App) linkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/library/link-info", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			URL string `json:"url"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		info, err := FetchLinkInfo(r.Context(), in.URL)
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, info)
	})
	mux.HandleFunc("POST /api/library/{id}/photo-from-link", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			URLs []string `json:"urls"`
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		id := r.PathValue("id")
		it, ok := a.Library.Get(id)
		if !ok {
			fail(w, 404, fmt.Errorf("model not found"))
			return
		}
		var errs []string
		for i, u := range in.URLs {
			if i >= 8 {
				break
			}
			if err := a.downloadPhoto(r.Context(), id, u, len(it.AddedPhotos)+i+1); err != nil {
				errs = append(errs, err.Error())
			}
		}
		out, _ := a.Library.Get(id)
		if len(errs) > 0 && len(errs) == len(in.URLs) {
			fail(w, 400, fmt.Errorf("%s", errs[0]))
			return
		}
		writeJSON(w, 200, out)
	})
}
