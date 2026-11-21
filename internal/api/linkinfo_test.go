package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParseOpenGraph(t *testing.T) {
	page := `<html><head><title>ignored</title>
<meta property="og:site_name" content="Thingiverse">
<meta property="og:title" content="Articulated Dragon by McGybeer - Thingiverse">
<meta property="og:description" content="A &amp; B &lt;fun&gt; dragon">
<meta property="og:image" content="/img/dragon.jpg">
<meta name="keywords" content="dragon, articulated, dragon , toy">
</head></html>`
	base, _ := url.Parse("https://www.thingiverse.com/thing:2755206")
	info := parseLinkPage(base, page)
	if info.Title != "Articulated Dragon" || info.Designer != "McGybeer" {
		t.Fatalf("title/designer: %q / %q", info.Title, info.Designer)
	}
	if info.Description != "A & B <fun> dragon" {
		t.Fatalf("description: %q", info.Description)
	}
	if strings.Join(info.Tags, "|") != "dragon|articulated|toy" {
		t.Fatalf("tags: %v", info.Tags)
	}
	if len(info.Images) != 1 || info.Images[0] != "https://www.thingiverse.com/img/dragon.jpg" {
		t.Fatalf("images: %v", info.Images)
	}
}

func TestParseJSONLD(t *testing.T) {
	page := `<script type="application/ld+json">{"@context":"https://schema.org","@graph":[
 {"@type":"BreadcrumbList","name":"crumbs"},
 {"@type":"Product","name":"Cable Clip","description":"<p>Holds cables.</p><p>Prints flat.</p>",
  "brand":{"@type":"Brand","name":"ClipMaker"},"keywords":["desk","cable"],
  "license":"https://creativecommons.org/licenses/by-nc-sa/4.0/","image":["https://cdn.x/a.png",{"url":"https://cdn.x/b.png"}]}]}</script>
<meta property="og:title" content="Cable Clip | Cults">`
	base, _ := url.Parse("https://cults3d.com/en/3d-model/home/cable-clip")
	info := parseLinkPage(base, page)
	if info.Title != "Cable Clip" || info.Designer != "ClipMaker" || info.License != "CC BY-NC-SA 4.0" {
		t.Fatalf("%+v", info)
	}
	if info.Description != "Holds cables.\nPrints flat." {
		t.Fatalf("description: %q", info.Description)
	}
	if len(info.Images) != 2 || len(info.Tags) != 2 {
		t.Fatalf("%+v", info)
	}
}

func TestParseNextData(t *testing.T) {
	page := `<meta property="og:title" content="Gridfinity Bin - MakerWorld">
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"design":{"id":5,"title":"Gridfinity Bin 2x3",
 "summary":"Stackable bin.","designCreator":{"name":"BinGuy"},"tags":["gridfinity","storage"],"license":"BY",
 "coverUrl":"https://makerworld.bblmw.com/cover.jpg","designExtension":{"design_pictures":[{"url":"https://makerworld.bblmw.com/p1.jpg"}]}}}}}</script>`
	base, _ := url.Parse("https://makerworld.com/en/models/123")
	info := parseLinkPage(base, page)
	if info.Title != "Gridfinity Bin 2x3" || info.Designer != "BinGuy" || info.Description != "Stackable bin." || info.License != "BY" {
		t.Fatalf("%+v", info)
	}
	if len(info.Images) != 2 || len(info.Tags) != 2 {
		t.Fatalf("%+v", info)
	}
}

func TestFetchLinkInfoErrors(t *testing.T) {
	if _, err := FetchLinkInfo(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("non-http accepted")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			http.Error(w, "no", 403)
			return
		}
		w.Write([]byte(`<meta property="og:title" content="Phone Stand">`))
	}))
	defer srv.Close()
	if _, err := FetchLinkInfo(context.Background(), srv.URL+"/blocked"); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("blocked: %v", err)
	}
	info, err := FetchLinkInfo(context.Background(), srv.URL+"/ok")
	if err != nil || info.Title != "Phone Stand" {
		t.Fatalf("%v %+v", err, info)
	}
}
