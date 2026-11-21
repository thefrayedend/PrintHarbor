// Package config loads and saves PrintHarbor's config.json.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// DefaultSliceArgs is the Orca CLI argument template. Placeholders:
// {plate} {settings} {filaments} {outdir} {out3mf} {input}
// Each element is one argv entry, so paths with spaces are safe.
var DefaultSliceArgs = []string{
	"--slice", "{plate}",
	"--load-settings", "{settings}",
	"--load-filaments", "{filaments}",
	"--arrange", "1",
	"--allow-newer-file",
	"--outputdir", "{outdir}",
	"--export-3mf", "{out3mf}",
	"{input}",
}

type Config struct {
	Listen              string   `json:"listen"`
	DataDir             string   `json:"data_dir"`
	OrcaPath            string   `json:"orca_path"`
	OrcaProfilesDir     string   `json:"orca_profiles_dir"`
	OrcaUserPresetsDir  string   `json:"orca_user_presets_dir,omitempty"`
	OrcaSliceArgs       []string `json:"orca_slice_args"`
	MaxConcurrentSlices int      `json:"max_concurrent_slices"`
	SliceTimeoutMinutes int      `json:"slice_timeout_minutes"`
	PollSeconds         int      `json:"poll_seconds"`
	// JobKeepDays: how long the files sliced for a finished job stay on disk
	// (7 by default; 0 keeps them for ever). Finished jobs are not listed on
	// the Queue; the print history ("Completed prints") does not need them.
	JobKeepDays int `json:"job_keep_days"`
	// Backups of your projects, printers, history and settings (see
	// internal/api/backup.go). BackupDir: where they go (empty: data\backups;
	// another drive is safer). BackupKeepDays: how long each is kept; 0 turns
	// backups off.
	BackupDir      string `json:"backup_dir"`
	BackupKeepDays int    `json:"backup_keep_days"`
	// PrintDefaults: your own values for quality settings, used in place of
	// what the stock Orca profiles say (support type, walls, infill, …). A
	// setting you change for one job, or one your own preset sets, still wins.
	// Never changed in place: a new map replaces it when Settings are saved.
	PrintDefaults map[string]string `json:"print_defaults,omitempty"`
	// ShowPrinterLight shows the light on/off button on printer cards. Off by
	// default — most people never toggle their printer light from here.
	ShowPrinterLight bool `json:"show_printer_light,omitempty"`
	// LibraryFolders are folders on disk scanned into the Library tab.
	LibraryFolders []string `json:"library_folders"`
	// ModelDir is where models imported from online sites are saved, as
	// ModelDir/<designer>/<model>/<files>. Empty = "Models" next to the data folder.
	ModelDir string `json:"model_dir,omitempty"`
	// Online model site keys (each optional; Printables needs none).
	ThingiverseToken string `json:"thingiverse_token,omitempty"`
	MakerWorldToken  string `json:"makerworld_token,omitempty"`
	CultsUser        string `json:"cults_user,omitempty"`
	CultsKey         string `json:"cults_key,omitempty"`
	// Optional integrations (empty = off).
	SpoolmanURL       string `json:"spoolman_url,omitempty"`
	SpoolmanWriteback bool   `json:"spoolman_writeback,omitempty"` // deduct used filament from Spoolman after a print
	ObicoMLURL        string `json:"obico_ml_url,omitempty"`
	OllamaURL         string `json:"ollama_url,omitempty"`
	OllamaModel       string `json:"ollama_model,omitempty"`
	// Workers are other PrintHarbor installs that slice for this one.
	Workers     []string `json:"workers,omitempty"`
	WorkerToken string   `json:"worker_token,omitempty"`
	// DiscordWebhook gets alerts (finished prints, AI warnings) with a camera frame.
	DiscordWebhook string `json:"discord_webhook,omitempty"`
	// PublicURL is how other machines reach PrintHarbor (auto-detected if empty).
	PublicURL string `json:"public_url,omitempty"`
	// Hostname announced over mDNS (harbor -> harbor.local). "-" disables it.
	MDNSName string `json:"mdns_name,omitempty"`
	// AllowedHosts are extra names this computer's own browser may use to open
	// PrintHarbor (localhost, IP addresses, *.local and PublicURL always work).
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

func Defaults() Config {
	return Config{
		Listen:              ":7621",
		DataDir:             "data",
		OrcaPath:            defaultOrcaPath(),
		OrcaProfilesDir:     defaultProfilesDir(),
		OrcaUserPresetsDir:  defaultUserPresetsDir(),
		OrcaSliceArgs:       DefaultSliceArgs,
		MaxConcurrentSlices: 1,
		SliceTimeoutMinutes: 30,
		PollSeconds:         3,
		JobKeepDays:         7,
		BackupKeepDays:      14,
		LibraryFolders:      []string{},
		MDNSName:            "harbor",
	}
}

// Load reads path, filling missing fields with defaults. If the file does not
// exist it is created with defaults so the user has something to edit.
func Load(path string) (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, Save(path, c)
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.MaxConcurrentSlices < 1 {
		c.MaxConcurrentSlices = 1
	}
	if len(c.OrcaSliceArgs) == 0 {
		c.OrcaSliceArgs = DefaultSliceArgs
	}
	if c.PollSeconds < 1 {
		c.PollSeconds = 3
	}
	if c.JobKeepDays < 0 {
		c.JobKeepDays = 0
	}
	if c.BackupKeepDays < 0 {
		c.BackupKeepDays = 0
	}
	if c.SliceTimeoutMinutes < 1 {
		c.SliceTimeoutMinutes = 30
	}
	return c, nil
}

func Save(path string, c Config) error {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// Write a new file and swap it in, so a crash or power cut half way can
	// never leave a broken config.json (PrintHarbor wouldn't start with one).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Can't swap (the file is held open, or read-only): write it directly.
		_ = os.Remove(tmp)
		return os.WriteFile(path, b, 0o644)
	}
	return nil
}

// ModelsDir is the folder imported models go into (absolute).
func (c Config) ModelsDir() string {
	d := c.ModelDir
	if d == "" {
		data, err := filepath.Abs(c.DataDir)
		if err != nil {
			data = c.DataDir
		}
		d = filepath.Join(filepath.Dir(data), "Models")
	}
	if abs, err := filepath.Abs(d); err == nil {
		d = abs
	}
	return filepath.Clean(d)
}

// ScanFolders are the library folders plus the imported-models folder.
func (c Config) ScanFolders() []string {
	md := c.ModelsDir()
	out := []string{}
	have := false
	for _, f := range c.LibraryFolders {
		if f == "" {
			continue
		}
		if a, err := filepath.Abs(f); err == nil && sameDir(a, md) {
			have = true
		}
		out = append(out, f)
	}
	if !have {
		out = append(out, md)
	}
	return out
}
