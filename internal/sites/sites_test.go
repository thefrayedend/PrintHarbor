package sites

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// The sites are tested against a pretend copy of each site's API: requests the
// code sends to api.thingiverse.com, api.printables.com, api.bambulab.com and
// cults3d.com are answered by a local server, which also records what was
// asked. Nothing here touches the internet.

type rewrite struct{ to *url.URL }

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-Site-Host", req.URL.Host)
	req.URL.Scheme, req.URL.Host = r.to.Scheme, r.to.Host
	return http.DefaultTransport.RoundTrip(req)
}

type call struct {
	Host, Method, Path, Query, Auth string
	Body                            map[string]any
}

type fakeWeb struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	calls []call
	// answer gets every API request and writes the reply.
	answer func(w http.ResponseWriter, c call)
}

func newFakeWeb(t *testing.T) *fakeWeb {
	f := &fakeWeb{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") { // a download link the fake API handed out
			w.Header().Set("X-Auth", r.Header.Get("Authorization"))
			io.WriteString(w, "FILE:"+strings.TrimPrefix(r.URL.Path, "/files/")+":"+r.Header.Get("Authorization"))
			return
		}
		c := call{Host: r.Header.Get("X-Site-Host"), Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &c.Body)
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		f.mu.Unlock()
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("%s %s: sent without PrintHarbor's User-Agent", c.Host, c.Path)
		}
		f.answer(w, c)
	}))
	u, _ := url.Parse(f.srv.URL)
	old := client
	client = &http.Client{Transport: rewrite{u}}
	t.Cleanup(func() { client = old; f.srv.Close() })
	return f
}

func (f *fakeWeb) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		f.t.Fatal("the site was never asked")
	}
	return f.calls[len(f.calls)-1]
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// body reads a download to the end: body(t)(site.Download(...)).
func body(t *testing.T) func(io.ReadCloser, error) string {
	return func(rc io.ReadCloser, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b)
	}
}

var ctx = context.Background()

func TestParseLinkAndHelpers(t *testing.T) {
	for link, want := range map[string][2]string{
		"https://www.printables.com/model/3161-3d-benchy":                           {"printables", "3161"},
		"https://www.printables.com/de/model/3161-3d-benchy/files":                  {"printables", "3161"},
		"https://www.thingiverse.com/thing:763622":                                  {"thingiverse", "763622"},
		"thingiverse.com/thing:763622/files":                                        {"thingiverse", "763622"},
		"https://makerworld.com/en/models/12345#profileId-6789":                     {"makerworld", "12345"},
		"https://makerworld.com/models/12345":                                       {"makerworld", "12345"},
		"https://cults3d.com/en/3d-model/various/benchy-the-boat":                   {"cults", "benchy-the-boat"},
		"  https://cults3d.com/fr/3d-model/various/benchy-the-boat?utm=x  ":         {"cults", "benchy-the-boat"},
		"https://example.com/model/3161":                                            {"", ""},
		"https://www.printables.com/@someone":                                       {"", ""},
		"":                                                                          {"", ""},
		"https://www.thingiverse.com/thing:abc":                                     {"", ""},
		"https://makerworld.com/en/@designer/collections/models":                    {"", ""},
		"https://www.printables.com/model/":                                         {"", ""},
		"not a link at all, just words about printables.com and models":             {"", ""},
		"https://www.printables.com/model/99-x and https://thingiverse.com/thing:5": {"printables", "99"},
	} {
		s, id, ok := ParseLink(link)
		if s != want[0] || id != want[1] || ok != (want[0] != "") {
			t.Errorf("ParseLink(%q) = %q %q %v, want %q %q", link, s, id, ok, want[0], want[1])
		}
	}
	for name, want := range map[string]string{"Boat.STL": "model", "a.3mf": "model", "x.step": "model", "pack.zip": "model", "p.gcode": "gcode",
		"p.bgcode": "gcode", "readme.txt": "other", "photo.jpg": "other", "noext": "other"} {
		if got := FileKind(name); got != want {
			t.Errorf("FileKind(%q) = %q, want %q", name, got, want)
		}
	}
	got := plain(`<p>A <b>boat</b> &amp; more.</p><p>Second&nbsp;line<br>third</p><ul><li>one</li><li>two</li></ul>` + "\n\n\n\n" + `<div>end &lt;3</div>`)
	want := "A boat & more.\nSecond line\nthird\none\ntwo\n\nend <3"
	if got != want {
		t.Errorf("plain() = %q, want %q", got, want)
	}
	if n := len(All(Keys{ThingiverseToken: " t "})); n != 4 {
		t.Errorf("All() has %d sites", n)
	}
	for _, s := range All(Keys{ThingiverseToken: " t "}) {
		i := s.Info()
		if i.ID == "" || i.Name == "" || !strings.HasPrefix(i.URL, "https://") {
			t.Errorf("site info incomplete: %+v", i)
		}
		// Ready exactly when it needs no key or has one.
		if want := !i.NeedsKey || i.ID == "thingiverse"; i.Ready != want {
			t.Errorf("%s: ready=%v, want %v", i.ID, i.Ready, want)
		}
	}
}

