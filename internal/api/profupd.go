package api

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thefrayedend/printharbor/internal/profiles"
)

// Keeping the printer, quality and filament profiles up to date.
//
// OrcaSlicer 2.5 stopped shipping its stock profiles as .json files (it packs
// them into .opc files PrintHarbor cannot read), so PrintHarbor gets them
// itself: a "profile pack" (a zip of OrcaSlicer's own profile .json files,
// built from OrcaSlicer's source by a workflow in PrintHarbor's repository) is
// downloaded into <data>/orca-profiles/current and read from there. It is
// looked for again every day, so new printers and fixed profiles arrive.
//
// Nothing a person relies on is lost by an update:
//
//   - Whenever an update changes what a profile comes out as (its own file or
//     any profile it inherits from), the settings it had before are saved, one
//     copy per profile ("previous"). Revert puts them back.
//   - A locked profile is pinned to exact settings and no update touches it.
//     Reverting locks. Unlocking returns it to the current stock profile.
//   - A profile that disappears from the pack is kept as it was.
//
// "Profile" here means what you pick in a list: a printer, a quality preset or
// a filament, with everything it inherits already worked in. That is what is
// compared, saved and pinned, so a fix made to a shared parent file counts as
// a change to every profile that uses it, and your own presets (which inherit
// from stock ones) are covered too.

// DefaultProfilesURL is where the profile packs are published.
const DefaultProfilesURL = "https://github.com/thefrayedend/PrintHarbor/releases/download/orca-profiles"

// profClient fetches the pack; tests replace it.
var profClient = func() *http.Client { return publicClient(5 * time.Minute) }

// ProfChange is one line of the "what changed" list.
type ProfChange struct {
	Type   string    `json:"type"`
	Name   string    `json:"name"`
	Vendor string    `json:"vendor,omitempty"`
	Source string    `json:"source,omitempty"` // system | orca-user | harbor
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // updated | added | removed
}

type profState struct {
	PackID    string       `json:"pack_id,omitempty"` // which pack is installed
	Ref       string       `json:"ref,omitempty"`     // what it was built from (e.g. "main @ 1478735")
	Built     string       `json:"built,omitempty"`
	FetchedAt time.Time    `json:"fetched_at"`
	CheckedAt time.Time    `json:"checked_at"`
	LastError string       `json:"last_error,omitempty"`
	History   []ProfChange `json:"history,omitempty"` // newest first
}

// savedProfile is a pinned ("frozen"), previous or kept profile on disk.
type savedProfile struct {
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Vendor   string         `json:"vendor,omitempty"`
	Source   string         `json:"source,omitempty"`
	SavedAt  time.Time      `json:"saved_at"`
	Settings map[string]any `json:"settings"`
}

// A real pack has thousands of files from dozens of brands; anything much
// smaller is a broken download. Tests lower these.
var packMinFiles, packMinBrands = 200, 5

var profMu sync.Mutex // one update / lock / revert at a time

func (a *App) profDir(parts ...string) string {
	return filepath.Join(append([]string{a.Store.Dir(), "orca-profiles"}, parts...)...)
}

func profKey(typ, name string) string { return typ + "\x00" + name }

// profFile is where a saved profile of a kind ("frozen", "prev", "kept") lives.
// The name is hashed: profile names hold characters file names can't.
func (a *App) profFile(kind, typ, name string) string {
	h := sha256.Sum256([]byte(name))
	return a.profDir(kind, typ, hex.EncodeToString(h[:10])+".json")
}

func readSaved(path string) (savedProfile, bool) {
	var s savedProfile
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.Name == "" || s.Type == "" || s.Settings == nil {
		return s, false
	}
	return s, true
}

