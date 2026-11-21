package sites

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Cults3D: official GraphQL API (https://cults3d.com/graphql) with HTTP
// Basic auth (your Cults nickname + an API key from cults3d.com/en/api/keys).
// Cults' API gives details and pictures but, by Cults' own rules, never the
// files: download them on cults3d.com and drop the zip on the import window,
// and PrintHarbor files them in your library with the model's details.
type Cults struct{ User, Key string }

const cultsAPI = "https://cults3d.com/graphql"

func (c *Cults) Info() Info {
	return Info{ID: "cults", Name: "Cults3D", URL: "https://cults3d.com", NeedsKey: true, Ready: c.User != "" && c.Key != "", CanDownload: false,
		KeyHelp: "Sign in at cults3d.com, create an API key at cults3d.com/en/api/keys, and enter your Cults nickname and the key in Settings → Model sites.",
		Note:    "Cults3D doesn't hand files to apps: open the model in PrintHarbor's browser and click Download there, and it's filed in your library automatically."}
}

func (c *Cults) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	if c.User == "" || c.Key == "" {
		return fmt.Errorf("add your Cults3D nickname and API key in Settings → Model sites")
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", cultsAPI, strings.NewReader(mustJSON(map[string]any{"query": query, "variables": vars})))
	req.SetBasicAuth(c.User, c.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("Cults3D refused the nickname or API key")
	}
	var env struct {
		Data   jsonRaw `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := unmarshal(b, &env); err != nil {
		return fmt.Errorf("Cults3D: %s", resp.Status)
	}
	if len(env.Errors) > 0 && len(env.Data) < 8 {
		return fmt.Errorf("Cults3D: %s", env.Errors[0].Message)
	}
	return unmarshal(env.Data, out)
}

const cultsFields = `identifier name url illustrationImageUrl likesCount downloadsCount creator { nick } price(currency: USD) { cents }`

type cItem struct {
	Identifier           string `json:"identifier"`
	Name                 string `json:"name"`
	URL                  string `json:"url"`
	IllustrationImageURL string `json:"illustrationImageUrl"`
	LikesCount           int    `json:"likesCount"`
	DownloadsCount       int    `json:"downloadsCount"`
	Creator              *struct {
		Nick string `json:"nick"`
	} `json:"creator"`
	Price *struct {
		Cents int `json:"cents"`
	} `json:"price"`
}

func (c *Cults) card(x cItem) Card {
	k := Card{Site: "cults", ID: x.Identifier, Title: x.Name, URL: x.URL, Thumb: x.IllustrationImageURL, Likes: x.LikesCount, Downloads: x.DownloadsCount,
		Free: x.Price == nil || x.Price.Cents == 0}
	if x.Creator != nil {
		k.Author = x.Creator.Nick
	}
	return k
}

func (c *Cults) Search(ctx context.Context, q Query) (Page, error) {
	const limit = 30
	sort := map[string]string{"newest": "BY_PUBLICATION", "trending": "BY_SALES", "popular": "BY_DOWNLOADS"}[q.Sort]
	if sort == "" {
		sort = "BY_DOWNLOADS"
	}
	var out struct {
		R struct {
			Total   int     `json:"total"`
			Results []cItem `json:"results"`
		} `json:"r"`
	}
	err := c.gql(ctx, `query($q: String!, $limit: Int, $offset: Int, $sort: CreationSearchSort) {
		r: creationsSearchBatch(query: $q, limit: $limit, offset: $offset, sort: $sort) { total results { `+cultsFields+` } } }`,
		map[string]any{"q": strings.TrimSpace(q.Text), "limit": limit, "offset": q.Page * limit, "sort": sort}, &out)
	if err != nil {
		return Page{}, err
	}
	pg := Page{Items: []Card{}, More: (q.Page+1)*limit < out.R.Total}
	for _, x := range out.R.Results {
		pg.Items = append(pg.Items, c.card(x))
	}
	return pg, nil
}

func (c *Cults) Model(ctx context.Context, id string) (Model, error) {
	var out struct {
		R *struct {
			cItem
			Description   string   `json:"description"`
			Tags          []string `json:"tags"`
			Illustrations []struct {
				ImageURL string `json:"imageUrl"`
			} `json:"illustrations"`
			License *struct {
				Name string `json:"name"`
			} `json:"license"`
			Category *struct {
				Name string `json:"name"`
			} `json:"category"`
		} `json:"r"`
	}
	err := c.gql(ctx, `query($id: String!) { r: creation(slug: $id) { `+cultsFields+` description tags illustrations { imageUrl }
		license { name } category { name } } }`, map[string]any{"id": id}, &out)
	if err != nil {
		return Model{}, err
	}
	if out.R == nil {
		return Model{}, fmt.Errorf("model not found on Cults3D")
	}
	d := out.R
	m := Model{Card: c.card(d.cItem), Description: plain(d.Description), Tags: append([]string{}, d.Tags...), Images: []string{}, Files: []File{},
		DownloadNote: c.Info().Note}
	for _, im := range d.Illustrations {
		if im.ImageURL != "" {
			m.Images = append(m.Images, im.ImageURL)
		}
	}
	if len(m.Images) == 0 && m.Thumb != "" {
		m.Images = append(m.Images, m.Thumb)
	}
	if d.License != nil {
		m.License = d.License.Name
	}
	if d.Category != nil {
		m.Category = d.Category.Name
	}
	return m, nil
}

func (c *Cults) Download(ctx context.Context, m Model, f File) (io.ReadCloser, error) {
	return nil, fmt.Errorf("Cults3D doesn't hand files to apps: open the model in PrintHarbor's browser and click Download there")
}