func TestThingiverse(t *testing.T) {
	f := newFakeWeb(t)
	f.answer = func(w http.ResponseWriter, c call) {
		if c.Host != "api.thingiverse.com" {
			t.Errorf("asked %s", c.Host)
		}
		if c.Auth != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case strings.HasPrefix(c.Path, "/search/"):
			jsonOut(w, map[string]any{"total": 61, "hits": []any{
				map[string]any{"id": 7, "name": "Benchy", "public_url": "https://www.thingiverse.com/thing:7", "thumbnail": "t.jpg", "preview_image": "p.jpg", "like_count": 5, "download_count": 9, "creator": map[string]any{"name": "Ann"}},
				map[string]any{"id": 8, "name": "No link"},
			}})
		case c.Path == "/things/7":
			jsonOut(w, map[string]any{"id": 7, "name": "Benchy", "thumbnail": "t.jpg", "description": "plain words", "description_html": "<p>A <b>boat</b></p>", "license": "CC BY", "creator": map[string]any{"name": "Ann"}})
		case c.Path == "/things/7/categories":
			jsonOut(w, []any{map[string]any{"name": "Toys"}, map[string]any{"name": "Other"}})
		case c.Path == "/things/7/tags":
			jsonOut(w, []any{map[string]any{"name": "boat"}, map[string]any{"name": "test"}})
		case c.Path == "/things/7/images":
			jsonOut(w, []any{
				map[string]any{"sizes": []any{map[string]any{"type": "thumb", "size": "large", "url": "thumb.jpg"}, map[string]any{"type": "display", "size": "medium", "url": "med.jpg"}, map[string]any{"type": "display", "size": "large", "url": "large.jpg"}}},
				map[string]any{"sizes": []any{map[string]any{"type": "display", "size": "medium", "url": "only-med.jpg"}}},
				map[string]any{"sizes": []any{map[string]any{"type": "thumb", "size": "small", "url": "skip.jpg"}}},
			})
		case c.Path == "/things/7/files":
			jsonOut(w, []any{
				map[string]any{"id": 11, "name": "benchy.stl", "size": 1234, "download_url": "dl", "direct_url": f.srv.URL + "/files/benchy.stl"},
				map[string]any{"id": 12, "name": "notes.txt", "size": 5, "download_url": f.srv.URL + "/files/notes.txt"},
				map[string]any{"id": 13, "name": "broken.stl"},
			})
		default:
			w.WriteHeader(404)
		}
	}
	tv := &Thingiverse{Token: "tok"}
	pg, err := tv.Search(ctx, Query{Text: " benchy boat ", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := f.last()
	q, _ := url.ParseQuery(c.Query)
	if c.Path != "/search/benchy boat" || q.Get("page") != "2" || q.Get("per_page") != "30" || q.Get("sort") != "relevant" || q.Get("type") != "things" {
		t.Errorf("search asked %s ? %s", c.Path, c.Query)
	}
	if len(pg.Items) != 2 || !pg.More {
		t.Fatalf("page: %+v", pg)
	}
	if got := pg.Items[0]; got != (Card{Site: "thingiverse", ID: "7", Title: "Benchy", Author: "Ann", Thumb: "p.jpg", URL: "https://www.thingiverse.com/thing:7", Likes: 5, Downloads: 9, Free: true}) {
		t.Errorf("card: %+v", got)
	}
	if pg.Items[1].URL != "https://www.thingiverse.com/thing:8" {
		t.Errorf("a missing link isn't filled in: %q", pg.Items[1].URL)
	}
	// The last page, and the sort the user picked.
	if pg, _ = tv.Search(ctx, Query{Sort: "newest", Page: 2}); pg.More {
		t.Error("page 3 of 61 results (30 a page) claims there is more")
	}
	if q, _ := url.ParseQuery(f.last().Query); q.Get("sort") != "newest" || q.Get("page") != "3" {
		t.Errorf("browse asked %s", f.last().Query)
	}

	m, err := tv.Model(ctx, "7")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Benchy" || m.Description != "A boat" || m.License != "CC BY" || m.Category != "Toys" || strings.Join(m.Tags, ",") != "boat,test" {
		t.Errorf("model: %+v", m)
	}
	if strings.Join(m.Images, ",") != "large.jpg,only-med.jpg" {
		t.Errorf("images: %v", m.Images)
	}
	if len(m.Files) != 3 || m.Files[0].Kind != "model" || m.Files[1].Kind != "other" || m.Files[0].Size != 1234 {
		t.Fatalf("files: %+v", m.Files)
	}
	if got := body(t)(tv.Download(ctx, m, m.Files[0])); got != "FILE:benchy.stl:Bearer tok" {
		t.Errorf("download: %q", got)
	}
	if got := body(t)(tv.Download(ctx, m, m.Files[1])); got != "FILE:notes.txt:Bearer tok" {
		t.Errorf("download by download_url: %q", got)
	}
	if _, err := tv.Download(ctx, m, m.Files[2]); err == nil {
		t.Error("a file without an address downloaded")
	}

	// No key: says what to do, and never asks the site.
	n := len(f.calls)
	if _, err := (&Thingiverse{}).Search(ctx, Query{}); err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Errorf("no token: %v", err)
	}
	if len(f.calls) != n {
		t.Error("asked the site without a token")
	}
	// A wrong key is explained, not shown as a raw status.
	if _, err := (&Thingiverse{Token: "bad"}).Model(ctx, "7"); err == nil || !strings.Contains(err.Error(), "check the key in Settings") {
		t.Errorf("bad token: %v", err)
	}
}