func writeSaved(path string, s savedProfile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// listSaved reads every saved profile of a kind.
func (a *App) listSaved(kind string) []savedProfile {
	var out []savedProfile
	_ = filepath.WalkDir(a.profDir(kind), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
			if s, ok := readSaved(path); ok {
				out = append(out, s)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (a *App) profChannel() string {
	switch c := strings.ToLower(strings.TrimSpace(a.cfg().ProfilesChannel)); c {
	case "off":
		return "off"
	default:
		return "newest"
	}
}

// systemProfilesDir is where the stock profiles are read from: the downloaded
// pack once there is one, otherwise OrcaSlicer's own folder.
func (a *App) systemProfilesDir() string {
	if a.profChannel() != "off" && dirHasProfiles(a.profDir("current")) {
		return a.profDir("current")
	}
	return a.cfg().OrcaProfilesDir
}

func dirHasProfiles(dir string) bool {
	es, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	n := 0
	for _, e := range es {
		if e.IsDir() {
			n++
		}
	}
	return n >= packMinBrands
}

// applySavedProfiles adds the kept profiles and pins the locked ones in a
// freshly loaded library.
func (a *App) applySavedProfiles(l *profiles.Library) {
	for _, s := range a.listSaved("kept") {
		if l.Get(s.Type, s.Name) == nil {
			raw := map[string]any{}
			for k, v := range s.Settings {
				raw[k] = v
			}
			delete(raw, "inherits") // the saved settings are complete
			raw["instantiation"] = "true"
			l.Add(raw, s.Vendor, "system", "")
		}
	}
	for _, s := range a.listSaved("frozen") {
		if l.Get(s.Type, s.Name) != nil {
			l.Freeze(s.Type, s.Name, s.Settings)
		}
	}
}

func (a *App) loadProfState() profState {
	var st profState
	if b, err := os.ReadFile(a.profDir("state.json")); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (a *App) saveProfState(st profState) {
	if len(st.History) > 400 {
		st.History = st.History[:400]
	}
	_ = os.MkdirAll(a.profDir(), 0o755)
	if b, err := json.MarshalIndent(st, "", " "); err == nil {
		_ = os.WriteFile(a.profDir("state.json"), b, 0o644)
	}
}

func settingsSum(m map[string]any) string {
	b, _ := json.Marshal(m) // map keys come out sorted: the same settings give the same sum
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// stockSums fingerprints what every selectable profile comes out as, without
// the pins.
func stockSums(l *profiles.Library) map[string]string {
	out := map[string]string{}
	for _, p := range l.All() {
		if r, err := l.ResolveStock(p.Type, p.Name); err == nil {
			out[profKey(p.Type, p.Name)] = settingsSum(r)
		}
	}
	return out
}

// extractPack unpacks the .json files of a profile pack into dir (emptied
// first) and reports how many there were.
func extractPack(zipPath, dir string) (int, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	n := 0
	vendors := map[string]bool{}
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") || strings.Contains(name, ":") {
			return 0, fmt.Errorf("the profile pack has a file with a bad name: %q", f.Name)
		}
		if f.UncompressedSize64 > 8<<20 {
			return 0, fmt.Errorf("the profile pack has a file that is too big: %q", f.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		rc, err := f.Open()
		if err != nil {
			return 0, err
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return 0, err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 8<<20))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return 0, err
		}
		if i := strings.IndexByte(name, '/'); i > 0 {
			vendors[name[:i]] = true
		}
		n++
	}
	if n < packMinFiles || len(vendors) < packMinBrands {
		return n, fmt.Errorf("the profile pack looks incomplete (%d files, %d brands), so it was not used", n, len(vendors))
	}
	return n, nil
}

// installPack swaps a downloaded pack in and records what it changed.
func (a *App) installPack(zipPath, id, ref, built string) (changed int, err error) {
	next := a.profDir("next")
	if _, err := extractPack(zipPath, next); err != nil {
		os.RemoveAll(next)
		return 0, err
	}
	old := a.Lib()
	oldSums := stockSums(old)

	cur, gone := a.profDir("current"), a.profDir("gone")
	os.RemoveAll(gone)
	if _, serr := os.Stat(cur); serr == nil {
		if err := os.Rename(cur, gone); err != nil {
			os.RemoveAll(next)
			return 0, fmt.Errorf("could not replace the profiles: %w", err)
		}
	}
	if err := os.Rename(next, cur); err != nil {
		_ = os.Rename(gone, cur) // put the old ones back
		return 0, fmt.Errorf("could not put the new profiles in place: %w", err)
	}
	os.RemoveAll(gone)

	// Profiles that are in the old set but not the new one are kept as they
	// were; write them before the reload so the new library has them.
	now := time.Now()
	newFiles := profiles.NewLibrary()
	_ = newFiles.LoadDir(cur, "system")
	var changes []ProfChange
	for _, p := range old.All() {
		if p.Source != "system" || newFiles.Get(p.Type, p.Name) != nil {
			continue
		}
		if _, ok := readSaved(a.profFile("kept", p.Type, p.Name)); ok {
			continue // already kept from an earlier update
		}
		if r, rerr := old.ResolveStock(p.Type, p.Name); rerr == nil {
			_ = writeSaved(a.profFile("kept", p.Type, p.Name), savedProfile{Type: p.Type, Name: p.Name, Vendor: p.Vendor, Source: p.Source, SavedAt: now, Settings: r})
			changes = append(changes, ProfChange{Type: p.Type, Name: p.Name, Vendor: p.Vendor, Source: p.Source, At: now, Kind: "removed"})
		}
	}

	l := a.ReloadProfiles()
	newSums := stockSums(l)
	st := a.loadProfState()
	first := st.PackID == ""
	for _, p := range l.All() {
		k := profKey(p.Type, p.Name)
		was, had := oldSums[k]
		switch {
		case !had:
			if !first { // the first pack "adds" thousands; that is not news
				changes = append(changes, ProfChange{Type: p.Type, Name: p.Name, Vendor: p.Vendor, Source: p.Source, At: now, Kind: "added"})
			}
		case was != newSums[k]:
			if r, rerr := old.ResolveStock(p.Type, p.Name); rerr == nil {
				_ = writeSaved(a.profFile("prev", p.Type, p.Name), savedProfile{Type: p.Type, Name: p.Name, Vendor: p.Vendor, Source: p.Source, SavedAt: now, Settings: r})
			}
			changes = append(changes, ProfChange{Type: p.Type, Name: p.Name, Vendor: p.Vendor, Source: p.Source, At: now, Kind: "updated"})
			changed++
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { // your own presets first, then by name
		if (changes[i].Source != "system") != (changes[j].Source != "system") {
			return changes[i].Source != "system"
		}
		return changes[i].Name < changes[j].Name
	})
	st.PackID, st.Ref, st.Built, st.FetchedAt, st.CheckedAt, st.LastError = id, ref, built, now, now, ""
	st.History = append(changes, st.History...)
	a.saveProfState(st)
	return changed, nil
}

type packManifest struct {
	ID     string `json:"id"`     // changes when the profiles change
	Ref    string `json:"ref"`    // e.g. "main @ 1478735"
	Built  string `json:"built"`  // when the pack was made
	Zip    string `json:"zip"`    // file name next to the manifest
	SHA256 string `json:"sha256"` // of the zip
	Files  int    `json:"files"`
}

// CheckProfiles looks for a newer profile pack and installs it. It returns a
// sentence for the Settings page.
func (a *App) CheckProfiles(ctx context.Context, force bool) (string, error) {
	if a.profChannel() == "off" {
		return "Profile updates are switched off.", nil
	}
	profMu.Lock()
	defer profMu.Unlock()
	base := strings.TrimRight(strings.TrimSpace(a.cfg().ProfilesURL), "/")
	if base == "" {
		base = DefaultProfilesURL
	}
	fail := func(err error) (string, error) {
		st := a.loadProfState()
		st.CheckedAt, st.LastError = time.Now(), err.Error()
		a.saveProfState(st)
		return "", err
	}
	c := profClient()
	var m packManifest
	if err := getInto(ctx, c, base+"/orca-profiles-newest.json", func(r io.Reader) error {
		return json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m)
	}); err != nil {
		return fail(fmt.Errorf("could not look for new profiles: %w", err))
	}
	if m.ID == "" || m.Zip == "" || strings.ContainsAny(m.Zip, "/\\") {
		return fail(fmt.Errorf("the profile pack's description was not understood"))
	}
	st := a.loadProfState()
	if !force && m.ID == st.PackID && dirHasProfiles(a.profDir("current")) {
		st.CheckedAt, st.LastError = time.Now(), ""
		a.saveProfState(st)
		return "The profiles are up to date.", nil
	}
	_ = os.MkdirAll(a.profDir(), 0o755)
	tmp := a.profDir("pack.zip")
	defer os.Remove(tmp)
	h := sha256.New()
	if err := getInto(ctx, c, base+"/"+m.Zip, func(r io.Reader) error {
		f, err := os.Create(tmp)
		if err != nil {
			return err
		}
		_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(r, 300<<20))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	}); err != nil {
		return fail(fmt.Errorf("could not download the new profiles: %w", err))
	}
	if got := hex.EncodeToString(h.Sum(nil)); m.SHA256 != "" && !strings.EqualFold(got, m.SHA256) {
		return fail(fmt.Errorf("the downloaded profiles did not match their checksum, so they were not used"))
	}
	first := st.PackID == ""
	n, err := a.installPack(tmp, m.ID, m.Ref, m.Built)
	if err != nil {
		return fail(err)
	}
	log.Printf("Profiles: installed pack %s (%s): %d profile(s) changed", shortID(m.ID), m.Ref, n)
	switch {
	case first:
		return fmt.Sprintf("Profiles downloaded (%d files). %d profile(s) differ from the ones used until now; their old settings were kept.", m.Files, n), nil
	case n == 0:
		return "New profiles downloaded; none of your profiles changed.", nil
	}
	return fmt.Sprintf("New profiles downloaded: %d profile(s) changed. Their previous settings were kept.", n), nil
}

func shortID(id string) string {
	if len(id) > 10 {
		return id[:10]
	}
	return id
}

func getInto(ctx context.Context, c *http.Client, url string, use func(io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "PrintHarbor/"+Version)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("the server answered %d", resp.StatusCode)
	}
	return use(resp.Body)
}

// profLoop looks for new profiles shortly after start and then once a day.
func (a *App) profLoop(ctx context.Context) {
	wait := 45 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		if a.profChannel() == "off" {
			continue
		}
		st := a.loadProfState()
		if time.Since(st.CheckedAt) < 23*time.Hour && dirHasProfiles(a.profDir("current")) {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Minute)
		if msg, err := a.CheckProfiles(c, false); err != nil {
			log.Printf("Profiles: %v", err)
		} else {
			log.Printf("Profiles: %s", msg)
		}
		cancel()
	}
}

// LockProfile pins a profile to the settings it has right now.
func (a *App) LockProfile(typ, name string) error {
	profMu.Lock()
	defer profMu.Unlock()
	l := a.Lib()
	p := l.Get(typ, name)
	if p == nil {
		return fmt.Errorf("%s profile %q not found", typ, name)
	}
	r, err := l.Resolve(typ, name)
	if err != nil {
		return err
	}
	if err := writeSaved(a.profFile("frozen", typ, name), savedProfile{Type: typ, Name: name, Vendor: p.Vendor, Source: p.Source, SavedAt: time.Now(), Settings: r}); err != nil {
		return err
	}
	a.ReloadProfiles()
	return nil
}

// UnlockProfile lets a profile follow the stock profiles again.
func (a *App) UnlockProfile(typ, name string) error {
	profMu.Lock()
	defer profMu.Unlock()
	if err := os.Remove(a.profFile("frozen", typ, name)); err != nil && !os.IsNotExist(err) {
		return err
	}
	a.ReloadProfiles()
	return nil
}

// RevertProfile puts a profile's previous settings back and locks it there, so
// the next update doesn't undo the revert. What it had until now becomes the
// "previous" copy, so reverting twice returns to where you started.
func (a *App) RevertProfile(typ, name string) error {
	profMu.Lock()
	defer profMu.Unlock()
	prev, ok := readSaved(a.profFile("prev", typ, name))
	if !ok {
		return fmt.Errorf("there are no earlier settings saved for %q", name)
	}
	l := a.Lib()
	p := l.Get(typ, name)
	if p == nil {
		return fmt.Errorf("%s profile %q not found", typ, name)
	}
	cur, err := l.Resolve(typ, name)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := writeSaved(a.profFile("frozen", typ, name), savedProfile{Type: typ, Name: name, Vendor: p.Vendor, Source: p.Source, SavedAt: now, Settings: prev.Settings}); err != nil {
		return err
	}
	_ = writeSaved(a.profFile("prev", typ, name), savedProfile{Type: typ, Name: name, Vendor: p.Vendor, Source: p.Source, SavedAt: now, Settings: cur})
	a.ReloadProfiles()
	return nil
}

// ProfileUpdates is what the Settings page shows.
func (a *App) ProfileUpdates() map[string]any {
	st := a.loadProfState()
	l := a.Lib()
	type row struct {
		ProfChange
		Locked    bool `json:"locked"`
		CanRevert bool `json:"can_revert"`
	}
	type lock struct {
		Type    string    `json:"type"`
		Name    string    `json:"name"`
		Vendor  string    `json:"vendor,omitempty"`
		Source  string    `json:"source,omitempty"`
		Since   time.Time `json:"since"`
		Differs bool      `json:"differs"` // the stock profile is different now: an update is waiting
		Revert  bool      `json:"can_revert"`
	}
	hasPrev := func(typ, name string) bool {
		_, err := os.Stat(a.profFile("prev", typ, name))
		return err == nil
	}
	rows := []row{}
	for i, c := range st.History {
		if i >= 150 {
			break
		}
		rows = append(rows, row{ProfChange: c, Locked: l.Frozen(c.Type, c.Name), CanRevert: c.Kind == "updated" && hasPrev(c.Type, c.Name)})
	}
	locks := []lock{}
	for _, s := range a.listSaved("frozen") {
		lk := lock{Type: s.Type, Name: s.Name, Vendor: s.Vendor, Source: s.Source, Since: s.SavedAt, Revert: hasPrev(s.Type, s.Name)}
		if r, err := l.ResolveStock(s.Type, s.Name); err == nil {
			lk.Differs = settingsSum(r) != settingsSum(s.Settings)
		}
		locks = append(locks, lk)
	}
	using := "OrcaSlicer's own folder"
	if a.systemProfilesDir() == a.profDir("current") {
		using = "downloaded"
	}
	return map[string]any{
		"channel": a.profChannel(), "using": using, "ref": st.Ref, "built": st.Built, "pack": shortID(st.PackID),
		"fetched_at": st.FetchedAt, "checked_at": st.CheckedAt, "last_error": st.LastError,
		"changes": rows, "total_changes": len(st.History), "locked": locks,
		"counts": map[string]int{"machine": len(l.List("machine")), "process": len(l.List("process")), "filament": len(l.List("filament"))},
	}
}

func (a *App) profRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/profiles/updates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.ProfileUpdates())
	})
	mux.HandleFunc("POST /api/profiles/updates/check", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		msg, err := a.CheckProfiles(ctx, r.URL.Query().Get("force") == "1")
		if err != nil {
			fail(w, 502, err)
			return
		}
		writeJSON(w, 200, map[string]any{"message": msg, "updates": a.ProfileUpdates()})
	})
	// Find profiles by name, to lock one that isn't in the "what changed" list.
	mux.HandleFunc("GET /api/profiles/find", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		out := []map[string]any{}
		if len(q) >= 2 {
			l := a.Lib()
			for _, p := range l.All() {
				if strings.Contains(strings.ToLower(p.Name), q) {
					out = append(out, map[string]any{"type": p.Type, "name": p.Name, "vendor": p.Vendor, "source": p.Source, "locked": l.Frozen(p.Type, p.Name)})
					if len(out) >= 40 {
						break
					}
				}
			}
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/profiles/lock", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Type, Name, Do string // Do: lock | unlock | revert
		}
		if err := readJSON(r, &in); err != nil {
			fail(w, 400, err)
			return
		}
		var err error
		switch in.Do {
		case "lock":
			err = a.LockProfile(in.Type, in.Name)
		case "unlock":
			err = a.UnlockProfile(in.Type, in.Name)
		case "revert":
			err = a.RevertProfile(in.Type, in.Name)
		default:
			err = fmt.Errorf("do must be lock, unlock or revert")
		}
		if err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, a.ProfileUpdates())
	})
}
