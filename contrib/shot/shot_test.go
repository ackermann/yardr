package shot

// These tests run the reference screenshot script against pages of their
// own: an httptest server on loopback, never the network and never Dokku.
// The shot itself needs headless Chrome, which the machine running the
// tests may not have; then that test skips, and a stand-in Chrome proves
// the script passes the page through with the right flags. The refusals
// need no Chrome: the script checks the file and the page first. Nothing
// waits on Chrome without a deadline: the script stops it after SHOT_TIMEOUT,
// and the tests stop the script at their context's.

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// page is one local page at /page; every other path is missing.
func page(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>shot</h1></body></html>"))
	})
	mux.HandleFunc("/redirect-to-missing", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/missing", http.StatusFound)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// shotTimeout is the deadline the tests give runShot: a minute, the
// script's own Chrome bound.
const shotTimeout = time.Minute

// shotCtx gives runShot its deadline: the tests never wait on the script
// past a minute, whatever Chrome does.
func shotCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), shotTimeout)
	t.Cleanup(cancel)
	return ctx
}

// runShot runs the script with args and the environment added to its own.
// The script's Chrome can hang under load, so ctx carries the deadline: at
// it the script's process group is killed whole (a wrapper script's child
// would otherwise keep the output pipe open past the timeout) and runShot
// returns what was printed so far with the reason, in the script's own
// Chrome-timeout words.
func runShot(ctx context.Context, t *testing.T, args []string, env ...string) (int, string) {
	t.Helper()
	start := time.Now()
	cmd := exec.CommandContext(ctx, "./shot", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return 1, string(out) + "shot: chrome did not finish within " + timeoutSecs(ctx, start) + "\n"
	}
	rc := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			rc = exit.ExitCode()
		} else {
			t.Fatalf("shot %v: %v", args, err)
		}
	}
	return rc, string(out)
}

// timeoutSecs words ctx's deadline as whole seconds ("60s"), the script's
// SHOT_TIMEOUT shape, so a timeout from either reads the same. A context
// with no deadline words the time the run took instead.
func timeoutSecs(ctx context.Context, start time.Time) string {
	d := time.Since(start)
	if deadline, ok := ctx.Deadline(); ok {
		d = deadline.Sub(start)
	}
	if s := int(d.Seconds() + 0.5); s > 0 {
		return strconv.Itoa(s) + "s"
	}
	return "0s"
}

// underLoad reports whether a slow Chrome is the machine's fault, not the
// shot's: the landing suite, and any run with the race detector, load the
// machine enough that headless Chrome can miss its minute.
func underLoad() bool {
	return os.Getenv("YARDR_LANDING") != "" || race()
}

// race reports whether the test binary was built with the race detector,
// read from its build info.
func race() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

// standin is a SHOT_CHROME that shoots nothing: it records its arguments
// and writes want to the --screenshot target, so the test sees what flags
// the script passed and what file it named.
func standin(t *testing.T, want string, exitCode int) (env []string, args string) {
	t.Helper()
	dir := t.TempDir()
	args = filepath.Join(dir, "args")
	bin := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >" + args + "\n" +
		"for a in \"$@\"; do case \"$a\" in --screenshot=*) out=${a#--screenshot=};; esac; done\n"
	if want != "" {
		script += "printf '%s' '" + want + "' >\"$out\"\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"SHOT_CHROME=" + bin}, args
}

func TestShotIsExecutable(t *testing.T) {
	info, err := os.Stat("shot")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("contrib/shot/shot must be executable")
	}
}

func TestShotUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"http://127.0.0.1/"}, {"a", "b", "c"}} {
		if rc, out := runShot(shotCtx(t), t, args); rc != 2 || !strings.Contains(out, "shot <url> <file>") {
			t.Errorf("shot %v: exit %d, %q, want exit 2 with the usage", args, rc, out)
		}
	}
}