func TestPrintables(t *testing.T) {
	f := newFakeWeb(t)
	item := map[string]any{"id": "3161", "name": "3D Benchy", "slug": "3d-benchy", "likesCount": 10, "downloadCount": 20,
		"image": map[string]any{"filePath": "/media/prints/3161/a.jpg"}, "user": map[string]any{"publicUsername": "", "handle": "prusa"}}
	linkFail := map[string]bool{}
	f.answer = func(w http.ResponseWriter, c call) {
		if c.Host != "api.printables.com" || c.Method != "POST" || c.Path != "/graphql/" {
			t.Errorf("asked %s %s %s", c.Method, c.Host, c.Path)
		}
		query, _ := c.Body["query"].(string)
		vars, _ := c.Body["variables"].(map[string]any)
		switch {
		case strings.Contains(query, "searchPrints2"):
			jsonOut(w, map[string]any{"data": map[string]any{"r": map[string]any{"totalCount": 37, "items": []any{item}}}})
		case strings.Contains(query, "morePrints"):
			cur := "next"
			if vars["cursor"] == "next" {
				cur = ""
			}
			jsonOut(w, map[string]any{"data": map[string]any{"r": map[string]any{"cursor": cur, "items": []any{item}}}})
		case strings.Contains(query, "print(id:"):
			if vars["id"] == "404" {
				jsonOut(w, map[string]any{"data": map[string]any{"print": nil}})
				return
			}
			if vars["id"] == "500" {
				jsonOut(w, map[string]any{"data": nil, "errors": []any{map[string]any{"message": "Something broke"}}})
				return
			}
			full := map[string]any{}
			for k, v := range item {
				full[k] = v
			}
			full["summary"], full["description"] = "short", "<p>The <i>jolly</i> boat</p>"
			full["images"] = []any{map[string]any{"filePath": "media/1.jpg"}, map[string]any{"filePath": ""}, map[string]any{"filePath": "/media/2.jpg"}}
			full["tags"] = []any{map[string]any{"name": "boat"}}
			full["license"] = map[string]any{"name": "Creative Commons", "abbreviation": "CC-BY"}
			full["category"] = map[string]any{"path": []any{map[string]any{"name": "Hobby"}, map[string]any{"name": "Toys"}}}
			full["stls"] = []any{map[string]any{"id": "1", "name": "benchy.stl", "fileSize": 100}}
			full["gcodes"] = []any{map[string]any{"id": "2", "name": "benchy.gcode", "fileSize": 200}}
			full["otherFiles"] = []any{map[string]any{"id": "3", "name": "benchy.step", "fileSize": 300}}
			jsonOut(w, map[string]any{"data": map[string]any{"print": full}})
		case strings.Contains(query, "getDownloadLink"):
			ft, _ := vars["fileType"].(string)
			if vars["printId"] != "3161" || vars["source"] != "model_detail" {
				t.Errorf("download link asked with %v", vars)
			}
			if linkFail[ft] {
				jsonOut(w, map[string]any{"data": map[string]any{"r": map[string]any{"ok": false, "errors": []any{map[string]any{"messages": []any{"File is not available"}}}}}})
				return
			}
			jsonOut(w, map[string]any{"data": map[string]any{"r": map[string]any{"ok": true, "output": map[string]any{"link": f.srv.URL + "/files/" + ft + "-" + vars["id"].(string)}}}})
		default:
			w.WriteHeader(400)
		}
	}
	p := &Printables{}
	vars := func() map[string]any { v, _ := f.last().Body["variables"].(map[string]any); return v }

	pg, err := p.Search(ctx, Query{Text: " benchy ", Page: 0})
	if err != nil {
		t.Fatal(err)
	}
	if v := vars(); v["q"] != "benchy" || v["offset"] != float64(0) || v["limit"] != float64(36) || v["ordering"] != "best_match" {
		t.Errorf("search variables: %v", v)
	}
	if len(pg.Items) != 1 || !pg.More {
		t.Fatalf("search page: %+v", pg)
	}
	if got := pg.Items[0]; got != (Card{Site: "printables", ID: "3161", Title: "3D Benchy", Author: "prusa", Thumb: "https://media.printables.com/media/prints/3161/a.jpg",
		URL: "https://www.printables.com/model/3161-3d-benchy", Likes: 10, Downloads: 20, Free: true}) {
		t.Errorf("card: %+v", got)
	}
	if pg, _ = p.Search(ctx, Query{Text: "benchy", Page: 1, Sort: "newest"}); pg.More {
		t.Error("the second page of 37 (36 a page) claims there is more")
	}
	if v := vars(); v["offset"] != float64(36) || v["ordering"] != "latest" {
		t.Errorf("page 2 variables: %v", v)
	}
	// Browsing pages by cursor and stops when the site returns none.
	pg, _ = p.Search(ctx, Query{Sort: "trending"})
	if v := vars(); v["cursor"] != nil || v["ordering"] != "-likes_count_7_days" {
		t.Errorf("browse variables: %v", v)
	}
	if !pg.More || pg.Cursor != "next" {
		t.Errorf("browse page: %+v", pg)
	}
	if pg, _ = p.Search(ctx, Query{Cursor: pg.Cursor}); pg.More {
		t.Error("the last browse page claims there is more")
	}
	if v := vars(); v["cursor"] != "next" || v["ordering"] != "-download_count_30_days" {
		t.Errorf("browse page 2 variables: %v", v)
	}

	m, err := p.Model(ctx, "3161")
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "The jolly boat" || m.License != "CC-BY" || m.Category != "Hobby/Toys" || strings.Join(m.Tags, ",") != "boat" ||
		strings.Join(m.Images, ",") != "https://media.printables.com/media/1.jpg,https://media.printables.com/media/2.jpg" {
		t.Errorf("model: %+v", m)
	}
	want := []File{{"stl:1", "benchy.stl", 100, "model"}, {"other:3", "benchy.step", 300, "model"}, {"gcode:2", "benchy.gcode", 200, "gcode"}}
	if len(m.Files) != 3 || m.Files[0] != want[0] || m.Files[1] != want[1] || m.Files[2] != want[2] {
		t.Errorf("files: %+v", m.Files)
	}
	if got := body(t)(p.Download(ctx, m, m.Files[0])); got != "FILE:stl-1:" {
		t.Errorf("download: %q", got)
	}
	if got := body(t)(p.Download(ctx, m, m.Files[2])); got != "FILE:gcode-2:" {
		t.Errorf("gcode download: %q", got)
	}
	// "Other" files are sometimes only handed out as the stl type: it falls back.
	linkFail["other"] = true
	if got := body(t)(p.Download(ctx, m, m.Files[1])); got != "FILE:stl-3:" {
		t.Errorf("other-file fallback: %q", got)
	}
	linkFail["stl"] = true
	if _, err := p.Download(ctx, m, m.Files[0]); err == nil || !strings.Contains(err.Error(), "File is not available") {
		t.Errorf("refused download: %v", err)
	}
	if _, err := p.Model(ctx, "404"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing model: %v", err)
	}
	if _, err := p.Model(ctx, "500"); err == nil || !strings.Contains(err.Error(), "Something broke") {
		t.Errorf("site error: %v", err)
	}
}

