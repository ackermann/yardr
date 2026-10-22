package notify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
)

const (
	// A user key and an application token of the shape Pushover gives
	// them: 30 letters and digits.
	packKey   = "uQ8nXw5ZrEa1CtYvMk0LgHs7DbFj3P"
	packToken = "aqmK3fVxN7Hz2PdW9sLc4TyRbJe6Uo"

	// A 1x1 picture of each kind the command tells apart by its type.
	packPNG  = "89504e470d0a1a0a0000000d4948445200000001000000010802000000907753de0000000c4944415478da6360000000020001e221bc330000000049454e44ae426082a6"
	packGIF  = "47494638396101000100800000000000ffffff21f90401000000002c00000000010001000002024401003b"
	packJPEG = "ffd8ffe000104a46494600010100000100010000ffd9"
)

// pushoverPack is the pushover pack's declaration with its image command
// copied under a home of the test's, as yardr source add copies it.
type pushoverPack struct {
	d    *source.Declaration
	home string
}

func newPushoverPack(t *testing.T) *pushoverPack {
	t.Helper()
	for _, tool := range []string{"jq", "curl", "file"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, and the pack's command is written with it", tool)
		}
	}
	dir := filepath.Join("..", "..", "packs", "pushover")
	text, err := os.ReadFile(filepath.Join(dir, "pushover.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "pushover.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	return &pushoverPack{d: d, home: home}
}

// serve is a stand-in for the service that takes a message with token, and
// the address of it as the pack's base.
func serve(t *testing.T, token string) (*standin.Server, string) {
	t.Helper()
	s := &standin.Server{PushToken: token}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv.URL + "/1"
}

// tell runs the notify key's image command as the daemon runs it: the
// object on its stdin, the declaration's environment with the credential
// and the base, and its answer read as {status}. More environment wins.
func (p *pushoverPack) tell(base, token string, in map[string]any, env ...string) (map[string]any, error) {
	p.d.Base = base
	argv := slices.Clone(p.d.Notify.Image)
	argv[0] = source.Command{Key: "notify.image", Argv: p.d.Notify.Image}.Program(p.home, p.d.Name)
	var got map[string]any
	err := source.Run(context.Background(), "notify.image of source "+p.d.Name, argv, in,
		append(p.d.CommandEnvOf(source.Update, token), env...), p.home, 0, func(out []byte) error {
			return json.Unmarshal(out, &got)
		})
	return got, err
}

// blob keeps data in a file called by its hash, as the yard keeps a picture.
func blob(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	path := filepath.Join(t.TempDir(), hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fromHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The pushover pack's image command sends the picture as the attachment
// part of the multipart form the service takes, with the words beside it,
// and answers {status}. A picture over what the service takes is refused
// before anything is sent. The token and the user key reach curl by its
// stdin and are no part of any argument; a key or a token with a quote or
// a line break in it is data, and adds no option to curl's config.
func TestPushoverPackTell(t *testing.T) {
	p := newPushoverPack(t)
	svc, base := serve(t, packToken)
	png := fromHex(t, packPNG)
	in := func(title, text string, image []byte) map[string]any {
		return map[string]any{"address": packKey, "title": title, "text": text, "image": blob(t, image)}
	}
	seen := 0
	next := func() standin.Message {
		t.Helper()
		ms := svc.Messages()
		if len(ms) != seen+1 {
			t.Fatalf("%d messages taken, want %d", len(ms), seen+1)
		}
		seen++
		return ms[seen-1]
	}

	// The picture arrives as the attachment, its bytes the file's own, with
	// a name and a type the service requires: a file the yard keeps is
	// called by its hash, which names nothing, so the name is made of its
	// type. One that no type is known of goes as the bytes it is.
	for _, c := range []struct {
		data        []byte
		name, ctype string
	}{
		{png, "photo.png", "image/png"},
		{fromHex(t, packJPEG), "photo.jpg", "image/jpeg"},
		{fromHex(t, packGIF), "photo.gif", "image/gif"},
		{[]byte("\x89PNG\r\n\x1a\nthe shed from the lane"), "photo", "application/octet-stream"},
	} {
		got, err := p.tell(base, packToken, in("mail from jo", "the shed lands Monday", c.data))
		if err != nil || fmt.Sprint(got) != "map[status:200]" {
			t.Fatalf("%s: %v, %v", c.name, got, err)
		}
		m := next()
		if m.User != packKey || m.Title != "mail from jo" || m.Text != "the shed lands Monday" || m.Attachment == nil ||
			m.Attachment.Name != c.name || m.Attachment.Type != c.ctype || !bytes.Equal(m.Attachment.Data, c.data) {
			t.Fatalf("%s: %+v, %+v", c.name, m, m.Attachment)
		}
	}

	// The words are cut as the declared call cuts them, in characters and
	// with its ellipsis, and are left alone at the bound.
	if _, err := p.tell(base, packToken, in(strings.Repeat("ä", 300), strings.Repeat("ö", 2000), png)); err != nil {
		t.Fatal(err)
	}
	if m := next(); m.Title != strings.Repeat("ä", 249)+"…" || m.Text != strings.Repeat("ö", 1023)+"…" {
		t.Fatalf("cut words: %d and %d characters", len([]rune(m.Title)), len([]rune(m.Text)))
	}
	if _, err := p.tell(base, packToken, in(strings.Repeat("ä", 250), strings.Repeat("ö", 1024), png)); err != nil {
		t.Fatal(err)
	}
	if m := next(); m.Title != strings.Repeat("ä", 250) || m.Text != strings.Repeat("ö", 1024) {
		t.Fatalf("words at the bound: %d and %d characters", len([]rune(m.Title)), len([]rune(m.Text)))
	}

	// The words are data: a leading @ or <, a type, a quote and a line
	// break are no part of the form's syntax.
	if _, err := p.tell(base, packToken, in("@/etc/hosts", "<shed.png;type=text/plain \"say\"\nand -F x=y", png)); err != nil {
		t.Fatal(err)
	}
	if m := next(); m.Title != "@/etc/hosts" || m.Text != "<shed.png;type=text/plain \"say\"\nand -F x=y" || !bytes.Equal(m.Attachment.Data, png) {
		t.Fatalf("words as data: %+v", m)
	}

	// The service requires a message: a picture sent with a title alone has
	// the title for it.
	if _, err := p.tell(base, packToken, in("mail from jo", "", png)); err != nil {
		t.Fatal(err)
	}
	if m := next(); m.Title != "" || m.Text != "mail from jo" || m.Attachment == nil {
		t.Fatalf("a title alone: %+v", m)
	}
	asked := svc.Asked()
	if _, err := p.tell(base, packToken, in("", "", png)); err == nil || !strings.Contains(err.Error(), "neither a title nor a text") {
		t.Fatalf("no words: %v", err)
	}

	// The most the service takes goes, to the byte; one byte more is refused
	// with its size before anything is sent.
	edge := append(bytes.Clone(png), make([]byte, 5242880-len(png))...)
	if _, err := p.tell(base, packToken, in("t", "x", edge)); err != nil {
		t.Fatal(err)
	}
	if m := next(); m.Attachment == nil || m.Attachment.Bytes != 5242880 || !bytes.Equal(m.Attachment.Data, edge) {
		t.Fatalf("a picture of 5242880 bytes: %+v", m.Attachment)
	}
	asked = svc.Asked()
	_, err := p.tell(base, packToken, in("t", "x", append(edge, 0)))
	if err == nil || !strings.Contains(err.Error(), "the picture is 5242881 bytes") || !strings.Contains(err.Error(), "5242880 at most") || len(err.Error()) > 300 {
		t.Fatalf("a picture of 5242881 bytes: %v", err)
	}
	if svc.Asked() != asked {
		t.Fatal("a picture over the limit was sent all the same")
	}

	// What it is not handed, or cannot find, it refuses before it sends.
	valid := in("t", "x", png)
	for _, c := range []struct {
		name, token string
		in          map[string]any
		want        string
	}{
		{"no image", packToken, map[string]any{"address": packKey, "title": "t", "text": "x", "image": ""}, "no image"},
		{"no address", packToken, map[string]any{"address": "", "title": "t", "text": "x", "image": valid["image"]}, "no address"},
		{"no credential", "", valid, "PUSHOVER_TOKEN"},
		{"no file", packToken, map[string]any{"address": packKey, "title": "t", "text": "x", "image": filepath.Join(t.TempDir(), "gone.png")}, "found no file"},
	} {
		if _, err := p.tell(base, c.token, c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if svc.Asked() != asked {
		t.Fatalf("%d requests for refused tells", svc.Asked()-asked)
	}

	// A token or a key with a quote, a backslash or a line break in it is
	// the service's to judge, not curl's config to read: it arrives as it
	// is, and the option a crafted key would add is not added.
	hostileToken := "t\"o\\k\nen=1"
	svc2, base2 := serve(t, hostileToken)
	wrote := filepath.Join(t.TempDir(), "written")
	hostileKey := "k\"\\\noutput = \"" + wrote + "\"\r\nurl = \"http://127.0.0.1:1/x"
	if _, err := p.tell(base2, hostileToken, map[string]any{"address": hostileKey, "title": "t", "text": "x", "image": valid["image"]}); err != nil {
		t.Fatal(err)
	}
	if ms := svc2.Messages(); len(ms) != 1 || ms[0].User != hostileKey || svc2.Asked() != 1 {
		t.Fatalf("a crafted key: %+v, asked %d", ms, svc2.Asked())
	}
	if _, err := os.Stat(wrote); !os.IsNotExist(err) {
		t.Fatalf("a crafted key wrote a file: %v", err)
	}

	// No argument of any curl the command runs holds the token or the key:
	// a curl first on its PATH records its arguments and runs the real one,
	// and the service took the message with both.
	svc3, base3 := serve(t, packToken)
	record := filepath.Join(t.TempDir(), "argv")
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	scripttest.Write(t, filepath.Join(shim, "curl"), "printf '%s\\000' \"$@\" >>'"+record+"'\nexec '"+curl+"' \"$@\"\n")
	if _, err := p.tell(base3, packToken, valid, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(record)
	if err != nil || !bytes.Contains(argv, []byte("attachment=@")) {
		t.Fatalf("the recorded arguments of curl: %q, %v", argv, err)
	}
	for what, secret := range map[string]string{"the token": packToken, "the user key": packKey} {
		if bytes.Contains(argv, []byte(secret)) {
			t.Errorf("%s is in curl's arguments: %q", what, argv)
		}
	}
	if ms := svc3.Messages(); len(ms) != 1 || ms[0].User != packKey || ms[0].Attachment == nil {
		t.Fatalf("with curl recorded: %+v", ms)
	}

	// What the service says of a refusal is in the error, with the token and
	// the key taken out should it echo them, and no control character; what
	// is no JSON adds nothing.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"status":0,"errors":["user %s is invalid","token %s is too, \u001b[31mred"]}`, packKey, packToken)
	}))
	defer echo.Close()
	_, err = p.tell(echo.URL+"/1", packToken, valid)
	if err == nil || !strings.Contains(err.Error(), "is invalid") || !strings.Contains(err.Error(), "is too, [31mred") ||
		strings.Contains(err.Error(), packKey) || strings.Contains(err.Error(), packToken) || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("a refusal that echoes the secrets: %q", err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer down.Close()
	_, err = p.tell(down.URL+"/1", packToken, valid)
	if err == nil || !strings.Contains(err.Error(), "curl exit 22") || strings.Contains(err.Error(), "html") {
		t.Fatalf("a gateway that is down: %v", err)
	}
}
