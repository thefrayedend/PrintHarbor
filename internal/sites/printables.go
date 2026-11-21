package sites

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Printables (Prusa): public GraphQL API, no account needed. Model details
// and getDownloadLink follow what working open-source tools use
// (catduckgnaf/print-handy, download_printables / model_job.py).
type Printables struct{}

const printablesAPI = "https://api.printables.com/graphql/"
const printablesMedia = "https://media.printables.com/"

func (p *Printables) Info() Info {
	return Info{ID: "printables", Name: "Printables", URL: "https://www.printables.com", Ready: true, CanDownload: true}
}

func (p *Printables) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	var env struct {
		Data   jsonRaw `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := doJSON(ctx, "POST", printablesAPI, map[string]any{"query": query, "variables": vars},
		map[string]string{"Origin": "https://www.printables.com", "Referer": "https://www.printables.com/"}, &env)
	if err != nil {
		return err
	}
	if len(env.Errors) > 0 && len(env.Data) < 8 {
		return fmt.Errorf("Printables: %s", env.Errors[0].Message)
	}
	return unmarshal(env.Data, out)
}

// printablesItem is the part of a print used in result lists.
const printablesItemFields = `id name slug likesCount downloadCount image { filePath } user { publicUsername handle }`

type pItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	LikesCount    int    `json:"likesCount"`
	DownloadCount int    `json:"downloadCount"`
	Image         *struct {
		FilePath string `json:"filePath"`
	} `json:"image"`
	User *struct {
		PublicUsername string `json:"publicUsername"`
		Handle         string `json:"handle"`
	} `json:"user"`
}

func (p *Printables) card(it pItem) Card {
	c := Card{Site: "printables", ID: it.ID, Title: it.Name, Likes: it.LikesCount, Downloads: it.DownloadCount, Free: true,
		URL: "https://www.printables.com/model/" + it.ID + "-" + it.Slug}
	if it.Image != nil && it.Image.FilePath != "" {
		c.Thumb = printablesMedia + strings.TrimPrefix(it.Image.FilePath, "/")
	}
	if it.User != nil {
		c.Author = it.User.PublicUsername
		if c.Author == "" {
			c.Author = it.User.Handle
		}
	}
	return c
}

// Orderings Printables' own pages use.
func printablesOrdering(sort string, search bool) string {
	if search {
		// SearchChoicesEnum (checked live): best_match, latest, popular, rating.
		switch sort {
		case "newest":
			return "latest"
		case "popular":
			return "popular"
		case "trending":
			return "rating"
		}
		return "best_match"
	}
	switch sort {
	case "newest":
		return "-first_publish"
	case "trending":
		return "-likes_count_7_days"
	}
	return "-download_count_30_days"
}

func (p *Printables) Search(ctx context.Context, q Query) (Page, error) {
	const limit = 36
	var out struct {
		R struct {
			Items      []pItem `json:"items"`
			Cursor     string  `json:"cursor"`
			TotalCount int     `json:"totalCount"`
		} `json:"r"`
	}
	var err error
	search := strings.TrimSpace(q.Text) != ""
	if search {
		// searchPrints2 pages by offset and has totalCount (checked live).
		err = p.gql(ctx, `query Search($q: String!, $limit: Int, $offset: Int, $ordering: SearchChoicesEnum) {
			r: searchPrints2(query: $q, limit: $limit, offset: $offset, ordering: $ordering) { totalCount items { `+printablesItemFields+` } } }`,
			map[string]any{"q": strings.TrimSpace(q.Text), "limit": limit, "offset": q.Page * limit, "ordering": printablesOrdering(q.Sort, true)}, &out)
	} else {
		err = p.gql(ctx, `query Browse($limit: Int!, $cursor: String, $ordering: String) {
			r: morePrints(limit: $limit, cursor: $cursor, ordering: $ordering) { cursor items { `+printablesItemFields+` } } }`,
			map[string]any{"limit": limit, "cursor": nilIfEmpty(q.Cursor), "ordering": printablesOrdering(q.Sort, false)}, &out)
	}
	if err != nil {
		return Page{}, err
	}
	pg := Page{Items: []Card{}, Cursor: out.R.Cursor}
	if search {
		pg.More = (q.Page+1)*limit < out.R.TotalCount
	} else {
		pg.More = out.R.Cursor != "" && len(out.R.Items) > 0
	}
	for _, it := range out.R.Items {
		pg.Items = append(pg.Items, p.card(it))
	}
	return pg, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (p *Printables) Model(ctx context.Context, id string) (Model, error) {
	var out struct {
		Print *struct {
			pItem
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Images      []struct {
				FilePath string `json:"filePath"`
			} `json:"images"`
			Tags []struct {
				Name string `json:"name"`
			} `json:"tags"`
			License *struct {
				Name         string `json:"name"`
				Abbreviation string `json:"abbreviation"`
			} `json:"license"`
			Category *struct {
				Path []struct {
					Name string `json:"name"`
				} `json:"path"`
			} `json:"category"`
			Stls       []pFile `json:"stls"`
			Gcodes     []pFile `json:"gcodes"`
			OtherFiles []pFile `json:"otherFiles"`
		} `json:"print"`
	}
	err := p.gql(ctx, `query Model($id: ID!) { print(id: $id) { `+printablesItemFields+` summary description
		images { filePath } tags { name } license { name abbreviation } category { path { name } }
		stls { id name fileSize } gcodes { id name fileSize } otherFiles { id name fileSize } } }`, map[string]any{"id": id}, &out)
	if err != nil {
		return Model{}, err
	}
	if out.Print == nil {
		return Model{}, fmt.Errorf("model not found on Printables")
	}
	d := out.Print
	m := Model{Card: p.card(d.pItem), Description: plain(firstNonEmpty(d.Description, d.Summary)), Images: []string{}, Tags: []string{}, Files: []File{}}
	for _, im := range d.Images {
		if im.FilePath != "" {
			m.Images = append(m.Images, printablesMedia+strings.TrimPrefix(im.FilePath, "/"))
		}
	}
	if len(m.Images) == 0 && m.Thumb != "" {
		m.Images = append(m.Images, m.Thumb)
	}
	for _, t := range d.Tags {
		m.Tags = append(m.Tags, t.Name)
	}
	if d.License != nil {
		m.License = firstNonEmpty(d.License.Abbreviation, d.License.Name)
	}
	if d.Category != nil {
		var parts []string
		for _, c := range d.Category.Path {
			parts = append(parts, c.Name)
		}
		m.Category = strings.Join(parts, "/")
	}
	add := func(list []pFile, kind string) {
		for _, f := range list {
			m.Files = append(m.Files, File{ID: kind + ":" + f.ID, Name: f.Name, Size: f.FileSize, Kind: FileKind(f.Name)})
		}
	}
	add(d.Stls, "stl")
	add(d.OtherFiles, "other")
	add(d.Gcodes, "gcode")
	return m, nil
}

type pFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	FileSize int64  `json:"fileSize"`
}

func (p *Printables) Download(ctx context.Context, m Model, f File) (io.ReadCloser, error) {
	kind, id, _ := strings.Cut(f.ID, ":")
	try := func(fileType string) (string, error) {
		var out struct {
			R struct {
				OK     bool `json:"ok"`
				Output *struct {
					Link string `json:"link"`
				} `json:"output"`
				Errors []struct {
					Messages []string `json:"messages"`
				} `json:"errors"`
			} `json:"r"`
		}
		err := p.gql(ctx, `mutation($id: ID!, $printId: ID!, $source: DownloadSourceEnum!, $fileType: DownloadFileTypeEnum!) {
			r: getDownloadLink(id: $id, printId: $printId, source: $source, fileType: $fileType) { ok output { link } errors { messages } } }`,
			map[string]any{"id": id, "printId": m.ID, "source": "model_detail", "fileType": fileType}, &out)
		if err != nil {
			return "", err
		}
		if !out.R.OK || out.R.Output == nil || out.R.Output.Link == "" {
			msg := "no download link"
			if len(out.R.Errors) > 0 && len(out.R.Errors[0].Messages) > 0 {
				msg = out.R.Errors[0].Messages[0]
			}
			return "", fmt.Errorf("Printables: %s", msg)
		}
		return out.R.Output.Link, nil
	}
	types := map[string][]string{"stl": {"stl"}, "gcode": {"gcode"}, "other": {"other", "stl"}}[kind]
	if types == nil {
		types = []string{"stl"}
	}
	var link string
	var err error
	for _, t := range types {
		if link, err = try(t); err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	return fetch(ctx, link, nil)
}

// Probe runs a raw GraphQL query and returns Printables' whole answer
// (used to check the API when Printables changes it; introspection is off).
func (p *Printables) Probe(ctx context.Context, query string, vars map[string]any) (any, error) {
	var out any
	err := doJSON(ctx, "POST", printablesAPI, map[string]any{"query": query, "variables": vars},
		map[string]string{"Origin": "https://www.printables.com", "Referer": "https://www.printables.com/"}, &out)
	return out, err
}

// Schema lists the query fields and their arguments (to check the API when
// Printables changes it).
func (p *Printables) Schema(ctx context.Context) (any, error) {
	var out any
	err := p.gql(ctx, `{ __schema { queryType { fields { name args { name type { name kind ofType { name kind ofType { name } } } } } } }
		so: __type(name: "SearchPrintOrderingEnum") { enumValues { name } }
		pt: __type(name: "PrintType") { fields { name } } }`, nil, &out)
	return out, err
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func itoa(i int) string { return strconv.Itoa(i) }