func TestMakerWorld(t *testing.T) {
	f := newFakeWeb(t)
	captcha := false
	f.answer = func(w http.ResponseWriter, c call) {
		if c.Host != "api.bambulab.com" || c.Auth != "Bearer cookie" {
			t.Errorf("asked %s with %q", c.Host, c.Auth)
		}
		if captcha {
			w.WriteHeader(418)
			return
		}
		switch {
		case strings.HasPrefix(c.Path, "/v1/search-service/select/design"):
			jsonOut(w, map[string]any{"total": 31, "hits": []any{map[string]any{"id": 55, "title": "Kasten", "titleTranslated": "Box", "cover": "c.jpg", "likeCount": 3, "downloadCount": 4, "designCreator": map[string]any{"name": "Bo"}}}})
		case c.Path == "/v1/design-service/design/55":
			jsonOut(w, map[string]any{"id": 55, "title": "Kasten", "coverUrl": "cover.jpg", "summary": "<p>Eine Kiste</p>", "summaryTranslated": "<p>A box</p>", "license": "Standard", "tags": []any{"box"},
				"designExtension": map[string]any{"design_pictures": []any{map[string]any{"url": "1.jpg"}, map[string]any{"url": ""}}},
				"instances":       []any{map[string]any{"id": 900, "title": "0.2mm", "isDefault": true}, map[string]any{"id": 901, "titleTranslated": "Fast"}, map[string]any{"id": 902}}})
		case c.Path == "/v1/design-service/instance/900/f3mf":
			jsonOut(w, map[string]any{"name": "box.3mf", "url": f.srv.URL + "/files/box.3mf"})
		case c.Path == "/v1/design-service/instance/901/f3mf":
			jsonOut(w, map[string]any{"name": "box.3mf", "url": ""})
		default:
			w.WriteHeader(404)
		}
	}
	mw := &MakerWorld{Token: "cookie"}
	pg, err := mw.Search(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if c := f.last(); c.Path != "/v1/search-service/select/design/nav" || !strings.Contains(c.Query, "orderBy=hotScore") || !strings.Contains(c.Query, "offset=0") {
		t.Errorf("browse asked %s ? %s", c.Path, c.Query)
	}
	if len(pg.Items) != 1 || !pg.More || pg.Items[0] != (Card{Site: "makerworld", ID: "55", Title: "Box", Author: "Bo", Thumb: "c.jpg", URL: "https://makerworld.com/en/models/55", Likes: 3, Downloads: 4, Free: true}) {
		t.Errorf("page: %+v", pg)
	}
	if pg, _ = mw.Search(ctx, Query{Text: "gear box", Sort: "popular", Page: 1}); pg.More {
		t.Error("page 2 of 31 claims there is more")
	}
	if c := f.last(); c.Path != "/v1/search-service/select/design2" || !strings.Contains(c.Query, "keyword=gear+box") || !strings.Contains(c.Query, "offset=30") || !strings.Contains(c.Query, "orderBy=downloadCount") {
		t.Errorf("search asked %s ? %s", c.Path, c.Query)
	}
	m, err := mw.Model(ctx, "55")
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Kasten" || m.Description != "A box" || m.Thumb != "cover.jpg" || strings.Join(m.Images, ",") != "1.jpg" || strings.Join(m.Tags, ",") != "box" {
		t.Errorf("model: %+v", m)
	}
	names := []string{}
	for _, x := range m.Files {
		names = append(names, x.ID+"="+x.Name+"/"+x.Kind)
	}
	if strings.Join(names, " ") != "900=0.2mm (default).3mf/model 901=Fast.3mf/model 902=Print profile 902.3mf/model" {
		t.Errorf("print profiles: %v", names)
	}
	if got := body(t)(mw.Download(ctx, m, m.Files[0])); got != "FILE:box.3mf:" {
		t.Errorf("download: %q", got) // the file itself is fetched without the token
	}
	if _, err := mw.Download(ctx, m, m.Files[1]); err == nil || !strings.Contains(err.Error(), "no download address") {
		t.Errorf("empty link: %v", err)
	}
	captcha = true
	if _, err := mw.Model(ctx, "55"); err == nil || !strings.Contains(err.Error(), "robot check") {
		t.Errorf("captcha: %v", err)
	}
	if _, err := mw.Search(ctx, Query{}); err == nil || !strings.Contains(err.Error(), "paste a MakerWorld link") {
		t.Errorf("captcha on search: %v", err)
	}
	if _, err := (&MakerWorld{}).Model(ctx, "55"); err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Errorf("no token: %v", err)
	}
}

