package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"time"
)

// The scripts and the stylesheet only: assets/README.md describes them and
// is not served.
//
//go:embed assets/*.js assets/*.css
var assetFS embed.FS

type asset struct {
	body  []byte
	ctype string
	// sum is the start of the file's sha256: the version in its URL.
	sum string
}

// Named here and not asked of package mime, which reads the machine's own
// tables and so answers differently from one machine to the next.
var assetTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
}

func loadAssets() (map[string]asset, error) {
	files, err := fs.Glob(assetFS, "assets/*")
	if err != nil {
		return nil, err
	}
	out := map[string]asset{}
	for _, f := range files {
		body, err := assetFS.ReadFile(f)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out[path.Base(f)] = asset{body: body, ctype: assetTypes[path.Ext(f)], sum: hex.EncodeToString(sum[:6])}
	}
	return out, nil
}

// assetURL is the URL templates name an asset by. It carries the file's
// checksum, so a new binary's stylesheet is never the old one from the
// browser's cache while an unchanged one is never fetched twice.
func (s *Server) assetURL(name string) (string, error) {
	a, ok := s.assets[name]
	if !ok {
		return "", fs.ErrNotExist
	}
	return "/assets/" + name + "?v=" + a.sum, nil
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	a, ok := s.assets[r.PathValue("file")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.ctype)
	if r.URL.Query().Get("v") == a.sum {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("ETag", `"`+a.sum+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.body))
}
