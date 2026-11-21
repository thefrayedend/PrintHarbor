package config

import (
	"os"
	"path/filepath"
	"runtime"
)

func defaultOrcaPath() string {
	switch runtime.GOOS {
	case "windows":
		return `C:\Program Files\OrcaSlicer\orca-slicer.exe`
	case "darwin":
		return "/Applications/OrcaSlicer.app/Contents/MacOS/OrcaSlicer"
	default:
		if p := os.Getenv("ORCA_PATH"); p != "" {
			return p
		}
		return "/opt/orcaslicer/bin/orca-slicer"
	}
}

// defaultUserPresetsDir is where desktop Orca keeps the user's own presets,
// so people's existing tweaks show up in PrintHarbor automatically.
func defaultUserPresetsDir() string {
	var p string
	switch runtime.GOOS {
	case "windows":
		p = filepath.Join(os.Getenv("APPDATA"), "OrcaSlicer", "user")
	case "darwin":
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, "Library", "Application Support", "OrcaSlicer", "user")
	default:
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, ".config", "OrcaSlicer", "user")
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func defaultProfilesDir() string {
	switch runtime.GOOS {
	case "windows":
		return `C:\Program Files\OrcaSlicer\resources\profiles`
	case "darwin":
		return "/Applications/OrcaSlicer.app/Contents/Resources/profiles"
	default:
		if p := os.Getenv("ORCA_PROFILES"); p != "" {
			return p
		}
		return filepath.FromSlash("/opt/orcaslicer/resources/profiles")
	}
}
