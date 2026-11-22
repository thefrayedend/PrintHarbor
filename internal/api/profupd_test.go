package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/thefrayedend/printharbor/internal/config"
)

func packZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := []string{}
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate})
		w.Write([]byte(files[n]))
	}
	zw.Close()
	return buf.Bytes()
}

// The whole promise of profile updates: new profiles arrive, a changed one
// keeps its previous settings, revert and lock hold through later updates,
// your own presets are covered, and a profile dropped upstream is kept.
func TestProfileUpdatesKeepWhatYouRelyOn(t *testing.T) {
	packMinFiles, packMinBrands = 1, 1
	defer func() { packMinFiles, packMinBrands = 200, 5 }()

	pack := map[string]string{}
	var id string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		z := packZip(t, pack)
		h := sha256.Sum256(z)
		switch r.URL.Path {
		case "/orca-profiles-newest.json":
			json.NewEncoder(w).Encode(packManifest{ID: id, Ref: "main @ test", Zip: "p.zip", SHA256: hex.EncodeToString(h[:]), Files: len(pack)})
		case "/p.zip":
			w.Write(z)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	profClient = func() *http.Client { return srv.Client() }
	defer func() { profClient = func() *http.Client { return publicClient(0) } }()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.OrcaPath, cfg.OrcaProfilesDir = "/none", filepath.Join(dir, "none")
	cfg.ProfilesURL = srv.URL
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	temp := func(typ, name string) string {
		t.Helper()
		r, err := a.Lib().Resolve(typ, name)
		if err != nil {
			t.Fatal(err)
		}
		s, _ := r["nozzle_temperature"].(string)
		return s
	}
	check := func() {
		t.Helper()
		if _, err := a.CheckProfiles(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}

	// v1
	pack = map[string]string{
		"Acme/filament/common.json": `{"type":"filament","name":"fdm_common","instantiation":"false","nozzle_temperature":"200"}`,
		"Acme/filament/pla.json":    `{"type":"filament","name":"Acme PLA","inherits":"fdm_common","instantiation":"true"}`,
		"Acme/filament/petg.json":   `{"type":"filament","name":"Acme PETG","instantiation":"true","nozzle_temperature":"240"}`,
		"Acme/filament/old.json":    `{"type":"filament","name":"Acme Old","instantiation":"true","nozzle_temperature":"190"}`,
		"Acme.json":                 `{"name":"Acme"}`,
	}
	id = "v1"
	check()
	if got := temp("filament", "Acme PLA"); got != "200" {
		t.Fatalf("v1 PLA = %q", got)
	}
	// my own preset on top of a stock one
	if err := a.SavePreset("filament", "My PLA", "Acme PLA", map[string]string{"filament_flow_ratio": "0.97"}); err != nil {
		t.Fatal(err)
	}
	// "this one is perfect": lock PETG
	if err := a.LockProfile("filament", "Acme PETG"); err != nil {
		t.Fatal(err)
	}

	// v2: the shared parent is fixed (changes PLA and My PLA), PETG changes, Old is gone, ABS is new
	pack["Acme/filament/common.json"] = `{"type":"filament","name":"fdm_common","instantiation":"false","nozzle_temperature":"215"}`
	pack["Acme/filament/petg.json"] = `{"type":"filament","name":"Acme PETG","instantiation":"true","nozzle_temperature":"250"}`
	pack["Acme/filament/abs.json"] = `{"type":"filament","name":"Acme ABS","instantiation":"true","nozzle_temperature":"260"}`
	delete(pack, "Acme/filament/old.json")
	id = "v2"
	check()

	if got := temp("filament", "Acme PLA"); got != "215" {
		t.Errorf("after the update PLA = %q, want the new 215", got)
	}
	if got := temp("filament", "My PLA"); got != "215" {
		t.Errorf("my preset should follow its parent: %q", got)
	}
	if got := temp("filament", "Acme PETG"); got != "240" {
		t.Errorf("a locked profile must not change: %q", got)
	}
	if got := temp("filament", "Acme Old"); got != "190" {
		t.Errorf("a profile removed upstream must be kept: %q", got)
	}
	if a.Lib().Get("filament", "Acme ABS") == nil {
		t.Error("the new profile did not arrive")
	}
	u := a.ProfileUpdates()
	kinds := map[string]string{}
	for _, c := range a.loadProfState().History {
		kinds[c.Name] = c.Kind
	}
	for n, k := range map[string]string{"Acme PLA": "updated", "My PLA": "updated", "Acme PETG": "updated", "Acme Old": "removed", "Acme ABS": "added"} {
		if kinds[n] != k {
			t.Errorf("history: %s = %q, want %q (%v)", n, kinds[n], k, kinds)
		}
	}
	b, _ := json.Marshal(u["locked"])
	if !bytes.Contains(b, []byte(`"differs":true`)) {
		t.Errorf("the locked PETG should say an update is waiting: %s", b)
	}

	// revert my preset to how it was; it holds through the next update
	if err := a.RevertProfile("filament", "My PLA"); err != nil {
		t.Fatal(err)
	}
	if got := temp("filament", "My PLA"); got != "200" {
		t.Errorf("reverted My PLA = %q, want 200", got)
	}
	r, _ := a.Lib().Resolve("filament", "My PLA")
	if r["filament_flow_ratio"] == nil {
		t.Errorf("the revert lost my own setting: %v", r)
	}
	pack["Acme/filament/common.json"] = `{"type":"filament","name":"fdm_common","instantiation":"false","nozzle_temperature":"222"}`
	id = "v3"
	check()
	if got := temp("filament", "My PLA"); got != "200" {
		t.Errorf("a reverted profile must stay reverted: %q", got)
	}
	if got := temp("filament", "Acme PLA"); got != "222" {
		t.Errorf("PLA = %q, want 222", got)
	}
	// reverting again goes forward to what it had before the revert (215)
	if err := a.RevertProfile("filament", "My PLA"); err != nil {
		t.Fatal(err)
	}
	if got := temp("filament", "My PLA"); got != "215" {
		t.Errorf("second revert = %q, want 215", got)
	}
	// unlock: back on stock
	if err := a.UnlockProfile("filament", "My PLA"); err != nil {
		t.Fatal(err)
	}
	if got := temp("filament", "My PLA"); got != "222" {
		t.Errorf("unlocked My PLA = %q, want 222", got)
	}
	if err := a.UnlockProfile("filament", "Acme PETG"); err != nil {
		t.Fatal(err)
	}
	if got := temp("filament", "Acme PETG"); got != "250" {
		t.Errorf("unlocked PETG = %q, want 250", got)
	}

	// a restart reads the same thing back
	a2, err := New(a.cfg())
	if err != nil {
		t.Fatal(err)
	}
	if r, err := a2.Lib().Resolve("filament", "Acme Old"); err != nil || r["nozzle_temperature"] != "190" {
		t.Errorf("after a restart the kept profile is gone: %v %v", r, err)
	}

	// a broken pack is refused and the profiles in use stay
	pack = map[string]string{"../evil.json": `{}`}
	id = "v4"
	if _, err := a.CheckProfiles(context.Background(), false); err == nil {
		t.Error("a pack with a bad file name was accepted")
	}
	if got := temp("filament", "Acme PLA"); got != "222" {
		t.Errorf("a refused pack changed the profiles: %q", got)
	}
	if _, err := os.Stat(a.profDir("next")); err == nil {
		t.Error("the refused pack was left behind")
	}
}
