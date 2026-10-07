package flowdraw

import (
	"io/fs"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/packs"
)

// Embedded draws every flow of a pack yardr embeds, by the name of the file
// each is kept in under docs/flows: <pack>.svg for the pack's first flow,
// the one a depot follows unless told otherwise, <pack>.<flow>.svg for any
// other. go generate writes them (gen), and a test holds the committed
// files to what this returns.
func Embedded(name string) (map[string][]byte, error) {
	sub, err := fs.Sub(packs.FS, name)
	if err != nil {
		return nil, err
	}
	p, err := pack.Load(sub, packs.Embedded)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for i, f := range p.Flows {
		file := name + "." + f.Name + ".svg"
		if i == 0 {
			file = name + ".svg"
		}
		out[file] = SVG(FromPack(p, f))
	}
	return out, nil
}
