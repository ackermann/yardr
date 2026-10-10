package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// serveStarter serves packs/starter as the pack at /starter/, as a web
// server holds a pack's directory.
func serveStarter(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/starter/", http.StripPrefix("/starter/", http.FileServer(http.FS(os.DirFS("../../packs/starter")))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A pack at a URL is the pack's directory on a web server: pack check reads
// it when the command runs, and an import records the URL, which pack list
// prints as the source and the pack's name reads again.
func TestPackAtURL(t *testing.T) {
	srv := serveStarter(t)
	base := srv.URL + "/starter/"
	ctx := context.Background()

	a := packApp(t)
	out, err := run(t, a.packCmd(), "check", base, "--depot", "web", "--crew", "yardmaster=boss")
	if err != nil {
		t.Fatalf("pack check %s: %v\n%s", base, err, out)
	}
	for _, want := range []string{
		"pack starter (" + base + "), kind git, into depot web\n",
		"  create           web-builders\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}

	p, err := resolvePack(ctx, a.st, base, "web")
	if err != nil || p.Name != "starter" || p.Dir != base {
		t.Fatalf("resolvePack URL = %+v, %v", p, err)
	}
	if _, err := a.st.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: base}, true, "me"); err != nil {
		t.Fatal(err)
	}
	q, err := resolvePack(ctx, a.st, "starter", "web")
	if err != nil || q.Name != "starter" || q.Dir != base {
		t.Fatalf("resolvePack name = %+v, %v", q, err)
	}
	if out, err := run(t, a.packCmd(), "list"); err != nil || !strings.Contains(out, "  source     "+base+"\n") {
		t.Errorf("pack list = %q, %v", out, err)
	}
}

// A name imported from a URL is read from it again: with the server gone the
// error says the URL and what fetching it said.
func TestPackNameImportedFromAGoneURL(t *testing.T) {
	srv := serveStarter(t)
	base := srv.URL + "/starter/"
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPack(ctx, store.PackImport{Pack: "starter", Depot: "web", Source: base}, true, "me"); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	_, err := resolvePack(ctx, st, "starter", "web")
	if err == nil || !strings.Contains(err.Error(), "which is gone") || !strings.Contains(err.Error(), base) {
		t.Fatalf("with the server gone: %v", err)
	}
}