func TestCults(t *testing.T) {
	f := newFakeWeb(t)
	f.answer = func(w http.ResponseWriter, c call) {
		if c.Host != "cults3d.com" || c.Path != "/graphql" {
			t.Errorf("asked %s%s", c.Host, c.Path)
		}
		if c.Auth != "Basic bmljazprZXk=" { // nick:key
			w.WriteHeader(401)
			return
		}
		query, _ := c.Body["query"].(string)
		vars, _ := c.Body["variables"].(map[string]any)
		switch {
		case strings.Contains(query, "creationsSearchBatch"):
			jsonOut(w, map[string]any{"data": map[string]any{"r": map[string]any{"total": 2, "results": []any{
				map[string]any{"identifier": "free-boat", "name": "Free boat", "url": "https://cults3d.com/en/3d-model/various/free-boat", "illustrationImageUrl": "f.jpg", "likesCount": 1, "downloadsCount": 2, "creator": map[string]any{"nick": "ann"}, "price": map[string]any{"cents": 0}},
				map[string]any{"identifier": "paid-boat", "name": "Paid boat", "price": map[string]any{"cents": 499}},
			}}}})
		case strings.Contains(query, "creation(slug:"):
			if vars["id"] == "gone" {
				jsonOut(w, map[string]any{"data": map[string]any{"r": nil}})
				return
			}
			if vars["id"] == "broken" {
				jsonOut(w, map[string]any{"errors": []any{map[string]any{"message": "Field is wrong"}}})
				return
			}
			jsonOut(w, map[string]any{"data": map[string]any{"r": map[string]any{"identifier": "free-boat", "name": "Free boat", "illustrationImageUrl": "f.jpg", "description": "<p>Floats</p>", "tags": []any{"boat"},
				"illustrations": []any{}, "license": map[string]any{"name": "CC"}, "category": map[string]any{"name": "Various"}}}})
		}
	}
	cu := &Cults{User: "nick", Key: "key"}
	pg, err := cu.Search(ctx, Query{Text: "boat", Sort: "newest", Page: 0})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.last().Body["variables"].(map[string]any); v["q"] != "boat" || v["sort"] != "BY_PUBLICATION" || v["limit"] != float64(30) || v["offset"] != float64(0) {
		t.Errorf("search variables: %v", v)
	}
	if len(pg.Items) != 2 || pg.More || !pg.Items[0].Free || pg.Items[1].Free || pg.Items[0].Author != "ann" {
		t.Errorf("page: %+v", pg)
	}
	m, err := cu.Model(ctx, "free-boat")
	if err != nil {
		t.Fatal(err)
	}
	if m.Description != "Floats" || m.License != "CC" || m.Category != "Various" || strings.Join(m.Images, ",") != "f.jpg" || len(m.Files) != 0 || m.DownloadNote == "" {
		t.Errorf("model: %+v", m)
	}
	// Cults never hands files to apps: the import page must be told why.
	if _, err := cu.Download(ctx, m, File{}); err == nil || !strings.Contains(err.Error(), "Download there") {
		t.Errorf("download: %v", err)
	}
	if cu.Info().CanDownload {
		t.Error("Cults claims it can download")
	}
	if _, err := cu.Model(ctx, "gone"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing model: %v", err)
	}
	if _, err := cu.Model(ctx, "broken"); err == nil || !strings.Contains(err.Error(), "Field is wrong") {
		t.Errorf("site error: %v", err)
	}
	if _, err := (&Cults{User: "nick", Key: "wrong"}).Search(ctx, Query{}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("wrong key: %v", err)
	}
	n := len(f.calls)
	if _, err := (&Cults{User: "nick"}).Search(ctx, Query{}); err == nil || !strings.Contains(err.Error(), "Settings") || len(f.calls) != n {
		t.Errorf("no key: %v (asked the site: %v)", err, len(f.calls) != n)
	}
}

// What every site does when the site itself is in trouble.
func TestSiteErrors(t *testing.T) {
	f := newFakeWeb(t)
	mode := ""
	f.answer = func(w http.ResponseWriter, c call) {
		switch mode {
		case "cloudflare":
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(403)
		case "down":
			w.WriteHeader(503)
			io.WriteString(w, strings.Repeat("x", 5000))
		case "html":
			io.WriteString(w, "<html>maintenance</html>")
		}
	}
	tv := &Thingiverse{Token: "t"}
	for m, want := range map[string]string{"cloudflare": "browser check", "down": "503", "html": "unexpected answer"} {
		mode = m
		_, err := tv.Search(ctx, Query{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", m, err)
		}
		if err != nil && len(err.Error()) > 1700 {
			t.Errorf("%s: the error message is %d characters long", m, len(err.Error()))
		}
	}
	// A dead download link is an error, not an empty file.
	mode = "down"
	if _, err := fetch(ctx, f.srv.URL+"/nothing-here", nil); err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Errorf("dead link: %v", err)
	}
}
