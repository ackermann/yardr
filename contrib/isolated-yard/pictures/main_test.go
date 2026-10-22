package main

import (
	"bytes"
	"crypto/sha256"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// The pictures are PNGs of the sizes the scenarios need, each with bytes of
// its own: every one but tall.png is a photo a chat service takes, and
// tall.png is over its 10000 pixels in width and height together.
func TestPicturesAreDistinctPNGsOfTheSizesTheScenariosNeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pics")
	if err := write(dir); err != nil {
		t.Fatal(err)
	}
	seen := map[[32]byte]string{}
	for _, p := range pictures {
		data, err := os.ReadFile(filepath.Join(dir, p.name))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width != p.width || cfg.Height != p.height {
			t.Errorf("%s = %dx%d, %v, want a PNG of %dx%d", p.name, cfg.Width, cfg.Height, err, p.width, p.height)
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Errorf("%s does not decode: %v", p.name, err)
		}
		if other, ok := seen[sha256.Sum256(data)]; ok {
			t.Errorf("%s has the bytes of %s", p.name, other)
		}
		seen[sha256.Sum256(data)] = p.name
		if over := cfg.Width+cfg.Height > 10000; over != (p.name == "tall.png") {
			t.Errorf("%s is %dx%d: over a photo's 10000 in total is %v", p.name, cfg.Width, cfg.Height, over)
		}
	}
	if len(seen) != 5 {
		t.Errorf("%d pictures, want the five scenarios' own", len(seen))
	}
}
