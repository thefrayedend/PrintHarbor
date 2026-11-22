package api

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thefrayedend/printharbor/internal/config"
	"github.com/thefrayedend/printharbor/internal/library"
)

func libApp(t *testing.T, base string, extra ...string) *App {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(base, "data")
	cfg.OrcaPath, cfg.OrcaProfilesDir, cfg.OrcaUserPresetsDir = "/none", filepath.Join(base, "none"), ""
	cfg.ModelDir = filepath.Join(base, "Models")
	cfg.LibraryFolders = extra
	cfg.ProfilesChannel = "off"
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Library.SetFolders(a.cfg().ScanFolders())
	a.Library.Rescan()
	return a
}

func put(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findItem(t *testing.T, a *App, title string) library.Item {
	t.Helper()
	items, _ := a.Library.Search(library.Query{Limit: 1000})
	for _, it := range items {
		if it.Title == title {
			return it
		}
	}
	all, _ := a.Library.Search(library.Query{Limit: 1000})
	var names []string
	for _, it := range all {
		names = append(names, it.Title)
	}
	t.Fatalf("no model %q in %v", title, names)
	return library.Item{}
}

// One zip carries the whole library to another PrintHarbor on another
// computer (other paths): files, the site details, and everything changed in
// PrintHarbor come back on the models.
func TestLibraryBackupMovesToAnotherCopy(t *testing.T) {
	src := t.TempDir()
	md := filepath.Join(src, "Models")
	put(t, filepath.Join(md, "Acme Designs", "Widget Holder", "holder.3mf"), strings.Repeat("3mf", 1000))
	put(t, filepath.Join(md, "Acme Designs", "Widget Holder", "images", "01.png"), "png")
	put(t, filepath.Join(md, "Acme Designs", "Widget Holder", library.SidecarName), `{"title":"Widget Holder v2","source_url":"https://example.com/m/1","license":"CC-BY","tags":["holder"]}`)
	put(t, filepath.Join(md, "Prusa", "Benchy", "benchy.stl"), strings.Repeat("solid ", 500))
	put(t, filepath.Join(md, "folder.png"), "cover")
	other := filepath.Join(src, "Old stuff")
	put(t, filepath.Join(other, "Vise", "vise.stl"), "solid vise")
	a := libApp(t, src, other)

	rx := findItem(t, a, "Widget Holder v2")
	notes, fav, cat := "print in PETG", true, "Printer parts"
	if _, err := a.Library.Update(rx.ID, library.Meta{Notes: &notes, Favorite: &fav, Category: &cat}); err != nil {
		t.Fatal(err)
	}
	ph := filepath.Join(src, "my.jpg")
	put(t, ph, "my photo")
	if _, err := a.Library.AddPhotos(rx.ID, []library.Incoming{{Name: "my.jpg", Tmp: ph}}); err != nil {
		t.Fatal(err)
	}
	cover := "added:my.jpg"
	a.Library.Update(rx.ID, library.Meta{Cover: &cover})
	a.Library.MarkPrinted(rx.ID)
	a.Library.MarkPrinted(rx.ID)
	up := filepath.Join(src, "up.stl")
	put(t, up, "solid uploaded")
	title := "My uploaded part"
	if _, err := a.Library.Create(library.Meta{Title: &title}, []library.Incoming{{Name: "part.stl", Tmp: up}}, nil); err != nil {
		t.Fatal(err)
	}
	vise := findItem(t, a, "Vise")
	a.Library.Delete(vise.ID) // hidden: comes back hidden

	// the timer is off by default: nothing happens on its own
	a.libBackupIfDue(timeNow())
	if _, err := os.Stat(a.libBackupPath()); err == nil {
		t.Fatal("a backup was made without being asked")
	}
	st, err := a.LibraryBackup()
	if err != nil {
		t.Fatal(err)
	}
	if st.Models != 4 || st.Bytes == 0 {
		t.Fatalf("backup: %+v", st)
	}
	// a second one replaces the first: still one file
	if _, err := a.LibraryBackup(); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(filepath.Dir(a.libBackupPath()))
	n := 0
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "printharbor-library") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d library backup files, want exactly 1", n)
	}
	// it opens as a plain zip, with the folders as they are on disk
	zr, err := zip.OpenReader(a.libBackupPath())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	zr.Close()
	for _, want := range []string{"Models/Acme Designs/Widget Holder/holder.3mf", "Models/Acme Designs/Widget Holder/" + library.SidecarName, "Models/folder.png", "Old stuff/Vise/vise.stl", library.ArchiveManifestName} {
		if !names[want] {
			t.Errorf("%s is not in the zip: %v", want, names)
		}
	}

	// another computer: different folders, empty library
	dst := t.TempDir()
	b := libApp(t, dst)
	res, err := b.RestoreLibrary(a.libBackupPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) > 0 || res.Uploaded != 1 || res.Models != 3 {
		t.Fatalf("restore: %+v", res)
	}
	got := findItem(t, b, "Widget Holder v2")
	if got.Notes != notes || !got.Favorite || got.Category != cat || got.PrintCount != 2 || got.SourceURL != "https://example.com/m/1" || got.Cover != cover {
		t.Errorf("details not restored: %+v", got)
	}
	if len(got.AddedPhotos) != 1 {
		t.Errorf("added photo not restored: %v", got.AddedPhotos)
	} else if rc, _, err := b.Library.Open(got.ID, "added", got.AddedPhotos[0]); err != nil {
		t.Errorf("added photo can't be opened: %v", err)
	} else {
		rc.Close()
	}
	if !strings.HasPrefix(got.Path, filepath.Join(dst, "Models")) {
		t.Errorf("restored into %s", got.Path)
	}
	upl := findItem(t, b, title)
	if rc, _, err := b.Library.Open(upl.ID, "file", "part.stl"); err != nil {
		t.Errorf("uploaded model not restored: %v", err)
	} else {
		rc.Close()
	}
	all, _ := b.Library.Search(library.Query{Limit: 100})
	for _, it := range all {
		if it.Title == "Vise" {
			t.Error("a hidden model came back visible")
		}
	}
	if !strings.Contains(strings.Join(b.cfg().LibraryFolders, "|"), "Old stuff") {
		t.Errorf("the other library folder was not added: %v", b.cfg().LibraryFolders)
	}
	if _, err := os.Stat(filepath.Join(dst, "Models", "folder.png")); err != nil {
		t.Error("loose file in the models folder was not restored")
	}

	// restoring again changes nothing and loses nothing
	res2, err := b.RestoreLibrary(a.libBackupPath())
	if err != nil || res2.Written != 0 || res2.Renamed != 0 {
		t.Fatalf("second restore: %+v %v", res2, err)
	}
	// a different file with the same name is kept, the backup's goes beside it
	put(t, filepath.Join(dst, "Models", "Prusa", "Benchy", "benchy.stl"), "my own changed benchy")
	res3, _ := b.RestoreLibrary(a.libBackupPath())
	if res3.Renamed != 1 {
		t.Errorf("conflict: %+v", res3)
	}
	if bb, _ := os.ReadFile(filepath.Join(dst, "Models", "Prusa", "Benchy", "benchy.stl")); string(bb) != "my own changed benchy" {
		t.Error("restore overwrote a file")
	}

	// the timer: on, nothing changed -> no new backup; a change -> a new one
	c := a.cfg()
	c.LibraryBackupHours = 1
	a.setCfg(c)
	before := a.libBackupState().Made
	a.libBackupIfDue(before.Add(2 * hour))
	if !a.libBackupState().Made.Equal(before) {
		t.Error("the timer made a backup though nothing changed")
	}
	put(t, filepath.Join(md, "Prusa", "Benchy", "benchy2.stl"), "solid two")
	a.libBackupIfDue(before.Add(2 * hour))
	if a.libBackupState().Made.Equal(before) {
		t.Error("the timer did not make a backup after a change")
	}
}

// A zip that is not a library backup, or that tries to write outside the
// models folder, is refused.
func TestLibraryRestoreRefusesBadZips(t *testing.T) {
	dst := t.TempDir()
	b := libApp(t, dst)
	p := filepath.Join(dst, "x.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("Models/../../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	f.Close()
	if _, err := b.RestoreLibrary(p); err == nil {
		t.Error("a zip without the backup's list was accepted")
	}
	f, _ = os.Create(p)
	zw = zip.NewWriter(f)
	w, _ = zw.Create(library.ArchiveManifestName)
	w.Write([]byte(`{"format":1,"roots":[{"name":"Models"}],"models":[]}`))
	w, _ = zw.Create("Models/../../evil.txt")
	w.Write([]byte("x"))
	zw.Close()
	f.Close()
	res, err := b.RestoreLibrary(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "evil.txt")); err == nil {
		t.Fatal("a file was written outside the models folder")
	}
	if len(res.Errors) == 0 {
		t.Error("the unsafe name was not reported")
	}
}

const hour = 60 * 60 * 1e9

func timeNow() time.Time { return time.Now() }