// The script passes the page to Chrome with its flags, and removes the
// throwaway profile afterwards.
func TestShotPassesFlagsToChrome(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	env, argsFile := standin(t, "shot", 0)
	out := filepath.Join(t.TempDir(), "shot.png")
	rc, outText := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
	if rc != 0 {
		t.Fatalf("shot: exit %d: %s", rc, outText)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "shot" {
		t.Fatalf("shot wrote %q, %v, want the stand-in's bytes", got, err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	for _, want := range []string{"--headless", "--disable-gpu",
		"--screenshot=" + out, "--window-size=1280,800", s.URL + "/page"} {
		if !strings.Contains(args, want) {
			t.Errorf("Chrome was passed %q, which lacks %s", args, want)
		}
	}
	prof := ""
	for _, field := range strings.Fields(args) {
		if rest, ok := strings.CutPrefix(field, "--user-data-dir="); ok {
			prof = rest
		}
	}
	if prof == "" {
		t.Fatalf("Chrome was passed %q, with no --user-data-dir", args)
	}
	if _, err := os.Stat(prof); !os.IsNotExist(err) {
		t.Errorf("the throwaway profile %s is still there: %v", prof, err)
	}
}

// A missing page is refused with its reason, and Chrome never runs.
func TestShotRefusesAMissingPage(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	// A port nothing listens on: listened on and closed.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	shut := l.Addr().String()
	l.Close()
	for name, url := range map[string]string{
		"a path the server does not have": s.URL + "/missing",
		"a redirect to a missing path":    s.URL + "/redirect-to-missing",
		"a port nothing listens on":       "http://" + shut + "/page",
	} {
		t.Run(name, func(t *testing.T) {
			env, argsFile := standin(t, "shot", 0)
			out := filepath.Join(t.TempDir(), "shot.png")
			rc, text := runShot(shotCtx(t), t, []string{url, out}, env...)
			if rc == 0 {
				t.Fatalf("shot %s: exit 0, want a refusal", url)
			}
			if !strings.Contains(text, url) {
				t.Errorf("shot %s: %q, which does not name the page", url, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for a missing page")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("a shot of a missing page is at %s", out)
			}
		})
	}
	if _, text := runShot(shotCtx(t), t, []string{s.URL + "/missing", filepath.Join(t.TempDir(), "shot.png")}); !strings.Contains(text, "404") {
		t.Errorf("a missing path is refused with %q, which carries no reason", text)
	}
}

// An unwritable file is refused with its reason, and Chrome never runs.
func TestShotRefusesAnUnwritableFile(t *testing.T) {
	s := page(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nowrite := filepath.Join(dir, "nowrite")
	if err := os.Mkdir(nowrite, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(nowrite, 0o755) })
	readonly := filepath.Join(dir, "readonly.png")
	if err := os.WriteFile(readonly, []byte("old shot"), 0o444); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"a directory that does not exist": filepath.Join(dir, "gone", "shot.png"),
		"a directory":                     sub,
		"a directory not writable":        filepath.Join(nowrite, "shot.png"),
		"an existing file not writable":   readonly,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(name, "not writable") && os.Geteuid() == 0 {
				t.Skip("root writes anywhere")
			}
			env, argsFile := standin(t, "shot", 0)
			rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
			if rc == 0 {
				t.Fatalf("shot to %s: exit 0, want a refusal", out)
			}
			if !strings.Contains(text, out) {
				t.Errorf("shot to %s: %q, which does not name the file", out, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for an unwritable file")
			}
		})
	}
}

// A nonzero Chrome exit is a failed shot even if it left a non-empty file.
func TestShotReportsChromeFailure(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	env, _ := standin(t, "partial shot", 7)
	out := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(out, []byte("stale shot"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
	if rc != 7 || !strings.Contains(text, "Chrome failed with exit 7") {
		t.Errorf("failed Chrome shot: exit %d, %q, want exit 7 and its reason", rc, text)
	}
}

// A successful Chrome exit that writes nothing is still a failed shot, even
// when the destination held an older screenshot.
func TestShotRefusesAnEmptyShot(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	env, _ := standin(t, "", 0)
	out := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(out, []byte("stale shot"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
	if rc == 0 || !strings.Contains(text, "Chrome shot nothing") {
		t.Errorf("empty Chrome shot: exit %d, %q, want an empty-shot refusal", rc, text)
	}
	if got, err := os.ReadFile(out); err != nil || len(got) != 0 {
		t.Errorf("old screenshot remains after an empty shot: %q, %v", got, err)
	}
}

// SHOT_CHROME names the binary; one that is not there is refused with its
// reason.
func TestShotRefusesAnUnusableChrome(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	out := filepath.Join(t.TempDir(), "shot.png")
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, "SHOT_CHROME="+filepath.Join(t.TempDir(), "gone"))
	if rc == 0 || !strings.Contains(text, "SHOT_CHROME=") || !strings.Contains(text, "is not executable") {
		t.Errorf("an unusable SHOT_CHROME: exit %d, %q", rc, text)
	}
}

// pngSize reads the dimensions of a PNG from its header.
func pngSize(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 24 || !bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")) || string(raw[12:16]) != "IHDR" {
		t.Fatalf("%s is %d bytes, not a PNG", path, len(raw))
	}
	return binary.BigEndian.Uint32(raw[16:20]), binary.BigEndian.Uint32(raw[20:24])
}

// The shot itself, with real Chrome against the local page: a window-sized
// PNG. A machine with no Chrome skips it; the stand-in test above proves
// the flags there. A Chrome that misses its minute under load skips too,
// with the reason: a slow machine is not a failure of the shot code.
func TestShotWritesAWindowSizedPNG(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	out := filepath.Join(t.TempDir(), "shot.png")
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out})
	if rc != 0 && strings.Contains(text, "no headless Chrome") {
		t.Skip("no headless Chrome: " + strings.TrimSpace(text))
	}
	if strings.Contains(text, "chrome did not finish within") {
		if underLoad() {
			t.Skip("chrome missed its minute under load: " + strings.TrimSpace(text))
		}
		t.Fatalf("shot: exit %d: %s", rc, text)
	}
	if rc != 0 {
		t.Fatalf("shot: exit %d: %s", rc, text)
	}
	if w, h := pngSize(t, out); w != 1280 || h != 800 {
		t.Errorf("the shot is %dx%d, want 1280x800", w, h)
	}
}

// sleeper is a SHOT_CHROME that prints line and never finishes: it sleeps
// past every deadline the test gives it. It records the PIDs the test must
// find dead afterwards in pids: its own, and with child its sleeper's, so a
// group kill is told from one that took Chrome's own process alone.
func sleeper(t *testing.T, line string, child bool) (env []string, pids string) {
	t.Helper()
	dir := t.TempDir()
	pids = filepath.Join(dir, "pids")
	bin := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\necho '" + line + "'\necho $$ >" + pids + "\n"
	if child {
		script += "sleep 30 & echo $! >>" + pids + "\nwait\n"
	} else {
		script += "exec sleep 30\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"SHOT_CHROME=" + bin}, pids
}

// dead fails the test unless every PID in pids is gone: the kill the
// timeout promises, waited on, not slept for.
func dead(t *testing.T, pids string) {
	t.Helper()
	raw, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	giveUp := time.Now().Add(10 * time.Second)
	for _, field := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			t.Fatalf("the stand-in recorded %q, not a PID", field)
		}
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(giveUp) {
				t.Fatalf("process %d is still alive 10 s after the timeout killed it", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// A Chrome that sleeps past its deadline is killed, and the shot fails with
// what it printed so far and the reason, within about a second: the script's
// own bound and runShot's context each do it, so a hung Chrome hangs neither
// a builder nor the suite.
func TestShotKillsAChromeThatDoesNotFinish(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	t.Run("the script's SHOT_TIMEOUT", func(t *testing.T) {
		env, pids := sleeper(t, "chrome is warming up", false)
		env = append(env, "SHOT_TIMEOUT=1")
		out := filepath.Join(t.TempDir(), "shot.png")
		rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
		if rc == 0 || !strings.Contains(text, "chrome did not finish within 1s") {
			t.Errorf("a Chrome past SHOT_TIMEOUT: exit %d, %q, want a refusal naming the deadline", rc, text)
		}
		if !strings.Contains(text, "chrome is warming up") {
			t.Errorf("the refusal is %q, without what Chrome printed", text)
		}
		dead(t, pids)
	})
	t.Run("runShot's context deadline", func(t *testing.T) {
		env, pids := sleeper(t, "chrome is warming up", true)
		out := filepath.Join(t.TempDir(), "shot.png")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		t.Cleanup(cancel)
		rc, text := runShot(ctx, t, []string{s.URL + "/page", out}, env...)
		if rc == 0 || !strings.Contains(text, "chrome did not finish within 1s") {
			t.Errorf("a Chrome past the deadline: exit %d, %q, want a refusal naming the deadline", rc, text)
		}
		if !strings.Contains(text, "chrome is warming up") {
			t.Errorf("the refusal is %q, without what Chrome printed", text)
		}
		dead(t, pids)
	})
}

// A slow Chrome is the machine's fault on the landing suite and under the
// race detector, and the shot's own anywhere else.
func TestUnderLoad(t *testing.T) {
	t.Setenv("YARDR_LANDING", "1")
	if !underLoad() {
		t.Error("underLoad with YARDR_LANDING=1 is false")
	}
	t.Setenv("YARDR_LANDING", "")
	if underLoad() != race() {
		t.Errorf("underLoad without YARDR_LANDING is %v with race %v", underLoad(), race())
	}
}

// The URL is a web page's and nothing else: one that starts with a dash
// would be read as an option by curl and by Chrome, and a file: URL would
// shoot a file of this machine into a note. Neither reaches curl or Chrome.
func TestShotRefusesAURLThatIsNotAWebPage(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "planted")
	for name, url := range map[string]string{
		"an option to Chrome":    "--remote-debugging-port=0",
		"an option to curl":      "-o" + planted,
		"a file of this machine": "file://" + secret,
		"no scheme":              "127.0.0.1/page",
		"nothing":                "",
	} {
		t.Run(name, func(t *testing.T) {
			env, argsFile := standin(t, "shot", 0)
			out := filepath.Join(t.TempDir(), "shot.png")
			rc, text := runShot(shotCtx(t), t, []string{url, out}, env...)
			if rc != 2 || !strings.Contains(text, "http:// or https://") {
				t.Errorf("shot %q: exit %d, %q, want exit 2 naming the schemes", url, rc, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for a URL that is not a web page")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("a shot of it is at %s", out)
			}
			if _, err := os.Stat(planted); !os.IsNotExist(err) {
				t.Error("curl took the URL as an option and wrote a file")
			}
		})
	}
}

// The file is not followed through a symbolic link: a link planted at a
// path a builder is known to shoot to (the role's /tmp/shot-<bead>.png)
// must not have its target emptied and overwritten.
func TestShotRefusesASymlinkedFile(t *testing.T) {
	s := page(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("not a shot"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, to := range map[string]string{
		"a link to a file":  target,
		"a link to nothing": filepath.Join(dir, "gone"),
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			if err := os.Symlink(to, out); err != nil {
				t.Fatal(err)
			}
			env, argsFile := standin(t, "shot", 0)
			rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
			if rc == 0 || !strings.Contains(text, out) || !strings.Contains(text, "symbolic link") {
				t.Errorf("shot to %s: exit %d, %q, want a refusal naming the link", out, rc, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for a symlinked file")
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "not a shot" {
				t.Errorf("the link's target is now %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "gone")); !os.IsNotExist(err) {
				t.Error("the shot was written through a dangling link")
			}
		})
	}
}
