// pictures writes the pictures a rehearsal sends (see ../README.md), one PNG
// for each of its scenarios, into the directory it is given:
//
//	go build -o D/bin/pictures ./contrib/isolated-yard/pictures
//	D/bin/pictures D/pics
//
// Each is its own, in colour and in the number of bars it shows, so that a
// receipt can say which picture arrived where by its hash: two scenarios
// that sent the same bytes would prove nothing about either. tall.png is
// 1280x9000, more than the 10000 pixels in width and height together that
// a chat service takes of a photo (a full-page screenshot is that tall).
// Every picture is indexed colour, which keeps the tall one to a few
// kilobytes and quick to make.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

// picture is one file: its size, the colour of its ground and of its ink,
// and how many bars of the ink stand on the ground.
type picture struct {
	name          string
	width, height int
	ground, ink   color.RGBA
	bars          int
}

var pictures = []picture{
	{"reply.png", 1280, 800, color.RGBA{0x2a, 0x2a, 0x2a, 255}, color.RGBA{0xe8, 0xb0, 0x4a, 255}, 1},
	{"second.png", 1280, 800, color.RGBA{0x4a, 0x2c, 0x82, 255}, color.RGBA{0xff, 0xff, 0xff, 255}, 2},
	{"mail.png", 1280, 800, color.RGBA{0x1d, 0x35, 0x57, 255}, color.RGBA{0x6f, 0xb9, 0x8f, 255}, 3},
	{"done.png", 1280, 800, color.RGBA{0xf2, 0xe8, 0xd5, 255}, color.RGBA{0x7a, 0x1f, 0x1f, 255}, 4},
	{"tall.png", 1280, 9000, color.RGBA{0xf2, 0xe8, 0xd5, 255}, color.RGBA{0x2d, 0x5f, 0x3f, 255}, 5},
}

// draw is the picture: bars of its ink, 120 pixels wide and 40 apart, each
// 200 pixels high, standing on a band at the foot of every 800 rows (so the
// tall one is eleven pages of the same).
func (p picture) draw() *image.Paletted {
	img := image.NewPaletted(image.Rect(0, 0, p.width, p.height), color.Palette{p.ground, p.ink})
	for page := 0; page*800 < p.height; page++ {
		top := page * 800
		for i := 0; i < p.bars; i++ {
			x := 96 + i*160
			for y := top + 360; y < top+560 && y < p.height; y++ {
				for dx := 0; dx < 120; dx++ {
					img.SetColorIndex(x+dx, y, 1)
				}
			}
		}
	}
	return img
}

// write makes every picture in dir, which it creates, and prints what it
// made.
func write(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, p := range pictures {
		path := filepath.Join(dir, p.name)
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		err = png.Encode(f, p.draw())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		fmt.Printf("%s %dx%d %d bytes\n", p.name, p.width, p.height, info.Size())
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: pictures <directory>")
	}
	if err := write(os.Args[1]); err != nil {
		log.Fatal(err)
	}
}
