package library

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, p, body string) {
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAndSearch(t *testing.T) {
	root := t.TempDir()
	// The library is laid out <designer>\<model>\…: every folder in a designer folder is one
	// model with everything under it (here a Printables download: files/ and images/).
	touch(t, filepath.Join(root, "Tools", "Quick Clamp", "files", "clamp_body.stl"), "solid x")
	touch(t, filepath.Join(root, "Tools", "Quick Clamp", "files", "jaws", "clamp_jaw.stl"), "solid x")
	touch(t, filepath.Join(root, "Tools", "Quick Clamp", "images", "photo1.jpg"), "jpg")
	touch(t, filepath.Join(root, "Tools", "Quick Clamp", "README.txt"), "A fast clamp for woodworking.")
	touch(t, filepath.Join(root, "Toys", "Dragon", "dragon.3mf"), "zip")
	// A model file lying loose in a designer folder is not a model.
	touch(t, filepath.Join(root, "Toys", "loose.stl"), "solid l")
	// A first-level folder with model files and no subfolders: a model with no designer.
	touch(t, filepath.Join(root, "Gear", "gear.stl"), "solid g")
	// A zip in the library folder itself.
	zf, _ := os.Create(filepath.Join(root, "Flexi Rex.zip"))
	zw := zip.NewWriter(zf)
	w, _ := zw.Create("Flexi Rex/rex.stl")
	w.Write([]byte("solid rex"))
	w, _ = zw.Create("Flexi Rex/rex.png")
	w.Write([]byte("png"))
	zw.Close()
	zf.Close()
	// Loose file at the top level, and a folder with only images (not a model).
	touch(t, filepath.Join(root, "benchy.stl"), "solid b")
	touch(t, filepath.Join(root, "Wallpapers", "a.jpg"), "jpg")

	l, err := Open(filepath.Join(t.TempDir(), "lib"), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	res := l.Rescan()
	if res.Added != 5 || len(res.Errors) != 0 {
		t.Fatalf("scan: %+v", res)
	}
	all, total := l.Search(Query{})
	if total != 5 {
		t.Fatalf("items: %+v", all)
	}
	got, _ := l.Search(Query{Text: "clamp wood"})
	if len(got) != 1 || got[0].Designer != "Tools" || len(got[0].Files) != 2 || len(got[0].Photos) != 1 || got[0].Title != "Quick Clamp" ||
		got[0].Files[1].Name != "files/jaws/clamp_jaw.stl" && got[0].Files[0].Name != "files/jaws/clamp_jaw.stl" {
		t.Fatalf("clamp: %+v", got)
	}
	if got, _ := l.Search(Query{Text: "dragon"}); len(got) != 1 || got[0].Designer != "Toys" {
		t.Fatalf("dragon: %+v", got)
	}
	if got, _ := l.Search(Query{Text: "loose"}); len(got) != 0 {
		t.Fatalf("loose file in a designer folder: %+v", got)
	}
	if got, _ := l.Search(Query{Text: "gear"}); len(got) != 1 || got[0].Designer != "" {
		t.Fatalf("gear: %+v", got)
	}
	if got, _ := l.Search(Query{Folder: filepath.Base(root) + "/Toys"}); len(got) != 1 {
		t.Fatalf("folder Toys: %+v", got)
	}
	rex, _ := l.Search(Query{Text: "rex"})
	if len(rex) != 1 || rex[0].Source != "zip" || rex[0].Files[0].Name != "Flexi Rex/rex.stl" {
		t.Fatalf("zip: %+v", rex)
	}
	// Reading and extracting from a zip.
	rc, _, err := l.Open(rex[0].ID, "file", "Flexi Rex/rex.stl")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "solid rex" {
		t.Fatal(string(b))
	}
	p, err := l.LocalPath(rex[0].ID, "Flexi Rex/rex.stl")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "solid rex" {
		t.Fatal("extract")
	}
	if _, _, err := l.Open(rex[0].ID, "file", "../../etc/passwd"); err == nil {
		t.Fatal("path escape must fail")
	}

	// Edits survive a rescan; tags searchable; favorites filter.
	tags := []string{"#Workshop", "clamp", "clamp"}
	fav := true
	if _, err := l.Update(got[0].ID, Meta{Tags: &tags, Favorite: &fav}); err != nil {
		t.Fatal(err)
	}
	l.Rescan()
	it, _ := l.Get(got[0].ID)
	if len(it.Tags) != 2 || it.Tags[0] != "workshop" || !it.Favorite {
		t.Fatalf("edits lost: %+v", it)
	}
	if f, _ := l.Search(Query{Favorites: true}); len(f) != 1 {
		t.Fatal("favorites")
	}
	if tg := l.Tags(); len(tg) != 2 {
		t.Fatal(tg)
	}
	// Folder items are hidden, never deleted from disk.
	if err := l.Delete(it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Tools", "Quick Clamp", "files", "clamp_body.stl")); err != nil {
		t.Fatal("folder files must stay on disk")
	}
	if _, n := l.Search(Query{}); n != 4 {
		t.Fatal("hidden item still listed")
	}
	// Removing the folder removes the item on rescan.
	os.RemoveAll(filepath.Join(root, "Toys", "Dragon"))
	if r := l.Rescan(); r.Removed != 1 {
		t.Fatalf("%+v", r)
	}
	// Reopen from disk.
	l2, _ := Open(l.dir, []string{root})
	// Toys now has a model file and no subfolders: by the rule it is a model itself.
	if _, n := l2.Search(Query{}); n != 4 {
		t.Fatal("index not persisted", n)
	}
}

func TestManagedItems(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(filepath.Join(dir, "lib"), nil)
	tmp := func(body string) string {
		f, _ := os.CreateTemp(dir, "u")
		f.WriteString(body)
		f.Close()
		return f.Name()
	}
	cat := "Home / Kitchen "
	it, err := l.Create(Meta{Category: &cat}, []Incoming{{Name: "spice_rack-v2.stl", Tmp: tmp("solid")}}, []Incoming{{Name: "rack.jpg", Tmp: tmp("jpg")}})
	if err != nil {
		t.Fatal(err)
	}
	if it.Title != "spice rack v2" || it.Category != "Home/Kitchen" || len(it.Files) != 1 || len(it.AddedPhotos) != 1 {
		t.Fatalf("%+v", it)
	}
	if _, err := l.AddFiles(it.ID, []Incoming{{Name: "spice_rack-v2.stl", Tmp: tmp("solid 2")}}); err != nil {
		t.Fatal(err)
	}
	it, _ = l.Get(it.ID)
	if len(it.Files) != 2 || it.Files[1].Name != "spice_rack-v2 (2).stl" {
		t.Fatalf("dup name: %+v", it.Files)
	}
	if _, err := l.AddPhotos(it.ID, []Incoming{{Name: "notes.txt", Tmp: tmp("x")}}); err == nil {
		t.Fatal("non-image photo must fail")
	}
	if it, err = l.RemoveFile(it.ID, "rack.jpg", true); err != nil || len(it.AddedPhotos) != 0 {
		t.Fatal(err)
	}
	if err := l.Delete(it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.itemDir(it.ID)); !os.IsNotExist(err) {
		t.Fatal("managed item files should be deleted")
	}
}

func TestRenameFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Ann", "Cat")
	os.MkdirAll(dir, 0o755)
	for _, n := range []string{"MMM_Cat_Body_v01.stl", "MMM_Cat_Head_v01.stl"} {
		os.WriteFile(filepath.Join(dir, n), []byte("solid"), 0o644)
	}
	l, err := Open(t.TempDir(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	l.Rescan()
	items, _ := l.Search(Query{Limit: 5})
	id := items[0].ID
	// Swap-proof and keeps the extension.
	it, err := l.RenameFiles(id, map[string]string{"MMM_Cat_Body_v01.stl": "Cat - Body", "MMM_Cat_Head_v01.stl": "Cat - Head.stl"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Files[0].Name != "Cat - Body.stl" || it.Files[1].Name != "Cat - Head.stl" {
		t.Fatalf("%+v", it.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "Cat - Body.stl")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.RenameFiles(id, map[string]string{"Cat - Body.stl": "Cat - Head"}); err == nil {
		t.Fatal("allowed a name clash")
	}
	l.Rescan()
	if got, _ := l.Get(id); len(got.Files) != 2 {
		t.Fatalf("%+v", got.Files)
	}
}
