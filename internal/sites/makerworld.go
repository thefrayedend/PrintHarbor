package sites

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// MakerWorld (Bambu Lab): Bambu's design-service API with your MakerWorld
// account token (the "token" cookie after signing in at makerworld.com).
// Model details and the 3MF download follow working open-source tools
// (print-handy model_job.py). MakerWorld may answer with a robot check
// (captcha); PrintHarbor never tries to get around it.
type MakerWorld struct{ Token string }

const mwAPI = "https://api.bambulab.com/v1"

func (w *MakerWorld) Info() Info {
	return Info{ID: "makerworld", Name: "MakerWorld", URL: "https://makerworld.com", NeedsKey: true, Ready: w.Token != "", CanDownload: true,
		KeyHelp: "Sign in at makerworld.com, open the browser's developer tools → Application/Storage → Cookies → makerworld.com, and copy the value of the \"token\" cookie into Settings → Model sites.",
		Note:    "MakerWorld sometimes asks for a robot check; if it does, open the model on makerworld.com once and try again."}
}

func (w *MakerWorld) hdr() map[string]string {
	return map[string]string{"Authorization": "Bearer " + w.Token, "Origin": "https://makerworld.com", "Referer": "https://makerworld.com/", "x-bbl-app-source": "makerworld"}
}

func (w *MakerWorld) get(ctx context.Context, path string, out any) error {
	if w.Token == "" {
		return fmt.Errorf("add your MakerWorld token in Settings → Model sites")
	}
	err := doJSON(ctx, "GET", mwAPI+path, nil, w.hdr(), out)
	if err != nil && strings.Contains(err.Error(), "418") {
		return fmt.Errorf("MakerWorld asked for a robot check (captcha). Open the model on makerworld.com while signed in, pass the check there, then try again")
	}
	return err
}

type mwDesign struct {
	ID              int    `json:"id"`
	Title           string `json:"title"`
	TitleTranslated string `json:"titleTranslated"`
	Cover           string `json:"cover"`
	CoverURL        string `json:"coverUrl"`
	LikeCount       int    `json:"likeCount"`
	DownloadCount   int    `json:"downloadCount"`
	DesignCreator   *struct {
		Name string `json:"name"`
	} `json:"designCreator"`
}

func (w *MakerWorld) card(d mwDesign) Card {
	c := Card{Site: "makerworld", ID: strconv.Itoa(d.ID), Title: firstNonEmpty(d.TitleTranslated, d.Title), Thumb: firstNonEmpty(d.Cover, d.CoverURL),
		Likes: d.LikeCount, Downloads: d.DownloadCount, Free: true, URL: "https://makerworld.com/en/models/" + strconv.Itoa(d.ID)}
	if d.DesignCreator != nil {
		c.Author = d.DesignCreator.Name
	}
	return c
}

func (w *MakerWorld) Search(ctx context.Context, q Query) (Page, error) {
	const limit = 30
	order := map[string]string{"newest": "time", "trending": "hotScore", "popular": "downloadCount"}[q.Sort]
	if order == "" {
		order = "hotScore"
	}
	path := "/search-service/select/design/nav?navKey=Trending&limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(q.Page*limit) + "&orderBy=" + order
	if t := strings.TrimSpace(q.Text); t != "" {
		path = "/search-service/select/design2?keyword=" + url.QueryEscape(t) + "&limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(q.Page*limit) + "&orderBy=" + order
	}
	var out struct {
		Total int        `json:"total"`
		Hits  []mwDesign `json:"hits"`
	}
	if err := w.get(ctx, path, &out); err != nil {
		return Page{}, fmt.Errorf("%v (you can still paste a MakerWorld link above)", err)
	}
	pg := Page{Items: []Card{}, More: (q.Page+1)*limit < out.Total}
	for _, d := range out.Hits {
		pg.Items = append(pg.Items, w.card(d))
	}
	return pg, nil
}

func (w *MakerWorld) Model(ctx context.Context, id string) (Model, error) {
	var d struct {
		mwDesign
		Summary           string   `json:"summary"`
		SummaryTranslated string   `json:"summaryTranslated"`
		License           string   `json:"license"`
		Tags              []string `json:"tags"`
		DesignExtension   *struct {
			DesignPictures []struct {
				URL string `json:"url"`
			} `json:"design_pictures"`
		} `json:"designExtension"`
		Instances []struct {
			ID              int    `json:"id"`
			Title           string `json:"title"`
			TitleTranslated string `json:"titleTranslated"`
			IsDefault       bool   `json:"isDefault"`
		} `json:"instances"`
	}
	if err := w.get(ctx, "/design-service/design/"+url.PathEscape(id), &d); err != nil {
		return Model{}, err
	}
	m := Model{Card: w.card(d.mwDesign), Description: plain(firstNonEmpty(d.SummaryTranslated, d.Summary)), License: d.License,
		Tags: append([]string{}, d.Tags...), Images: []string{}, Files: []File{}}
	if d.DesignExtension != nil {
		for _, p := range d.DesignExtension.DesignPictures {
			if p.URL != "" {
				m.Images = append(m.Images, p.URL)
			}
		}
	}
	if len(m.Images) == 0 && m.Thumb != "" {
		m.Images = append(m.Images, m.Thumb)
	}
	// Each "print profile" (instance) is a ready 3MF.
	for _, in := range d.Instances {
		name := firstNonEmpty(in.TitleTranslated, in.Title, "Print profile "+strconv.Itoa(in.ID))
		if in.IsDefault {
			name += " (default)"
		}
		m.Files = append(m.Files, File{ID: strconv.Itoa(in.ID), Name: name + ".3mf", Kind: "model"})
	}
	return m, nil
}

func (w *MakerWorld) Download(ctx context.Context, m Model, f File) (io.ReadCloser, error) {
	var r struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := w.get(ctx, "/design-service/instance/"+url.PathEscape(f.ID)+"/f3mf?type=download", &r); err != nil {
		return nil, err
	}
	if r.URL == "" {
		return nil, fmt.Errorf("MakerWorld gave no download address")
	}
	return fetch(ctx, r.URL, nil)
}
