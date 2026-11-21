package sites

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Thingiverse: official REST API (api.thingiverse.com) with a free App
// Token from https://www.thingiverse.com/developers, sent as a Bearer token.
type Thingiverse struct{ Token string }

const tvAPI = "https://api.thingiverse.com"

func (t *Thingiverse) Info() Info {
	return Info{ID: "thingiverse", Name: "Thingiverse", URL: "https://www.thingiverse.com", NeedsKey: true, Ready: t.Token != "", CanDownload: true,
		KeyHelp: "Free: sign in at thingiverse.com/developers, create an app (\"Desktop app\"), and copy its App Token into Settings → Model sites."}
}

func (t *Thingiverse) get(ctx context.Context, path string, out any) error {
	if t.Token == "" {
		return fmt.Errorf("add your free Thingiverse App Token in Settings → Model sites")
	}
	return doJSON(ctx, "GET", tvAPI+path, nil, map[string]string{"Authorization": "Bearer " + t.Token}, out)
}

type tvThing struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	PublicURL     string `json:"public_url"`
	Thumbnail     string `json:"thumbnail"`
	PreviewImage  string `json:"preview_image"`
	LikeCount     int    `json:"like_count"`
	DownloadCount int    `json:"download_count"`
	CollectCount  int    `json:"collect_count"`
	Creator       *struct {
		Name string `json:"name"`
	} `json:"creator"`
}

func (t *Thingiverse) card(x tvThing) Card {
	c := Card{Site: "thingiverse", ID: strconv.Itoa(x.ID), Title: x.Name, URL: x.PublicURL, Likes: x.LikeCount, Downloads: x.DownloadCount, Free: true,
		Thumb: firstNonEmpty(x.PreviewImage, x.Thumbnail)}
	if c.URL == "" {
		c.URL = "https://www.thingiverse.com/thing:" + c.ID
	}
	if x.Creator != nil {
		c.Author = x.Creator.Name
	}
	return c
}

func (t *Thingiverse) Search(ctx context.Context, q Query) (Page, error) {
	const per = 30
	sort := map[string]string{"newest": "newest", "trending": "popular", "popular": "popular"}[q.Sort]
	if sort == "" {
		sort = "popular"
	}
	if strings.TrimSpace(q.Text) != "" && q.Sort == "" {
		sort = "relevant"
	}
	path := "/search/" + url.PathEscape(strings.TrimSpace(q.Text)) + "?type=things&per_page=" + strconv.Itoa(per) + "&page=" + strconv.Itoa(q.Page+1) + "&sort=" + sort
	var out struct {
		Total int       `json:"total"`
		Hits  []tvThing `json:"hits"`
	}
	if err := t.get(ctx, path, &out); err != nil {
		return Page{}, err
	}
	pg := Page{Items: []Card{}, More: (q.Page+1)*per < out.Total}
	for _, x := range out.Hits {
		pg.Items = append(pg.Items, t.card(x))
	}
	return pg, nil
}

func (t *Thingiverse) Model(ctx context.Context, id string) (Model, error) {
	var d struct {
		tvThing
		Description     string `json:"description"`
		DescriptionHTML string `json:"description_html"`
		License         string `json:"license"`
		Categories      []struct {
			Name string `json:"name"`
		} `json:"categories"`
	}
	if err := t.get(ctx, "/things/"+url.PathEscape(id), &d); err != nil {
		return Model{}, err
	}
	m := Model{Card: t.card(d.tvThing), Description: plain(firstNonEmpty(d.DescriptionHTML, d.Description)), License: d.License,
		Images: []string{}, Tags: []string{}, Files: []File{}}
	var cats []struct {
		Name string `json:"name"`
	}
	if t.get(ctx, "/things/"+url.PathEscape(id)+"/categories", &cats) == nil && len(cats) > 0 {
		m.Category = cats[0].Name
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if t.get(ctx, "/things/"+url.PathEscape(id)+"/tags", &tags) == nil {
		for _, x := range tags {
			m.Tags = append(m.Tags, x.Name)
		}
	}
	var imgs []struct {
		Sizes []struct {
			Type string `json:"type"`
			Size string `json:"size"`
			URL  string `json:"url"`
		} `json:"sizes"`
	}
	if t.get(ctx, "/things/"+url.PathEscape(id)+"/images", &imgs) == nil {
		for _, im := range imgs {
			best := ""
			for _, s := range im.Sizes {
				if s.Type == "display" && s.Size == "large" {
					best = s.URL
				}
				if best == "" && s.Type == "display" {
					best = s.URL
				}
			}
			if best != "" {
				m.Images = append(m.Images, best)
			}
		}
	}
	if len(m.Images) == 0 && m.Thumb != "" {
		m.Images = append(m.Images, m.Thumb)
	}
	var files []struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		DownloadURL string `json:"download_url"`
		DirectURL   string `json:"direct_url"`
	}
	if err := t.get(ctx, "/things/"+url.PathEscape(id)+"/files", &files); err != nil {
		return Model{}, err
	}
	for _, f := range files {
		m.Files = append(m.Files, File{ID: strconv.Itoa(f.ID) + "|" + firstNonEmpty(f.DirectURL, f.DownloadURL), Name: f.Name, Size: f.Size, Kind: FileKind(f.Name)})
	}
	return m, nil
}

func (t *Thingiverse) Download(ctx context.Context, m Model, f File) (io.ReadCloser, error) {
	_, u, _ := strings.Cut(f.ID, "|")
	if u == "" {
		return nil, fmt.Errorf("no download address for %s", f.Name)
	}
	return fetch(ctx, u, map[string]string{"Authorization": "Bearer " + t.Token})
}
