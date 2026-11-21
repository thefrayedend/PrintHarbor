package tray

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestIconPNG(t *testing.T) {
	b := iconPNG(32)
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 32 {
		t.Fatalf("bad icon: %v", err)
	}
	if p := os.Getenv("ICON_OUT"); p != "" {
		os.WriteFile(p, iconPNG(256), 0o644)
	}
}
